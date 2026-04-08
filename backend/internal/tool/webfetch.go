package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

const (
	webFetchMaxContentBytes = 10 * 1024 * 1024 // 10MB
	webFetchMaxMarkdownLen  = 100_000           // 截断阈值，避免 token 爆炸
	webFetchCacheTTL        = 15 * time.Minute
	webFetchTimeout         = 60 * time.Second
	webFetchMaxRedirects    = 10

	// 伪装成浏览器，提高抓取成功率
	webFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// WebFetchTool 抓取 URL 内容，转为 Markdown，用二次模型处理后返回。
// 对标 Claude Code 的 WebFetchTool。
type WebFetchTool struct {
	// Provider 用于二次模型调用（提取/摘要内容）
	// 若为 nil，直接返回原始 Markdown（截断后）
	Provider WebFetchProvider

	cache   webFetchCache
	client  *http.Client
	initOnce sync.Once
}

// WebFetchProvider 是 WebFetchTool 对 provider 包的最小依赖接口，避免循环导入。
type WebFetchProvider interface {
	// Summarize 用小模型对 markdown 内容执行 prompt，返回结果。
	Summarize(ctx context.Context, content, prompt string) (string, error)
}

type webFetchCacheEntry struct {
	content   string
	expiresAt time.Time
}

type webFetchCache struct {
	mu      sync.Mutex
	entries map[string]webFetchCacheEntry
}

func (c *webFetchCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		delete(c.entries, key)
		return "", false
	}
	return e.content, true
}

func (c *webFetchCache) set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]webFetchCacheEntry)
	}
	c.entries[key] = webFetchCacheEntry{
		content:   value,
		expiresAt: time.Now().Add(webFetchCacheTTL),
	}
}

func (t *WebFetchTool) init() {
	t.initOnce.Do(func() {
		// 手动处理重定向，安全控制跳转行为
		t.client = &http.Client{
			Timeout: webFetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= webFetchMaxRedirects {
					return fmt.Errorf("too many redirects (exceeded %d)", webFetchMaxRedirects)
				}
				// 只允许同域重定向（含 www 变换）
				if len(via) > 0 {
					orig := via[0].URL
					next := req.URL
					if !isSameOriginRedirect(orig, next) {
						// 跨域重定向：停止跟随，返回当前响应
						return http.ErrUseLastResponse
					}
				}
				return nil
			},
		}
	})
}

// isSameOriginRedirect 允许同域（含 www 变换）重定向。
func isSameOriginRedirect(orig, next *url.URL) bool {
	if orig.Scheme != next.Scheme {
		return false
	}
	if orig.Port() != next.Port() {
		return false
	}
	stripWWW := func(h string) string {
		return strings.TrimPrefix(h, "www.")
	}
	return stripWWW(orig.Hostname()) == stripWWW(next.Hostname())
}

func (t *WebFetchTool) Name() string { return "WebFetch" }

func (t *WebFetchTool) Description() string {
	return `Fetches content from a specified URL and processes it using an AI model.
- Takes a URL and a prompt as input
- Fetches the URL content, converts HTML to markdown
- Processes the content with the prompt using a small, fast model
- Returns the model's response about the content
- Use this tool when you need to retrieve and analyze web content

Usage notes:
  - The URL must be a fully-formed valid URL
  - HTTP URLs will be automatically upgraded to HTTPS
  - The prompt should describe what information you want to extract from the page
  - This tool is read-only and does not modify any files
  - Results may be summarized if the content is very large
  - Includes a self-cleaning 15-minute cache for faster responses when repeatedly accessing the same URL
  - When a URL redirects to a different host, the tool will inform you and provide the redirect URL. Make a new WebFetch request with the redirect URL to fetch the content.
  - For GitHub URLs, prefer using the Bash tool with gh CLI instead (e.g., gh pr view, gh issue view, gh api).`
}

func (t *WebFetchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch content from",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The prompt to run on the fetched content",
			},
		},
		"required": []string{"url", "prompt"},
	}
}

type webFetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

func (t *WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	t.init()

	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.URL == "" {
		return &Result{Content: "Error: url is required", IsError: true}, nil
	}
	if in.Prompt == "" {
		return &Result{Content: "Error: prompt is required", IsError: true}, nil
	}

	// 升级 http → https
	fetchURL := in.URL
	if strings.HasPrefix(fetchURL, "http://") {
		fetchURL = "https://" + fetchURL[7:]
	}

	// 基本 URL 校验
	parsed, err := url.Parse(fetchURL)
	if err != nil || parsed.Host == "" {
		return &Result{Content: fmt.Sprintf("Error: invalid URL %q", in.URL), IsError: true}, nil
	}

	// 缓存命中
	if cached, ok := t.cache.get(fetchURL); ok {
		result, err := t.applyPrompt(ctx, cached, in.Prompt)
		if err != nil {
			return &Result{Content: fmt.Sprintf("Error processing content: %v", err), IsError: true}, nil
		}
		return &Result{Content: result}, nil
	}

	// 抓取
	markdown, finalURL, err := t.fetchMarkdown(ctx, fetchURL)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error fetching URL: %v", err), IsError: true}, nil
	}

	// 跨域重定向提示
	if finalURL != "" && finalURL != fetchURL {
		msg := fmt.Sprintf(`REDIRECT DETECTED: The URL redirects to a different host.

Original URL: %s
Redirect URL: %s

To complete your request, please use WebFetch again with:
- url: %q
- prompt: %q`, fetchURL, finalURL, finalURL, in.Prompt)
		return &Result{Content: msg}, nil
	}

	t.cache.set(fetchURL, markdown)

	result, err := t.applyPrompt(ctx, markdown, in.Prompt)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error processing content: %v", err), IsError: true}, nil
	}
	return &Result{Content: result}, nil
}

// fetchMarkdown 抓取 URL，返回 (markdown内容, 跨域重定向目标URL, error)
func (t *WebFetchTool) fetchMarkdown(ctx context.Context, rawURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", webFetchUserAgent)
	req.Header.Set("Accept", "text/html,text/markdown,*/*")

	resp, err := t.client.Do(req)
	if err != nil {
		// 检查是否是跨域重定向被拦截（ErrUseLastResponse 会触发正常响应返回，不会到这里）
		return "", "", err
	}
	defer resp.Body.Close()

	// 检查是否停在了跨域重定向（CheckRedirect 返回 ErrUseLastResponse 时 resp 是重定向响应）
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		location := resp.Header.Get("Location")
		if location != "" {
			redirectURL, err := url.Parse(location)
			if err == nil && redirectURL.Host != "" {
				return "", location, nil
			}
		}
	}

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxContentBytes))
	if err != nil {
		return "", "", fmt.Errorf("reading response: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	var markdown string
	if strings.Contains(contentType, "text/html") {
		md, err := htmltomarkdown.ConvertString(string(body))
		if err != nil {
			// 转换失败降级为原始文本
			markdown = string(body)
		} else {
			markdown = md
		}
	} else {
		markdown = string(body)
	}

	// 截断
	if len(markdown) > webFetchMaxMarkdownLen {
		markdown = markdown[:webFetchMaxMarkdownLen] + "\n\n[Content truncated due to length...]"
	}

	return markdown, "", nil
}

// applyPrompt 用二次模型对内容执行 prompt；无 provider 时直接返回内容。
func (t *WebFetchTool) applyPrompt(ctx context.Context, content, prompt string) (string, error) {
	if t.Provider == nil {
		// 无二次模型，直接返回 markdown（已截断）
		return content, nil
	}
	return t.Provider.Summarize(ctx, content, prompt)
}

func (t *WebFetchTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *WebFetchTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *WebFetchTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *WebFetchTool) IsFileEdit(_ json.RawMessage) bool        { return false }

func (t *WebFetchTool) PermissionDescription(input json.RawMessage) string {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "Fetch web page"
	}
	parsed, err := url.Parse(in.URL)
	if err != nil || parsed.Host == "" {
		return "Fetch web page"
	}
	return "Fetch " + parsed.Host
}
