package provider

import (
	"context"
	"encoding/json"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
)

type AnthropicProvider struct {
	client anthropic.Client
	model  string
}

func NewAnthropicProvider(apiKey, model, baseURL string) *AnthropicProvider {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &AnthropicProvider{
		client: anthropic.NewClient(opts...),
		model:  model,
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
		Messages:  message.ToAnthropicMessages(req.Messages),
	}

	if req.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: req.SystemPrompt},
		}
	}

	if len(req.Tools) > 0 {
		params.Tools = message.ToAnthropicToolSchemas(req.Tools)
	}

	if req.Thinking != nil && req.Thinking.Enabled {
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfEnabled: &anthropic.ThinkingConfigEnabledParam{
				BudgetTokens: int64(req.Thinking.BudgetTokens),
			},
		}
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		p.consumeStream(stream, ch, model)
	}()

	return ch, nil
}

func (p *AnthropicProvider) consumeStream(
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	ch chan<- StreamEvent,
	model string,
) {
	var assembled message.Message
	assembled.Role = message.RoleAssistant

	// Track in-progress tool_use block
	var currentToolID, currentToolName string
	var currentToolInput []byte
	var fullText string
	var thinkingText, thinkingSignature string

	for stream.Next() {
		evt := stream.Current()

		switch e := evt.AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			block := e.ContentBlock
			switch block.Type {
			case "tool_use":
				currentToolID = block.ID
				currentToolName = block.Name
				currentToolInput = nil
			case "text":
				// text block started, accumulate via deltas
			case "thinking":
				thinkingText = ""
				thinkingSignature = ""
			}

		case anthropic.ContentBlockDeltaEvent:
			delta := e.Delta
			switch delta.Type {
			case "text_delta":
				fullText += delta.Text
				ch <- StreamEvent{Type: "text_delta", Delta: delta.Text}
			case "input_json_delta":
				currentToolInput = append(currentToolInput, []byte(delta.PartialJSON)...)
			case "thinking_delta":
				thinkingText += delta.Thinking
				ch <- StreamEvent{Type: "thinking_delta", Delta: delta.Thinking}
			case "signature_delta":
				thinkingSignature += delta.Signature
			}

		case anthropic.ContentBlockStopEvent:
			if currentToolID != "" {
				var inputRaw json.RawMessage
				if len(currentToolInput) > 0 {
					inputRaw = json.RawMessage(currentToolInput)
				} else {
					inputRaw = json.RawMessage("{}")
				}
				toolBlock := &message.ToolUseBlock{
					ID:    currentToolID,
					Name:  currentToolName,
					Input: inputRaw,
				}
				assembled.Content = append(assembled.Content,
					message.NewToolUseBlock(currentToolID, currentToolName, inputRaw))
				ch <- StreamEvent{Type: "tool_use", ToolUse: toolBlock}
				currentToolID = ""
				currentToolName = ""
				currentToolInput = nil
			}
			if thinkingText != "" {
				assembled.Content = append(assembled.Content, message.ContentBlock{
					Type: message.BlockThinking,
					Thinking: &message.ThinkingBlock{
						Text:      thinkingText,
						Signature: thinkingSignature,
						Model:     model,
					},
				})
				thinkingText = ""
				thinkingSignature = ""
			}

		case anthropic.MessageStopEvent:
			// handled after loop
		}
	}

	if err := stream.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: fmt.Errorf("anthropic stream: %w", err)}
		return
	}

	// Prepend accumulated text as the first content block
	if fullText != "" {
		assembled.Content = append([]message.ContentBlock{message.NewTextBlock(fullText)}, assembled.Content...)
	}
	ch <- StreamEvent{
		Type:    "message_complete",
		Message: &assembled,
	}
}
