package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// settingsWrapper wraps the top-level .edoc/settings.json structure.
// Extends hook/settings.go pattern to include lsp_servers.
type lspSettingsWrapper struct {
	LSPServers map[string]json.RawMessage `json:"lsp_servers"`
}

// LoadLSPSettings loads LSP server configurations from <workDir>/.edoc/settings.json.
// Returns nil config (not error) if the file doesn't exist or has no lsp_servers.
func LoadLSPSettings(workDir string) (map[string]ServerConfig, error) {
	path := filepath.Join(workDir, ".edoc", "settings.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var wrapper lspSettingsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(wrapper.LSPServers) == 0 {
		return nil, nil
	}

	configs := make(map[string]ServerConfig, len(wrapper.LSPServers))
	for name, raw := range wrapper.LSPServers {
		var cfg ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse lsp_servers.%s: %w", name, err)
		}
		if cfg.Command == "" {
			continue // skip entries without command
		}
		configs[name] = cfg
	}

	return configs, nil
}
