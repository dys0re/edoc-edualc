package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all application configuration.
// 对标 Spring Boot application.yml + application-{profile}.yml。
// Viper 加载优先级: 命令行 flag > 环境变量 > application-{profile}.yml > application.yml > 默认值
type Config struct {
	Server    ServerConfig             `mapstructure:"server"`
	Provider  ProviderConfig           `mapstructure:"provider"`
	Anthropic AnthropicConfig          `mapstructure:"anthropic"`
	OpenAI    OpenAIConfig             `mapstructure:"openai"`
	Agent     AgentConfig              `mapstructure:"agent"`
	Tools     ToolsConfig              `mapstructure:"tools"`
	Database  DatabaseConfig           `mapstructure:"database"`
	Log       LogConfig                `mapstructure:"log"`
	MCPServers map[string]MCPServerConfig `mapstructure:"mcp_servers"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"` // debug / release / test
}

type ProviderConfig struct {
	Default     string   `mapstructure:"default"`       // 默认 provider: anthropic / openai
	Model       string   `mapstructure:"model"`          // 默认模型
	ModelBackup string   `mapstructure:"model_backup"`   // 限流时降级模型，空=不降级
	Models      []string `mapstructure:"models"`         // 可用模型列表，空=只用 Model
}

type AnthropicConfig struct {
	APIKey  string `mapstructure:"api_key"`
	BaseURL string `mapstructure:"base_url"`
}

type OpenAIConfig struct {
	APIKey  string `mapstructure:"api_key"`
	BaseURL string `mapstructure:"base_url"`
}

type AgentConfig struct {
	MaxTurns             int `mapstructure:"max_turns"`              // 0 = 无限
	MaxTokens            int `mapstructure:"max_tokens"`             // 单次回复最大 token
	AutoCompactThreshold int `mapstructure:"auto_compact_threshold"` // 0 = 禁用自动压缩
}

type ToolsConfig struct {
	WorkDir        string   `mapstructure:"work_dir"`        // 工作目录
	Shell          string   `mapstructure:"shell"`           // auto / powershell / bash / cmd
	MemoryDir      string   `mapstructure:"memory_dir"`      // 记忆目录，空=自动推导 ~/.edoc/projects/<path>/memory/
	PermissionMode string   `mapstructure:"permission_mode"` // bypass / default / accept-edits / strict
	AllowRules     []string `mapstructure:"allow_rules"`     // e.g. ["Read", "Bash:git *"]
	PlansDir       string   `mapstructure:"plans_dir"`       // 计划文件目录，空=~/.edoc/plans/
	BochaAPIKey    string   `mapstructure:"bocha_api_key"`   // Bocha AI Search API key
}

type DatabaseConfig struct {
	URL      string `mapstructure:"url"`      // 完整连接串，优先级最高 (DATABASE_URL)
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	SSLMode  string `mapstructure:"sslmode"`
}

// DSN 返回 PostgreSQL 连接串。URL 优先，否则从字段拼接。
func (d DatabaseConfig) DSN() string {
	if d.URL != "" {
		return d.URL
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.SSLMode)
}

type LogConfig struct {
	Level string `mapstructure:"level"` // debug / info / warn / error
}

// MCPServerConfig describes a single MCP server connection.
// Maps to Claude Code's mcpServers in settings.json.
type MCPServerConfig struct {
	Type    string   `mapstructure:"type"`    // "stdio" (default) or "sse" or "http"
	Command string   `mapstructure:"command"` // stdio: executable
	Args    []string `mapstructure:"args"`    // stdio: arguments
	Env     []string `mapstructure:"env"`     // stdio: KEY=VALUE env vars
	URL     string            `mapstructure:"url"`     // sse/http: server URL
	Headers map[string]string `mapstructure:"headers"` // HTTP headers (e.g. Authorization)

	// OAuth fields (for SSE/HTTP servers requiring authentication)
	OAuthClientID     string   `mapstructure:"oauth_client_id"`
	OAuthClientSecret string   `mapstructure:"oauth_client_secret"`
	OAuthScopes       []string `mapstructure:"oauth_scopes"`
	OAuthRedirectURI  string   `mapstructure:"oauth_redirect_uri"`
	OAuthPKCE         bool     `mapstructure:"oauth_pkce"`
}
// Load reads config from application.yml + application-{profile}.yml + env vars + defaults.
// 对标 Spring Boot 配置加载:
//   1. 加载 application.yml (base)
//   2. 读取 profile 字段 (默认 "dev")，合并 application-{profile}.yml
//   3. 环境变量覆盖一切
//
// configFile: 显式配置文件路径，非空则跳过自动搜索 (兼容 --config 参数)
func Load(configFile string) (*Config, error) {
	v := viper.New()

	// 默认值
	setDefaults(v)

	// 搜索路径 — 对标 Spring Boot 的 classpath:/resources/ 约定
	searchPaths := []string{
		"./resources",
		".",
		"./backend/resources",
		"./backend",
		"$HOME/.edoc",
		"/etc/edoc",
	}

	// 配置文件
	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("application")
		v.SetConfigType("yaml")
		for _, p := range searchPaths {
			v.AddConfigPath(p)
		}
	}

	// 环境变量覆盖: EDOC_SERVER_PORT → server.port
	v.SetEnvPrefix("EDOC")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 特殊映射: API key 环境变量名不含 EDOC_ 前缀（兼容 Claude Code）
	_ = v.BindEnv("anthropic.api_key", "ANTHROPIC_API_KEY")
	_ = v.BindEnv("openai.api_key", "OPENAI_API_KEY")
	_ = v.BindEnv("anthropic.base_url", "ANTHROPIC_BASE_URL")
	_ = v.BindEnv("openai.base_url", "OPENAI_BASE_URL")
	// 兼容旧环境变量名
	_ = v.BindEnv("provider.model", "EDOC_MODEL")
	_ = v.BindEnv("provider.default", "EDOC_PROVIDER")
	_ = v.BindEnv("server.port", "EDOC_PORT")
	// 数据库连接支持 DATABASE_URL（对标 Prisma/Drizzle 风格）
	_ = v.BindEnv("database.url", "DATABASE_URL")
	// Bocha AI Search API key
	_ = v.BindEnv("tools.bocha_api_key", "BOCHA_API_KEY")

	// 读 base 配置文件（不存在不报错，纯环境变量也能跑）
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	// --- Profile 合并: 对标 Spring Boot spring.profiles.active ---
	// 优先级: EDOC_PROFILE 环境变量 > application.yml 中的 profile 字段
	profile := os.Getenv("EDOC_PROFILE")
	if profile == "" {
		profile = v.GetString("profile")
	}
	if profile != "" {
		pv := viper.New()
		pv.SetConfigName("application-" + profile)
		pv.SetConfigType("yaml")
		if configFile != "" {
			// --config 指定了路径，profile 文件在同目录
			pv.AddConfigPath(filepath.Dir(configFile))
		} else {
			for _, p := range searchPaths {
				pv.AddConfigPath(p)
			}
		}
		if err := pv.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("read profile config (application-%s.yml): %w", profile, err)
			}
			// profile 文件不存在不报错，只用 base
		} else {
			// 合并: profile 覆盖 base
			if err := v.MergeConfigMap(pv.AllSettings()); err != nil {
				return nil, fmt.Errorf("merge profile config: %w", err)
			}
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 校验
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// server
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "release")

	// provider
	v.SetDefault("provider.default", "anthropic")
	v.SetDefault("provider.model", "claude-sonnet-4-20250514")
	v.SetDefault("provider.model_backup", "")

	// agent
	v.SetDefault("agent.max_turns", 0)
	v.SetDefault("agent.max_tokens", 8192)
	v.SetDefault("agent.auto_compact_threshold", 0)

	// tools
	v.SetDefault("tools.work_dir", ".")
	v.SetDefault("tools.shell", "auto")
	v.SetDefault("tools.permission_mode", "bypass")

	// database
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "edoc")
	v.SetDefault("database.password", "")
	v.SetDefault("database.dbname", "edoc")
	v.SetDefault("database.sslmode", "disable")

	// log
	v.SetDefault("log.level", "info")
}

// Validate checks required fields and logical constraints.
func (c *Config) Validate() error {
	if c.Provider.Default != "anthropic" && c.Provider.Default != "openai" {
		return fmt.Errorf("provider.default must be 'anthropic' or 'openai', got %q", c.Provider.Default)
	}

	if c.Provider.Default == "anthropic" && c.Anthropic.APIKey == "" {
		return fmt.Errorf("anthropic.api_key is required when provider is 'anthropic'")
	}
	if c.Provider.Default == "openai" && c.OpenAI.APIKey == "" {
		return fmt.Errorf("openai.api_key is required when provider is 'openai'")
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be 1-65535, got %d", c.Server.Port)
	}

	return nil
}

// ShellType 将配置字符串转换为 tool.ShellType 值。
// 避免循环依赖: config 不 import tool，返回字符串由调用方转换。
func ShellType(s string) string {
	return s // "auto" / "powershell" / "bash" / "cmd"
}
