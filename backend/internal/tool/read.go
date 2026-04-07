package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ReadTool struct{}

func NewReadTool() *ReadTool { return &ReadTool{} }

func (t *ReadTool) Name() string { return "Read" }

func (t *ReadTool) Description() string {
	return "Read a file from the filesystem."
}

func (t *ReadTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to read",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Line number to start reading from (0-based)",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Number of lines to read",
			},
		},
		"required": []string{"file_path"},
	}
}

type readInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func (t *ReadTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in readInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	data, err := os.ReadFile(in.FilePath)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error reading file: %v", err), IsError: true}, nil
	}

	lines := strings.Split(string(data), "\n")

	start := in.Offset
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}

	end := len(lines)
	if in.Limit > 0 {
		end = start + in.Limit
		if end > len(lines) {
			end = len(lines)
		}
	}

	// Format with line numbers like cat -n
	var sb strings.Builder
	for i := start; i < end; i++ {
		sb.WriteString(strconv.Itoa(i+1))
		sb.WriteString("\t")
		sb.WriteString(lines[i])
		if i < end-1 {
			sb.WriteString("\n")
		}
	}

	return &Result{Content: sb.String()}, nil
}

func (t *ReadTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *ReadTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
