package message

import "encoding/json"

// Role represents the message sender.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// Message is the internal unified message type.
// Maps to Claude Code's types/message.js Message union.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a polymorphic block within a message.
// Only one of the fields will be non-nil.
type ContentBlock struct {
	Type       BlockType        `json:"type"`
	Text       *TextBlock       `json:"text,omitempty"`
	ToolUse    *ToolUseBlock    `json:"tool_use,omitempty"`
	ToolResult *ToolResultBlock `json:"tool_result,omitempty"`
	Thinking   *ThinkingBlock   `json:"thinking,omitempty"`
}

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockThinking   BlockType = "thinking"
)

type TextBlock struct {
	Text string `json:"text"`
}

type ToolUseBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ThinkingBlock is Anthropic-specific. The Signature is model-bound;
// cross-model fallback requires stripping these blocks.
type ThinkingBlock struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
	Model     string `json:"model,omitempty"`
}

// --- Constructors ---

func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: &TextBlock{Text: text}}
}

func NewToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{Type: BlockToolUse, ToolUse: &ToolUseBlock{ID: id, Name: name, Input: input}}
}

func NewToolResultBlock(toolUseID, content string, isError bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolResult: &ToolResultBlock{ToolUseID: toolUseID, Content: content, IsError: isError}}
}

func NewUserMessage(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{NewTextBlock(text)}}
}

func NewToolResultMessage(toolUseID, content string, isError bool) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{NewToolResultBlock(toolUseID, content, isError)}}
}

// ExtractToolUseBlocks returns all tool_use blocks from a message.
func (m *Message) ExtractToolUseBlocks() []*ToolUseBlock {
	var blocks []*ToolUseBlock
	for i := range m.Content {
		if m.Content[i].Type == BlockToolUse && m.Content[i].ToolUse != nil {
			blocks = append(blocks, m.Content[i].ToolUse)
		}
	}
	return blocks
}

// StripThinkingBlocks removes thinking blocks that don't match the given model.
// Used during model fallback to avoid signature verification failures.
func StripThinkingBlocks(messages []Message, keepModel string) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != RoleAssistant {
			out = append(out, msg)
			continue
		}
		filtered := make([]ContentBlock, 0, len(msg.Content))
		for _, block := range msg.Content {
			if block.Type == BlockThinking && block.Thinking != nil && block.Thinking.Model != keepModel {
				continue
			}
			filtered = append(filtered, block)
		}
		out = append(out, Message{Role: msg.Role, Content: filtered})
	}
	return out
}
