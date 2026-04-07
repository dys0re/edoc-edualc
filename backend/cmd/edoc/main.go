package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/api"
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/db"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	var configFile string

	// 简单参数解析: --config path / serve / -p / --help
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			configFile = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	cfg, err := config.Load(configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	// 连接 PostgreSQL（可选，失败不退出）
	var pool *pgxpool.Pool
	ctx := context.Background()
	pool, err = db.Connect(ctx, cfg.Database.DSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "DB connect failed (memory disabled): %v\n", err)
	} else {
		defer pool.Close()
		if err := db.Migrate(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "DB migrate failed: %v\n", err)
		}
	}

	if len(args) > 0 {
		switch args[0] {
		case "serve":
			runServer(cfg, pool)
			return
		case "-p":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "Usage: edoc -p \"prompt\"")
				os.Exit(1)
			}
			runOnce(cfg, pool, strings.Join(args[1:], " "))
			return
		case "--help", "-h":
			printUsage()
			return
		}
	}

	runREPL(cfg, pool)
}

func printUsage() {
	fmt.Println("edoc-edualc - AI coding assistant")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  edoc                        Start interactive REPL")
	fmt.Println("  edoc -p \"prompt\"            Run a single prompt")
	fmt.Println("  edoc serve                  Start the web API server")
	fmt.Println("  edoc --config path          Specify config file")
	fmt.Println()
	fmt.Println("Config: config.yaml (or set --config). Env vars: EDOC_* prefix.")
	fmt.Println("See config.example.yaml for all options.")
}

// buildProvider 根据 config 创建 Provider 实例
func buildProvider(cfg *config.Config) provider.Provider {
	switch cfg.Provider.Default {
	case "openai":
		return provider.NewOpenAIProvider(cfg.OpenAI.APIKey, cfg.Provider.Model, cfg.OpenAI.BaseURL)
	default:
		return provider.NewAnthropicProvider(cfg.Anthropic.APIKey, cfg.Provider.Model, cfg.Anthropic.BaseURL)
	}
}

// parseShellType 将配置字符串转换为 tool.ShellType
func parseShellType(s string) tool.ShellType {
	switch s {
	case "powershell":
		return tool.ShellPowerShell
	case "bash":
		return tool.ShellBash
	case "cmd":
		return tool.ShellCmd
	default:
		return tool.ShellAuto
	}
}

// buildMemoryStore 创建记忆存储（PG 可用时用 PG，否则 nil）
func buildMemoryStore(pool *pgxpool.Pool, workDir string) *memory.Store {
	if pool == nil {
		return nil
	}
	projectKey := memory.SanitizeProjectKey(workDir)
	return memory.NewStore(pool, "", projectKey)
}

// buildSystemPrompt 构建 system prompt（注入记忆）
func buildSystemPrompt(cfg *config.Config, workDir string, store *memory.Store) string {
	var memorySection string
	if store != nil {
		memorySection = memory.BuildMemoryPromptSectionPG(context.Background(), store)
	}

	if memorySection == "" {
		// 回退到文件版
		memoryDir := cfg.Tools.MemoryDir
		if memoryDir == "" {
			memoryDir = memory.GetMemoryDir(workDir)
		}
		memorySection = memory.BuildMemoryPromptSection(memoryDir)
	}

	return prompt.BuildSystemPromptWithMemory(workDir, memorySection)
}

// buildAgentConfig 组装 agent.Config
func buildAgentConfig(cfg *config.Config, pool *pgxpool.Pool) agent.Config {
	workDir := cfg.Tools.WorkDir
	if workDir == "." {
		workDir, _ = os.Getwd()
	}

	shell := parseShellType(cfg.Tools.Shell)
	reg := tool.NewRegistry()
	reg.Register(tool.NewBashTool(workDir, shell))
	reg.Register(tool.NewReadTool())
	reg.Register(tool.NewWriteTool())
	reg.Register(tool.NewGlobTool())
	reg.Register(tool.NewGrepTool())
	reg.Register(tool.NewEditTool())

	memStore := buildMemoryStore(pool, workDir)

	return agent.Config{
		Provider:     buildProvider(cfg),
		Registry:     reg,
		SystemPrompt: buildSystemPrompt(cfg, workDir, memStore),
		Model:        cfg.Provider.Model,
		MaxTokens:    cfg.Agent.MaxTokens,
		MaxTurns:     cfg.Agent.MaxTurns,
		AutoCompactThreshold: cfg.Agent.AutoCompactThreshold,
		ModelBackup:          cfg.Provider.ModelBackup,
		MemoryStore:          memStore,
	}
}

// runOnce executes a single prompt and exits. Maps to `claude -p "..."`.
func runOnce(cfg *config.Config, pool *pgxpool.Pool, userPrompt string) {
	agentCfg := buildAgentConfig(cfg, pool)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	for evt := range agent.Run(ctx, agentCfg, userPrompt) {
		switch evt.Type {
		case "text_delta":
			fmt.Print(evt.Delta)
		case "tool_use":
			fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", evt.ToolName)
		case "tool_result":
			if evt.ToolResult != nil && evt.ToolResult.IsError {
				fmt.Fprintf(os.Stderr, "[tool error: %s]\n", evt.ToolResult.Content)
			}
		case "error":
			fmt.Fprintf(os.Stderr, "\nError: %v\n", evt.Error)
			os.Exit(1)
		case "turn_complete":
			fmt.Println()
		}
	}
}

// runREPL starts an interactive read-eval-print loop.
func runREPL(cfg *config.Config, pool *pgxpool.Pool) {
	agentCfg := buildAgentConfig(cfg, pool)
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("edoc-edualc (type /quit to exit)")
	fmt.Printf("model: %s, provider: %s\n", cfg.Provider.Model, cfg.Provider.Default)
	if pool != nil {
		fmt.Println("db: connected")
	}
	fmt.Println()

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "/quit" || input == "/exit" {
			break
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

		for evt := range agent.Run(ctx, agentCfg, input) {
			switch evt.Type {
			case "text_delta":
				fmt.Print(evt.Delta)
			case "tool_use":
				fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", evt.ToolName)
			case "tool_result":
				if evt.ToolResult != nil && evt.ToolResult.IsError {
					fmt.Fprintf(os.Stderr, "[tool error: %s]\n", evt.ToolResult.Content)
				}
			case "error":
				fmt.Fprintf(os.Stderr, "\nError: %v\n", evt.Error)
			case "turn_complete":
				fmt.Println()
			}
		}

		cancel()
		fmt.Println()
	}
}

// runServer starts the Gin HTTP server.
func runServer(cfg *config.Config, pool *pgxpool.Pool) {
	p := buildProvider(cfg)

	workDir := cfg.Tools.WorkDir
	if workDir == "." {
		workDir, _ = os.Getwd()
	}

	memStore := buildMemoryStore(pool, workDir)

	r := api.NewRouter(p, cfg, workDir, memStore)
	fmt.Printf("Starting server on :%d (model: %s)\n", cfg.Server.Port, cfg.Provider.Model)
	if pool != nil {
		fmt.Println("db: connected")
	}
	if err := r.Run(fmt.Sprintf(":%d", cfg.Server.Port)); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
