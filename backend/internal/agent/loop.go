package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/dysorder/edoc-edualc/backend/internal/compact"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
	"github.com/dysorder/edoc-edualc/backend/internal/token"
)

const (
	maxOutputTokensRecoveryLimit = 3 // 截断续写最大恢复次数，对标 query.ts:164
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

		// ── 3. Microcompact: 清空旧 tool_result 内容 ──
		// 对标 query.ts:414 deps.microcompact call
		state.Messages = compact.Microcompact(state.Messages, 10)

		// ── 4. Auto compact: token 超阈值时压缩 ──
		// 对标 query.ts:454 deps.autocompact call
		if cfg.AutoCompactThreshold > 0 {
			tokenCount := token.EstimateMessages(state.Messages)
			if tokenCount > cfg.AutoCompactThreshold {
				compactCfg := compact.CompactConfig{
					Provider:  cfg.Provider,
					Model:     currentModel,
					MaxTokens: 8192,
				}
				result, err := compact.Compact(ctx, compactCfg, state.Messages, "")
				if err == nil {
					state.Messages = result.NewMessages
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
			ch <- Event{Type: "turn_complete"}
			return
		}

		// ── 10. 执行 tools ──
		results := executeTools(ctx, cfg.Registry, toolUseBlocks)

		for _, r := range results {
			state.Messages = append(state.Messages, r.msg)
			ch <- Event{Type: "tool_result", ToolResult: &r.result, ToolName: r.toolName}
		}

		// Tool 执行成功，重置恢复计数
		state.MaxOutputTokensRecoveryCount = 0
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

	case provider.ErrorAuth:
		ch <- Event{Type: "error", Error: fmt.Errorf("authentication failed: %w", err)}
		return false

	case provider.ErrorServer:
		ch <- Event{Type: "error", Error: fmt.Errorf("server error: %w", err)}
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
