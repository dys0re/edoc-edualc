package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type WriteTool struct{}

func NewWriteTool() *WriteTool { return &WriteTool{} }

func (t *WriteTool) Name() string { return "Write" }

func (t *WriteTool) Description() string {
	return "Write content to a file, creating directories as needed."
}

func (t *WriteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to write",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "The content to write",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func (t *WriteTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in writeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	dir := filepath.Dir(in.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Result{Content: fmt.Sprintf("Error creating directory: %v", err), IsError: true}, nil
	}

	if err := os.WriteFile(in.FilePath, []byte(in.Content), 0644); err != nil {
		return &Result{Content: fmt.Sprintf("Error writing file: %v", err), IsError: true}, nil
	}

	return &Result{Content: fmt.Sprintf("Successfully wrote %d bytes to %s", len(in.Content), in.FilePath)}, nil
}

func (t *WriteTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *WriteTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
