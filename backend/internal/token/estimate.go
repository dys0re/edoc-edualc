package token

import (
	"encoding/json"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
)

const bytesPerToken = 4

// RoughEstimate estimates token count from text length.
// Maps to services/tokenEstimation.ts:roughTokenCountEstimation.
func RoughEstimate(text string) int {
	return len(text) / bytesPerToken
}

// EstimateMessages estimates total token count for a message list.
// Walks all content blocks and estimates based on text length.
func EstimateMessages(msgs []message.Message) int {
	total := 0
	for _, msg := range msgs {
		for _, block := range msg.Content {
			total += estimateBlock(block)
		}
	}
	// Pad by 4/3 to be conservative (matches Claude Code's approach)
	return total * 4 / 3
}

func estimateBlock(block message.ContentBlock) int {
	switch block.Type {
	case message.BlockText:
		if block.Text != nil {
			return RoughEstimate(block.Text.Text)
		}
	case message.BlockToolUse:
		if block.ToolUse != nil {
			return RoughEstimate(block.ToolUse.Name + string(block.ToolUse.Input))
		}
	case message.BlockToolResult:
		if block.ToolResult != nil {
			return RoughEstimate(block.ToolResult.Content)
		}
	case message.BlockThinking:
		if block.Thinking != nil {
			return RoughEstimate(block.Thinking.Text)
		}
	default:
		// Fallback: serialize the block
		b, _ := json.Marshal(block)
		return RoughEstimate(string(b))
	}
	return 0
}

// EstimateText extracts all plain text from messages and estimates tokens.
// Useful for compact where we only care about readable content size.
func EstimateText(msgs []message.Message) int {
	var sb strings.Builder
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == message.BlockText && block.Text != nil {
				sb.WriteString(block.Text.Text)
				sb.WriteString(" ")
			}
			if block.Type == message.BlockToolResult && block.ToolResult != nil {
				sb.WriteString(block.ToolResult.Content)
				sb.WriteString(" ")
			}
		}
	}
	return RoughEstimate(sb.String())
}
