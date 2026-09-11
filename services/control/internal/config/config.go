// Package config 负责从环境变量加载服务配置，并在启动时做fail-fast校验。
// 依据 PRD §29.3：配置缺失时提供清晰的启动检查，不生成随机凭据、不用占位配置蒙混。
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config 是 API 服务运行所需的全部配置。
// 字段全部公开，由 main 装配后按需传给各组件，组件自身不读环境变量——
// 这样组件保持纯函数化，测试时直接构造 Config 即可，不需要设置环境。
type Config struct {
	// APIAddr 是 HTTP 监听地址，如 ":8080"。
	APIAddr string

	// DatabaseURL 是 PostgreSQL 连接串，业务状态唯一权威来源（宪法 IV）。
	DatabaseURL string

	// GitHubWebhookSecret 用于校验 X-Hub-Signature-256 HMAC 签名。
	GitHubWebhookSecret string

	// GitHubAppID 与 PrivateKeyPath 用于以 GitHub App 身份获取 installation token。
	GitHubAppID          string
	GitHubPrivateKeyPath string

	// InternalToken 是 Go ↔ Python 服务间共享密钥（宪法 III：凭据隔离）。
	InternalToken string

	// RuntimeURL 是 Python 智能层的基地址（runner 出站调用用），仅 runner 消费。
	RuntimeURL string

	// ArtifactDir 是产物落盘目录（content-addressable），仅 runner 消费。
	ArtifactDir string
}

// Load 读取环境变量并校验必填项，缺失时返回包含具体变量名的错误。
func Load() (Config, error) {
	cfg := Config{
		APIAddr:              getenv("CONTROL_API_ADDR", ":8080"),
		RuntimeURL:           getenv("RUNTIME_URL", "http://127.0.0.1:8100"),
		ArtifactDir:          getenv("ARTIFACT_DIR", "data/artifacts"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		GitHubWebhookSecret:  os.Getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubAppID:          os.Getenv("GITHUB_APP_ID"),
		GitHubPrivateKeyPath: os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"),
		InternalToken:        os.Getenv("DEVFLOW_INTERNAL_TOKEN"),
	}

	// 用切片而不是 map：map 迭代顺序随机，会漏报且每次报的变量不同；
	// 切片保持声明顺序，一次性列出所有缺失项，操作者一次补齐。
	required := []struct{ name, value string }{
		{"DATABASE_URL", cfg.DatabaseURL},
		{"GITHUB_WEBHOOK_SECRET", cfg.GitHubWebhookSecret},
		{"GITHUB_APP_ID", cfg.GitHubAppID},
		{"GITHUB_APP_PRIVATE_KEY_PATH", cfg.GitHubPrivateKeyPath},
		{"DEVFLOW_INTERNAL_TOKEN", cfg.InternalToken},
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("缺少必需的环境变量: %s（参考 infra/.env.example）", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// getenv 返回环境变量的值；为空时返回默认值。
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
