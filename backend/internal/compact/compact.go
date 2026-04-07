package compact

import (
	"context"
	"errors"
	"fmt"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/token"
)

// CompactConfig holds configuration for the compaction process.
type CompactConfig struct {
	Provider  provider.Provider
	Model     string
	MaxTokens int // max output tokens for the summary request
}

// CompactResult is the output of a successful compaction.
type CompactResult struct {
	BoundaryMessage   message.Message
	SummaryMessages   []message.Message
	NewMessages       []message.Message // full post-compact message chain
	PreCompactTokens  int
	PostCompactTokens int
}

var (
	ErrNotEnoughMessages = errors.New("not enough messages to compact")
	ErrNoSummary         = errors.New("compaction failed: no summary generated")
)

// Compact performs a full conversation compaction.
// It summarizes the conversation history into a structured summary
// and replaces the message chain with [boundary, summary].
// Maps to services/compact/compact.ts:compactConversation.
func Compact(ctx context.Context, cfg CompactConfig, messages []message.Message, customInstructions string) (*CompactResult, error) {
	if len(messages) < 2 {
		return nil, ErrNotEnoughMessages
	}

	preCompactTokens := token.EstimateMessages(messages)

	// Step 1: Run microcompact to reduce token count before summarization
	messages = Microcompact(messages, 10)

	// Step 2: Build the compact prompt
	compactPrompt := BuildCompactPrompt(customInstructions)

	// Step 3: Prepare messages for the summarization API call
	// We send the conversation history + a user message requesting summary
	summaryRequest := message.NewUserMessage(compactPrompt)
	apiMessages := append(messages, summaryRequest)

	// Step 4: Call the provider to generate the summary
	req := provider.ChatRequest{
		Messages:     apiMessages,
		SystemPrompt: "You are a helpful AI assistant tasked with summarizing conversations.",
		Model:        cfg.Model,
		MaxTokens:    cfg.MaxTokens,
		// No tools — the prompt explicitly forbids tool use
	}

	streamCh, err := cfg.Provider.StreamChat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("compact: provider error: %w", err)
	}

	// Step 5: Collect the summary from the stream
	var summary string
	for evt := range streamCh {
		switch evt.Type {
		case "text_delta":
			summary += evt.Delta
		case "error":
			return nil, fmt.Errorf("compact: stream error: %w", evt.Error)
		case "message_complete":
			// summary already accumulated via deltas
		}
	}

	if summary == "" {
		return nil, ErrNoSummary
	}

	// Step 6: Build post-compact messages
	// Strip <analysis> tags from summary — it's a drafting scratchpad
	summary = formatSummary(summary)

	boundaryMsg := message.NewCompactBoundaryMessage("manual", preCompactTokens)
	summaryContent := BuildSummaryUserMessage(summary)
	summaryMsg := message.NewUserMessage(summaryContent)

	postCompactMessages := []message.Message{boundaryMsg, summaryMsg}
	postCompactTokens := token.EstimateMessages(postCompactMessages)

	return &CompactResult{
		BoundaryMessage:   boundaryMsg,
		SummaryMessages:   []message.Message{summaryMsg},
		NewMessages:       postCompactMessages,
		PreCompactTokens:  preCompactTokens,
		PostCompactTokens: postCompactTokens,
	}, nil
}

// formatSummary strips the <analysis> drafting scratchpad and
// reformats <summary> tags into readable headers.
// Maps to services/compact/prompt.ts:formatCompactSummary.
func formatSummary(summary string) string {
	// Strip analysis section
	// Simple approach: find <analysis>...</analysis> and remove
	result := summary
	if start := findTag(result, "<analysis>"); start >= 0 {
		if end := findTag(result, "</analysis>"); end > start {
			result = result[:start] + result[end+len("</analysis>"):]
		}
	}

	// Replace <summary>...</summary> with just the content
	if start := findTag(result, "<summary>"); start >= 0 {
		contentStart := start + len("<summary>")
		if end := findTag(result, "</summary>"); end > contentStart {
			content := result[contentStart:end]
			result = result[:start] + "Summary:\n" + content + result[end+len("</summary>"):]
		}
	}

	return result
}

func findTag(s, tag string) int {
	for i := 0; i <= len(s)-len(tag); i++ {
		if s[i:i+len(tag)] == tag {
			return i
		}
	}
	return -1
}
