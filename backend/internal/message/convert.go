package message

import (
	"encoding/json"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/sashabaranov/go-openai"
)

// --- Anthropic conversion ---

func ToAnthropicMessages(msgs []Message) []anthropic.MessageParam {
	var out []anthropic.MessageParam
	for _, msg := range msgs {
		switch msg.Role {
		case RoleUser:
			out = append(out, toAnthropicUserMessage(msg))
		case RoleAssistant:
			out = append(out, toAnthropicAssistantMessage(msg))
		// system messages are handled separately via system prompt
		}
	}
	return out
}

func toAnthropicUserMessage(msg Message) anthropic.MessageParam {
	var parts []anthropic.ContentBlockParamUnion
	for _, block := range msg.Content {
		switch block.Type {
		case BlockText:
			parts = append(parts, anthropic.NewTextBlock(block.Text.Text))
		case BlockToolResult:
			r := block.ToolResult
			tb := anthropic.NewToolResultBlock(r.ToolUseID, r.Content, r.IsError)
			parts = append(parts, tb)
		}
	}
	return anthropic.NewUserMessage(parts...)
}

func toAnthropicAssistantMessage(msg Message) anthropic.MessageParam {
	var parts []anthropic.ContentBlockParamUnion
	for _, block := range msg.Content {
		switch block.Type {
		case BlockText:
			parts = append(parts, anthropic.NewTextBlock(block.Text.Text))
		case BlockToolUse:
			tu := block.ToolUse
			parts = append(parts, anthropic.NewToolUseBlock(tu.ID, tu.Input, tu.Name))
		case BlockThinking:
			parts = append(parts, anthropic.NewThinkingBlock(block.Thinking.Signature, block.Thinking.Text))
		}
	}
	return anthropic.NewAssistantMessage(parts...)
}

func FromAnthropicResponse(resp *anthropic.Message) Message {
	msg := Message{Role: RoleAssistant}
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			msg.Content = append(msg.Content, NewTextBlock(b.Text))
		case anthropic.ToolUseBlock:
			raw, _ := json.Marshal(b.Input)
			msg.Content = append(msg.Content, NewToolUseBlock(b.ID, b.Name, raw))
		case anthropic.ThinkingBlock:
			msg.Content = append(msg.Content, ContentBlock{
				Type: BlockThinking,
				Thinking: &ThinkingBlock{
					Text:      b.Thinking,
					Signature: b.Signature,
				},
			})
		}
	}
	return msg
}

// ToAnthropicToolSchemas converts internal tool schemas to Anthropic API format.
func ToAnthropicToolSchemas(tools []ToolSchema) []anthropic.ToolUnionParam {
	var out []anthropic.ToolUnionParam
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{
			Properties: t.InputSchema["properties"],
		}
		if req, ok := t.InputSchema["required"].([]string); ok {
			schema.Required = req
		} else if reqAny, ok := t.InputSchema["required"].([]interface{}); ok {
			for _, r := range reqAny {
				if s, ok := r.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		}
		out = append(out, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			},
		})
	}
	return out
}

// --- OpenAI conversion ---

func ToOpenAIMessages(msgs []Message, systemPrompt string) []openai.ChatCompletionMessage {
	var out []openai.ChatCompletionMessage
	if systemPrompt != "" {
		out = append(out, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}
	for _, msg := range msgs {
		switch msg.Role {
		case RoleUser:
			out = append(out, toOpenAIUserMessage(msg))
		case RoleAssistant:
			out = append(out, toOpenAIAssistantMessages(msg)...)
		}
	}
	return out
}

func toOpenAIUserMessage(msg Message) openai.ChatCompletionMessage {
	// tool_result blocks become separate tool messages in OpenAI
	// plain text becomes a user message
	for _, block := range msg.Content {
		if block.Type == BlockToolResult && block.ToolResult != nil {
			return openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    block.ToolResult.Content,
				ToolCallID: block.ToolResult.ToolUseID,
			}
		}
	}
	// regular text user message
	var text string
	for _, block := range msg.Content {
		if block.Type == BlockText && block.Text != nil {
			text += block.Text.Text
		}
	}
	return openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: text,
	}
}

func toOpenAIAssistantMessages(msg Message) []openai.ChatCompletionMessage {
	var text string
	var toolCalls []openai.ToolCall

	for _, block := range msg.Content {
		switch block.Type {
		case BlockText:
			text += block.Text.Text
		case BlockToolUse:
			tu := block.ToolUse
			toolCalls = append(toolCalls, openai.ToolCall{
				ID:   tu.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tu.Name,
					Arguments: string(tu.Input),
				},
			})
		// thinking blocks are Anthropic-only, skip for OpenAI
		}
	}

	result := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: text,
	}
	if len(toolCalls) > 0 {
		result.ToolCalls = toolCalls
	}
	return []openai.ChatCompletionMessage{result}
}

func FromOpenAIResponse(resp openai.ChatCompletionMessage) Message {
	msg := Message{Role: RoleAssistant}
	if resp.Content != "" {
		msg.Content = append(msg.Content, NewTextBlock(resp.Content))
	}
	for _, tc := range resp.ToolCalls {
		msg.Content = append(msg.Content, NewToolUseBlock(
			tc.ID,
			tc.Function.Name,
			json.RawMessage(tc.Function.Arguments),
		))
	}
	return msg
}

// --- Shared types ---

// ToolSchema is a provider-agnostic tool definition used for conversion.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ToOpenAIToolSchemas converts internal tool schemas to OpenAI API format.
func ToOpenAIToolSchemas(tools []ToolSchema) []openai.Tool {
	var out []openai.Tool
	for _, t := range tools {
		params, _ := json.Marshal(t.InputSchema)
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(params),
			},
		})
	}
	return out
}
