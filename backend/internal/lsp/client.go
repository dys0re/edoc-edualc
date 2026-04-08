package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const defaultStartupTimeout = 30 * time.Second

// Client manages a JSON-RPC 2.0 connection to a single LSP server over stdio.
// 对标 Claude Code 的 LSPClient.ts + LSPServerInstance.ts。
type Client struct {
	name string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	writeMu sync.Mutex   // protects stdin writes
	nextID  atomic.Int64 // request ID counter

	pendingMu sync.Mutex
	pending   map[int64]chan *jsonrpcResponse

	capabilities json.RawMessage
	initialized  bool

	state        ServerState
	restartCount int
	lastError    error
	stateMu      sync.Mutex

	done chan struct{} // closed when readLoop exits
}

// NewClient creates a new LSP client (not yet started).
func NewClient(name string) *Client {
	return &Client{
		name:    name,
		pending: make(map[int64]chan *jsonrpcResponse),
		state:   StateStopped,
		done:    make(chan struct{}),
	}
}

// State returns the current server state.
func (c *Client) State() ServerState {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state
}

// IsHealthy returns true if the server is running and initialized.
func (c *Client) IsHealthy() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state == StateRunning && c.initialized
}

// Start spawns the LSP server process and begins the read loop.
func (c *Client) Start(command string, args []string, env map[string]string, cwd string) error {
	c.stateMu.Lock()
	if c.state == StateRunning || c.state == StateStarting {
		c.stateMu.Unlock()
		return nil
	}
	c.state = StateStarting
	c.stateMu.Unlock()

	c.cmd = exec.Command(command, args...)
	c.cmd.Dir = cwd

	// Build environment
	c.cmd.Env = os.Environ()
	for k, v := range env {
		c.cmd.Env = append(c.cmd.Env, k+"="+v)
	}

	// Hide console window on Windows
	setSysProcAttr(c.cmd)

	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		c.setState(StateError, err)
		return fmt.Errorf("LSP %s: stdin pipe: %w", c.name, err)
	}

	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		c.setState(StateError, err)
		return fmt.Errorf("LSP %s: stdout pipe: %w", c.name, err)
	}
	c.reader = bufio.NewReaderSize(stdout, 64*1024)

	// Discard stderr (could log in debug mode)
	c.cmd.Stderr = io.Discard

	if err := c.cmd.Start(); err != nil {
		c.setState(StateError, err)
		return fmt.Errorf("LSP %s: start: %w", c.name, err)
	}

	c.done = make(chan struct{})
	go c.readLoop()

	return nil
}

// Initialize performs the LSP initialize/initialized handshake.
// 对标 LSPServerInstance.ts:167-237
func (c *Client) Initialize(workspaceDir string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = defaultStartupTimeout
	}

	absDir, _ := filepath.Abs(workspaceDir)
	workspaceURI := filePathToURI(absDir)

	params := map[string]interface{}{
		"processId": os.Getpid(),
		"rootUri":   workspaceURI,
		"rootPath":  absDir,
		"workspaceFolders": []map[string]string{
			{"uri": workspaceURI, "name": filepath.Base(absDir)},
		},
		"initializationOptions": map[string]interface{}{},
		"capabilities": map[string]interface{}{
			"workspace": map[string]interface{}{
				"configuration":    false,
				"workspaceFolders": false,
			},
			"textDocument": map[string]interface{}{
				"synchronization": map[string]interface{}{
					"dynamicRegistration": false,
					"willSave":            false,
					"willSaveWaitUntil":   false,
					"didSave":             true,
				},
				"publishDiagnostics": map[string]interface{}{
					"relatedInformation": true,
				},
				"hover": map[string]interface{}{
					"dynamicRegistration": false,
					"contentFormat":       []string{"markdown", "plaintext"},
				},
				"definition": map[string]interface{}{
					"dynamicRegistration": false,
					"linkSupport":         true,
				},
				"references": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"documentSymbol": map[string]interface{}{
					"dynamicRegistration":              false,
					"hierarchicalDocumentSymbolSupport": true,
				},
			},
			"general": map[string]interface{}{
				"positionEncodings": []string{"utf-16"},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := c.sendRequestCtx(ctx, "initialize", params)
	if err != nil {
		c.setState(StateError, err)
		return fmt.Errorf("LSP %s: initialize: %w", c.name, err)
	}

	c.capabilities = result

	// Send initialized notification
	if err := c.SendNotification("initialized", map[string]interface{}{}); err != nil {
		c.setState(StateError, err)
		return fmt.Errorf("LSP %s: initialized notification: %w", c.name, err)
	}

	c.stateMu.Lock()
	c.initialized = true
	c.state = StateRunning
	c.stateMu.Unlock()

	return nil
}

// Shutdown gracefully stops the LSP server (shutdown + exit + kill).
func (c *Client) Shutdown() error {
	c.stateMu.Lock()
	if c.state == StateStopped || c.state == StateStopping {
		c.stateMu.Unlock()
		return nil
	}
	c.state = StateStopping
	c.stateMu.Unlock()

	// Try graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.sendRequestCtx(ctx, "shutdown", nil) // ignore error
	c.SendNotification("exit", nil)        // ignore error

	// Close stdin to signal EOF
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Wait for readLoop to finish or timeout
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
	}

	// Kill process if still running
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait() // reap zombie
	}

	// Drain pending requests
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()

	c.stateMu.Lock()
	c.state = StateStopped
	c.initialized = false
	c.stateMu.Unlock()

	return nil
}

// SendRequest sends a JSON-RPC request and waits for the response.
func (c *Client) SendRequest(method string, params interface{}) (json.RawMessage, error) {
	return c.sendRequestCtx(context.Background(), method, params)
}

func (c *Client) sendRequestCtx(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if !c.IsHealthy() && method != "initialize" && method != "shutdown" {
		return nil, fmt.Errorf("LSP %s: server not healthy (state=%s)", c.name, c.State())
	}

	id := c.nextID.Add(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	respCh := make(chan *jsonrpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		c.removePending(id)
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if err := c.writeMessage(data); err != nil {
		c.removePending(id)
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("LSP %s: connection closed", c.name)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.removePending(id)
		return nil, ctx.Err()
	}
}

// SendNotification sends a JSON-RPC notification (no response expected).
func (c *Client) SendNotification(method string, params interface{}) error {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	return c.writeMessage(data)
}

// --- internal ---

func (c *Client) setState(s ServerState, err error) {
	c.stateMu.Lock()
	c.state = s
	if err != nil {
		c.lastError = err
	}
	c.stateMu.Unlock()
}

func (c *Client) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// readLoop reads JSON-RPC messages from stdout and dispatches them.
func (c *Client) readLoop() {
	defer close(c.done)

	for {
		data, err := c.readMessage()
		if err != nil {
			// EOF or pipe closed — normal during shutdown
			return
		}

		// Try to parse as response (has "id" field)
		var resp jsonrpcResponse
		if err := json.Unmarshal(data, &resp); err == nil && resp.ID != nil {
			c.pendingMu.Lock()
			ch, ok := c.pending[*resp.ID]
			if ok {
				delete(c.pending, *resp.ID)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- &resp
			}
			continue
		}

		// It's a notification (no id) — we ignore most for now
		// Could handle textDocument/publishDiagnostics here in the future
	}
}

// writeMessage writes a Content-Length framed message to stdin.
func (c *Client) writeMessage(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err := c.stdin.Write(data)
	return err
}

// readMessage reads a Content-Length framed message from stdout.
func (c *Client) readMessage() ([]byte, error) {
	// Read headers until empty line
	contentLength := -1
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "Content-Length: "))
			if err == nil {
				contentLength = n
			}
		}
	}

	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

// filePathToURI converts a file path to a file:// URI.
func filePathToURI(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(path) > 0 && path[0] != '/' {
		path = "/" + path
	}
	return "file://" + url.PathEscape(path)
}

// uriToFilePath converts a file:// URI back to a file path.
func uriToFilePath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	path := strings.TrimPrefix(uri, "file://")
	decoded, err := url.PathUnescape(path)
	if err != nil {
		decoded = path
	}
	// On Windows, /C:/path → C:/path
	if runtime.GOOS == "windows" && len(decoded) > 2 && decoded[0] == '/' && decoded[2] == ':' {
		decoded = decoded[1:]
	}
	return filepath.FromSlash(decoded)
}

// setSysProcAttr sets platform-specific process attributes.
// On Windows, hides the console window.
func setSysProcAttr(cmd *exec.Cmd) {
	// Platform-specific implementation in client_windows.go / client_other.go
	setSysProcAttrImpl(cmd)
}
