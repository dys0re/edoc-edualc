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

// Run executes the agent loop. Maps to query.ts:241 queryLoop.
// Events are sent to the returned channel. The channel is closed when done.
func Run(ctx context.Context, cfg Config, userPrompt string) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		runLoop(ctx, cfg, userPrompt, ch)
	}()
	return ch
}

func runLoop(ctx context.Context, cfg Config, userPrompt string, ch chan<- Event) {
	state := &State{
		Messages:  []message.Message{message.NewUserMessage(userPrompt)},
		TurnCount: 0,
	}

	toolSchemas := ToolSchemas(cfg.Registry)

	for {
		// Check context cancellation
		if ctx.Err() != nil {
			ch <- Event{Type: "error", Error: ctx.Err()}
			return
		}

		// Check maxTurns limit
		if cfg.MaxTurns > 0 && state.TurnCount >= cfg.MaxTurns {
			ch <- Event{Type: "turn_complete"}
			return
		}

		state.TurnCount++

		// Microcompact: clear old tool_result content (zero API cost)
		// Maps to query.ts:414 deps.microcompact call
		state.Messages = compact.Microcompact(state.Messages, 10)

		// Auto compact: if token count exceeds threshold, summarize
		// Maps to query.ts:454 deps.autocompact call
		if cfg.AutoCompactThreshold > 0 {
			tokenCount := token.EstimateMessages(state.Messages)
			if tokenCount > cfg.AutoCompactThreshold {
				compactCfg := compact.CompactConfig{
					Provider:  cfg.Provider,
					Model:     cfg.Model,
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
				// If compact fails, continue with current messages —
				// the API may still accept them or return prompt_too_long
			}
		}

		// Call the provider
		req := provider.ChatRequest{
			Messages:     state.Messages,
			SystemPrompt: cfg.SystemPrompt,
			Tools:        toolSchemas,
			Model:        cfg.Model,
			MaxTokens:    cfg.MaxTokens,
		}

		streamCh, err := cfg.Provider.StreamChat(ctx, req)
		if err != nil {
			ch <- Event{Type: "error", Error: fmt.Errorf("provider error: %w", err)}
			return
		}

		// Consume stream, collect the final assistant message and tool_use blocks
		var assistantMsg *message.Message
		var toolUseBlocks []*message.ToolUseBlock

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
				ch <- Event{Type: "message_complete", Message: evt.Message}
			case "error":
				ch <- Event{Type: "error", Error: evt.Error}
				return
			}
		}

		// Check for abort after streaming
		if ctx.Err() != nil {
			// Backfill missing tool_results for any pending tool_use blocks
			// (maps to yieldMissingToolResultBlocks in query.ts:123)
			for _, tu := range toolUseBlocks {
				state.Messages = append(state.Messages,
					message.NewToolResultMessage(tu.ID, "Interrupted by user", true))
			}
			ch <- Event{Type: "error", Error: ctx.Err()}
			return
		}

		if assistantMsg == nil {
			ch <- Event{Type: "error", Error: fmt.Errorf("no assistant message received")}
			return
		}

		// Append assistant message to history
		state.Messages = append(state.Messages, *assistantMsg)

		// No tool calls → conversation turn complete
		if len(toolUseBlocks) == 0 {
			ch <- Event{Type: "turn_complete"}
			return
		}

		// Execute tools
		// Maps to toolOrchestration.ts:runTools — partition into concurrent/serial batches
		results := executeTools(ctx, cfg.Registry, toolUseBlocks)

		// Append tool results to messages
		for _, r := range results {
			state.Messages = append(state.Messages, r.msg)
			ch <- Event{Type: "tool_result", ToolResult: &r.result, ToolName: r.toolName}
		}

		// Continue the loop — next iteration sends tool_results back to the model
	}
}

type toolExecResult struct {
	toolName string
	msg      message.Message
	result   tool.Result
}

// executeTools runs tool calls, respecting concurrency safety.
// Maps to toolOrchestration.ts:19 runTools.
func executeTools(ctx context.Context, reg *tool.Registry, blocks []*message.ToolUseBlock) []toolExecResult {
	// Partition into batches: consecutive concurrency-safe tools run in parallel,
	// others run serially. Maps to toolOrchestration.ts:partitionToolCalls.
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
		// Tool not found — maps to toolExecution.ts:370
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

// RunWithMessages starts the loop with pre-existing message history (for session resume).
func RunWithMessages(ctx context.Context, cfg Config, messages []message.Message) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		runLoopWithMessages(ctx, cfg, messages, ch)
	}()
	return ch
}

func runLoopWithMessages(ctx context.Context, cfg Config, messages []message.Message, ch chan<- Event) {
	state := &State{
		Messages:  messages,
		TurnCount: 0,
	}

	toolSchemas := ToolSchemas(cfg.Registry)

	for {
		if ctx.Err() != nil {
			ch <- Event{Type: "error", Error: ctx.Err()}
			return
		}
		if cfg.MaxTurns > 0 && state.TurnCount >= cfg.MaxTurns {
			ch <- Event{Type: "turn_complete"}
			return
		}
		state.TurnCount++

		// Microcompact + auto compact (same as runLoop)
		state.Messages = compact.Microcompact(state.Messages, 10)
		if cfg.AutoCompactThreshold > 0 {
			tokenCount := token.EstimateMessages(state.Messages)
			if tokenCount > cfg.AutoCompactThreshold {
				compactCfg := compact.CompactConfig{
					Provider:  cfg.Provider,
					Model:     cfg.Model,
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

		req := provider.ChatRequest{
			Messages:     state.Messages,
			SystemPrompt: cfg.SystemPrompt,
			Tools:        toolSchemas,
			Model:        cfg.Model,
			MaxTokens:    cfg.MaxTokens,
		}

		streamCh, err := cfg.Provider.StreamChat(ctx, req)
		if err != nil {
			ch <- Event{Type: "error", Error: fmt.Errorf("provider error: %w", err)}
			return
		}

		var assistantMsg *message.Message
		var toolUseBlocks []*message.ToolUseBlock

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
				ch <- Event{Type: "message_complete", Message: evt.Message}
			case "error":
				ch <- Event{Type: "error", Error: evt.Error}
				return
			}
		}

		if ctx.Err() != nil {
			for _, tu := range toolUseBlocks {
				state.Messages = append(state.Messages,
					message.NewToolResultMessage(tu.ID, "Interrupted by user", true))
			}
			ch <- Event{Type: "error", Error: ctx.Err()}
			return
		}

		if assistantMsg == nil {
			ch <- Event{Type: "error", Error: fmt.Errorf("no assistant message received")}
			return
		}

		state.Messages = append(state.Messages, *assistantMsg)

		if len(toolUseBlocks) == 0 {
			ch <- Event{Type: "turn_complete"}
			return
		}

		results := executeTools(ctx, cfg.Registry, toolUseBlocks)
		for _, r := range results {
			state.Messages = append(state.Messages, r.msg)
			ch <- Event{Type: "tool_result", ToolResult: &r.result, ToolName: r.toolName}
		}
	}
}
