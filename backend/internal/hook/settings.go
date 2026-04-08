package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// settingsFile is the settings file name within .edoc/
const settingsFile = "settings.json"

// settingsWrapper wraps the top-level settings.json structure.
// Only the "hooks" key is used; other keys are ignored for forward compatibility.
type settingsWrapper struct {
	Hooks map[string]json.RawMessage `json:"hooks"`
}

// LoadSettings loads hooks configuration from <workDir>/.edoc/settings.json.
// Returns nil config (not error) if the file doesn't exist.
// 对标 Claude Code 的 .claude/settings.json hooks 配置加载。
func LoadSettings(workDir string) (HooksConfig, error) {
	path := filepath.Join(workDir, ".edoc", settingsFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no settings file — not an error
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var wrapper settingsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(wrapper.Hooks) == 0 {
		return nil, nil
	}

	cfg := make(HooksConfig)
	for eventName, raw := range wrapper.Hooks {
		event := HookEvent(eventName)
		if !isValidEvent(event) {
			continue // skip unknown events for forward compatibility
		}

		var matchers []HookMatcher
		if err := json.Unmarshal(raw, &matchers); err != nil {
			return nil, fmt.Errorf("parse hooks.%s: %w", eventName, err)
		}
		if len(matchers) > 0 {
			cfg[event] = matchers
		}
	}

	return cfg, nil
}

// SettingsPath returns the path to the settings file for the given workDir.
func SettingsPath(workDir string) string {
	return filepath.Join(workDir, ".edoc", settingsFile)
}

func isValidEvent(e HookEvent) bool {
	switch e {
	case PreToolUse, PostToolUse, UserPromptSubmit, Stop:
		return true
	}
	return false
}
