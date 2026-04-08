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
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
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
		fmt.Fprintf(os.Stderr, "DB connect failed (memory+session disabled): %v\n", err)
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
	fmt.Println("REPL commands:")
	fmt.Println("  /quit, /exit               Exit")
	fmt.Println("  /sessions                  List saved sessions")
	fmt.Println("  /resume <id>               Resume a saved session")
	fmt.Println("  /new                       Start a new session")
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

// buildSessionStore 创建会话存储（PG 可用时用 PG，否则 nil）
func buildSessionStore(pool *pgxpool.Pool) *session.Store {
	if pool == nil {
		return nil
	}
	return session.NewStore(pool)
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

// buildAgentConfig 组装 agent.Config (with Agent tool wired in).
func buildAgentConfig(cfg *config.Config, pool *pgxpool.Pool, sessionID string, permCallback tool.PermissionCallback) agent.Config {
	workDir := cfg.Tools.WorkDir
	if workDir == "." {
		workDir, _ = os.Getwd()
	}

	shell := parseShellType(cfg.Tools.Shell)
	p := buildProvider(cfg)
	webFetchProvider := tool.NewProviderWebFetch(p, cfg.Provider.ModelBackup)
	reg := tool.NewRegistry()
	reg.Register(tool.NewBashTool(workDir, shell))
	reg.Register(tool.NewReadTool())
	reg.Register(tool.NewWriteTool())
	reg.Register(tool.NewGlobTool())
	reg.Register(tool.NewGrepTool())
	reg.Register(tool.NewEditTool())
	reg.Register(&tool.WebFetchTool{Provider: webFetchProvider})

	memStore := buildMemoryStore(pool, workDir)
	sessStore := buildSessionStore(pool)

	agentCfg := agent.Config{
		Provider:     p,
		Registry:     reg,
		SystemPrompt: buildSystemPrompt(cfg, workDir, memStore),
		Model:        cfg.Provider.Model,
		MaxTokens:    cfg.Agent.MaxTokens,
		MaxTurns:     cfg.Agent.MaxTurns,
		AutoCompactThreshold: cfg.Agent.AutoCompactThreshold,
		ModelBackup:          cfg.Provider.ModelBackup,
		MemoryStore:          memStore,
		SessionStore:         sessStore,
		SessionID:            sessionID,
		PermissionMode:       tool.ParsePermissionMode(cfg.Tools.PermissionMode),
		AllowRules:           cfg.Tools.AllowRules,
		PermissionCallback:   permCallback,
	}

	// Wire Agent tool with subagent resolver (references the config being built)
	resolver := agent.NewSubagentResolver(agentCfg)
	reg.Register(&tool.AgentTool{Resolver: resolver})

	return agentCfg
}

// buildPermissionCallback creates a REPL permission callback that reads y/n from stdin.
func buildPermissionCallback(scanner *bufio.Scanner) tool.PermissionCallback {
	return func(toolName, description string) (bool, error) {
		fmt.Printf("\n  [Permission] %s: %s\n  Allow? (y/n): ", toolName, description)
		if !scanner.Scan() {
			return false, nil
		}
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return answer == "y" || answer == "yes", nil
	}
}

// runOnce executes a single prompt and exits. Maps to `claude -p "..."`.
func runOnce(cfg *config.Config, pool *pgxpool.Pool, userPrompt string) {
	scanner := bufio.NewScanner(os.Stdin)
	agentCfg := buildAgentConfig(cfg, pool, "", buildPermissionCallback(scanner))
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
	scanner := bufio.NewScanner(os.Stdin)
	sessStore := buildSessionStore(pool)

	// 创建或恢复会话
	var currentSessionID string
	if sessStore != nil {
		workDir := cfg.Tools.WorkDir
		if workDir == "." {
			workDir, _ = os.Getwd()
		}
		projectKey := memory.SanitizeProjectKey(workDir)
		sess, err := sessStore.Create(context.Background(), "", projectKey, cfg.Provider.Model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create session: %v\n", err)
		} else {
			currentSessionID = sess.ID
		}
	}

	fmt.Println("edoc-edualc (type /quit to exit)")
	fmt.Printf("model: %s, provider: %s\n", cfg.Provider.Model, cfg.Provider.Default)
	if currentSessionID != "" {
		fmt.Printf("session: %s\n", currentSessionID)
	}
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

		// Handle commands
		if input == "/quit" || input == "/exit" {
			break
		}

		if input == "/new" {
			if sessStore != nil {
				workDir := cfg.Tools.WorkDir
				if workDir == "." {
					workDir, _ = os.Getwd()
				}
				projectKey := memory.SanitizeProjectKey(workDir)
				sess, err := sessStore.Create(context.Background(), "", projectKey, cfg.Provider.Model)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error creating session: %v\n", err)
				} else {
					currentSessionID = sess.ID
					fmt.Printf("New session: %s\n", currentSessionID)
				}
			}
			continue
		}

		if input == "/sessions" {
			if sessStore != nil {
				workDir := cfg.Tools.WorkDir
				if workDir == "." {
					workDir, _ = os.Getwd()
				}
				projectKey := memory.SanitizeProjectKey(workDir)
				sessions, err := sessStore.List(context.Background(), "", projectKey, 20)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				} else if len(sessions) == 0 {
					fmt.Println("No sessions found.")
				} else {
					for _, s := range sessions {
						title := s.Title
						if title == "" {
							title = s.ID[:8] + "..."
						}
						fmt.Printf("  %s  %s  %s\n", s.ID[:8], s.UpdatedAt.Format("2006-01-02 15:04"), title)
					}
				}
			} else {
				fmt.Println("Database not available.")
			}
			continue
		}

		if strings.HasPrefix(input, "/resume ") {
			id := strings.TrimSpace(strings.TrimPrefix(input, "/resume "))
			if sessStore == nil {
				fmt.Println("Database not available.")
				continue
			}
			// Verify session exists
			sess, err := sessStore.Get(context.Background(), id)
			if err != nil {
				// Try partial ID match
				workDir := cfg.Tools.WorkDir
				if workDir == "." {
					workDir, _ = os.Getwd()
				}
				projectKey := memory.SanitizeProjectKey(workDir)
				sessions, listErr := sessStore.List(context.Background(), "", projectKey, 50)
				if listErr != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", listErr)
					continue
				}
				found := false
				for _, s := range sessions {
					if strings.HasPrefix(s.ID, id) {
						sess = &s
						found = true
						break
					}
				}
				if !found {
					fmt.Fprintf(os.Stderr, "Session not found: %s\n", id)
					continue
				}
			}

			currentSessionID = sess.ID
			fmt.Printf("Resumed session: %s\n", sess.ID)

			// Load history and run with it
			history, err := sessStore.LoadMessages(context.Background(), sess.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading session: %v\n", err)
				continue
			}
			fmt.Printf("Loaded %d messages from history.\n", len(history))

			agentCfg := buildAgentConfig(cfg, pool, currentSessionID, buildPermissionCallback(scanner))
			runAgentLoop(scanner, agentCfg, history)
			continue
		}

		// Normal prompt
		agentCfg := buildAgentConfig(cfg, pool, currentSessionID, buildPermissionCallback(scanner))
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
			case "warning":
				fmt.Fprintf(os.Stderr, "\n[warning: %s]\n", evt.Delta)
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
	sessStore := buildSessionStore(pool)

	r := api.NewRouter(p, cfg, workDir, memStore, sessStore)
	fmt.Printf("Starting server on :%d (model: %s)\n", cfg.Server.Port, cfg.Provider.Model)
	if pool != nil {
		fmt.Println("db: connected")
	}
	if err := r.Run(fmt.Sprintf(":%d", cfg.Server.Port)); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// runAgentLoop runs a multi-turn agent loop in the REPL with existing messages.
func runAgentLoop(scanner *bufio.Scanner, agentCfg agent.Config, history []message.Message) {
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
		if input == "/new" || input == "/sessions" || strings.HasPrefix(input, "/resume ") {
			fmt.Println("Exit resume mode first with /quit, then use the command.")
			continue
		}

		// Append new user message to history
		history = append(history, message.NewUserMessage(input))

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

		for evt := range agent.RunWithMessages(ctx, agentCfg, history) {
			switch evt.Type {
			case "text_delta":
				fmt.Print(evt.Delta)
			case "tool_use":
				fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", evt.ToolName)
			case "tool_result":
				if evt.ToolResult != nil && evt.ToolResult.IsError {
					fmt.Fprintf(os.Stderr, "[tool error: %s]\n", evt.ToolResult.Content)
				}
			case "warning":
				fmt.Fprintf(os.Stderr, "\n[warning: %s]\n", evt.Delta)
			case "error":
				fmt.Fprintf(os.Stderr, "\nError: %v\n", evt.Error)
			case "turn_complete":
				fmt.Println()
			}
		}

		cancel()
		fmt.Println()

		// Reload history from DB (agent loop persisted new messages)
		if agentCfg.SessionStore != nil && agentCfg.SessionID != "" {
			updated, err := agentCfg.SessionStore.LoadMessages(context.Background(), agentCfg.SessionID)
			if err == nil {
				history = updated
			}
		}
	}
}
