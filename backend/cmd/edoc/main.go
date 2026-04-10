package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/api"
	"github.com/dysorder/edoc-edualc/backend/internal/command"
	"github.com/dysorder/edoc-edualc/backend/internal/compact"
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/db"
	"github.com/dysorder/edoc-edualc/backend/internal/hook"
	"github.com/dysorder/edoc-edualc/backend/internal/lsp"
	"github.com/dysorder/edoc-edualc/backend/internal/mcp"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/dysorder/edoc-edualc/backend/internal/skill"
	"github.com/dysorder/edoc-edualc/backend/internal/snapshot"
	"github.com/dysorder/edoc-edualc/backend/internal/task"
	"github.com/dysorder/edoc-edualc/backend/internal/team"
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

// buildSkillRegistry 加载 skill 注册表（用户 skill 优先于内置 skill）
func buildSkillRegistry(workDir string) *skill.Registry {
	dirs := skill.DefaultDirs(workDir)
	reg, err := skill.Load(dirs)
	if err != nil || reg == nil {
		reg = skill.NewRegistry()
	}
	// 注册内置 skill（commit, review 等）
	// Register 不会覆盖同名的用户 skill，用户定义优先
	for _, s := range skill.BuiltinSkills() {
		reg.Register(s)
	}
	return reg
}

// buildSystemPrompt 构建 system prompt（注入记忆 + skill 列表 + 完整环境信息）
func buildSystemPrompt(cfg *config.Config, workDir string, store *memory.Store, skillReg *skill.Registry, reg *tool.Registry, model string, shell tool.ShellType) string {
	var memorySection string
	if store != nil {
		memorySection = memory.BuildMemoryPromptSectionPG(context.Background(), store)
	}
	if memorySection == "" {
		memoryDir := cfg.Tools.MemoryDir
		if memoryDir == "" {
			memoryDir = memory.GetMemoryDir(workDir)
		}
		memorySection = memory.BuildMemoryPromptSection(memoryDir)
	}

	skillSection := skill.BuildSystemReminderSection(skillReg.All())

	env := buildEnvContext(workDir, model, shell, reg)
	return prompt.BuildSystemPromptFull(env, memorySection, skillSection)
}

// buildEnvContext 收集环境信息，对标 Claude Code 的 computeSimpleEnvInfo + getGitStatus。
func buildEnvContext(workDir, model string, shell tool.ShellType, reg *tool.Registry) prompt.EnvContext {
	env := prompt.EnvContext{
		WorkDir: workDir,
		Model:   model,
		Shell:   shellName(shell),
	}

	// OS version
	env.OSVersion = getOSVersion()

	// Git info
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err == nil {
		env.IsGit = true
		env.GitBranch = gitOutput(workDir, "rev-parse", "--abbrev-ref", "HEAD")
		env.GitMainBranch = detectMainBranch(workDir)
		env.GitUser = gitOutput(workDir, "config", "user.name")
		env.GitStatus = gitOutput(workDir, "--no-optional-locks", "status", "--short")
		env.GitLog = gitOutput(workDir, "--no-optional-locks", "log", "--oneline", "-n", "5")

		// Truncate long status
		if len(env.GitStatus) > 2000 {
			env.GitStatus = env.GitStatus[:2000] + "\n... (truncated)"
		}
	}

	// Enabled tools
	if reg != nil {
		for _, t := range reg.All() {
			env.EnabledTools = append(env.EnabledTools, t.Name())
		}
	}

	return env
}

// gitOutput runs a git command and returns trimmed stdout, empty on error.
func gitOutput(workDir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectMainBranch 检测默认分支名（main 或 master）。
func detectMainBranch(workDir string) string {
	// Try remote HEAD first
	ref := gitOutput(workDir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if ref != "" {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	// Fallback: check if main exists, else master
	if gitOutput(workDir, "rev-parse", "--verify", "refs/heads/main") != "" {
		return "main"
	}
	return "master"
}

// getOSVersion returns a human-readable OS version string.
func getOSVersion() string {
	if runtime.GOOS == "windows" {
		// Try ver command for Windows version
		out, err := exec.Command("cmd", "/c", "ver").Output()
		if err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v
			}
		}
	}
	return fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
}

// shellName converts ShellType to a display name.
func shellName(s tool.ShellType) string {
	switch s {
	case tool.ShellBash:
		return "bash"
	case tool.ShellPowerShell:
		return "powershell"
	case tool.ShellCmd:
		return "cmd"
	default:
		return "auto"
	}
}

// buildAgentConfig 组装 agent.Config (with Agent tool wired in).
// taskMgr 可由调用方传入以复用（REPL 场景），传 nil 则内部新建。
// snapStore 可由调用方传入以复用（REPL 场景），传 nil 则内部新建。
func buildAgentConfig(cfg *config.Config, pool *pgxpool.Pool, sessionID string, scanner *bufio.Scanner, permCallback tool.PermissionCallback, externalTaskMgr *task.Manager, externalSnapStore *snapshot.Store) (agent.Config, *task.Manager, *team.Manager) {
	workDir := cfg.Tools.WorkDir
	if workDir == "." {
		workDir, _ = os.Getwd()
	}

	shell := parseShellType(cfg.Tools.Shell)
	p := buildProvider(cfg)
	webFetchProvider := tool.NewProviderWebFetch(p, cfg.Provider.ModelBackup)
	reg := tool.NewRegistry()

	// 后台任务管理器。对标 Claude Code 的 AppState.tasks。
	var taskMgr *task.Manager
	if externalTaskMgr != nil {
		taskMgr = externalTaskMgr
	} else {
		taskMgr = task.NewManager()
	}

	bashTool := tool.NewBashTool(workDir, shell)
	bashTool.SetTaskManager(taskMgr)
	reg.Register(bashTool)
	reg.Register(tool.NewReadTool())

	// 文件快照：优先使用外部传入的 store（REPL 场景跨轮次共享），否则新建
	var snapStore *snapshot.Store
	if externalSnapStore != nil {
		snapStore = externalSnapStore
	} else {
		snapStore = snapshot.NewStore(workDir)
	}

	writeTool := tool.NewWriteTool()
	writeTool.Snapshots = snapStore
	reg.Register(writeTool)

	reg.Register(tool.NewGlobTool())
	reg.Register(tool.NewGrepTool())

	editTool := tool.NewEditTool()
	editTool.Snapshots = snapStore
	reg.Register(editTool)
	reg.Register(&tool.WebFetchTool{Provider: webFetchProvider})
	reg.Register(&tool.WebSearchTool{APIKey: cfg.Tools.BochaAPIKey})

	memStore := buildMemoryStore(pool, workDir)
	sessStore := buildSessionStore(pool)
	skillReg := buildSkillRegistry(workDir)

	reg.Register(&tool.SkillTool{Registry: skillReg})

	// Plan mode tools
	plansDir := cfg.Tools.PlansDir
	if plansDir == "" {
		home, _ := os.UserHomeDir()
		plansDir = filepath.Join(home, ".edoc", "plans")
	}
	reg.Register(&tool.EnterPlanModeTool{PlansDir: plansDir})
	reg.Register(&tool.ExitPlanModeTool{PlansDir: plansDir, PermissionCallback: permCallback})

	// TodoWrite + AskUserQuestion
	reg.Register(&tool.TodoWriteTool{})
	reg.Register(&tool.AskUserQuestionTool{Callback: buildAskCallback(scanner)})

	// Worktree tools
	reg.Register(&tool.EnterWorktreeTool{WorkDir: workDir})
	reg.Register(&tool.ExitWorktreeTool{})

	// Task tools — 后台任务输出读取和停止
	reg.Register(&tool.TaskOutputTool{Manager: taskMgr})
	reg.Register(&tool.TaskStopTool{Manager: taskMgr})

	// Sleep + ScheduleCron tools
	reg.Register(&tool.SleepTool{})
	cronMgr := tool.NewCronManager()
	reg.Register(&tool.CronCreateTool{Manager: cronMgr})
	reg.Register(&tool.CronDeleteTool{Manager: cronMgr})
	reg.Register(&tool.CronListTool{Manager: cronMgr})

	// MCP: 连接所有配置的 server，注册发现的工具
	if len(cfg.MCPServers) > 0 {
		mcpMgr := mcp.NewManager()
		mcpCfgs := make(map[string]mcp.ServerConfig, len(cfg.MCPServers))
		for name, s := range cfg.MCPServers {
			mcpCfgs[name] = mcp.ServerConfig{
				Type:    s.Type,
				Command: s.Command,
				Args:    s.Args,
				Env:     s.Env,
				URL:     s.URL,
			}
		}
		if errs := mcpMgr.Connect(context.Background(), mcpCfgs); len(errs) > 0 {
			for name, err := range errs {
				fmt.Fprintf(os.Stderr, "MCP server %q connect failed: %v\n", name, err)
			}
		}
		mcp.RegisterTools(reg, mcpMgr)
	}

	agentCfg := agent.Config{
		Provider:             p,
		Registry:             reg,
		SystemPrompt:         buildSystemPrompt(cfg, workDir, memStore, skillReg, reg, cfg.Provider.Model, shell),
		Model:        cfg.Provider.Model,
		MaxTokens:    cfg.Agent.MaxTokens,
		MaxTurns:     cfg.Agent.MaxTurns,
		WorkDir:      workDir,
		AutoCompactThreshold: cfg.Agent.AutoCompactThreshold,
		ModelBackup:          cfg.Provider.ModelBackup,
		MemoryStore:          memStore,
		MemoryDir:            memory.GetMemoryDir(workDir),
		SessionStore:         sessStore,
		SessionID:            sessionID,
		PermissionMode:       tool.ParsePermissionMode(cfg.Tools.PermissionMode),
		AllowRules:           cfg.Tools.AllowRules,
		PermissionCallback:   permCallback,
		TaskNotifier:         taskMgr,
	}

	// Hooks: 从 .edoc/settings.json 加载
	hooksCfg, hookErr := hook.LoadSettings(workDir)
	if hookErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: hooks config: %v\n", hookErr)
	}
	if len(hooksCfg) > 0 {
		runner := &hook.Runner{
			Config:  hooksCfg,
			WorkDir: workDir,
			Shell:   cfg.Tools.Shell,
		}
		// Wire PromptEvaluator for type=prompt hooks
		runner.PromptEval = buildPromptEvaluator(p, cfg.Provider.Model)
		agentCfg.HookRunner = runner
	}

	// LSP: 从 .edoc/settings.json 加载 lsp_servers 配置
	lspConfigs, lspErr := lsp.LoadLSPSettings(workDir)
	if lspErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: LSP config: %v\n", lspErr)
	}
	if len(lspConfigs) > 0 {
		lspManager := lsp.NewManager(workDir)
		if err := lspManager.Initialize(lspConfigs); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: LSP init: %v\n", err)
		} else {
			reg.Register(&tool.LSPTool{Manager: lspManager})
			agentCfg.LSPManager = lspManager
		}
	}

	// Wire Agent tool with subagent resolver (references the config being built)
	resolver := agent.NewSubagentResolver(agentCfg)

	// Team management: 对标 Claude Code 的 TeamCreate + TeammateMailbox
	teamMgr := team.NewManager(agentCfg)
	reg.Register(&tool.TeamCreateTool{Manager: teamMgr})
	reg.Register(&tool.TeamDeleteTool{Manager: teamMgr})
	reg.Register(&tool.SendMessageTool{Manager: teamMgr})
	agentCfg.TeamInbox = teamMgr.LeadInbox()

	reg.Register(&tool.AgentTool{Resolver: resolver, TeamMgr: teamMgr})

	return agentCfg, taskMgr, teamMgr
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

// buildAskCallback wraps a PermissionCallback into an AskUserQuestion callback.
// Prints the question and reads a free-form answer from stdin.
func buildAskCallback(scanner *bufio.Scanner) func(string) (string, error) {
	return func(question string) (string, error) {
		fmt.Printf("\n  [Question] %s\n  > ", question)
		if !scanner.Scan() {
			return "", nil
		}
		return strings.TrimSpace(scanner.Text()), nil
	}
}

// buildPromptEvaluator creates a PromptEvaluator for prompt-type hooks.
// Calls the LLM with a simple system prompt asking for {"ok": true/false, "reason": "..."}.
// 对标 execPromptHook.ts
func buildPromptEvaluator(p provider.Provider, defaultModel string) hook.PromptEvaluator {
	return func(ctx context.Context, promptText string, model string) (bool, string, error) {
		if model == "" {
			model = defaultModel
		}
		sysPrompt := `You are evaluating a hook condition. Your response must be a JSON object:
1. If the condition is met: {"ok": true}
2. If not met: {"ok": false, "reason": "why"}`

		msgs := []message.Message{message.NewUserMessage(promptText)}
		req := provider.ChatRequest{
			Messages:     msgs,
			SystemPrompt: sysPrompt,
			Model:        model,
			MaxTokens:    1024,
		}
		streamCh, err := p.StreamChat(ctx, req)
		if err != nil {
			return false, "", err
		}
		// Consume stream to get full response
		var text string
		for evt := range streamCh {
			if evt.Type == "text_delta" {
				text += evt.Delta
			}
			if evt.Type == "message_complete" && evt.Message != nil {
				for _, block := range evt.Message.Content {
					if block.Text != nil {
						text = block.Text.Text
					}
				}
			}
		}
		// Parse JSON response
		text = strings.TrimSpace(text)
		var result struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			return false, fmt.Sprintf("failed to parse LLM response: %s", text), nil
		}
		return result.OK, result.Reason, nil
	}
}

// runOnce executes a single prompt and exits. Maps to `claude -p "..."`.
func runOnce(cfg *config.Config, pool *pgxpool.Pool, userPrompt string) {
	scanner := bufio.NewScanner(os.Stdin)
	agentCfg, taskMgr, teamMgr := buildAgentConfig(cfg, pool, "", scanner, buildPermissionCallback(scanner), nil, nil)
	defer taskMgr.Close()
	defer teamMgr.Close()
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

	workDir := cfg.Tools.WorkDir
	if workDir == "." {
		workDir, _ = os.Getwd()
	}

	// 持久化 taskMgr，跨 agent 轮次保留后台任务状态
	replTaskMgr := task.NewManager()
	defer replTaskMgr.Close()

	// 持久化 snapStore，跨 agent 轮次共享文件快照
	replSnapStore := snapshot.NewStore(workDir)

	// 创建或恢复会话
	var currentSessionID string
	if sessStore != nil {
		projectKey := memory.SanitizeProjectKey(workDir)
		sess, err := sessStore.Create(context.Background(), "", projectKey, cfg.Provider.Model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create session: %v\n", err)
		} else {
			currentSessionID = sess.ID
		}
	}

	currentModel := cfg.Provider.Model

	fmt.Println("edoc-edualc (type /help for commands)")
	fmt.Printf("model: %s, provider: %s\n", currentModel, cfg.Provider.Default)
	if currentSessionID != "" {
		fmt.Printf("session: %s\n", currentSessionID)
	}
	if pool != nil {
		fmt.Println("db: connected")
	}
	fmt.Println()

	// history tracks the current conversation for /compact and /cost
	var history []message.Message

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// ── Slash commands ──
		if input == "/quit" || input == "/exit" {
			break
		}

		if input == "/help" {
			fmt.Println(command.Help())
			continue
		}

		if input == "/clear" {
			history = nil
			fmt.Println("Conversation cleared.")
			continue
		}

		if input == "/cost" {
			fmt.Println(command.Cost(history))
			continue
		}

		if input == "/compact" {
			if len(history) == 0 {
				fmt.Println("Nothing to compact.")
				continue
			}
			p := buildProvider(cfg)
			compactCfg := compact.CompactConfig{
				Provider:  p,
				Model:     currentModel,
				MaxTokens: 8192,
			}
			result, err := compact.Compact(context.Background(), compactCfg, history, "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Compact error: %v\n", err)
			} else {
				history = result.NewMessages
				fmt.Printf("Compacted: ~%d → ~%d tokens\n", result.PreCompactTokens, result.PostCompactTokens)
			}
			continue
		}

		if strings.HasPrefix(input, "/model ") {
			newModel := strings.TrimSpace(strings.TrimPrefix(input, "/model "))
			if newModel == "" {
				fmt.Fprintf(os.Stderr, "Usage: /model <model-name>\n")
			} else {
				currentModel = newModel
				cfg.Provider.Model = newModel
				fmt.Printf("Model switched to: %s\n", currentModel)
			}
			continue
		}

		if input == "/memory" {
			fmt.Println(command.Memory(pool, workDir, cfg))
			continue
		}

		if input == "/new" {
			history = nil
			if sessStore != nil {
				projectKey := memory.SanitizeProjectKey(workDir)
				sess, err := sessStore.Create(context.Background(), "", projectKey, currentModel)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error creating session: %v\n", err)
				} else {
					currentSessionID = sess.ID
					fmt.Printf("New session: %s\n", currentSessionID)
				}
			} else {
				fmt.Println("New conversation started.")
			}
			continue
		}

		if input == "/sessions" {
			if sessStore != nil {
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
			sess, err := sessStore.Get(context.Background(), id)
			if err != nil {
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

			loaded, err := sessStore.LoadMessages(context.Background(), sess.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading session: %v\n", err)
				continue
			}
			history = loaded
			fmt.Printf("Loaded %d messages from history.\n", len(history))

			agentCfg, _, teamMgr := buildAgentConfig(cfg, pool, currentSessionID, scanner, buildPermissionCallback(scanner), replTaskMgr, replSnapStore)
			defer teamMgr.Close()
			history = runAgentLoop(scanner, agentCfg, history)
			continue
		}

		// ── P0/P1 slash commands ──
		if input == "/session" {
			fmt.Println(command.Session(currentSessionID, sessStore, currentModel))
			continue
		}

		if strings.HasPrefix(input, "/commit") {
			args := strings.Fields(strings.TrimPrefix(input, "/commit"))
			if len(args) == 0 {
				// Interactive: prompt for message
				fmt.Print("Commit message: ")
				if !scanner.Scan() {
					continue
				}
				msg := strings.TrimSpace(scanner.Text())
				if msg == "" {
					fmt.Fprintln(os.Stderr, "Commit message cannot be empty.")
					continue
				}
				args = []string{msg}
			}
			out, err := command.Commit(workDir, args)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			} else {
				fmt.Println(out)
			}
			continue
		}

		if strings.HasPrefix(input, "/diff") {
			args := strings.Fields(strings.TrimPrefix(input, "/diff"))
			out, err := command.Diff(workDir, args)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			} else {
				fmt.Println(out)
			}
			continue
		}

		if strings.HasPrefix(input, "/branch") {
			args := strings.Fields(strings.TrimPrefix(input, "/branch"))
			out, err := command.Branch(workDir, args)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			} else {
				fmt.Println(out)
			}
			continue
		}

		if input == "/init" {
			out, err := command.Init(workDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			} else {
				fmt.Println(out)
			}
			continue
		}

		if input == "/doctor" {
			fmt.Println(command.Doctor(cfg, workDir, pool))
			continue
		}

		if input == "/mcp" {
			fmt.Println(command.MCP(cfg))
			continue
		}

		if input == "/hooks" {
			out, err := command.Hooks(workDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			} else {
				fmt.Println(out)
			}
			continue
		}

		if input == "/permissions" {
			fmt.Println(command.Permissions(cfg))
			continue
		}

		if input == "/config" {
			fmt.Println(command.Config(cfg))
			continue
		}

		if input == "/tasks" {
			fmt.Println(command.Tasks(replTaskMgr))
			continue
		}

		if strings.HasPrefix(input, "/rewind") {
			args := strings.Fields(strings.TrimPrefix(strings.TrimPrefix(input, "/rewind"), " "))
			out, err := command.Rewind(replSnapStore, args)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			} else {
				fmt.Println(out)
			}
			continue
		}

		if input == "/fast" {
			newModel, out := command.Fast(cfg)
			currentModel = newModel
			fmt.Println(out)
			continue
		}

		if strings.HasPrefix(input, "/effort ") {
			level := strings.TrimSpace(strings.TrimPrefix(input, "/effort "))
			newModel, out := command.Effort(cfg, level)
			currentModel = newModel
			fmt.Println(out)
			continue
		}

		if strings.HasPrefix(input, "/review") {
			args := strings.Fields(strings.TrimPrefix(strings.TrimPrefix(input, "/review"), " "))
			diff, err := command.ReviewDiff(workDir, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				continue
			}
			reviewPrompt := "Please review the following git diff for bugs, issues, and improvements:\n\n```diff\n" + diff + "\n```"
			agentCfg, _, teamMgr := buildAgentConfig(cfg, pool, currentSessionID, scanner, buildPermissionCallback(scanner), replTaskMgr, replSnapStore)
			defer teamMgr.Close()
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			for evt := range agent.Run(ctx, agentCfg, reviewPrompt) {
				history = handleReplEvent(evt, history)
			}
			cancel()
			fmt.Println()
			continue
		}

		// ── Normal prompt ──
		agentCfg, _, teamMgr := buildAgentConfig(cfg, pool, currentSessionID, scanner, buildPermissionCallback(scanner), replTaskMgr, replSnapStore)
		defer teamMgr.Close()

		// UserPromptSubmit hooks: 在用户提交 prompt 后、agent 执行前触发
		if agentCfg.HookRunner != nil {
			hookResult, _ := agentCfg.HookRunner.RunUserPromptSubmit(context.Background(), input)
			if hookResult != nil && hookResult.Decision == "block" {
				errMsg := "Blocked by UserPromptSubmit hook"
				if len(hookResult.BlockingErrors) > 0 {
					errMsg = hookResult.BlockingErrors[0]
				}
				fmt.Fprintf(os.Stderr, "\n[hook blocked]: %s\n", errMsg)
				continue
			}
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

		var msgs []message.Message
		if len(history) > 0 {
			msgs = append(msgs, history...)
			msgs = append(msgs, message.NewUserMessage(input))
			for evt := range agent.RunWithMessages(ctx, agentCfg, msgs) {
				history = handleReplEvent(evt, history)
			}
		} else {
			for evt := range agent.Run(ctx, agentCfg, input) {
				history = handleReplEvent(evt, history)
			}
		}

		cancel()
		fmt.Println()
	}
}

// handleReplEvent prints agent events and accumulates assistant messages into history.
func handleReplEvent(evt agent.Event, history []message.Message) []message.Message {
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
	case "message_complete":
		if evt.Message != nil {
			history = append(history, *evt.Message)
		}
	}
	return history
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

	r := api.NewRouter(p, cfg, workDir, memStore, sessStore, pool)
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
// Returns the final message history.
func runAgentLoop(scanner *bufio.Scanner, agentCfg agent.Config, history []message.Message) []message.Message {
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

		history = append(history, message.NewUserMessage(input))

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

		for evt := range agent.RunWithMessages(ctx, agentCfg, history) {
			history = handleReplEvent(evt, history)
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
	return history
}
