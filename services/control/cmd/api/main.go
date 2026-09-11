// api 服务是 DevFlow 控制层的对外入口：GitHub Webhook、操作者 API 与审批发布。
// M1 阶段本文件只装配骨架：配置加载 → 数据库连接 → 路由 → 优雅停机。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/impself/DevFlow/services/control/internal/config"
	"github.com/impself/DevFlow/services/control/internal/controller"
	"github.com/impself/DevFlow/services/control/internal/github"
	"github.com/impself/DevFlow/services/control/internal/middleware"
	"github.com/impself/DevFlow/services/control/internal/obs"
	"github.com/impself/DevFlow/services/control/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api 服务退出", "err", err)
		os.Exit(1)
	}
}

// run 把 main 的逻辑收拢到一个返回 error 的函数里：
// 失败路径只有一条（返回 err），成功路径的 defer 都能正常执行。
func run() error {
	// 横切设施最先装配：此后所有日志（含配置报错）都是结构化 JSON。
	// Setup 内部已 slog.SetDefault，业务代码统一用 slog 包级函数即可。
	obs.Setup("devflow-api")

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// NotifyContext 把 SIGINT/SIGTERM 转成 context 取消信号：
	// 下面 server 与数据库都挂在这个 ctx 的生命周期上，Ctrl+C 即触发优雅停机。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// M1 为 no-op 追踪占位：API 就位，未来换 OTLP exporter 时业务代码零改动。
	shutdownTracer, err := obs.InitTracer(ctx, "devflow-api")
	if err != nil {
		return fmt.Errorf("初始化追踪: %w", err)
	}
	defer shutdownTracer()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	// 启动即迁移：库不就绪不开门（fail-fast）。
	if err := store.Migrate(ctx, st.Pool()); err != nil {
		return fmt.Errorf("迁移: %w", err)
	}
	slog.Info("数据库连接成功")

	// GitHub App 身份：启动时读私钥并验证可解析（fail-fast），
	// installation token 由 ghinstallation 在首次调用时惰性换取。
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
	_ = ghFactory // T014（预取文件）与 publisher（发布评论）将注入使用

	deps := &server{
		store:         st,
		webhook:       controller.NewWebhookHandler(st, cfg.GitHubWebhookSecret),
		approval:      controller.NewApprovalHandler(controller.NewApprovalService(st)),
		operatorToken: cfg.OperatorToken,
	}
	srv := &http.Server{
		Addr:    cfg.APIAddr,
		Handler: deps.router(),
	}

	// HTTP 服务在独立 goroutine 中运行，主 goroutine 等待退出信号。
	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	slog.Info("api 服务已启动", "addr", cfg.APIAddr)

	select {
	case err := <-serverErr:
		return fmt.Errorf("http 服务异常退出: %w", err)
	case <-ctx.Done():
		// 收到信号：给在途请求最多 10 秒完成，超时强制返回。
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("优雅停机超时，强制返回", "err", err)
		}
		slog.Info("api 服务已优雅退出")
		return nil
	}
}

// server 持有路由处理函数共享的依赖。
// 依赖集中注入而不是用全局变量，是为了让每个 handler 都可以独立构造、单测。
type server struct {
	store         *store.Store
	webhook       *controller.WebhookHandler
	approval      *controller.ApprovalHandler
	operatorToken string
}

func (s *server) router() *gin.Engine {
	// gin.New() 创建不带任何中间件的白板引擎；
	// gin.Default() 会附赠 Logger+Recovery，这里显式添加以保持依赖可见。
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", s.healthz)
	// webhook 的身份认证就是 HMAC 验签本身，不再叠加其他认证
	r.POST("/webhook", s.webhook.Handle)

	// 操作者 API：服务端令牌认证（AC32），不信任请求声明身份
	operator := r.Group("/api", middleware.OperatorAuth(s.operatorToken))
	{
		operator.POST("/cases/:caseID/approval-bundle", s.approval.Create)
		operator.POST("/bundles/:bundleID/approve", s.approval.Approve)
		operator.POST("/bundles/:bundleID/reject", s.approval.Reject)
	}
	return r
}

// healthz 是健康检查与依赖状态页（PRD §26.3）：
// 数据库可达返回 200；不可达返回 503 与 degraded 状态——不掩盖依赖故障。
func (s *server) healthz(c *gin.Context) {
	pingCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	dbOK := s.store.Pool().Ping(pingCtx) == nil
	code := http.StatusOK
	status := "ok"
	if !dbOK {
		code = http.StatusServiceUnavailable
		status = "degraded"
	}
	c.JSON(code, gin.H{
		"status":   status,
		"database": dbOK,
		"time":     time.Now().UTC().Format(time.RFC3339),
	})
}
