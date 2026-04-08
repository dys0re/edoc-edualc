// Package lsp implements LSP (Language Server Protocol) client support.
// 对标 Claude Code 的 services/lsp/，通过 stdio spawn LSP server 子进程，
// 用 JSON-RPC 2.0 通信，提供 go-to-definition、find-references、hover、
// document-symbol 等代码智能能力。
package lsp

import "encoding/json"

// ServerConfig describes a single LSP server connection.
// 对标 Claude Code 的 ScopedLspServerConfig。
type ServerConfig struct {
	Command        string            `json:"command"`                    // 可执行文件
	Args           []string          `json:"args,omitempty"`             // 命令行参数
	Env            map[string]string `json:"env,omitempty"`              // 环境变量
	Extensions     map[string]string `json:"extensions"`                 // ".go" → "go" (扩展名 → languageId)
	WorkspaceDir   string            `json:"workspace_dir,omitempty"`    // 工作目录，空=使用全局 workDir
	StartupTimeout int               `json:"startup_timeout,omitempty"`  // 初始化超时 ms，默认 30000
	MaxRestarts    int               `json:"max_restarts,omitempty"`     // 最大重启次数，默认 3
}

// ServerState 状态机，对标 LSPServerInstance.ts 的 LspServerState
type ServerState string

const (
	StateStopped  ServerState = "stopped"
	StateStarting ServerState = "starting"
	StateRunning  ServerState = "running"
	StateStopping ServerState = "stopping"
	StateError    ServerState = "error"
)

// Location represents an LSP location result (1-based line/char).
type Location struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`      // 1-based
	Char     int    `json:"character"` // 1-based
}

// HoverResult holds hover information.
type HoverResult struct {
	Content string `json:"content"` // markdown or plaintext
}

// Symbol represents a document or workspace symbol.
type Symbol struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`               // "Function" / "Class" / "Variable" etc.
	FilePath string   `json:"file_path,omitempty"` // only for workspace symbols
	Line     int      `json:"line"`                // 1-based
	Char     int      `json:"character"`           // 1-based
	Children []Symbol `json:"children,omitempty"`  // nested children (documentSymbol)
}

// --- JSON-RPC 2.0 types ---

type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonrpcError) Error() string {
	return e.Message
}

// jsonrpcNotification is a JSON-RPC notification (no id).
type jsonrpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// --- LSP protocol types (minimal subset) ---

// lspPosition is 0-based, as per LSP spec.
type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

// lspLocationLink is the alternative response format for definition/implementation.
type lspLocationLink struct {
	TargetURI            string   `json:"targetUri"`
	TargetRange          lspRange `json:"targetRange"`
	TargetSelectionRange lspRange `json:"targetSelectionRange"`
}

type lspTextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type lspTextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type lspVersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type lspTextDocumentContentChangeEvent struct {
	Text string `json:"text"` // full content replacement
}

// lspHover is the response from textDocument/hover.
type lspHover struct {
	Contents interface{} `json:"contents"` // string | MarkupContent | MarkedString[]
}

// lspMarkupContent is used in hover responses.
type lspMarkupContent struct {
	Kind  string `json:"kind"`  // "plaintext" | "markdown"
	Value string `json:"value"`
}

// lspDocumentSymbol is the hierarchical symbol format.
type lspDocumentSymbol struct {
	Name           string              `json:"name"`
	Kind           int                 `json:"kind"`
	Range          lspRange            `json:"range"`
	SelectionRange lspRange            `json:"selectionRange"`
	Children       []lspDocumentSymbol `json:"children,omitempty"`
}

// lspSymbolInformation is the flat symbol format.
type lspSymbolInformation struct {
	Name     string      `json:"name"`
	Kind     int         `json:"kind"`
	Location lspLocation `json:"location"`
}

// symbolKindName maps LSP SymbolKind numbers to human-readable names.
func symbolKindName(kind int) string {
	names := map[int]string{
		1: "File", 2: "Module", 3: "Namespace", 4: "Package",
		5: "Class", 6: "Method", 7: "Property", 8: "Field",
		9: "Constructor", 10: "Enum", 11: "Interface", 12: "Function",
		13: "Variable", 14: "Constant", 15: "String", 16: "Number",
		17: "Boolean", 18: "Array", 19: "Object", 20: "Key",
		21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event",
		25: "Operator", 26: "TypeParameter",
	}
	if n, ok := names[kind]; ok {
		return n
	}
	return "Unknown"
}
