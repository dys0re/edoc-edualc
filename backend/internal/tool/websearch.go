package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	bochaSearchURL     = "https://api.bochaai.com/v1/web-search"
	bochaSearchTimeout = 30 * time.Second
)

// WebSearchTool 调用 Bocha AI Web Search API 搜索网页。
// 对标 Claude Code 的 WebSearchTool。
type WebSearchTool struct {
	APIKey string
}

func (t *WebSearchTool) Name() string { return "WebSearch" }

func (t *WebSearchTool) Description() string {
	return `Search the web and use the results to inform responses.
- Provides up-to-date information for current events and recent data
- Returns search result information including titles, URLs, snippets, and summaries
- Use this tool for accessing information beyond the model's knowledge cutoff

Usage notes:
  - The query should be a clear, specific search query
  - Results include web pages with name, url, snippet, summary, siteName, datePublished
  - Use allowed_domains to restrict results to specific websites
  - Use freshness to filter by time range: noLimit, day, week, month, year
  - count controls number of results (1-10, default 8)`
}

func (t *WebSearchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "The search query",
			},
			"count": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results to return (1-10, default 8)",
			},
			"freshness": map[string]interface{}{
				"type":        "string",
				"description": "Time range filter: noLimit (default), day, week, month, year",
				"enum":        []string{"noLimit", "day", "week", "month", "year"},
			},
			"summary": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to include AI-generated summaries for each result (default true)",
			},
		},
		"required": []string{"query"},
	}
}

type webSearchInput struct {
	Query     string `json:"query"`
	Count     int    `json:"count,omitempty"`
	Freshness string `json:"freshness,omitempty"`
	Summary   *bool  `json:"summary,omitempty"`
}

// bochaRequest is the request body sent to Bocha API.
type bochaRequest struct {
	Query     string `json:"query"`
	Freshness string `json:"freshness,omitempty"`
	Summary   bool   `json:"summary"`
	Count     int    `json:"count"`
}

// bochaResponse mirrors the Bing-compatible response format from Bocha.
type bochaResponse struct {
	Type         string `json:"_type"`
	QueryContext struct {
		OriginalQuery string `json:"originalQuery"`
	} `json:"queryContext"`
	WebPages *struct {
		Value []bochaWebPage `json:"value"`
	} `json:"webPages"`
	Images *struct {
		Value []bochaImage `json:"value"`
	} `json:"images"`
}

type bochaWebPage struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	Summary       string `json:"summary"`
	SiteName      string `json:"siteName"`
	SiteIcon      string `json:"siteIcon"`
	DatePublished string `json:"datePublished"`
}

type bochaImage struct {
	Name           string `json:"name"`
	ContentURL     string `json:"contentUrl"`
	HostPageURL    string `json:"hostPageUrl"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
}

func (t *WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	if t.APIKey == "" {
		return &Result{Content: "Error: WebSearch requires BOCHA_API_KEY to be configured", IsError: true}, nil
	}

	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	if in.Query == "" {
		return &Result{Content: "Error: query is required", IsError: true}, nil
	}

	count := in.Count
	if count <= 0 {
		count = 8
	}
	freshness := in.Freshness
	if freshness == "" {
		freshness = "noLimit"
	}
	summary := true
	if in.Summary != nil {
		summary = *in.Summary
	}

	reqBody := bochaRequest{
		Query:     in.Query,
		Freshness: freshness,
		Summary:   summary,
		Count:     count,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error encoding request: %v", err), IsError: true}, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, bochaSearchURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error creating request: %v", err), IsError: true}, nil
	}
	httpReq.Header.Set("Authorization", "Bearer "+t.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: bochaSearchTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error calling search API: %v", err), IsError: true}, nil
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error reading response: %v", err), IsError: true}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return &Result{Content: fmt.Sprintf("Search API error (HTTP %d): %s", resp.StatusCode, string(respBytes)), IsError: true}, nil
	}

	var bochaResp bochaResponse
	if err := json.Unmarshal(respBytes, &bochaResp); err != nil {
		return &Result{Content: fmt.Sprintf("Error parsing response: %v\nRaw: %s", err, string(respBytes)), IsError: true}, nil
	}

	return &Result{Content: formatSearchResults(&bochaResp)}, nil
}

// formatSearchResults formats the Bocha response into readable markdown.
func formatSearchResults(r *bochaResponse) string {
	if r.WebPages == nil || len(r.WebPages.Value) == 0 {
		return "No results found."
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Search results for: %s\n\n", r.QueryContext.OriginalQuery)

	for i, page := range r.WebPages.Value {
		fmt.Fprintf(&buf, "%d. **%s**\n", i+1, page.Name)
		fmt.Fprintf(&buf, "   URL: %s\n", page.URL)
		if page.SiteName != "" {
			fmt.Fprintf(&buf, "   Site: %s\n", page.SiteName)
		}
		if page.DatePublished != "" {
			fmt.Fprintf(&buf, "   Published: %s\n", page.DatePublished)
		}
		if page.Snippet != "" {
			fmt.Fprintf(&buf, "   %s\n", page.Snippet)
		}
		if page.Summary != "" && page.Summary != page.Snippet {
			fmt.Fprintf(&buf, "   Summary: %s\n", page.Summary)
		}
		buf.WriteByte('\n')
	}

	return buf.String()
}

func (t *WebSearchTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *WebSearchTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *WebSearchTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *WebSearchTool) IsFileEdit(_ json.RawMessage) bool        { return false }

func (t *WebSearchTool) PermissionDescription(_ json.RawMessage) string {
	return "Search the web"
}
