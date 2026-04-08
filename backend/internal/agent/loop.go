package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/compact"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
	"github.com/dysorder/edoc-edualc/backend/internal/token"
)

const (
	maxOutputTokensRecoveryLimit = 3 // 截断续写最大恢复次数，对标 query.ts:164
	maxServerErrorRetries        = 3 // 5xx 最大重试次数，对标 withRetry.ts:52
	serverErrorBaseDelay         = 500 * time.Millisecond
)

// Run executes the agent loop with a new user prompt.
// Maps to query.ts:241 queryLoop.
func Run(ctx context.Context, cfg Config, userPrompt string) <-chan Event {
	return runWithMessages(ctx, cfg, []message.Message{message.NewUserMessage(userPrompt)})
}

// RunWithMessages starts the loop with pre-existing message history (for session resume).
func RunWithMessages(ctx context.Context, cfg Config, messages []message.Message) <-chan Event {
	return runWithMessages(ctx, cfg, messages)
}

func runWithMessages(ctx context.Context, cfg Config, messages []message.Message) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		loop(ctx, cfg, messages, ch)
	}()
	return ch
}

// loop is the main agent loop. 对标 query.ts:306 while(true).
func loop(ctx context.Context, cfg Config, messages []message.Message, ch chan<- Event) {
	state := &State{
		Messages: messages,
	}

	toolSchemas := ToolSchemas(cfg.Registry)

	// 当前使用的模型（可能被 fallback 切换）
	currentModel := cfg.Model

	// 持久化：记录 loop 开始前的消息数量
	prevMsgCount := len(state.Messages)

	// 如果是 session resume，先持久化新追加的 user message
	persistNewMessages(ctx, cfg, state, prevMsgCount, ch)

	for {
		// ── 1. Context 取消检查 ──
		if ctx.Err() != nil {
			ch <- Event{Type: "error", Error: ctx.Err()}
			return
		}

		// ── 2. MaxTurns 检查 ──
		// 对标 query.ts:1705
		if cfg.MaxTurns > 0 && state.TurnCount >= cfg.MaxTurns {
			ch <- Event{
				Type: "max_turns_reached",
				Delta: fmt.Sprintf("Reached maximum turns (%d)", cfg.MaxTurns),
			}
			return
		}

		state.TurnCount++
		prevMsgCount = len(state.Messages)

		// ── 3. Microcompact: 清空旧 tool_result 内容 ──
		// 对标 query.ts:414 deps.microcompact call
		state.Messages = compact.Microcompact(state.Messages, 10)

		// ── 4. Auto compact: token 超阈值时压缩 ──
		// 对标 query.ts:454 deps.autocompact call
		if cfg.AutoCompactThreshold > 0 {
			tokenCount := token.EstimateMessages(state.Messages)
			if tokenCount > cfg.AutoCompactThreshold {
				// PreCompact hook
				if cfg.HookRunner != nil {
					cfg.HookRunner.RunPreCompact(ctx)
				}
				compactCfg := compact.CompactConfig{
					Provider:  cfg.Provider,
					Model:     currentModel,
					MaxTokens: 8192,
				}
				result, err := compact.Compact(ctx, compactCfg, state.Messages, "")
				if err == nil {
					state.Messages = result.NewMessages
					prevMsgCount = 0 // compact 后全部是新消息
					ch <- Event{Type: "compacted", Message: &message.Message{
						Role: message.RoleSystem,
						Content: []message.ContentBlock{{
							Type: message.BlockText,
							Text: &message.TextBlock{Text: fmt.Sprintf(
								"Auto-compacted: %d → %d tokens",
								result.PreCompactTokens,
								result.PostCompactTokens,
							)},
						}},
					}}
					// PostCompact hook
					if cfg.HookRunner != nil {
						cfg.HookRunner.RunPostCompact(ctx)
					}
				}
			}
		}

		// ── 5. 调用 Provider ──
		req := provider.ChatRequest{
			Messages:     state.Messages,
			SystemPrompt: cfg.SystemPrompt,
			Tools:        toolSchemas,
			Model:        currentModel,
			MaxTokens:    cfg.MaxTokens,
		}

		streamCh, err := cfg.Provider.StreamChat(ctx, req)
		if err != nil {
			if handleProviderError(ctx, cfg, state, ch, err, &currentModel) {
				continue
			}
			return
		}

		// ── 6. 消费流式响应 ──
		assistantMsg, toolUseBlocks, stopReason, recovered := consumeStream(streamCh, ctx, cfg, state, ch, &currentModel)
		if recovered {
			continue // 流式错误已恢复，重试
		}
		if assistantMsg == nil && !recovered {
			// consumeStream 已经发出了 error event（或者正常完成但无消息）
			if len(toolUseBlocks) == 0 {
				// 无消息也无 tool_use → 已在 consumeStream 中处理
				return
			}
		}

		// ── 7. 用户中断检查 ──
		if ctx.Err() != nil {
			for _, tu := range toolUseBlocks {
				state.Messages = append(state.Messages,
					message.NewToolResultMessage(tu.ID, "Interrupted by user", true))
			}
			persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
			ch <- Event{Type: "error", Error: ctx.Err()}
			return
		}

		if assistantMsg == nil {
			return // consumeStream 已发出错误
		}

		state.Messages = append(state.Messages, *assistantMsg)

		// ── 8. Max output tokens 恢复 ──
		// 对标 query.ts:1185-1256
		if provider.IsMaxTokensStop(stopReason) && state.MaxOutputTokensRecoveryCount < maxOutputTokensRecoveryLimit {
			state.MaxOutputTokensRecoveryCount++
			state.Messages = append(state.Messages, message.NewUserMessage(
				"Output token limit hit. Resume directly — pick up mid-thought. Break remaining work into smaller pieces.",
			))
			ch <- Event{
				Type: "max_tokens_recovery",
				Delta: fmt.Sprintf("Response truncated, auto-continuing (%d/%d)",
					state.MaxOutputTokensRecoveryCount, maxOutputTokensRecoveryLimit),
			}
			// 持久化截断前的消息
			persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
			continue
		}

		if provider.IsMaxTokensStop(stopReason) {
			ch <- Event{
				Type: "warning",
				Delta: fmt.Sprintf("Response truncated and recovery limit (%d) reached", maxOutputTokensRecoveryLimit),
			}
		}

		// ── 9. 无 tool 调用 → 对话结束 ──
		if len(toolUseBlocks) == 0 {
			// Stop hooks: 在 agent 即将结束响应时触发
			// 对标 hooks.ts executeStopHooks — exit code 2 注入 blocking error 继续对话
			if cfg.HookRunner != nil {
				stopResult, _ := cfg.HookRunner.RunStop(ctx)
				if stopResult != nil && len(stopResult.BlockingErrors) > 0 {
					errMsg := strings.Join(stopResult.BlockingErrors, "\n")
					state.Messages = append(state.Messages,
						message.NewUserMessage("Stop hook blocking error: "+errMsg))
					ch <- Event{Type: "warning", Delta: "[stop hook: " + errMsg + "]"}
					persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
					continue // 继续循环，让 LLM 处理 blocking error
				}
			}
			persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
			ch <- Event{Type: "turn_complete"}
			return
		}


		// Permission check
		if cfg.PermissionMode != tool.PermissionBypass && cfg.PermissionCallback != nil {
			var approved []*message.ToolUseBlock
			for _, tu := range toolUseBlocks {
				t, tErr := cfg.Registry.Get(tu.Name)
				if tErr != nil {
					approved = append(approved, tu)
					continue
				}
				decision := tool.CheckPermission(cfg.PermissionMode, cfg.AllowRules, t, tu.Input)
				switch decision {
				case tool.DecisionAllow:
					approved = append(approved, tu)
				case tool.DecisionDeny:
					state.Messages = append(state.Messages,
						message.NewToolResultMessage(tu.ID, "Permission denied", true))
					ch <- Event{Type: "tool_result", ToolName: tu.Name,
						ToolResult: &tool.Result{Content: "Permission denied", IsError: true}}
					// PermissionDenied hook
					if cfg.HookRunner != nil {
						cfg.HookRunner.RunPermissionDenied(ctx, tu.Name, tu.ID, tu.Input, "rule-based deny")
					}
				case tool.DecisionAsk:
					desc := t.PermissionDescription(tu.Input)
					ch <- Event{Type: "permission_request",
						PermissionToolName: tu.Name, PermissionDesc: desc}
					allowed, cbErr := cfg.PermissionCallback(tu.Name, desc)
					if cbErr != nil || !allowed {
						state.Messages = append(state.Messages,
							message.NewToolResultMessage(tu.ID, "Permission denied by user", true))
						ch <- Event{Type: "tool_result", ToolName: tu.Name,
							ToolResult: &tool.Result{Content: "Permission denied by user", IsError: true}}
						// PermissionDenied hook
						if cfg.HookRunner != nil {
							cfg.HookRunner.RunPermissionDenied(ctx, tu.Name, tu.ID, tu.Input, "user denied")
						}
					} else {
						approved = append(approved, tu)
					}
				}
			}
			toolUseBlocks = approved
			if len(toolUseBlocks) == 0 {
				persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
				ch <- Event{Type: "turn_complete"}
				return
			}
		}

		// ── 10. Plan mode: 拦截写操作工具 ──
		// 在 plan mode 下，只允许 EnterPlanMode / ExitPlanMode / 只读工具。
		if state.PlanMode {
			var allowed []*message.ToolUseBlock
			for _, tu := range toolUseBlocks {
				if tu.Name == "ExitPlanMode" || tu.Name == "EnterPlanMode" {
					allowed = append(allowed, tu)
					continue
				}
				t, tErr := cfg.Registry.Get(tu.Name)
				if tErr != nil || !t.IsReadOnly(tu.Input) {
					errMsg := fmt.Sprintf("Tool %q is not allowed in plan mode. Only read-only tools and ExitPlanMode are permitted.", tu.Name)
					state.Messages = append(state.Messages,
						message.NewToolResultMessage(tu.ID, errMsg, true))
					ch <- Event{Type: "tool_result", ToolName: tu.Name,
						ToolResult: &tool.Result{Content: errMsg, IsError: true}}
				} else {
					allowed = append(allowed, tu)
				}
			}
			toolUseBlocks = allowed
			if len(toolUseBlocks) == 0 {
				persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
				continue
			}
		}

		// ── 11. PreToolUse hooks ──
		// 对标 toolHooks.ts:runPreToolUseHooks — 在 tool 执行前触发，可 block/修改 input
		if cfg.HookRunner != nil {
			var hookApproved []*message.ToolUseBlock
			for _, tu := range toolUseBlocks {
				hookResult, hookErr := cfg.HookRunner.RunPreToolUse(ctx, tu.Name, tu.ID, tu.Input)
				if hookErr != nil {
					ch <- Event{Type: "warning", Delta: fmt.Sprintf("[PreToolUse hook error: %v]", hookErr)}
					hookApproved = append(hookApproved, tu)
					continue
				}
				if hookResult == nil {
					hookApproved = append(hookApproved, tu)
					continue
				}

				// Block decision → deny tool execution
				if hookResult.Decision == "block" {
					errMsg := "Blocked by PreToolUse hook"
					if len(hookResult.BlockingErrors) > 0 {
						errMsg = strings.Join(hookResult.BlockingErrors, "; ")
					}
					state.Messages = append(state.Messages,
						message.NewToolResultMessage(tu.ID, errMsg, true))
					ch <- Event{Type: "tool_result", ToolName: tu.Name,
						ToolResult: &tool.Result{Content: errMsg, IsError: true}}
					continue
				}

				// UpdatedInput → replace tool input
				if hookResult.UpdatedInput != nil {
					updated, marshalErr := json.Marshal(hookResult.UpdatedInput)
					if marshalErr == nil {
						tu.Input = updated
					}
				}

				// AdditionalContext → inject as system-reminder
				if len(hookResult.AdditionalContext) > 0 {
					ctx := strings.Join(hookResult.AdditionalContext, "\n")
					state.Messages = append(state.Messages,
						message.NewUserMessage("<system-reminder>PreToolUse hook: "+ctx+"</system-reminder>"))
				}

				hookApproved = append(hookApproved, tu)
			}
			toolUseBlocks = hookApproved
			if len(toolUseBlocks) == 0 {
				persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
				continue
			}
		}

		// ── 12. 执行 tools ──
		results := executeTools(ctx, cfg.Registry, toolUseBlocks)

		for _, r := range results {
			state.Messages = append(state.Messages, r.msg)
			ch <- Event{Type: "tool_result", ToolResult: &r.result, ToolName: r.toolName}

			// Skill inline 执行：把 skill 内容注入为 user message，LLM 下一轮跟随执行
			if r.result.Metadata["type"] == "skill_inline" && r.result.Content != "" {
				state.Messages = append(state.Messages, message.NewUserMessage(r.result.Content))
			}

			// Plan mode 状态切换
			switch r.result.Metadata["type"] {
			case "enter_plan_mode":
				state.PlanMode = true
				ch <- Event{Type: "warning", Delta: "[plan mode: on]"}
			case "exit_plan_mode":
				state.PlanMode = false
				ch <- Event{Type: "warning", Delta: "[plan mode: off]"}
			case "enter_worktree":
				// 切换 BashTool 工作目录到 worktree
				if wtPath := r.result.Metadata["worktree_path"]; wtPath != "" {
					if bashTool, err := cfg.Registry.Get("Bash"); err == nil {
						if bt, ok := bashTool.(*tool.BashTool); ok {
							bt.SetWorkDir(wtPath)
						}
					}
					// 同步更新 EnterWorktreeTool 的 WorkDir
					if wtTool, err := cfg.Registry.Get("EnterWorktree"); err == nil {
						if ewt, ok := wtTool.(*tool.EnterWorktreeTool); ok {
							ewt.WorkDir = wtPath
						}
					}
					ch <- Event{Type: "warning", Delta: "[worktree: entered " + wtPath + "]"}
				}
			case "exit_worktree":
				// 恢复 BashTool 工作目录到原始 CWD
				if origCwd := r.result.Metadata["original_cwd"]; origCwd != "" {
					if bashTool, err := cfg.Registry.Get("Bash"); err == nil {
						if bt, ok := bashTool.(*tool.BashTool); ok {
							bt.SetWorkDir(origCwd)
						}
					}
					if wtTool, err := cfg.Registry.Get("EnterWorktree"); err == nil {
						if ewt, ok := wtTool.(*tool.EnterWorktreeTool); ok {
							ewt.WorkDir = origCwd
						}
					}
					ch <- Event{Type: "warning", Delta: "[worktree: exited, back to " + origCwd + "]"}
				}
			}

			// ── 13. PostToolUse / PostToolUseFailure hooks ──
			if cfg.HookRunner != nil {
				if r.result.IsError {
					// PostToolUseFailure hook — tool 执行失败时触发
					cfg.HookRunner.RunPostToolUseFailure(ctx, r.toolName, "", nil, r.result.Content, false)
				} else {
					// PostToolUse hook — tool 执行成功后触发
					postResult, _ := cfg.HookRunner.RunPostToolUse(ctx, r.toolName, "", nil, &r.result)
					if postResult != nil {
						if len(postResult.AdditionalContext) > 0 {
							ctxMsg := strings.Join(postResult.AdditionalContext, "\n")
							state.Messages = append(state.Messages,
								message.NewUserMessage("<system-reminder>PostToolUse hook: "+ctxMsg+"</system-reminder>"))
						}
						if len(postResult.BlockingErrors) > 0 {
							errMsg := strings.Join(postResult.BlockingErrors, "; ")
							state.Messages = append(state.Messages,
								message.NewUserMessage("PostToolUse hook blocking error: "+errMsg))
							ch <- Event{Type: "warning", Delta: "[PostToolUse hook: " + errMsg + "]"}
						}
					}
				}
			}

			// ── 14. LSP file sync — Edit/Write 后通知 LSP server ──
			if cfg.LSPManager != nil && !r.result.IsError {
				if r.toolName == "Write" || r.toolName == "Edit" {
					var fileInput struct {
						FilePath string `json:"file_path"`
					}
					// 找到对应的 toolUseBlock 获取 input
					for _, b := range toolUseBlocks {
						if b.Name == r.toolName {
							json.Unmarshal(b.Input, &fileInput)
							break
						}
					}
					if fileInput.FilePath != "" {
						if content, readErr := os.ReadFile(fileInput.FilePath); readErr == nil {
							cfg.LSPManager.ChangeFile(fileInput.FilePath, string(content))
							cfg.LSPManager.SaveFile(fileInput.FilePath)
						}
					}
				}
			}
		}

		// Tool 执行成功，重置恢复计数
		state.MaxOutputTokensRecoveryCount = 0

		// 持久化本轮新增的所有消息（assistant + tool results）
		persistNewMessages(ctx, cfg, state, prevMsgCount, ch)
	}
}

// persistNewMessages 持久化从 prevMsgCount 开始的新增消息到 session store。
func persistNewMessages(ctx context.Context, cfg Config, state *State, prevMsgCount int, ch chan<- Event) {
	if cfg.SessionStore == nil || cfg.SessionID == "" {
		return
	}
	newMsgs := state.Messages[prevMsgCount:]
	if len(newMsgs) == 0 {
		return
	}
	if err := cfg.SessionStore.AppendMessages(ctx, cfg.SessionID, newMsgs); err != nil {
		ch <- Event{Type: "warning", Delta: "failed to persist messages: " + err.Error()}
	}
}

// consumeStream 消费流式响应，返回 assistant 消息、tool_use blocks、stop reason。
// 如果遇到可恢复错误，返回 recovered=true，调用方应 continue 重试。
func consumeStream(
	streamCh <-chan provider.StreamEvent,
	ctx context.Context,
	cfg Config,
	state *State,
	ch chan<- Event,
	currentModel *string,
) (assistantMsg *message.Message, toolUseBlocks []*message.ToolUseBlock, stopReason string, recovered bool) {
	for evt := range streamCh {
		switch evt.Type {
		case "text_delta":
			ch <- Event{Type: "text_delta", Delta: evt.Delta}
		case "thinking_delta":
			ch <- Event{Type: "thinking_delta", Delta: evt.Delta}
		case "tool_use":
			ch <- Event{Type: "tool_use", ToolName: evt.ToolUse.Name, ToolInput: string(evt.ToolUse.Input)}
			toolUseBlocks = append(toolUseBlocks, evt.ToolUse)
		case "message_complete":
			assistantMsg = evt.Message
			stopReason = evt.StopReason
			ch <- Event{Type: "message_complete", Message: evt.Message}
		case "error":
			if handleProviderError(ctx, cfg, state, ch, evt.Error, currentModel) {
				return nil, nil, "", true
			}
			return nil, nil, "", false
		}
	}
	return assistantMsg, toolUseBlocks, stopReason, false
}

// handleProviderError 处理 Provider 错误，尝试恢复。
// 返回 true 表示已恢复（应 continue 重试），false 表示不可恢复（已发出 error event）。
// 对标 query.ts:893-953 的 fallback + 错误恢复逻辑。
func handleProviderError(ctx context.Context, cfg Config, state *State, ch chan<- Event, err error, currentModel *string) bool {
	errType := provider.ClassifyError(err)

	switch errType {
	case provider.ErrorRateLimit:
		// 模型限流 → 尝试 fallback model
		if cfg.ModelBackup != "" && !state.HasAttemptedFallback {
			state.HasAttemptedFallback = true
			*currentModel = cfg.ModelBackup

			// Strip thinking blocks from previous model (signature is model-bound)
			state.Messages = message.StripThinkingBlocks(state.Messages, cfg.ModelBackup)

			ch <- Event{
				Type: "warning",
				Delta: fmt.Sprintf("Rate limited on %s, falling back to %s", cfg.Model, cfg.ModelBackup),
			}
			return true
		}
		ch <- Event{Type: "error", Error: fmt.Errorf("rate limited and no fallback available: %w", err)}
		return false

	case provider.ErrorPromptTooLong:
		// 上下文超限 → 尝试 compact 恢复
		if !state.HasAttemptedCompactRecovery {
			state.HasAttemptedCompactRecovery = true
			compactCfg := compact.CompactConfig{
				Provider:  cfg.Provider,
				Model:     *currentModel,
				MaxTokens: 8192,
			}
			result, compactErr := compact.Compact(ctx, compactCfg, state.Messages, "")
			if compactErr == nil {
				state.Messages = result.NewMessages
				ch <- Event{Type: "compacted", Message: &message.Message{
					Role: message.RoleSystem,
					Content: []message.ContentBlock{{
						Type: message.BlockText,
						Text: &message.TextBlock{Text: fmt.Sprintf(
							"Emergency compact (prompt too long): %d → %d tokens",
							result.PreCompactTokens, result.PostCompactTokens,
						)},
					}},
				}}
				return true
			}
		}
		ch <- Event{Type: "error", Error: provider.ErrPromptTooLong}
		return false

	case provider.ErrorServer:
		// 5xx server error → 指数退避重试
		// 对标 withRetry.ts:getRetryDelay (base 500ms × 2^attempt + jitter)
		if state.ServerErrorRetries < maxServerErrorRetries {
			state.ServerErrorRetries++
			attempt := state.ServerErrorRetries
			delay := serverErrorBaseDelay * time.Duration(1<<attempt)
			jitter := time.Duration(rand.Intn(250)) * time.Millisecond
			delay += jitter

			ch <- Event{
				Type:  "warning",
				Delta: fmt.Sprintf("Server error, retrying in %v (%d/%d)", delay, attempt, maxServerErrorRetries),
			}

			select {
			case <-ctx.Done():
				return false
			case <-time.After(delay):
				return true // retry
			}
		}
		ch <- Event{Type: "error", Error: fmt.Errorf("server error after %d retries: %w", maxServerErrorRetries, err)}
		return false

	case provider.ErrorAuth:
		ch <- Event{Type: "error", Error: fmt.Errorf("authentication failed: %w", err)}
		return false

	default:
		ch <- Event{Type: "error", Error: err}
		return false
	}
}

// --- Tool execution ---

type toolExecResult struct {
	toolName string
	msg      message.Message
	result   tool.Result
}

// executeTools runs tool calls, respecting concurrency safety.
// Maps to toolOrchestration.ts:19 runTools.
func executeTools(ctx context.Context, reg *tool.Registry, blocks []*message.ToolUseBlock) []toolExecResult {
	type batch struct {
		concurrent bool
		blocks     []*message.ToolUseBlock
	}

	var batches []batch
	for _, b := range blocks {
		t, err := reg.Get(b.Name)
		isSafe := err == nil && t.IsConcurrencySafe(b.Input)

		if isSafe && len(batches) > 0 && batches[len(batches)-1].concurrent {
			batches[len(batches)-1].blocks = append(batches[len(batches)-1].blocks, b)
		} else {
			batches = append(batches, batch{concurrent: isSafe, blocks: []*message.ToolUseBlock{b}})
		}
	}

	var results []toolExecResult
	for _, bat := range batches {
		if bat.concurrent && len(bat.blocks) > 1 {
			results = append(results, runConcurrent(ctx, reg, bat.blocks)...)
		} else {
			for _, b := range bat.blocks {
				results = append(results, runSingle(ctx, reg, b))
			}
		}
	}
	return results
}

func runConcurrent(ctx context.Context, reg *tool.Registry, blocks []*message.ToolUseBlock) []toolExecResult {
	results := make([]toolExecResult, len(blocks))
	var wg sync.WaitGroup
	for i, b := range blocks {
		wg.Add(1)
		go func(idx int, block *message.ToolUseBlock) {
			defer wg.Done()
			results[idx] = runSingle(ctx, reg, block)
		}(i, b)
	}
	wg.Wait()
	return results
}

func runSingle(ctx context.Context, reg *tool.Registry, block *message.ToolUseBlock) toolExecResult {
	t, err := reg.Get(block.Name)
	if err != nil {
		errMsg := fmt.Sprintf("Error: No such tool available: %s", block.Name)
		return toolExecResult{
			toolName: block.Name,
			msg:      message.NewToolResultMessage(block.ID, errMsg, true),
			result:   tool.Result{Content: errMsg, IsError: true},
		}
	}

	result, err := t.Execute(ctx, block.Input)
	if err != nil {
		errMsg := fmt.Sprintf("Tool execution error: %v", err)
		return toolExecResult{
			toolName: block.Name,
			msg:      message.NewToolResultMessage(block.ID, errMsg, true),
			result:   tool.Result{Content: errMsg, IsError: true},
		}
	}

	return toolExecResult{
		toolName: block.Name,
		msg:      message.NewToolResultMessage(block.ID, result.Content, result.IsError),
		result:   *result,
	}
}
