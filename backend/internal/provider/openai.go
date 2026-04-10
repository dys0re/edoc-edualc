package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
	openai "github.com/sashabaranov/go-openai"
)

type OpenAIProvider struct {
	client *openai.Client
	model  string
}

func NewOpenAIProvider(apiKey, model, baseURL string) *OpenAIProvider {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return &OpenAIProvider{
		client: openai.NewClientWithConfig(config),
		model:  model,
	}
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	params := openai.ChatCompletionRequest{
		Model:               model,
		Messages:            message.ToOpenAIMessages(req.Messages, req.SystemPrompt),
		MaxCompletionTokens: maxTokens,
		Stream:              true,
	}

	if len(req.Tools) > 0 {
		params.Tools = message.ToOpenAIToolSchemas(req.Tools)
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai stream create: %w", err)
	}

	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		defer stream.Close()
		p.consumeStream(stream, ch)
	}()

	return ch, nil
}

func (p *OpenAIProvider) consumeStream(stream *openai.ChatCompletionStream, ch chan<- StreamEvent) {
	var assembled message.Message
	assembled.Role = message.RoleAssistant

	// Track in-progress tool calls by index
	type toolAccum struct {
		id       string
		name     string
		argsJSON []byte
	}
	toolAccums := map[int]*toolAccum{}

	var fullText string

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: fmt.Errorf("openai stream: %w", err)}
			return
		}

		for _, choice := range resp.Choices {
			delta := choice.Delta

			// Text content
			if delta.Content != "" {
				fullText += delta.Content
				ch <- StreamEvent{Type: "text_delta", Delta: delta.Content}
			}

			// Tool calls (streamed incrementally)
			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				acc, ok := toolAccums[idx]
				if !ok {
					acc = &toolAccum{}
					toolAccums[idx] = acc
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					acc.argsJSON = append(acc.argsJSON, []byte(tc.Function.Arguments)...)
				}
			}

			// Check finish reason
			if choice.FinishReason != "" {
				// Emit completed tool_use blocks
				for _, acc := range toolAccums {
					if acc.id == "" {
						continue
					}
					var inputRaw json.RawMessage
					if len(acc.argsJSON) > 0 {
						inputRaw = json.RawMessage(acc.argsJSON)
					} else {
						inputRaw = json.RawMessage("{}")
					}
					toolBlock := &message.ToolUseBlock{
						ID:    acc.id,
						Name:  acc.name,
						Input: inputRaw,
					}
					assembled.Content = append(assembled.Content, message.NewToolUseBlock(acc.id, acc.name, inputRaw))
					ch <- StreamEvent{Type: "tool_use", ToolUse: toolBlock}
				}

				if fullText != "" {
					assembled.Content = append([]message.ContentBlock{message.NewTextBlock(fullText)}, assembled.Content...)
				}

				ch <- StreamEvent{
					Type:       "message_complete",
					Message:    &assembled,
					StopReason: string(choice.FinishReason),
				}
			}
		}
	}
}
