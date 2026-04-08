package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager manages multiple LSP server instances and routes requests by file extension.
// 对标 Claude Code 的 LSPServerManager.ts。
type Manager struct {
	mu           sync.RWMutex
	servers      map[string]*ServerInstance // name → instance
	extensionMap map[string][]string        // ".go" → ["gopls"]
	openedFiles  map[string]string          // fileURI → serverName
	workDir      string
}

// ServerInstance wraps a Client with state management and restart logic.
// 对标 Claude Code 的 LSPServerInstance.ts。
type ServerInstance struct {
	Name   string
	Config ServerConfig
	client *Client
	mu     sync.Mutex
}

// NewManager creates an empty LSP manager.
func NewManager(workDir string) *Manager {
	return &Manager{
		servers:      make(map[string]*ServerInstance),
		extensionMap: make(map[string][]string),
		openedFiles:  make(map[string]string),
		workDir:      workDir,
	}
}

// Initialize builds the extension map and creates server instances (lazy — not started yet).
func (m *Manager) Initialize(configs map[string]ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, cfg := range configs {
		if cfg.Command == "" {
			continue
		}

		instance := &ServerInstance{
			Name:   name,
			Config: cfg,
			client: NewClient(name),
		}
		m.servers[name] = instance

		// Build extension → server mapping
		for ext := range cfg.Extensions {
			normalized := strings.ToLower(ext)
			m.extensionMap[normalized] = append(m.extensionMap[normalized], name)
		}
	}

	return nil
}

// Shutdown stops all running servers.
func (m *Manager) Shutdown() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for name, inst := range m.servers {
		if inst.client.State() == StateRunning || inst.client.State() == StateError {
			if err := inst.client.Shutdown(); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			}
		}
	}
	m.servers = make(map[string]*ServerInstance)
	m.extensionMap = make(map[string][]string)
	m.openedFiles = make(map[string]string)

	if len(errs) > 0 {
		return fmt.Errorf("LSP shutdown errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// IsConnected returns true if at least one server is not in error state.
func (m *Manager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.servers {
		if inst.client.State() != StateError {
			return true
		}
	}
	return len(m.servers) > 0
}

// GetServerForFile returns the server instance for a file path, or nil.
func (m *Manager) GetServerForFile(filePath string) *ServerInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ext := strings.ToLower(filepath.Ext(filePath))
	names := m.extensionMap[ext]
	if len(names) == 0 {
		return nil
	}
	return m.servers[names[0]]
}

// EnsureServerStarted lazily starts the server for the given file type.
// 对标 LSPServerManager.ts:ensureServerStarted — 懒启动。
func (m *Manager) EnsureServerStarted(filePath string) (*ServerInstance, error) {
	inst := m.GetServerForFile(filePath)
	if inst == nil {
		return nil, nil
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()

	state := inst.client.State()
	if state == StateRunning {
		return inst, nil
	}

	if state == StateError {
		maxRestarts := inst.Config.MaxRestarts
		if maxRestarts == 0 {
			maxRestarts = 3
		}
		if inst.client.restartCount >= maxRestarts {
			return nil, fmt.Errorf("LSP %s: exceeded max restarts (%d)", inst.Name, maxRestarts)
		}
		// Attempt restart
		inst.client.Shutdown()
		inst.client = NewClient(inst.Name)
		inst.client.restartCount++
	}

	cwd := inst.Config.WorkspaceDir
	if cwd == "" {
		cwd = m.workDir
	}

	if err := inst.client.Start(inst.Config.Command, inst.Config.Args, inst.Config.Env, cwd); err != nil {
		return nil, err
	}

	timeout := time.Duration(inst.Config.StartupTimeout) * time.Millisecond
	if timeout == 0 {
		timeout = defaultStartupTimeout
	}

	if err := inst.client.Initialize(cwd, timeout); err != nil {
		inst.client.Shutdown()
		return nil, err
	}

	return inst, nil
}

// --- File synchronization (called by Edit/Write tools) ---

// OpenFile sends textDocument/didOpen to the appropriate server.
func (m *Manager) OpenFile(filePath string, content string) error {
	inst, err := m.EnsureServerStarted(filePath)
	if inst == nil || err != nil {
		return err
	}

	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	m.mu.Lock()
	if m.openedFiles[uri] == inst.Name {
		m.mu.Unlock()
		return nil // already open
	}
	m.mu.Unlock()

	ext := strings.ToLower(filepath.Ext(filePath))
	languageID := inst.Config.Extensions[ext]
	if languageID == "" {
		languageID = "plaintext"
	}

	err = inst.client.SendNotification("textDocument/didOpen", map[string]interface{}{
		"textDocument": lspTextDocumentItem{
			URI:        uri,
			LanguageID: languageID,
			Version:    1,
			Text:       content,
		},
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.openedFiles[uri] = inst.Name
	m.mu.Unlock()
	return nil
}

// ChangeFile sends textDocument/didChange (full content replacement).
func (m *Manager) ChangeFile(filePath string, content string) error {
	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	m.mu.RLock()
	serverName := m.openedFiles[uri]
	m.mu.RUnlock()

	if serverName == "" {
		// File not opened yet — open it
		return m.OpenFile(filePath, content)
	}

	m.mu.RLock()
	inst := m.servers[serverName]
	m.mu.RUnlock()
	if inst == nil || !inst.client.IsHealthy() {
		return m.OpenFile(filePath, content)
	}

	return inst.client.SendNotification("textDocument/didChange", map[string]interface{}{
		"textDocument": lspVersionedTextDocumentIdentifier{URI: uri, Version: 1},
		"contentChanges": []lspTextDocumentContentChangeEvent{
			{Text: content},
		},
	})
}

// SaveFile sends textDocument/didSave.
func (m *Manager) SaveFile(filePath string) error {
	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	m.mu.RLock()
	serverName := m.openedFiles[uri]
	m.mu.RUnlock()
	if serverName == "" {
		return nil
	}

	m.mu.RLock()
	inst := m.servers[serverName]
	m.mu.RUnlock()
	if inst == nil || !inst.client.IsHealthy() {
		return nil
	}

	return inst.client.SendNotification("textDocument/didSave", map[string]interface{}{
		"textDocument": lspTextDocumentIdentifier{URI: uri},
	})
}

// --- LSP requests (called by LSPTool) ---

// ensureFileOpen reads the file and opens it in the LSP server if not already open.
func (m *Manager) ensureFileOpen(filePath string) (*ServerInstance, error) {
	inst, err := m.EnsureServerStarted(filePath)
	if inst == nil || err != nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	m.mu.RLock()
	alreadyOpen := m.openedFiles[uri] == inst.Name
	m.mu.RUnlock()

	if !alreadyOpen {
		content, err := os.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("read file for LSP: %w", err)
		}
		if err := m.OpenFile(filePath, string(content)); err != nil {
			return nil, err
		}
	}

	return inst, nil
}

// GoToDefinition returns definition locations for the symbol at the given position.
func (m *Manager) GoToDefinition(filePath string, line, char int) ([]Location, error) {
	inst, err := m.ensureFileOpen(filePath)
	if inst == nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	result, err := inst.client.SendRequest("textDocument/definition", map[string]interface{}{
		"textDocument": lspTextDocumentIdentifier{URI: uri},
		"position":     lspPosition{Line: line - 1, Character: char - 1},
	})
	if err != nil {
		return nil, err
	}

	return parseLocations(result)
}

// FindReferences returns all references to the symbol at the given position.
func (m *Manager) FindReferences(filePath string, line, char int) ([]Location, error) {
	inst, err := m.ensureFileOpen(filePath)
	if inst == nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	result, err := inst.client.SendRequest("textDocument/references", map[string]interface{}{
		"textDocument": lspTextDocumentIdentifier{URI: uri},
		"position":     lspPosition{Line: line - 1, Character: char - 1},
		"context":      map[string]bool{"includeDeclaration": true},
	})
	if err != nil {
		return nil, err
	}

	return parseLocations(result)
}

// Hover returns hover information for the symbol at the given position.
func (m *Manager) Hover(filePath string, line, char int) (*HoverResult, error) {
	inst, err := m.ensureFileOpen(filePath)
	if inst == nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	result, err := inst.client.SendRequest("textDocument/hover", map[string]interface{}{
		"textDocument": lspTextDocumentIdentifier{URI: uri},
		"position":     lspPosition{Line: line - 1, Character: char - 1},
	})
	if err != nil {
		return nil, err
	}

	if result == nil || string(result) == "null" {
		return nil, nil
	}

	var hover lspHover
	if err := json.Unmarshal(result, &hover); err != nil {
		return nil, err
	}

	content := extractHoverContent(hover.Contents)
	if content == "" {
		return nil, nil
	}
	return &HoverResult{Content: content}, nil
}

// DocumentSymbol returns symbols in the given file.
func (m *Manager) DocumentSymbol(filePath string) ([]Symbol, error) {
	inst, err := m.ensureFileOpen(filePath)
	if inst == nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(filePath)
	uri := filePathToURI(absPath)

	result, err := inst.client.SendRequest("textDocument/documentSymbol", map[string]interface{}{
		"textDocument": lspTextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return nil, err
	}

	if result == nil || string(result) == "null" {
		return nil, nil
	}

	return parseSymbols(result, filePath)
}

// --- result parsing helpers ---

// parseLocations handles both Location[] and LocationLink[] responses.
func parseLocations(raw json.RawMessage) ([]Location, error) {
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}

	// Try as single Location
	var single lspLocation
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{locationFromLSP(single)}, nil
	}

	// Try as Location[]
	var locs []lspLocation
	if err := json.Unmarshal(raw, &locs); err == nil && len(locs) > 0 && locs[0].URI != "" {
		result := make([]Location, 0, len(locs))
		for _, l := range locs {
			if l.URI != "" {
				result = append(result, locationFromLSP(l))
			}
		}
		return result, nil
	}

	// Try as LocationLink[]
	var links []lspLocationLink
	if err := json.Unmarshal(raw, &links); err == nil && len(links) > 0 {
		result := make([]Location, 0, len(links))
		for _, l := range links {
			if l.TargetURI != "" {
				result = append(result, Location{
					FilePath: uriToFilePath(l.TargetURI),
					Line:     l.TargetSelectionRange.Start.Line + 1,
					Char:     l.TargetSelectionRange.Start.Character + 1,
				})
			}
		}
		return result, nil
	}

	return nil, nil
}

func locationFromLSP(l lspLocation) Location {
	return Location{
		FilePath: uriToFilePath(l.URI),
		Line:     l.Range.Start.Line + 1,
		Char:     l.Range.Start.Character + 1,
	}
}

// extractHoverContent extracts text from various hover content formats.
func extractHoverContent(contents interface{}) string {
	if contents == nil {
		return ""
	}

	// Re-marshal and try different formats
	raw, err := json.Marshal(contents)
	if err != nil {
		return fmt.Sprintf("%v", contents)
	}

	// Try MarkupContent { kind, value }
	var mc lspMarkupContent
	if err := json.Unmarshal(raw, &mc); err == nil && mc.Value != "" {
		return mc.Value
	}

	// Try plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}

	// Try MarkedString[] or string[]
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, item := range arr {
			var str string
			if json.Unmarshal(item, &str) == nil {
				parts = append(parts, str)
				continue
			}
			var marked struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			}
			if json.Unmarshal(item, &marked) == nil && marked.Value != "" {
				if marked.Language != "" {
					parts = append(parts, "```"+marked.Language+"\n"+marked.Value+"\n```")
				} else {
					parts = append(parts, marked.Value)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}

	return string(raw)
}

// parseSymbols handles both DocumentSymbol[] and SymbolInformation[] responses.
func parseSymbols(raw json.RawMessage, filePath string) ([]Symbol, error) {
	// Try DocumentSymbol[] (has "range" field)
	var docSymbols []lspDocumentSymbol
	if err := json.Unmarshal(raw, &docSymbols); err == nil && len(docSymbols) > 0 {
		// Check if it's really DocumentSymbol (has range)
		if hasRangeField(raw) {
			return convertDocumentSymbols(docSymbols, filePath), nil
		}
	}

	// Try SymbolInformation[] (has "location" field)
	var symInfos []lspSymbolInformation
	if err := json.Unmarshal(raw, &symInfos); err == nil && len(symInfos) > 0 {
		result := make([]Symbol, 0, len(symInfos))
		for _, si := range symInfos {
			result = append(result, Symbol{
				Name:     si.Name,
				Kind:     symbolKindName(si.Kind),
				FilePath: uriToFilePath(si.Location.URI),
				Line:     si.Location.Range.Start.Line + 1,
				Char:     si.Location.Range.Start.Character + 1,
			})
		}
		return result, nil
	}

	return nil, nil
}

func convertDocumentSymbols(symbols []lspDocumentSymbol, filePath string) []Symbol {
	result := make([]Symbol, 0, len(symbols))
	for _, ds := range symbols {
		s := Symbol{
			Name:     ds.Name,
			Kind:     symbolKindName(ds.Kind),
			FilePath: filePath,
			Line:     ds.SelectionRange.Start.Line + 1,
			Char:     ds.SelectionRange.Start.Character + 1,
		}
		if len(ds.Children) > 0 {
			s.Children = convertDocumentSymbols(ds.Children, filePath)
		}
		result = append(result, s)
	}
	return result
}

// hasRangeField checks if the first element of a JSON array has a "range" field.
func hasRangeField(raw json.RawMessage) bool {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		_, ok := arr[0]["range"]
		return ok
	}
	return false
}
