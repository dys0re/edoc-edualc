package compact

import (
	"github.com/dysorder/edoc-edualc/backend/internal/message"
)

const clearedMessage = "[Old tool result content cleared]"

// compactableTools are tool names whose results can be safely cleared.
// Matches Claude Code's COMPACTABLE_TOOLS in microCompact.ts.
var compactableTools = map[string]bool{
	"read":  true,
	"bash":  true,
	"grep":  true,
	"glob":  true,
	"write": true,
	"edit":  true,
}

// Microcompact clears old tool_result content to save tokens.
// Zero API cost — no LLM call needed.
// Keeps the most recent `keepRecent` compactable tool results intact.
// Maps to services/compact/microCompact.ts time-based path.
func Microcompact(messages []message.Message, keepRecent int) []message.Message {
	if keepRecent <= 0 {
		keepRecent = 10
	}

	// Pass 1: collect compactable tool_use IDs in encounter order
	var compactableIDs []string
	for _, msg := range messages {
		if msg.Role != message.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == message.BlockToolUse && block.ToolUse != nil {
				if compactableTools[block.ToolUse.Name] {
					compactableIDs = append(compactableIDs, block.ToolUse.ID)
				}
			}
		}
	}

	if len(compactableIDs) <= keepRecent {
		return messages // nothing to clear
	}

	// Build set of IDs to clear (all except the most recent keepRecent)
	clearSet := make(map[string]bool)
	for _, id := range compactableIDs[:len(compactableIDs)-keepRecent] {
		clearSet[id] = true
	}

	// Pass 2: replace tool_result content for cleared IDs
	result := make([]message.Message, len(messages))
	for i, msg := range messages {
		if msg.Role != message.RoleUser {
			result[i] = msg
			continue
		}

		touched := false
		newContent := make([]message.ContentBlock, len(msg.Content))
		for j, block := range msg.Content {
			if block.Type == message.BlockToolResult && block.ToolResult != nil && clearSet[block.ToolResult.ToolUseID] {
				touched = true
				newContent[j] = message.ContentBlock{
					Type: message.BlockToolResult,
					ToolResult: &message.ToolResultBlock{
						ToolUseID: block.ToolResult.ToolUseID,
						Content:   clearedMessage,
						IsError:   block.ToolResult.IsError,
					},
				}
			} else {
				newContent[j] = block
			}
		}

		if touched {
			result[i] = message.Message{Role: msg.Role, Content: newContent}
		} else {
			result[i] = msg
		}
	}

	return result
}
