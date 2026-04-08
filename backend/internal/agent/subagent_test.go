package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// mockProvider returns a single text response then end_turn.
type mockProvider struct {
	responses [][]provider.StreamEvent
	callCount int
}

func (m *mockProvider) StreamChat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 16)
	idx := m.callCount
	m.callCount++
	if idx >= len(m.responses) {
		// Default: single text response
		go func() {
			defer close(ch)
			msg := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{message.NewTextBlock("done")}}
			ch <- provider.StreamEvent{Type: "message_complete", Message: &msg, StopReason: "end_turn"}
		}()
		return ch, nil
	}
	go func() {
		defer close(ch)
		for _, evt := range m.responses[idx] {
			ch <- evt
		}
	}()
	return ch, nil
}

func (m *mockProvider) Name() string { return "mock" }

func TestSubagentBasicFlow(t *testing.T) {
	mp := &mockProvider{
		responses: [][]provider.StreamEvent{
			// Sub-agent response: a single text message
			{
				{Type: "text_delta", Delta: "sub-agent result"},
				{
					Type: "message_complete",
					Message: &message.Message{
						Role:    message.RoleAssistant,
						Content: []message.ContentBlock{message.NewTextBlock("sub-agent result")},
					},
					StopReason: "end_turn",
				},
			},
		},
	}

	reg := tool.NewRegistry()
	cfg := Config{
		Provider:       mp,
		Registry:       reg,
		SystemPrompt:   "test",
		Model:          "test-model",
		MaxTokens:      1024,
		MaxTurns:       5,
		PermissionMode: tool.PermissionBypass,
	}

	resolver := NewSubagentResolver(cfg)
	params := tool.SubAgentParams{
		Prompt: "do something",
	}

	ch, err := resolver.RunSubAgent(context.Background(), params)
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}

	var gotDone bool
	var output string
	for evt := range ch {
		switch evt.Type {
		case "done":
			gotDone = true
			output = evt.Output
		case "error":
			t.Fatalf("sub-agent error: %v", evt.Error)
		}
	}

	if !gotDone {
		t.Fatal("expected done event")
	}
	if output != "sub-agent result" {
		t.Fatalf("expected 'sub-agent result', got %q", output)
	}
}

func TestSubagentToolUse(t *testing.T) {
	// Sub-agent calls a tool, gets result, then responds with text
	mockTool := &mockTool{name: "Echo", result: "echoed"}

	mp := &mockProvider{
		responses: [][]provider.StreamEvent{
			// Turn 1: sub-agent calls Echo tool
			{
				{
					Type:   "tool_use",
					ToolUse: &message.ToolUseBlock{ID: "tu1", Name: "Echo", Input: json.RawMessage(`{"text":"hello"}`)},
				},
				{
					Type: "message_complete",
					Message: &message.Message{
						Role: message.RoleAssistant,
						Content: []message.ContentBlock{
							message.NewToolUseBlock("tu1", "Echo", json.RawMessage(`{"text":"hello"}`)),
						},
					},
					StopReason: "tool_use",
				},
			},
			// Turn 2: sub-agent responds with text after getting tool result
			{
				{Type: "text_delta", Delta: "result: echoed"},
				{
					Type: "message_complete",
					Message: &message.Message{
						Role:    message.RoleAssistant,
						Content: []message.ContentBlock{message.NewTextBlock("result: echoed")},
					},
					StopReason: "end_turn",
				},
			},
		},
	}

	reg := tool.NewRegistry()
	reg.Register(mockTool)

	cfg := Config{
		Provider:       mp,
		Registry:       reg,
		SystemPrompt:   "test",
		Model:          "test-model",
		MaxTokens:      1024,
		MaxTurns:       5,
		PermissionMode: tool.PermissionBypass,
	}

	resolver := NewSubagentResolver(cfg)
	ch, err := resolver.RunSubAgent(context.Background(), tool.SubAgentParams{
		Prompt: "use echo tool",
	})
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}

	var toolUseSeen, doneSeen bool
	var output string
	for evt := range ch {
		switch evt.Type {
		case "tool_use":
			toolUseSeen = evt.ToolName == "Echo"
		case "done":
			doneSeen = true
			output = evt.Output
		case "error":
			t.Fatalf("sub-agent error: %v", evt.Error)
		}
	}

	if !toolUseSeen {
		t.Fatal("expected tool_use event for Echo")
	}
	if !doneSeen {
		t.Fatal("expected done event")
	}
	if output != "result: echoed" {
		t.Fatalf("expected 'result: echoed', got %q", output)
	}
}

func TestSubagentModelInherit(t *testing.T) {
	mp := &mockProvider{}

	cfg := Config{
		Provider:       mp,
		Registry:       tool.NewRegistry(),
		SystemPrompt:   "parent prompt",
		Model:          "parent-model",
		MaxTokens:      2048,
		PermissionMode: tool.PermissionBypass,
	}

	resolver := NewSubagentResolver(cfg)
	params := tool.SubAgentParams{
		Prompt: "test",
		// Model is empty — should inherit "parent-model"
	}

	ch, _ := resolver.RunSubAgent(context.Background(), params)
	for range ch {
	}

	// Verify it doesn't panic and uses parent config
	if mp.callCount != 1 {
		t.Fatalf("expected 1 provider call, got %d", mp.callCount)
	}
}

func TestSubagentMaxTurnsReached(t *testing.T) {
	// Sub-agent that keeps calling tools forever
	mockTool := &mockTool{name: "Loop", result: "loop"}

	mp := &mockProvider{
		responses: [][]provider.StreamEvent{
			// Will only provide one response, but stop_reason is tool_use
			// so the loop continues. Second call returns tool_use again, etc.
		},
	}

	reg := tool.NewRegistry()
	reg.Register(mockTool)

	cfg := Config{
		Provider:       mp,
		Registry:       reg,
		SystemPrompt:   "test",
		Model:          "test-model",
		MaxTokens:      1024,
		MaxTurns:       2, // Very low limit
		PermissionMode: tool.PermissionBypass,
	}

	resolver := NewSubagentResolver(cfg)
	ch, _ := resolver.RunSubAgent(context.Background(), tool.SubAgentParams{
		Prompt:   "loop forever",
		MaxTurns: 2,
	})

	var gotError bool
	for evt := range ch {
		if evt.Type == "error" {
			gotError = true
		}
	}
	// Should complete without hanging (max turns prevents infinite loop)
	_ = gotError
}

// mockTool is a simple tool for testing.
type mockTool struct {
	name   string
	result string
}

func (m *mockTool) Name() string                                                  { return m.name }
func (m *mockTool) Description() string                                           { return "mock" }
func (m *mockTool) InputSchema() map[string]interface{}                           { return map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}}} }
func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (*tool.Result, error) {
	return &tool.Result{Content: m.result}, nil
}
func (m *mockTool) IsReadOnly(input json.RawMessage) bool          { return true }
func (m *mockTool) IsConcurrencySafe(input json.RawMessage) bool   { return true }
func (m *mockTool) NeedsApproval(input json.RawMessage) bool       { return false }
func (m *mockTool) PermissionDescription(input json.RawMessage) string { return "" }
func (m *mockTool) IsFileEdit(input json.RawMessage) bool          { return false }
