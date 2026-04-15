// Package mcp implements MCP (Model Context Protocol) client support.
// Manages connections to external MCP servers and exposes their tools
// as native tool.Tool instances in the agent registry.
// Maps to Claude Code's services/mcp/client.ts.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ServerConfig describes a single MCP server connection.
// Maps to Claude Code's McpSdkServerConfig.
type ServerConfig struct {
	// Type: "stdio" (default) or "sse" or "http"
	Type string `yaml:"type" mapstructure:"type"`

	// Stdio fields
	Command string   `yaml:"command" mapstructure:"command"`
	Args    []string `yaml:"args"    mapstructure:"args"`
	Env     []string `yaml:"env"     mapstructure:"env"` // KEY=VALUE pairs

	// SSE / HTTP fields
	URL     string            `yaml:"url"     mapstructure:"url"`
	Headers map[string]string `yaml:"headers" mapstructure:"headers"` // HTTP headers (e.g. Authorization)

	// OAuth fields (for SSE/HTTP servers requiring authentication)
	OAuthClientID     string   `yaml:"oauth_client_id"     mapstructure:"oauth_client_id"`
	OAuthClientSecret string   `yaml:"oauth_client_secret" mapstructure:"oauth_client_secret"`
	OAuthScopes       []string `yaml:"oauth_scopes"        mapstructure:"oauth_scopes"`
	OAuthRedirectURI  string   `yaml:"oauth_redirect_uri"  mapstructure:"oauth_redirect_uri"`
	OAuthPKCE         bool     `yaml:"oauth_pkce"          mapstructure:"oauth_pkce"`
}

// ConnectedServer holds a live MCP client connection and its discovered tools.
type ConnectedServer struct {
	Name   string
	Config ServerConfig
	client *mcpclient.Client
	Tools  []DiscoveredTool
}

// DiscoveredTool is a tool discovered from an MCP server.
type DiscoveredTool struct {
	// FullName is the namespaced name: mcp__<server>__<tool>
	FullName    string
	ServerName  string
	ToolName    string
	Description string
	// InputSchema is the raw JSON Schema from the MCP server
	InputSchema map[string]interface{}
	// Annotations from MCP server tool hints
	ReadOnlyHint    bool
	DestructiveHint bool
	IdempotentHint  bool
	OpenWorldHint   bool
	client          *mcpclient.Client
}

// toolRef is an index entry for O(1) tool lookup.
type toolRef struct {
	server *ConnectedServer
	tool   *DiscoveredTool
}

// Manager manages connections to all configured MCP servers.
type Manager struct {
	mu        sync.RWMutex
	servers   map[string]*ConnectedServer
	toolIndex map[string]*toolRef // fullName → ref
}

// NewManager creates an empty manager.
func NewManager() *Manager {
	return &Manager{
		servers:   make(map[string]*ConnectedServer),
		toolIndex: make(map[string]*toolRef),
	}
}

// Connect connects to all servers in the config map.
// Errors for individual servers are logged but don't abort others.
// Returns a list of connection errors (keyed by server name).
func (m *Manager) Connect(ctx context.Context, configs map[string]ServerConfig) map[string]error {
	errs := make(map[string]error)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, cfg := range configs {
		wg.Add(1)
		go func(n string, c ServerConfig) {
			defer wg.Done()
			if err := m.connectOne(ctx, n, c); err != nil {
				mu.Lock()
				errs[n] = err
				mu.Unlock()
			}
		}(name, cfg)
	}
	wg.Wait()
	return errs
}

func (m *Manager) connectOne(ctx context.Context, name string, cfg ServerConfig) error {
	var c *mcpclient.Client
	var err error

	hasOAuth := cfg.OAuthClientID != "" || cfg.OAuthPKCE

	switch strings.ToLower(cfg.Type) {
	case "sse", "http":
		if cfg.URL == "" {
			return fmt.Errorf("MCP server %q: url is required for type=%q", name, cfg.Type)
		}

		if hasOAuth {
			// Use OAuth-aware client directly
			c, err = connectWithOAuth(ctx, name, cfg.URL, cfg)
			if err != nil {
				return err
			}
		} else {
			// Try plain connection first
			var sseOpts []transport.ClientOption
			if len(cfg.Headers) > 0 {
				sseOpts = append(sseOpts, transport.WithHeaders(cfg.Headers))
			}
			c, err = mcpclient.NewSSEMCPClient(cfg.URL, sseOpts...)
			if err != nil {
				return fmt.Errorf("MCP server %q: SSE connect: %w", name, err)
			}
			if err = c.Start(ctx); err != nil {
				// Check if server requires OAuth
				if mcpclient.IsOAuthAuthorizationRequiredError(err) {
					log.Printf("[mcp] server %q requires OAuth, starting authorization flow", name)
					c, err = connectWithOAuth(ctx, name, cfg.URL, cfg)
					if err != nil {
						return err
					}
				} else {
					return fmt.Errorf("MCP server %q: SSE start: %w", name, err)
				}
			}
		}
	default: // stdio
		if cfg.Command == "" {
			return fmt.Errorf("MCP server %q: command is required for stdio transport", name)
		}
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
		if err != nil {
			return fmt.Errorf("MCP server %q: stdio connect: %w", name, err)
		}
	}

	// Initialize
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "edoc",
		Version: "0.1",
	}
	if _, err = c.Initialize(initCtx, initReq); err != nil {
		c.Close()
		return fmt.Errorf("MCP server %q: initialize: %w", name, err)
	}

	// Discover tools
	listCtx, cancel2 := context.WithTimeout(ctx, 15*time.Second)
	defer cancel2()

	result, err := c.ListTools(listCtx, mcp.ListToolsRequest{})
	if err != nil {
		c.Close()
		return fmt.Errorf("MCP server %q: list tools: %w", name, err)
	}

	tools := make([]DiscoveredTool, 0, len(result.Tools))
	for _, t := range result.Tools {
		schema := toolInputSchemaToMap(t)
		tools = append(tools, DiscoveredTool{
			FullName:        buildToolName(name, t.Name),
			ServerName:      name,
			ToolName:        t.Name,
			Description:     t.Description,
			InputSchema:     schema,
			ReadOnlyHint:    derefBool(t.Annotations.ReadOnlyHint, false),
			DestructiveHint: derefBool(t.Annotations.DestructiveHint, true),
			IdempotentHint:  derefBool(t.Annotations.IdempotentHint, false),
			OpenWorldHint:   derefBool(t.Annotations.OpenWorldHint, true),
			client:          c,
		})
	}

	m.mu.Lock()
	srv := &ConnectedServer{
		Name:   name,
		Config: cfg,
		client: c,
		Tools:  tools,
	}
	m.servers[name] = srv
	for i := range srv.Tools {
		m.toolIndex[srv.Tools[i].FullName] = &toolRef{server: srv, tool: &srv.Tools[i]}
	}
	m.mu.Unlock()

	return nil
}

// AllTools returns all discovered tools across all connected servers.
func (m *Manager) AllTools() []DiscoveredTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []DiscoveredTool
	for _, srv := range m.servers {
		all = append(all, srv.Tools...)
	}
	return all
}

// Close disconnects all servers.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, srv := range m.servers {
		if err := srv.client.Close(); err != nil {
			log.Printf("[mcp] close server %q: %v", name, err)
		}
	}
	m.servers = make(map[string]*ConnectedServer)
	m.toolIndex = make(map[string]*toolRef)
}

// CallTool invokes a tool on the appropriate MCP server.
// Uses O(1) index lookup. Attempts one reconnect on failure.
func (m *Manager) CallTool(ctx context.Context, fullName string, input json.RawMessage) (string, bool, error) {
	m.mu.RLock()
	ref, ok := m.toolIndex[fullName]
	m.mu.RUnlock()
	if !ok {
		return "", true, fmt.Errorf("MCP tool not found: %s", fullName)
	}

	content, isErr, err := callMCPTool(ctx, ref.tool.client, ref.tool.ToolName, input)
	if err != nil {
		// Attempt reconnect once
		serverName := ref.server.Name
		log.Printf("[mcp] tool call failed for %s, attempting reconnect to %q: %v", fullName, serverName, err)
		if reconnectErr := m.reconnectServer(ctx, serverName); reconnectErr != nil {
			return "", true, fmt.Errorf("MCP tool call failed: %w (reconnect also failed: %v)", err, reconnectErr)
		}
		// Re-lookup after reconnect (index is rebuilt)
		m.mu.RLock()
		ref, ok = m.toolIndex[fullName]
		m.mu.RUnlock()
		if !ok {
			return "", true, fmt.Errorf("MCP tool not found after reconnect: %s", fullName)
		}
		return callMCPTool(ctx, ref.tool.client, ref.tool.ToolName, input)
	}
	return content, isErr, nil
}

// reconnectServer closes and reconnects a single MCP server.
func (m *Manager) reconnectServer(ctx context.Context, name string) error {
	m.mu.RLock()
	srv, ok := m.servers[name]
	cfg := srv.Config
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("server %q not found for reconnect", name)
	}
	// Clean old index entries for this server
	m.mu.Lock()
	for k, ref := range m.toolIndex {
		if ref.server.Name == name {
			delete(m.toolIndex, k)
		}
	}
	delete(m.servers, name)
	m.mu.Unlock()

	return m.connectOne(ctx, name, cfg)
}

func callMCPTool(ctx context.Context, c *mcpclient.Client, toolName string, input json.RawMessage) (string, bool, error) {
	var args map[string]interface{}
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", true, fmt.Errorf("invalid tool input: %w", err)
		}
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	result, err := c.CallTool(ctx, req)
	if err != nil {
		return "", true, err
	}

	// Collect text content from result
	var parts []string
	for _, content := range result.Content {
		switch c := content.(type) {
		case mcp.TextContent:
			parts = append(parts, c.Text)
		default:
			// Non-text content: marshal to JSON
			b, _ := json.Marshal(c)
			parts = append(parts, string(b))
		}
	}

	return strings.Join(parts, "\n"), result.IsError, nil
}

// buildToolName creates the namespaced tool name: mcp__<server>__<tool>
// Maps to Claude Code's buildMcpToolName in mcpStringUtils.ts.
func buildToolName(serverName, toolName string) string {
	return "mcp__" + normalizeName(serverName) + "__" + normalizeName(toolName)
}

// normalizeName replaces non-alphanumeric chars with underscores.
func normalizeName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

// toolInputSchemaToMap converts mcp.Tool's InputSchema to map[string]interface{}.
func toolInputSchemaToMap(t mcp.Tool) map[string]interface{} {
	// Try RawInputSchema first (arbitrary JSON Schema)
	if t.RawInputSchema != nil {
		var m map[string]interface{}
		if err := json.Unmarshal(t.RawInputSchema, &m); err == nil {
			return m
		}
	}
	// Fall back to structured InputSchema
	schema := t.InputSchema
	result := map[string]interface{}{
		"type": schema.Type,
	}
	if schema.Properties != nil {
		result["properties"] = schema.Properties
	}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}
	return result
}

// derefBool dereferences a *bool with a default value.
func derefBool(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}
