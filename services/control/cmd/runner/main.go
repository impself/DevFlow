// runner 是 DevFlow 的 Run 执行器进程：领取 → 心跳 → 调智能层 → 提交。
// M1 与 api 分进程部署：webhook 的 1 秒响应预算不应被长任务挤占（PRD §26.2）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/impself/DevFlow/services/control/internal/config"
	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/obs"
	"github.com/impself/DevFlow/services/control/internal/runtimeclient"
	"github.com/impself/DevFlow/services/control/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "runner 退出:", err)
		os.Exit(1)
	}
}

func run() error {
	// 日志最先装配（理由同 api）；runner 只消费 DATABASE_URL，
	// 复用 config.Load 全量校验——两个进程共享同一份 .env，缺配置一起报。
	logger := obs.Setup("devflow-runner")

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	// 启动即迁移：库不就绪 worker 不开工（fail-fast，同 api）。
	if err := store.Migrate(ctx, st.Pool()); err != nil {
		return fmt.Errorf("迁移: %w", err)
	}
	logger.Info("数据库就绪")

	// GitHub App 身份：与 api 相同的 fail-fast 装配（T014 预取需要）
	privateKeyPEM, err := os.ReadFile(cfg.GitHubPrivateKeyPath)
	if err != nil {
		return fmt.Errorf("读取 GitHub App 私钥: %w", err)
	}
	appID, err := strconv.ParseInt(cfg.GitHubAppID, 10, 64)
	if err != nil {
		return fmt.Errorf("GITHUB_APP_ID 应为数字: %w", err)
	}
	ghFactory, err := github.NewClientFactory(appID, privateKeyPEM)
	if err != nil {
		return err
	}

	// 出站客户端：分析调用 300s 兜底超时（run 总预算 900s 在执行器层）
	analyzer := runtimeclient.New(cfg.RuntimeURL, cfg.InternalToken, 5*time.Minute)

	owner := fmt.Sprintf("runner-%d", os.Getpid())
	executor := controller.NewRunExecutor(st, github.NewContents(ghFactory), analyzer, owner)
	worker := controller.NewWorker(st, owner, executor.Execute)

	errCh := make(chan error, 2)
	go func() { errCh <- worker.Run(ctx) }()
	go func() { errCh <- controller.Sweeper(ctx, st) }()

	slog.Info("runner 已启动", "owner", owner)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("runner 异常退出: %w", err)
		}
	case <-ctx.Done():
	}

	// 停机宽限：等 worker/sweeper 排空（在途的终态写回、最后一次心跳），
	// 最多 5s。超时强退在协议上是安全的——run 留在 RUNNING，重启后由
	// Sweeper 翻 RECOVERING 重跑，只是白费一次执行。
	slog.Info("停机中，等待 worker 收尾（最多 5s）")
	for range 2 {
		select {
		case err := <-errCh:
			if err != nil {
				slog.Error("组件退出异常", "err", err)
			}
		case <-time.After(5 * time.Second):
			slog.Warn("收尾超时，强制退出")
			return nil
		}
	}
	slog.Info("runner 已优雅退出")
	return nil
}
