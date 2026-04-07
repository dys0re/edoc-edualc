package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EditTool performs exact string replacement in files.
// Maps to FileEditTool.ts. Improvements over Claude Code's version:
//   - Tab/space normalization for fuzzy matching (the #1 Edit failure cause)
//   - Curly quote normalization (API transmission can mangle quotes)
//   - Trailing whitespace stripping on old_string
//   - replace_all support
type EditTool struct{}

func NewEditTool() *EditTool { return &EditTool{} }

func (t *EditTool) Name() string { return "Edit" }

func (t *EditTool) Description() string {
	return "Performs exact string replacements in files. old_string must uniquely match in the file."
}

func (t *EditTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to edit",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "The text to replace",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "The text to replace it with",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "Replace all occurrences (default false)",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}

type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func (t *EditTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in editInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.OldString == in.NewString {
		return &Result{Content: "No changes to make: old_string and new_string are exactly the same.", IsError: true}, nil
	}

	// Read file
	data, err := os.ReadFile(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Result{Content: fmt.Sprintf("File does not exist: %s", in.FilePath), IsError: true}, nil
		}
		return &Result{Content: fmt.Sprintf("Error reading file: %v", err), IsError: true}, nil
	}

	content := string(data)

	// Normalize line endings to LF for matching
	content = strings.ReplaceAll(content, "\r\n", "\n")
	in.OldString = strings.ReplaceAll(in.OldString, "\r\n", "\n")
	in.NewString = strings.ReplaceAll(in.NewString, "\r\n", "\n")

	// Find the actual string, with fuzzy matching fallbacks
	actualOld, err := findActualString(content, in.OldString)
	if err != nil {
		return &Result{Content: fmt.Sprintf("String to replace not found in file.\n%s", err.Error()), IsError: true}, nil
	}

	// Count matches
	matches := strings.Count(content, actualOld)

	if matches > 1 && !in.ReplaceAll {
		return &Result{
			Content: fmt.Sprintf("Found %d matches of the string to replace, but replace_all is false. "+
				"Provide more context to uniquely identify the instance, or set replace_all to true.", matches),
			IsError: true,
		}, nil
	}

	// Apply replacement
	var newContent string
	if in.ReplaceAll {
		newContent = strings.ReplaceAll(content, actualOld, in.NewString)
	} else {
		newContent = strings.Replace(content, actualOld, in.NewString, 1)
	}

	if newContent == content {
		return &Result{Content: "String not found in file. Failed to apply edit.", IsError: true}, nil
	}

	// Ensure parent directory exists (for safety)
	dir := filepath.Dir(in.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Result{Content: fmt.Sprintf("Error creating directory: %v", err), IsError: true}, nil
	}

	// Write back
	if err := os.WriteFile(in.FilePath, []byte(newContent), 0644); err != nil {
		return &Result{Content: fmt.Sprintf("Error writing file: %v", err), IsError: true}, nil
	}

	summary := formatEditSummary(in.FilePath, actualOld, in.NewString, in.ReplaceAll, matches)
	return &Result{Content: summary}, nil
}

func (t *EditTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *EditTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

// findActualString tries multiple matching strategies to locate old_string in content.
// Strategies in order:
//  1. Exact match
//  2. Curly quote normalization (API can convert " → \u201C/\u201D)
//  3. Trailing whitespace stripped match
//  4. Tab→space normalization (the #1 Edit failure cause)
//  5. Combined tab normalization + trailing whitespace strip
//
// Returns the actual string found in content (may differ from oldString in whitespace).
func findActualString(content, oldString string) (string, error) {
	if oldString == "" {
		return "", nil
	}

	// Strategy 1: Exact match
	if strings.Contains(content, oldString) {
		return oldString, nil
	}

	// Strategy 2: Normalize curly quotes to straight quotes
	quoteNormOld := normalizeQuotes(oldString)
	quoteNormFile := normalizeQuotes(content)
	if quoteNormOld != oldString {
		idx := strings.Index(quoteNormFile, quoteNormOld)
		if idx >= 0 {
			return content[idx : idx+len(oldString)], nil
		}
	}

	// Strategy 3: Strip trailing whitespace from each line of oldString
	strippedOld := stripTrailingWhitespace(oldString)
	if strippedOld != oldString && strings.Contains(content, strippedOld) {
		return strippedOld, nil
	}

	// Strategy 4: Normalize tabs to spaces
	tabNormOld := normalizeIndentation(oldString)
	tabNormContent := normalizeIndentation(content)

	if tabNormOld != oldString {
		idx := strings.Index(tabNormContent, tabNormOld)
		if idx >= 0 {
			actual, err := mapBackToOriginal(content, tabNormContent, idx, len(tabNormOld))
			if err == nil {
				return actual, nil
			}
		}
	}

	// Strategy 5: Combined — normalize tabs + strip trailing whitespace
	if strippedOld != oldString && tabNormOld != oldString {
		combinedOld := normalizeIndentation(strippedOld)
		if combinedOld != tabNormOld {
			idx := strings.Index(tabNormContent, combinedOld)
			if idx >= 0 {
				actual, err := mapBackToOriginal(content, tabNormContent, idx, len(combinedOld))
				if err == nil {
					return actual, nil
				}
			}
		}
	}

	return "", fmt.Errorf("String to replace not found in file.\nString: %s", truncateString(oldString, 200))
}

// normalizeQuotes converts curly quotes to straight quotes.
// API transmission can convert " → \u201C/\u201D, ' → \u2018/\u2019.
func normalizeQuotes(s string) string {
	s = strings.ReplaceAll(s, "\u201C", `"`)  // left double curly → straight
	s = strings.ReplaceAll(s, "\u201D", `"`)  // right double curly → straight
	s = strings.ReplaceAll(s, "\u2018", "'")  // left single curly → straight
	s = strings.ReplaceAll(s, "\u2019", "'")  // right single curly → straight
	return s
}

// normalizeIndentation converts all leading tabs on each line to 4 spaces.
func normalizeIndentation(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = normalizeLeadingTabs(line)
	}
	return strings.Join(lines, "\n")
}

func normalizeLeadingTabs(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\t' {
			b.WriteString("    ")
			i++
		} else {
			break
		}
	}
	b.WriteString(s[i:])
	return b.String()
}

// mapBackToOriginal maps a match position in normalized content back to the
// actual substring in the original content.
func mapBackToOriginal(original, normalized string, normIdx, normLen int) (string, error) {
	origIdx := 0
	normPos := 0

	for origIdx < len(original) && normPos < normIdx {
		if original[origIdx] == '\t' && normPos+4 <= len(normalized) && normalized[normPos:normPos+4] == "    " {
			origIdx++
			normPos += 4
		} else if original[origIdx] == '\r' && origIdx+1 < len(original) && original[origIdx+1] == '\n' {
			origIdx += 2
			normPos++
		} else {
			origIdx++
			normPos++
		}
	}

	if normPos != normIdx {
		return "", fmt.Errorf("position mapping mismatch")
	}

	startOrig := origIdx
	endNorm := normIdx + normLen
	for origIdx < len(original) && normPos < endNorm {
		if original[origIdx] == '\t' && normPos+4 <= len(normalized) && normalized[normPos:normPos+4] == "    " {
			origIdx++
			normPos += 4
		} else if original[origIdx] == '\r' && origIdx+1 < len(original) && original[origIdx+1] == '\n' {
			origIdx += 2
			normPos++
		} else {
			origIdx++
			normPos++
		}
	}

	if origIdx > len(original) {
		return "", fmt.Errorf("end position out of bounds")
	}

	return original[startOrig:origIdx], nil
}

func stripTrailingWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func formatEditSummary(filePath, oldStr, newStr string, replaceAll bool, matches int) string {
	oldLines := strings.Count(oldStr, "\n") + 1
	newLines := strings.Count(newStr, "\n") + 1

	if replaceAll && matches > 1 {
		return fmt.Sprintf("The file %s has been updated. Replaced %d occurrences.", filePath, matches)
	}

	if oldLines == 1 && newLines == 1 {
		return fmt.Sprintf("The file %s has been updated.", filePath)
	}

	return fmt.Sprintf("The file %s has been updated (%d lines → %d lines).", filePath, oldLines, newLines)
}
