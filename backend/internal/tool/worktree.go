package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// WorktreeSession tracks the active worktree state.
// 对标 worktree.ts:WorktreeSession
type WorktreeSession struct {
	OriginalCwd        string
	WorktreePath       string
	WorktreeName       string
	WorktreeBranch     string
	OriginalBranch     string
	OriginalHeadCommit string
}

var (
	currentWorktreeSession *WorktreeSession
	worktreeMu             sync.Mutex
)

// GetCurrentWorktreeSession returns the active worktree session, or nil.
func GetCurrentWorktreeSession() *WorktreeSession {
	worktreeMu.Lock()
	defer worktreeMu.Unlock()
	return currentWorktreeSession
}

func setWorktreeSession(s *WorktreeSession) {
	worktreeMu.Lock()
	defer worktreeMu.Unlock()
	currentWorktreeSession = s
}

// --- slug validation ---

var validSlugSegment = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const maxSlugLength = 64

// validateWorktreeSlug validates a worktree name.
// 对标 worktree.ts:validateWorktreeSlug
func validateWorktreeSlug(slug string) error {
	if len(slug) > maxSlugLength {
		return fmt.Errorf("worktree name must be %d characters or fewer (got %d)", maxSlugLength, len(slug))
	}
	for _, seg := range strings.Split(slug, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("worktree name %q must not contain \".\" or \"..\" path segments", slug)
		}
		if !validSlugSegment.MatchString(seg) {
			return fmt.Errorf("worktree name %q: each segment must contain only letters, digits, dots, underscores, and dashes", slug)
		}
	}
	return nil
}

// flattenSlug converts nested slugs: "user/feature" → "user+feature"
func flattenSlug(slug string) string {
	return strings.ReplaceAll(slug, "/", "+")
}

func worktreeBranchName(slug string) string {
	return "worktree-" + flattenSlug(slug)
}

func worktreeDir(repoRoot, slug string) string {
	return filepath.Join(repoRoot, ".claude", "worktrees", flattenSlug(slug))
}

// --- git helpers ---

// gitCmd runs a git command and returns stdout, exit code.
func gitCmd(dir string, args ...string) (string, int) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		return "", 1
	}
	return strings.TrimSpace(string(out)), 0
}

// findGitRoot returns the git repo root, or "" if not in a git repo.
func findGitRoot(dir string) string {
	out, code := gitCmd(dir, "rev-parse", "--show-toplevel")
	if code != 0 {
		return ""
	}
	return out
}

// getDefaultBranch returns the default branch name (main/master).
func getDefaultBranch(repoRoot string) string {
	// Try symbolic-ref first
	out, code := gitCmd(repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
	if code == 0 {
		// "refs/remotes/origin/main" → "main"
		parts := strings.Split(out, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	// Fallback: check if main or master exists
	if _, code := gitCmd(repoRoot, "rev-parse", "--verify", "refs/heads/main"); code == 0 {
		return "main"
	}
	if _, code := gitCmd(repoRoot, "rev-parse", "--verify", "refs/heads/master"); code == 0 {
		return "master"
	}
	return "main"
}

// getCurrentBranch returns the current branch name.
func getCurrentBranch(dir string) string {
	out, code := gitCmd(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if code != 0 {
		return ""
	}
	return out
}

// getHeadCommit returns the HEAD commit SHA.
func getHeadCommit(dir string) string {
	out, code := gitCmd(dir, "rev-parse", "HEAD")
	if code != 0 {
		return ""
	}
	return out
}

// countWorktreeChanges returns (changedFiles, commits) or (-1,-1) on error.
// 对标 ExitWorktreeTool.ts:countWorktreeChanges
func countWorktreeChanges(worktreePath, originalHeadCommit string) (int, int, bool) {
	statusOut, code := gitCmd(worktreePath, "status", "--porcelain")
	if code != 0 {
		return 0, 0, false
	}
	changedFiles := 0
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.TrimSpace(line) != "" {
			changedFiles++
		}
	}

	if originalHeadCommit == "" {
		return changedFiles, 0, false // can't count commits without baseline
	}

	revOut, code := gitCmd(worktreePath, "rev-list", "--count", originalHeadCommit+"..HEAD")
	if code != 0 {
		return changedFiles, 0, false
	}
	commits := 0
	fmt.Sscanf(strings.TrimSpace(revOut), "%d", &commits)

	return changedFiles, commits, true
}

// ============================================================
// EnterWorktreeTool
// ============================================================

// EnterWorktreeTool creates an isolated git worktree and switches the session into it.
// 对标 EnterWorktreeTool.ts
type EnterWorktreeTool struct {
	WorkDir string // current working directory, set at startup
}

type enterWorktreeInput struct {
	Name string `json:"name,omitempty"`
}

func (t *EnterWorktreeTool) Name() string { return "EnterWorktree" }

func (t *EnterWorktreeTool) Description() string {
	return "Create an isolated git worktree and switch the session into it. Use when the user explicitly asks to work in a worktree."
}

func (t *EnterWorktreeTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Optional name for the worktree. A random name is generated if not provided.",
			},
		},
	}
}

func (t *EnterWorktreeTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	var in enterWorktreeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	// Check not already in a worktree session
	if GetCurrentWorktreeSession() != nil {
		return &Result{Content: "Already in a worktree session. Use ExitWorktree first.", IsError: true}, nil
	}

	workDir := t.WorkDir
	repoRoot := findGitRoot(workDir)
	if repoRoot == "" {
		return &Result{
			Content: "Cannot create a worktree: not in a git repository.",
			IsError: true,
		}, nil
	}

	// Generate slug
	slug := in.Name
	if slug == "" {
		slug = fmt.Sprintf("session-%d", uint32(hashString(workDir+fmt.Sprint(ctx))))
	}

	if err := validateWorktreeSlug(slug); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid worktree name: %v", err), IsError: true}, nil
	}

	wtPath := worktreeDir(repoRoot, slug)
	wtBranch := worktreeBranchName(slug)
	originalBranch := getCurrentBranch(repoRoot)
	originalHead := getHeadCommit(repoRoot)

	// Check if worktree already exists (fast resume)
	existingHead, code := gitCmd(wtPath, "rev-parse", "HEAD")
	if code == 0 && existingHead != "" {
		// Worktree exists, resume it
		session := &WorktreeSession{
			OriginalCwd:        workDir,
			WorktreePath:       wtPath,
			WorktreeName:       slug,
			WorktreeBranch:     wtBranch,
			OriginalBranch:     originalBranch,
			OriginalHeadCommit: existingHead,
		}
		setWorktreeSession(session)

		return &Result{
			Content: fmt.Sprintf("Resumed existing worktree at %s on branch %s. The session is now working in the worktree. Use ExitWorktree to leave.", wtPath, wtBranch),
			Metadata: map[string]string{
				"type":          "enter_worktree",
				"worktree_path": wtPath,
			},
		}, nil
	}

	// Create .claude/worktrees/ directory
	if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
		return &Result{Content: fmt.Sprintf("Failed to create worktrees directory: %v", err), IsError: true}, nil
	}

	// Determine base branch
	defaultBranch := getDefaultBranch(repoRoot)
	baseBranch := "HEAD"
	if _, code := gitCmd(repoRoot, "rev-parse", "--verify", "refs/remotes/origin/"+defaultBranch); code == 0 {
		baseBranch = "origin/" + defaultBranch
	}

	// Create worktree: git worktree add -B <branch> <path> <base>
	_, createCode := gitCmd(repoRoot, "worktree", "add", "-B", wtBranch, wtPath, baseBranch)
	if createCode != 0 {
		return &Result{
			Content: fmt.Sprintf("Failed to create worktree at %s", wtPath),
			IsError: true,
		}, nil
	}

	session := &WorktreeSession{
		OriginalCwd:        workDir,
		WorktreePath:       wtPath,
		WorktreeName:       slug,
		WorktreeBranch:     wtBranch,
		OriginalBranch:     originalBranch,
		OriginalHeadCommit: originalHead,
	}
	setWorktreeSession(session)

	return &Result{
		Content: fmt.Sprintf("Created worktree at %s on branch %s. The session is now working in the worktree. Use ExitWorktree to leave.", wtPath, wtBranch),
		Metadata: map[string]string{
			"type":          "enter_worktree",
			"worktree_path": wtPath,
		},
	}, nil
}

func (t *EnterWorktreeTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *EnterWorktreeTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *EnterWorktreeTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *EnterWorktreeTool) IsFileEdit(_ json.RawMessage) bool        { return false }

func (t *EnterWorktreeTool) PermissionDescription(input json.RawMessage) string {
	var in enterWorktreeInput
	json.Unmarshal(input, &in)
	if in.Name != "" {
		return "Create worktree: " + in.Name
	}
	return "Create a new git worktree"
}

// ============================================================
// ExitWorktreeTool
// ============================================================

// ExitWorktreeTool exits a worktree session created by EnterWorktree.
// 对标 ExitWorktreeTool.ts
type ExitWorktreeTool struct{}

type exitWorktreeInput struct {
	Action         string `json:"action"`                    // "keep" or "remove"
	DiscardChanges bool   `json:"discard_changes,omitempty"` // required true for remove with changes
}

func (t *ExitWorktreeTool) Name() string { return "ExitWorktree" }

func (t *ExitWorktreeTool) Description() string {
	return `Exit a worktree session created by EnterWorktree and return to the original working directory.

- action "keep": leave the worktree and branch on disk
- action "remove": delete the worktree directory and branch
- discard_changes: required true when removing a worktree with uncommitted files or unmerged commits`
}

func (t *ExitWorktreeTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"keep", "remove"},
				"description": `"keep" leaves the worktree on disk; "remove" deletes both worktree and branch.`,
			},
			"discard_changes": map[string]interface{}{
				"type":        "boolean",
				"description": "Required true when action is \"remove\" and the worktree has uncommitted files or unmerged commits.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *ExitWorktreeTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in exitWorktreeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	session := GetCurrentWorktreeSession()
	if session == nil {
		return &Result{
			Content: "No-op: there is no active EnterWorktree session to exit. This tool only operates on worktrees created by EnterWorktree in the current session.",
			IsError: true,
		}, nil
	}

	if in.Action != "keep" && in.Action != "remove" {
		return &Result{Content: `action must be "keep" or "remove"`, IsError: true}, nil
	}

	// Safety check for remove: count changes
	if in.Action == "remove" && !in.DiscardChanges {
		changedFiles, commits, ok := countWorktreeChanges(session.WorktreePath, session.OriginalHeadCommit)
		if !ok {
			return &Result{
				Content: fmt.Sprintf("Could not verify worktree state at %s. Re-invoke with discard_changes: true to proceed, or use action: \"keep\".", session.WorktreePath),
				IsError: true,
			}, nil
		}
		if changedFiles > 0 || commits > 0 {
			var parts []string
			if changedFiles > 0 {
				parts = append(parts, fmt.Sprintf("%d uncommitted file(s)", changedFiles))
			}
			if commits > 0 {
				parts = append(parts, fmt.Sprintf("%d commit(s) on %s", commits, session.WorktreeBranch))
			}
			return &Result{
				Content: fmt.Sprintf("Worktree has %s. Removing will discard this work permanently. Confirm with the user, then re-invoke with discard_changes: true — or use action: \"keep\".", strings.Join(parts, " and ")),
				IsError: true,
			}, nil
		}
	}

	originalCwd := session.OriginalCwd
	wtPath := session.WorktreePath
	wtBranch := session.WorktreeBranch

	if in.Action == "keep" {
		setWorktreeSession(nil)
		return &Result{
			Content: fmt.Sprintf("Exited worktree. Your work is preserved at %s on branch %s. Session is now back in %s.", wtPath, wtBranch, originalCwd),
			Metadata: map[string]string{
				"type":         "exit_worktree",
				"original_cwd": originalCwd,
			},
		}, nil
	}

	// action == "remove"
	// Remove worktree
	gitCmd(originalCwd, "worktree", "remove", "--force", wtPath)

	// Delete the branch
	gitCmd(originalCwd, "branch", "-D", wtBranch)

	// Re-count for message
	changedFiles, commits, _ := countWorktreeChanges(wtPath, session.OriginalHeadCommit)

	setWorktreeSession(nil)

	var discardNote string
	if changedFiles > 0 || commits > 0 {
		var parts []string
		if commits > 0 {
			parts = append(parts, fmt.Sprintf("%d commit(s)", commits))
		}
		if changedFiles > 0 {
			parts = append(parts, fmt.Sprintf("%d uncommitted file(s)", changedFiles))
		}
		discardNote = " Discarded " + strings.Join(parts, " and ") + "."
	}

	return &Result{
		Content: fmt.Sprintf("Exited and removed worktree at %s.%s Session is now back in %s.", wtPath, discardNote, originalCwd),
		Metadata: map[string]string{
			"type":         "exit_worktree",
			"original_cwd": originalCwd,
		},
	}, nil
}

func (t *ExitWorktreeTool) IsReadOnly(_ json.RawMessage) bool { return false }
func (t *ExitWorktreeTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *ExitWorktreeTool) NeedsApproval(input json.RawMessage) bool {
	var in exitWorktreeInput
	json.Unmarshal(input, &in)
	return in.Action == "remove" // only remove needs approval
}
func (t *ExitWorktreeTool) IsFileEdit(_ json.RawMessage) bool { return false }

func (t *ExitWorktreeTool) PermissionDescription(input json.RawMessage) string {
	var in exitWorktreeInput
	json.Unmarshal(input, &in)
	if in.Action == "remove" {
		return "Remove worktree and delete branch"
	}
	return "Exit worktree (keep on disk)"
}

// hashString is a simple hash for generating default slug names.
func hashString(s string) int {
	h := 0
	for _, c := range s {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}
