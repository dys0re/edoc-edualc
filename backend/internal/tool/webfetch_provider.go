package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
)

// providerWebFetchAdapter 将 provider.Provider 适配为 WebFetchProvider。
// 用小模型（haiku 级别）对抓取内容执行 prompt。
type providerWebFetchAdapter struct {
	p     provider.Provider
	model string // 二次处理用的模型，空=用 provider 默认
}

// NewProviderWebFetch 创建一个基于 provider 的 WebFetchProvider。
// model 为空时使用 provider 默认模型。
func NewProviderWebFetch(p provider.Provider, model string) WebFetchProvider {
	return &providerWebFetchAdapter{p: p, model: model}
}

func (a *providerWebFetchAdapter) Summarize(ctx context.Context, content, prompt string) (string, error) {
	userMsg := fmt.Sprintf(`Web page content:
---
%s
---

%s

Provide a concise response based on the content above.`, content, prompt)

	req := provider.ChatRequest{
		Model:     a.model,
		MaxTokens: 4096,
		Messages: []message.Message{
			message.NewUserMessage(userMsg),
		},
	}

	ch, err := a.p.StreamChat(ctx, req)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for evt := range ch {
		switch evt.Type {
		case "text_delta":
			sb.WriteString(evt.Delta)
		case "error":
			return "", evt.Error
		}
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		return content, nil // 降级返回原始内容
	}
	return result, nil
}
