package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type GlobTool struct{}

func NewGlobTool() *GlobTool { return &GlobTool{} }

func (t *GlobTool) Name() string { return "Glob" }

func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern."
}

func (t *GlobTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to match files (e.g. **/*.go)",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search in",
			},
		},
		"required": []string{"pattern"},
	}
}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

func (t *GlobTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	root := in.Path
	if root == "" {
		root, _ = os.Getwd()
	}

	pattern := filepath.Join(root, in.Pattern)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Glob error: %v", err), IsError: true}, nil
	}

	sort.Strings(matches)

	if len(matches) == 0 {
		return &Result{Content: "No files found"}, nil
	}

	// Return relative paths
	var lines []string
	for _, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			rel = m
		}
		lines = append(lines, rel)
	}

	return &Result{Content: strings.Join(lines, "\n")}, nil
}

func (t *GlobTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *GlobTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
