package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type GrepTool struct{}

func NewGrepTool() *GrepTool { return &GrepTool{} }

func (t *GrepTool) Name() string { return "Grep" }

func (t *GrepTool) Description() string {
	return "Search file contents using regex patterns."
}

func (t *GrepTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Regex pattern to search for",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory to search in",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to filter files (e.g. *.go)",
			},
		},
		"required": []string{"pattern"},
	}
}

type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Glob    string `json:"glob,omitempty"`
}

func (t *GrepTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in grepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Invalid regex: %v", err), IsError: true}, nil
	}

	root := in.Path
	if root == "" {
		root, _ = os.Getwd()
	}

	var matches []string
	maxMatches := 250

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if len(matches) >= maxMatches {
			return filepath.SkipAll
		}

		// Apply glob filter
		if in.Glob != "" {
			matched, _ := filepath.Match(in.Glob, filepath.Base(path))
			if !matched {
				return nil
			}
		}

		// Skip binary/large files
		if info.Size() > 1<<20 { // 1MB
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		rel, _ := filepath.Rel(root, path)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNum, line))
				if len(matches) >= maxMatches {
					break
				}
			}
		}
		return nil
	})

	if err != nil {
		return &Result{Content: fmt.Sprintf("Search error: %v", err), IsError: true}, nil
	}

	if len(matches) == 0 {
		return &Result{Content: "No matches found"}, nil
	}

	return &Result{Content: strings.Join(matches, "\n")}, nil
}

func (t *GrepTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *GrepTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *GrepTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *GrepTool) PermissionDescription(_ json.RawMessage) string { return "" }
func (t *GrepTool) IsFileEdit(_ json.RawMessage) bool        { return false }
