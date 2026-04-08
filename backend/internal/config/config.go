package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all application configuration.
// 对标 Spring Boot application.yml，统一管理所有配置项。
// Viper 加载优先级: 命令行 flag > 环境变量 > config.yaml > 默认值
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
	Default     string `mapstructure:"default"`       // 默认 provider: anthropic / openai
	Model       string `mapstructure:"model"`          // 默认模型
	ModelBackup string `mapstructure:"model_backup"`   // 限流时降级模型，空=不降级
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
	Type    string   `mapstructure:"type"`    // "stdio" (default) or "sse"
	Command string   `mapstructure:"command"` // stdio: executable
	Args    []string `mapstructure:"args"`    // stdio: arguments
	Env     []string `mapstructure:"env"`     // stdio: KEY=VALUE env vars
	URL     string   `mapstructure:"url"`     // sse: server URL
}

// Load reads config from file + env vars + defaults.
// configFile: 配置文件路径，空则自动搜索 (./config.yaml, ./config.yml)
func Load(configFile string) (*Config, error) {
	v := viper.New()

	// 默认值
	setDefaults(v)

	// 配置文件
	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./backend")
		v.AddConfigPath("$HOME/.edoc")
		v.AddConfigPath("/etc/edoc")
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

	// 读配置文件（不存在不报错，纯环境变量也能跑）
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
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
