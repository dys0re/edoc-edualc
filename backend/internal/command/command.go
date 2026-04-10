// Package command implements slash command logic shared between REPL and Web API.
// Each command returns (string, error) instead of printing directly.
package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/hook"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/dysorder/edoc-edualc/backend/internal/snapshot"
	"github.com/dysorder/edoc-edualc/backend/internal/task"
	"github.com/dysorder/edoc-edualc/backend/internal/token"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GitRun runs a git command in the given directory and returns its output.
func GitRun(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimRight(out.String(), "\n"), err
}

// Help returns the help text for all available commands.
func Help() string {
	return `Commands:
  /new                  Start a new session
  /sessions             List saved sessions
  /session              Show current session info
  /clear                Clear conversation history
  /compact              Compact conversation context
  /model <name>         Switch model
  /cost                 Show token usage estimate
  /memory               Show loaded memory
  /commit [msg]         Stage all and commit (git)
  /diff [args]          Show git diff
  /review [ref]         Review git diff with AI
  /branch [name]        List or create git branch
  /init                 Initialize .edoc/settings.json
  /doctor               Check environment and config
  /mcp                  List MCP servers
  /hooks                List configured hooks
  /permissions          Show permission mode and rules
  /config               Show current configuration
  /tasks                List background tasks
  /rewind [n]           Restore last n file snapshots (default 1)
  /rewind list          List all recorded snapshots
  /fast                 Toggle fast (backup) model
  /effort <low|med|high> Switch effort level`
}

// Config returns the current configuration (redacts secrets).
func Config(cfg *config.Config) string {
	apiKey := func(k string) string {
		if k == "" {
			return "(not set)"
		}
		if len(k) > 8 {
			return k[:4] + "..." + k[len(k)-4:]
		}
		return "****"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "provider:    %s\n", cfg.Provider.Default)
	fmt.Fprintf(&b, "model:       %s\n", cfg.Provider.Model)
	if cfg.Provider.ModelBackup != "" {
		fmt.Fprintf(&b, "model_backup: %s\n", cfg.Provider.ModelBackup)
	}
	fmt.Fprintf(&b, "anthropic_key: %s\n", apiKey(cfg.Anthropic.APIKey))
	if cfg.Anthropic.BaseURL != "" {
		fmt.Fprintf(&b, "anthropic_url: %s\n", cfg.Anthropic.BaseURL)
	}
	fmt.Fprintf(&b, "openai_key:  %s\n", apiKey(cfg.OpenAI.APIKey))
	if cfg.OpenAI.BaseURL != "" {
		fmt.Fprintf(&b, "openai_url:  %s\n", cfg.OpenAI.BaseURL)
	}
	fmt.Fprintf(&b, "work_dir:    %s\n", cfg.Tools.WorkDir)
	fmt.Fprintf(&b, "shell:       %s\n", cfg.Tools.Shell)
	fmt.Fprintf(&b, "permission:  %s\n", cfg.Tools.PermissionMode)
	fmt.Fprintf(&b, "server_port: %d\n", cfg.Server.Port)
	if cfg.Agent.MaxTurns > 0 {
		fmt.Fprintf(&b, "max_turns:   %d\n", cfg.Agent.MaxTurns)
	}
	if cfg.Agent.AutoCompactThreshold > 0 {
		fmt.Fprintf(&b, "auto_compact: %d tokens\n", cfg.Agent.AutoCompactThreshold)
	}
	dbStatus := "not connected"
	if cfg.Database.URL != "" || cfg.Database.Host != "" {
		dbStatus = fmt.Sprintf("%s:%d/%s", cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)
	}
	fmt.Fprintf(&b, "database:    %s\n", dbStatus)
	return strings.TrimRight(b.String(), "\n")
}

// Doctor checks environment and configuration.
func Doctor(cfg *config.Config, workDir string, pool *pgxpool.Pool) string {
	var b strings.Builder
	ok := true
	check := func(label string, pass bool, detail string) {
		if pass {
			fmt.Fprintf(&b, "  [ok] %s\n", label)
		} else {
			fmt.Fprintf(&b, "  [!!] %s — %s\n", label, detail)
			ok = false
		}
	}

	b.WriteString("edoc doctor\n\n")

	switch cfg.Provider.Default {
	case "anthropic":
		check("Anthropic API key", cfg.Anthropic.APIKey != "", "set ANTHROPIC_API_KEY")
	case "openai":
		check("OpenAI API key", cfg.OpenAI.APIKey != "", "set OPENAI_API_KEY")
	}

	check("Database", pool != nil, "PostgreSQL not connected (sessions/memory disabled)")

	_, wdErr := os.Stat(workDir)
	check("Work directory", wdErr == nil, fmt.Sprintf("%s not accessible", workDir))

	_, gitErr := GitRun(workDir, "rev-parse", "--git-dir")
	check("Git repository", gitErr == nil, "not a git repo")

	settingsPath := filepath.Join(workDir, ".edoc", "settings.json")
	_, settingsErr := os.Stat(settingsPath)
	check(".edoc/settings.json", settingsErr == nil, "run /init to create")

	b.WriteString("\n")
	if ok {
		b.WriteString("All checks passed.")
	} else {
		b.WriteString("Some checks failed.")
	}
	return b.String()
}

// MCP lists configured MCP servers.
func MCP(cfg *config.Config) string {
	if len(cfg.MCPServers) == 0 {
		return "No MCP servers configured.\nAdd mcp_servers to config.yaml to enable MCP."
	}

	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		srv := cfg.MCPServers[name]
		t := srv.Type
		if t == "" {
			t = "stdio"
		}
		fmt.Fprintf(&b, "  %s  [%s]", name, t)
		if srv.Command != "" {
			fmt.Fprintf(&b, "  %s", srv.Command)
		} else if srv.URL != "" {
			fmt.Fprintf(&b, "  %s", srv.URL)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Hooks lists configured hooks.
func Hooks(workDir string) (string, error) {
	hooksCfg, err := hook.LoadSettings(workDir)
	if err != nil {
		return "", fmt.Errorf("error loading hooks: %v", err)
	}
	if len(hooksCfg) == 0 {
		return fmt.Sprintf("No hooks configured.\nEdit %s to add hooks.", hook.SettingsPath(workDir)), nil
	}

	events := make([]string, 0, len(hooksCfg))
	for ev := range hooksCfg {
		events = append(events, string(ev))
	}
	sort.Strings(events)

	var b strings.Builder
	for _, ev := range events {
		matchers := hooksCfg[hook.HookEvent(ev)]
		fmt.Fprintf(&b, "%s:\n", ev)
		for _, m := range matchers {
			matcher := m.Matcher
			if matcher == "" {
				matcher = "*"
			}
			for _, h := range m.Hooks {
				async := ""
				if h.Async {
					async = " [async]"
				}
				switch h.Type {
				case "command":
					fmt.Fprintf(&b, "  [%s] command: %s%s\n", matcher, h.Command, async)
				case "http":
					fmt.Fprintf(&b, "  [%s] http: %s%s\n", matcher, h.URL, async)
				case "prompt":
					fmt.Fprintf(&b, "  [%s] prompt: %s%s\n", matcher, h.Prompt, async)
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Permissions shows current permission mode and allow rules.
func Permissions(cfg *config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Permission mode: %s\n", cfg.Tools.PermissionMode)
	if len(cfg.Tools.AllowRules) == 0 {
		b.WriteString("Allow rules: (none)")
	} else {
		b.WriteString("Allow rules:\n")
		for _, r := range cfg.Tools.AllowRules {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Tasks lists background tasks.
func Tasks(taskMgr *task.Manager) string {
	if taskMgr == nil {
		return "No task manager available."
	}
	tasks := taskMgr.List()
	if len(tasks) == 0 {
		return "No background tasks."
	}
	var b strings.Builder
	for _, t := range tasks {
		end := ""
		if t.EndTime != nil {
			end = fmt.Sprintf(" → %s", t.EndTime.Format("15:04:05"))
		}
		fmt.Fprintf(&b, "  %s  [%s]  %s  %s%s\n",
			t.ID, t.Status, t.StartTime.Format("15:04:05"), t.Description, end)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Session shows current session info.
func Session(sessionID string, sessStore *session.Store, model string) string {
	if sessionID == "" {
		return fmt.Sprintf("No active session (database not available or not started).\nModel: %s", model)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session: %s\n", sessionID)
	fmt.Fprintf(&b, "Model:   %s\n", model)
	if sessStore != nil {
		sess, err := sessStore.Get(context.Background(), sessionID)
		if err == nil {
			if sess.Title != "" {
				fmt.Fprintf(&b, "Title:   %s\n", sess.Title)
			}
			fmt.Fprintf(&b, "Created: %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05"))
			fmt.Fprintf(&b, "Updated: %s\n", sess.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Cost estimates token usage for given messages.
func Cost(msgs []message.Message) string {
	est := token.EstimateMessages(msgs)
	return fmt.Sprintf("Estimated tokens in context: ~%d", est)
}

// Memory shows loaded memory content.
func Memory(pool *pgxpool.Pool, workDir string, cfg *config.Config) string {
	memStore := buildMemoryStore(pool, workDir)
	var section string
	if memStore != nil {
		section = memory.BuildMemoryPromptSectionPG(context.Background(), memStore)
	}
	if section == "" {
		memDir := cfg.Tools.MemoryDir
		if memDir == "" {
			memDir = memory.GetMemoryDir(workDir)
		}
		section = memory.BuildMemoryPromptSection(memDir)
	}
	if section == "" {
		return "No memory loaded."
	}
	return section
}

// buildMemoryStore creates a MemoryStore from pool if available.
func buildMemoryStore(pool *pgxpool.Pool, workDir string) *memory.Store {
	if pool == nil {
		return nil
	}
	projectKey := memory.SanitizeProjectKey(workDir)
	store := memory.NewStore(pool, "", projectKey)
	return store
}

// Diff shows git diff.
func Diff(workDir string, args []string) (string, error) {
	gitArgs := []string{"diff"}
	if len(args) > 0 {
		gitArgs = append(gitArgs, args...)
	}
	out, err := GitRun(workDir, gitArgs...)
	if err != nil && out == "" {
		return "", fmt.Errorf("git diff: %v", err)
	}
	if out == "" {
		return "No changes.", nil
	}
	return out, nil
}

// Branch lists or creates git branches.
func Branch(workDir string, args []string) (string, error) {
	if len(args) == 0 {
		out, err := GitRun(workDir, "branch", "-v")
		if err != nil {
			return "", fmt.Errorf("git branch: %v", err)
		}
		return out, nil
	}
	out, err := GitRun(workDir, "checkout", "-b", args[0])
	if err != nil {
		return "", fmt.Errorf("git checkout -b: %v\n%s", err, out)
	}
	return out, nil
}

// Commit stages all changes and commits with a message.
// Unlike the REPL version, this does not prompt for a message interactively.
func Commit(workDir string, args []string) (string, error) {
	status, err := GitRun(workDir, "status", "--short")
	if err != nil {
		return "", fmt.Errorf("git status: %v", err)
	}
	if status == "" {
		return "Nothing to commit.", nil
	}

	if len(args) == 0 {
		return "", fmt.Errorf("commit message required (usage: /commit <message>)")
	}
	msg := strings.Join(args, " ")

	if out, err := GitRun(workDir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %v\n%s", err, out)
	}
	out, err := GitRun(workDir, "commit", "-m", msg)
	if err != nil {
		return "", fmt.Errorf("git commit: %v\n%s", err, out)
	}
	return status + "\n" + out, nil
}

// Init initializes .edoc/settings.json if not present.
func Init(workDir string) (string, error) {
	edocDir := filepath.Join(workDir, ".edoc")
	settingsPath := filepath.Join(edocDir, "settings.json")

	if _, err := os.Stat(settingsPath); err == nil {
		return fmt.Sprintf("Already initialized: %s", settingsPath), nil
	}

	if err := os.MkdirAll(edocDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %v", edocDir, err)
	}

	template := "{\n  \"hooks\": {}\n}\n"
	if err := os.WriteFile(settingsPath, []byte(template), 0644); err != nil {
		return "", fmt.Errorf("write %s: %v", settingsPath, err)
	}
	return fmt.Sprintf("Created %s", settingsPath), nil
}

// Rewind restores file snapshots.
func Rewind(store *snapshot.Store, args []string) (string, error) {
	if store == nil {
		return "Snapshot system not available.", nil
	}

	if len(args) > 0 && args[0] == "list" {
		snaps := store.List()
		if len(snaps) == 0 {
			return "No snapshots recorded.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d snapshot(s):\n", len(snaps))
		for _, s := range snaps {
			exists := "(deleted)"
			if s.BlobHash != "" {
				exists = s.BlobHash[:8]
			}
			fmt.Fprintf(&b, "  %s  %s  [%s]  %s\n", s.ID, s.CreatedAt.Format("15:04:05"), s.ToolName, exists+" "+s.FilePath)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}

	n := 1
	if len(args) > 0 {
		fmt.Sscanf(args[0], "%d", &n)
		if n < 1 {
			n = 1
		}
	}

	restored, errs := store.RewindN(n)
	var b strings.Builder
	for _, s := range restored {
		fmt.Fprintf(&b, "  restored  %s  (%s)\n", s.FilePath, s.ToolName)
	}
	for _, e := range errs {
		fmt.Fprintf(&b, "  error: %v\n", e)
	}
	if len(restored) == 0 && len(errs) == 0 {
		return "No snapshots to rewind.", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Fast toggles the fast (backup) model. Returns the new model name and a status message.
func Fast(cfg *config.Config) (newModel string, output string) {
	if cfg.Provider.ModelBackup != "" {
		cfg.Provider.Model, cfg.Provider.ModelBackup = cfg.Provider.ModelBackup, cfg.Provider.Model
		return cfg.Provider.Model, fmt.Sprintf("Fast mode: switched to %s", cfg.Provider.Model)
	}
	return cfg.Provider.Model, "No backup model configured (set provider.model_backup in config)."
}

// Effort switches model effort level. Returns the new model name and a status message.
func Effort(cfg *config.Config, level string) (newModel string, output string) {
	switch strings.ToLower(level) {
	case "low":
		if cfg.Provider.ModelBackup != "" {
			cfg.Provider.Model = cfg.Provider.ModelBackup
			return cfg.Provider.Model, fmt.Sprintf("Effort low: switched to %s", cfg.Provider.Model)
		}
		return cfg.Provider.Model, "No backup model configured for low effort."
	case "medium", "med":
		return cfg.Provider.Model, fmt.Sprintf("Effort medium: using %s", cfg.Provider.Model)
	case "high":
		return cfg.Provider.Model, fmt.Sprintf("Effort high: using %s (no higher model configured)", cfg.Provider.Model)
	default:
		return cfg.Provider.Model, "Usage: /effort <low|medium|high>"
	}
}

// ReviewDiff returns the diff to review.
func ReviewDiff(workDir string, args []string) (string, error) {
	gitArgs := []string{"diff"}
	if len(args) > 0 {
		gitArgs = append(gitArgs, args...)
	} else {
		gitArgs = append(gitArgs, "HEAD")
	}
	out, err := GitRun(workDir, gitArgs...)
	if err != nil && out == "" {
		return "", fmt.Errorf("git diff: %v", err)
	}
	if out == "" {
		return "", fmt.Errorf("no changes to review")
	}
	return out, nil
}
