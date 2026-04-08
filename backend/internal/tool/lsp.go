package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/lsp"
)

// LSPTool exposes LSP operations (goToDefinition, findReferences, hover, documentSymbol)
// to the model. 对标 Claude Code 的 tools/LSPTool/LSPTool.ts。
type LSPTool struct {
	Manager *lsp.Manager
}

func (t *LSPTool) Name() string { return "LSP" }

func (t *LSPTool) Description() string {
	return `Use the Language Server Protocol to perform code intelligence operations.
Operations:
- goToDefinition: Find where a symbol is defined
- findReferences: Find all references to a symbol
- hover: Get type/documentation info for a symbol
- documentSymbol: List all symbols in a file

line and character are 1-based (as shown in editors).`
}

func (t *LSPTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"goToDefinition", "findReferences", "hover", "documentSymbol"},
				"description": "The LSP operation to perform",
			},
			"filePath": map[string]interface{}{
				"type":        "string",
				"description": "Absolute or relative path to the file",
			},
			"line": map[string]interface{}{
				"type":        "integer",
				"description": "Line number (1-based, as shown in editors)",
			},
			"character": map[string]interface{}{
				"type":        "integer",
				"description": "Character offset (1-based, as shown in editors)",
			},
		},
		"required": []string{"operation", "filePath"},
	}
}

type lspInput struct {
	Operation string `json:"operation"`
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

func (t *LSPTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in lspInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.FilePath == "" {
		return &Result{Content: "filePath is required", IsError: true}, nil
	}

	absPath, err := filepath.Abs(in.FilePath)
	if err != nil {
		absPath = in.FilePath
	}

	switch in.Operation {
	case "goToDefinition":
		if in.Line == 0 || in.Character == 0 {
			return &Result{Content: "line and character are required for goToDefinition", IsError: true}, nil
		}
		locs, err := t.Manager.GoToDefinition(absPath, in.Line, in.Character)
		if err != nil {
			return &Result{Content: fmt.Sprintf("goToDefinition error: %v", err), IsError: true}, nil
		}
		return &Result{Content: formatLocations("Definition", locs)}, nil

	case "findReferences":
		if in.Line == 0 || in.Character == 0 {
			return &Result{Content: "line and character are required for findReferences", IsError: true}, nil
		}
		locs, err := t.Manager.FindReferences(absPath, in.Line, in.Character)
		if err != nil {
			return &Result{Content: fmt.Sprintf("findReferences error: %v", err), IsError: true}, nil
		}
		return &Result{Content: formatLocations("References", locs)}, nil

	case "hover":
		if in.Line == 0 || in.Character == 0 {
			return &Result{Content: "line and character are required for hover", IsError: true}, nil
		}
		hover, err := t.Manager.Hover(absPath, in.Line, in.Character)
		if err != nil {
			return &Result{Content: fmt.Sprintf("hover error: %v", err), IsError: true}, nil
		}
		if hover == nil {
			return &Result{Content: "No hover information available at this position"}, nil
		}
		return &Result{Content: hover.Content}, nil

	case "documentSymbol":
		symbols, err := t.Manager.DocumentSymbol(absPath)
		if err != nil {
			return &Result{Content: fmt.Sprintf("documentSymbol error: %v", err), IsError: true}, nil
		}
		return &Result{Content: formatSymbols(symbols)}, nil

	default:
		return &Result{
			Content: fmt.Sprintf("Unknown operation: %s. Use goToDefinition, findReferences, hover, or documentSymbol.", in.Operation),
			IsError: true,
		}, nil
	}
}

func (t *LSPTool) IsReadOnly(_ json.RawMessage) bool          { return true }
func (t *LSPTool) IsConcurrencySafe(_ json.RawMessage) bool    { return true }
func (t *LSPTool) NeedsApproval(_ json.RawMessage) bool        { return false }
func (t *LSPTool) PermissionDescription(_ json.RawMessage) string { return "" }
func (t *LSPTool) IsFileEdit(_ json.RawMessage) bool           { return false }

// --- formatting helpers ---

func formatLocations(label string, locs []lsp.Location) string {
	if len(locs) == 0 {
		return fmt.Sprintf("No %s found", strings.ToLower(label))
	}

	// Count unique files
	files := make(map[string]bool)
	for _, l := range locs {
		files[l.FilePath] = true
	}

	var sb strings.Builder
	if len(locs) == 1 {
		sb.WriteString(fmt.Sprintf("%s found:\n", label))
	} else {
		sb.WriteString(fmt.Sprintf("%s (%d in %d file(s)):\n", label, len(locs), len(files)))
	}

	for _, l := range locs {
		sb.WriteString(fmt.Sprintf("  %s:%d:%d\n", l.FilePath, l.Line, l.Char))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatSymbols(symbols []lsp.Symbol) string {
	if len(symbols) == 0 {
		return "No symbols found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Symbols (%d):\n", countSymbols(symbols)))
	formatSymbolsIndented(&sb, symbols, "  ")
	return strings.TrimRight(sb.String(), "\n")
}

func formatSymbolsIndented(sb *strings.Builder, symbols []lsp.Symbol, indent string) {
	for _, s := range symbols {
		sb.WriteString(fmt.Sprintf("%s[%s] %s (line %d)\n", indent, s.Kind, s.Name, s.Line))
		if len(s.Children) > 0 {
			formatSymbolsIndented(sb, s.Children, indent+"  ")
		}
	}
}

func countSymbols(symbols []lsp.Symbol) int {
	count := len(symbols)
	for _, s := range symbols {
		count += countSymbols(s.Children)
	}
	return count
}
