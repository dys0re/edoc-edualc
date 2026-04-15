// Package mcp OAuth 2.0 + PKCE support for remote MCP servers.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
)

// FileTokenStore implements transport.TokenStore with file-based persistence.
// Tokens are stored at ~/.edoc/oauth_tokens/<server_name>.json
type FileTokenStore struct {
	filePath string
}

// NewFileTokenStore creates a file-based token store for the given server name.
func NewFileTokenStore(serverName string) *FileTokenStore {
	dir := tokenDir()
	os.MkdirAll(dir, 0700)
	return &FileTokenStore{
		filePath: filepath.Join(dir, serverName+".json"),
	}
}

func tokenDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".edoc", "oauth_tokens")
}

func (s *FileTokenStore) GetToken(ctx context.Context) (*transport.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, transport.ErrNoToken
		}
		return nil, err
	}
	var token transport.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *FileTokenStore) SaveToken(ctx context.Context, token *transport.Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0600)
}

// oauthResult holds the outcome of an OAuth flow.
type oauthResult struct {
	client *mcpclient.Client
	err    error
}

// handleOAuthFlow runs the full OAuth 2.0 + PKCE authorization flow.
// It opens a browser for user consent and waits for the callback.
func handleOAuthFlow(ctx context.Context, serverName, baseURL string, cfg oauthConfig) (*mcpclient.Client, error) {
	tokenStore := NewFileTokenStore(serverName)

	oauthCfg := transport.OAuthConfig{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Scopes:       cfg.Scopes,
		TokenStore:   tokenStore,
		PKCEEnabled:  cfg.PKCE,
	}

	// Try to get a valid token first (handles refresh automatically)
	handler := transport.NewOAuthHandler(oauthCfg)
	handler.SetBaseURL(baseURL)

	token, err := handler.GetAuthorizationHeader(ctx)
	if err == nil && token != "" {
		// We have a valid token, create an OAuth client
		log.Printf("[mcp] server %q: using cached OAuth token", serverName)
		return createOAuthClient(baseURL, oauthCfg, cfg.ServerType)
	}

	// Need to go through authorization flow
	codeVerifier, err := mcpclient.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("MCP OAuth %q: generate code verifier: %w", serverName, err)
	}
	codeChallenge := mcpclient.GenerateCodeChallenge(codeVerifier)
	state, err := mcpclient.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("MCP OAuth %q: generate state: %w", serverName, err)
	}

	// Find an available port for the callback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("MCP OAuth %q: failed to create callback listener: %w", serverName, err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Update redirect URI
	oauthCfg.RedirectURI = redirectURI
	handler = transport.NewOAuthHandler(oauthCfg)
	handler.SetBaseURL(baseURL)

	// Get authorization URL
	authURL, err := handler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("MCP OAuth %q: failed to get auth URL: %w", serverName, err)
	}

	// Dynamic client registration if no client ID
	if oauthCfg.ClientID == "" {
		if regErr := handler.RegisterClient(ctx, "edoc"); regErr != nil {
			log.Printf("[mcp] server %q: dynamic client registration failed (may not be supported): %v", serverName, regErr)
		}
		// Re-generate auth URL with new client ID
		authURL, err = handler.GetAuthorizationURL(ctx, state, codeChallenge)
		if err != nil {
			listener.Close()
			return nil, fmt.Errorf("MCP OAuth %q: failed to get auth URL after registration: %w", serverName, err)
		}
	}

	// Start callback server
	resultCh := make(chan oauthResult, 1)
	srv := &http.Server{}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			return
		}
		code := r.URL.Query().Get("code")
		callbackState := r.URL.Query().Get("state")
		if code == "" {
			http.Error(w, "missing code parameter", http.StatusBadRequest)
			resultCh <- oauthResult{err: fmt.Errorf("no code in callback")}
			return
		}
		if callbackState != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- oauthResult{err: fmt.Errorf("OAuth state mismatch")}
			return
		}

		// Exchange code for token
		if err := handler.ProcessAuthorizationResponse(ctx, code, callbackState, codeVerifier); err != nil {
			http.Error(w, fmt.Sprintf("token exchange failed: %v", err), http.StatusInternalServerError)
			resultCh <- oauthResult{err: err}
			return
		}

		// Success
		fmt.Fprintf(w, "<html><body><h1>Authorization successful!</h1><p>You can close this tab.</p></body></html>")
		client, err := createOAuthClient(baseURL, oauthCfg, cfg.ServerType)
		resultCh <- oauthResult{client: client, err: err}
	})

	go srv.Serve(listener)

	// Open browser
	fmt.Printf("\n[MCP] Server %q requires OAuth authorization.\n", serverName)
	fmt.Printf("[MCP] Opening browser: %s\n", authURL)
	openBrowser(authURL)

	// Wait for callback with timeout
	timeout := 120 * time.Second
	select {
	case result := <-resultCh:
		srv.Close()
		if result.err != nil {
			return nil, fmt.Errorf("MCP OAuth %q: %w", serverName, result.err)
		}
		return result.client, nil
	case <-time.After(timeout):
		srv.Close()
		return nil, fmt.Errorf("MCP OAuth %q: authorization timed out after %v", serverName, timeout)
	case <-ctx.Done():
		srv.Close()
		return nil, fmt.Errorf("MCP OAuth %q: cancelled: %w", serverName, ctx.Err())
	}
}

// oauthConfig holds OAuth parameters from our config.
type oauthConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	PKCE         bool
	ServerType   string // "sse" or "http"
}

// connectWithOAuth connects to an MCP server using OAuth.
// It tries cached tokens first, then falls back to the full auth flow.
func connectWithOAuth(ctx context.Context, name, baseURL string, cfg ServerConfig) (*mcpclient.Client, error) {
	oauthCfg := oauthConfig{
		ClientID:     cfg.OAuthClientID,
		ClientSecret: cfg.OAuthClientSecret,
		Scopes:       cfg.OAuthScopes,
		PKCE:         cfg.OAuthPKCE,
		ServerType:   strings.ToLower(cfg.Type),
	}
	return handleOAuthFlow(ctx, name, baseURL, oauthCfg)
}

// createOAuthClient creates an MCP client with OAuth support.
func createOAuthClient(baseURL string, oauthCfg transport.OAuthConfig, serverType string) (*mcpclient.Client, error) {
	switch serverType {
	case "http", "streamable-http":
		return mcpclient.NewOAuthStreamableHttpClient(baseURL, oauthCfg)
	default: // sse
		return mcpclient.NewOAuthSSEClient(baseURL, oauthCfg)
	}
}

// openBrowser opens the given URL in the user's default browser.
func openBrowser(url string) {
	// Best-effort; don't fail if we can't open the browser
	var name string
	var args []string
	switch {
	case commandExists("xdg-open"):
		name, args = "xdg-open", []string{url}
	case commandExists("open"):
		name, args = "open", []string{url}
	case commandExists("rundll32"):
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		log.Printf("[mcp] cannot open browser; please visit: %s", url)
		return
	}
	cmd := exec.Command(name, args...)
	cmd.Start()
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
