package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// MCPTool wraps a DiscoveredTool as a tool.Tool for the agent registry.
// Maps to Claude Code's MCPTool template in services/mcp/client.ts.
type MCPTool struct {
	discovered DiscoveredTool
	manager    *Manager
}

// NewMCPTool creates a tool.Tool from a DiscoveredTool.
func NewMCPTool(d DiscoveredTool, m *Manager) tool.Tool {
	return &MCPTool{discovered: d, manager: m}
}

func (t *MCPTool) Name() string        { return t.discovered.FullName }
func (t *MCPTool) Description() string { return t.discovered.Description }

func (t *MCPTool) InputSchema() map[string]interface{} {
	if t.discovered.InputSchema != nil {
		return t.discovered.InputSchema
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func (t *MCPTool) Execute(ctx context.Context, input json.RawMessage) (*tool.Result, error) {
	content, isError, err := t.manager.CallTool(ctx, t.discovered.FullName, input)
	if err != nil {
		return &tool.Result{Content: fmt.Sprintf("MCP error: %v", err), IsError: true}, nil
	}
	return &tool.Result{Content: content, IsError: isError}, nil
}

func (t *MCPTool) IsReadOnly(_ json.RawMessage) bool {
	return t.discovered.ReadOnlyHint
}

func (t *MCPTool) IsConcurrencySafe(_ json.RawMessage) bool {
	return t.discovered.IdempotentHint
}

func (t *MCPTool) NeedsApproval(_ json.RawMessage) bool {
	return !t.discovered.ReadOnlyHint || t.discovered.DestructiveHint
}

func (t *MCPTool) IsFileEdit(_ json.RawMessage) bool { return false }

func (t *MCPTool) PermissionDescription(_ json.RawMessage) string {
	hint := ""
	if t.discovered.ReadOnlyHint {
		hint = " [read-only]"
	}
	return fmt.Sprintf("MCP: %s (%s)%s", t.discovered.ToolName, t.discovered.ServerName, hint)
}

// RegisterTools adds all tools from the manager into the given registry.
func RegisterTools(reg *tool.Registry, m *Manager) {
	for _, d := range m.AllTools() {
		dt := d // capture
		reg.Register(NewMCPTool(dt, m))
	}
}
