package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/genai-io/san/internal/mcp/transport"
)

const (
	// ProtocolVersion is the MCP protocol version this client supports
	ProtocolVersion = "2024-11-05"

	// ClientName is the name of this MCP client
	ClientName = "san"

	// ClientVersion is the version of this MCP client
	ClientVersion = "1.0.0"
)

var requestIDCounter atomic.Uint64

// nextRequestID generates a unique request ID
func nextRequestID() uint64 {
	return requestIDCounter.Add(1)
}

// Client is an MCP client that connects to a single MCP server
type Client struct {
	config    ServerConfig
	transport transport.Transport

	// TransportFactory overrides the default transport creation.
	// When set, Connect() uses this instead of createTransport().
	// This allows tests to inject a fake transport.
	TransportFactory func() (transport.Transport, error)

	mu           sync.RWMutex
	connected    bool
	capabilities ServerCapabilities
	serverInfo   ServerInfo
	tools        []MCPTool
	resources    []MCPResource
	prompts      []MCPPrompt

	// Callbacks for dynamic updates
	onToolsChanged func()
}

// NewClient creates a new MCP client for the given server configuration
func NewClient(config ServerConfig) *Client {
	return &Client{
		config: config,
	}
}

// newRequest creates a new JSON-RPC request
func newRequest(method string, params any) *transport.JSONRPCRequest {
	return &transport.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      nextRequestID(),
		Method:  method,
		Params:  params,
	}
}

// newNotification creates a new JSON-RPC notification
func newNotification(method string, params any) *transport.JSONRPCNotification {
	return &transport.JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
}

// parseResponse parses a JSON-RPC response and unmarshals the result
func parseResponse(resp *transport.JSONRPCResponse, target any) error {
	if resp.Error != nil {
		return fmt.Errorf("JSON-RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, target)
}

// createTransport creates the appropriate transport based on config type
func (c *Client) createTransport() (transport.Transport, error) {
	switch c.config.GetType() {
	case TransportSTDIO:
		return transport.NewSTDIOTransport(transport.STDIOConfig{
			Command: c.config.Command,
			Args:    c.config.Args,
			Env:     c.config.Env,
		}), nil
	case TransportHTTP:
		return transport.NewHTTPTransport(transport.HTTPConfig{
			URL:     c.config.URL,
			Headers: c.config.Headers,
		}), nil
	case TransportSSE:
		return transport.NewSSETransport(transport.SSEConfig{
			URL:     c.config.URL,
			Headers: c.config.Headers,
		}), nil
	default:
		return nil, fmt.Errorf("unknown transport type: %s", c.config.GetType())
	}
}

// Connect establishes a connection to the MCP server
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	var trans transport.Transport
	var err error
	if c.TransportFactory != nil {
		trans, err = c.TransportFactory()
	} else {
		trans, err = c.createTransport()
	}
	if err != nil {
		return err
	}
	c.transport = trans

	// Start transport
	if err := c.transport.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %w", err)
	}

	// Set up notification handler
	c.transport.SetNotificationHandler(c.handleNotification)

	// Send initialize request
	initParams := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    ClientCapabilities{},
		ClientInfo: ClientInfo{
			Name:    ClientName,
			Version: ClientVersion,
		},
	}

	req := newRequest(MethodInitialize, initParams)
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		_ = c.transport.Close()
		return fmt.Errorf("initialize request failed: %w", err)
	}

	var initResult InitializeResult
	if err := parseResponse(resp, &initResult); err != nil {
		_ = c.transport.Close()
		return fmt.Errorf("failed to parse initialize response: %w", err)
	}

	c.capabilities = initResult.Capabilities
	c.serverInfo = initResult.ServerInfo

	// Send initialized notification
	notif := newNotification(MethodInitialized, nil)
	if err := c.transport.SendNotification(ctx, notif); err != nil {
		_ = c.transport.Close()
		return fmt.Errorf("failed to send initialized notification: %w", err)
	}

	c.connected = true

	// Fetch initial tool list
	if c.capabilities.Tools != nil {
		if tools, err := c.listToolsLocked(ctx); err == nil {
			c.tools = tools
		}
	}

	// Fetch initial resource list
	if c.capabilities.Resources != nil {
		if resources, err := c.listResourcesLocked(ctx); err == nil {
			c.resources = resources
		}
	}

	// Fetch initial prompt list
	if c.capabilities.Prompts != nil {
		if prompts, err := c.listPromptsLocked(ctx); err == nil {
			c.prompts = prompts
		}
	}

	return nil
}

// Disconnect closes the connection to the MCP server
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	c.connected = false
	if c.transport != nil {
		return c.transport.Close()
	}
	return nil
}

// IsConnected returns true if the client is connected
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected && c.transport != nil && c.transport.IsAlive()
}

// GetServerInfo returns information about the connected server
func (c *Client) GetServerInfo() ServerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverInfo
}

// GetCapabilities returns the server's capabilities
func (c *Client) GetCapabilities() ServerCapabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capabilities
}

// ListTools returns the tools available from the server
func (c *Client) ListTools(ctx context.Context) ([]MCPTool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listToolsLocked(ctx)
}

func (c *Client) listToolsLocked(ctx context.Context) ([]MCPTool, error) {
	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	req := newRequest(MethodToolsList, nil)
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list request failed: %w", err)
	}

	var result ToolsListResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools/list response: %w", err)
	}

	c.tools = result.Tools
	return result.Tools, nil
}

// GetCachedTools returns a copy of the cached tools without making an API call.
func (c *Client) GetCachedTools() []MCPTool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]MCPTool, len(c.tools))
	copy(out, c.tools)
	return out
}

// getTransport returns the transport if connected, or an error
func (c *Client) getTransport() (transport.Transport, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}
	return c.transport, nil
}

// CallTool calls a tool on the MCP server
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	trans, err := c.getTransport()
	if err != nil {
		return nil, err
	}

	params := ToolsCallParams{
		Name:      name,
		Arguments: arguments,
	}

	req := newRequest(MethodToolsCall, params)
	resp, err := trans.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/call request failed: %w", err)
	}

	var result ToolResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools/call response: %w", err)
	}

	return &result, nil
}

// ListResources returns the resources available from the server
func (c *Client) ListResources(ctx context.Context) ([]MCPResource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listResourcesLocked(ctx)
}

func (c *Client) listResourcesLocked(ctx context.Context) ([]MCPResource, error) {
	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	req := newRequest(MethodResourcesList, nil)
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resources/list request failed: %w", err)
	}

	var result ResourcesListResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resources/list response: %w", err)
	}

	c.resources = result.Resources
	return result.Resources, nil
}

// GetCachedResources returns a copy of the cached resources without making an API call.
func (c *Client) GetCachedResources() []MCPResource {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]MCPResource, len(c.resources))
	copy(out, c.resources)
	return out
}

// ReadResource reads a resource from the MCP server
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	trans, err := c.getTransport()
	if err != nil {
		return nil, err
	}

	params := ResourcesReadParams{URI: uri}
	req := newRequest(MethodResourcesRead, params)
	resp, err := trans.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resources/read request failed: %w", err)
	}

	var result ResourcesReadResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resources/read response: %w", err)
	}

	return result.Contents, nil
}

// ListPrompts returns the prompts available from the server
func (c *Client) ListPrompts(ctx context.Context) ([]MCPPrompt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listPromptsLocked(ctx)
}

func (c *Client) listPromptsLocked(ctx context.Context) ([]MCPPrompt, error) {
	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	req := newRequest(MethodPromptsList, nil)
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("prompts/list request failed: %w", err)
	}

	var result PromptsListResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse prompts/list response: %w", err)
	}

	c.prompts = result.Prompts
	return result.Prompts, nil
}

// GetCachedPrompts returns a copy of the cached prompts without making an API call.
func (c *Client) GetCachedPrompts() []MCPPrompt {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]MCPPrompt, len(c.prompts))
	copy(out, c.prompts)
	return out
}

// GetPrompt retrieves a specific prompt with the given arguments
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) (*PromptResult, error) {
	trans, err := c.getTransport()
	if err != nil {
		return nil, err
	}

	params := PromptsGetParams{
		Name:      name,
		Arguments: arguments,
	}

	req := newRequest(MethodPromptsGet, params)
	resp, err := trans.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("prompts/get request failed: %w", err)
	}

	var result PromptResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse prompts/get response: %w", err)
	}

	return &result, nil
}

// Ping sends a ping to check if the server is responsive
func (c *Client) Ping(ctx context.Context) error {
	trans, err := c.getTransport()
	if err != nil {
		return err
	}

	req := newRequest(MethodPing, nil)
	resp, err := trans.Send(ctx, req)
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	return parseResponse(resp, nil)
}

// SetOnToolsChanged sets a callback for when tools list changes
func (c *Client) SetOnToolsChanged(callback func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onToolsChanged = callback
}

// handleNotification processes incoming notifications from the server.
// Runs in a goroutine to avoid deadlocking when Connect() holds mu.
func (c *Client) handleNotification(method string, _ []byte) {
	if method != MethodToolsListChanged {
		return
	}

	go func() {
		ctx := context.Background()
		if _, err := c.ListTools(ctx); err != nil {
			return // Tool list refresh failed; will retry on next notification
		}

		c.mu.RLock()
		callback := c.onToolsChanged
		c.mu.RUnlock()

		if callback != nil {
			callback()
		}
	}()
}

// Config returns the server configuration
func (c *Client) Config() ServerConfig {
	return c.config
}

// ToServer converts the client state to a Server struct for display
func (c *Client) ToServer() Server {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return Server{
		Config:       c.config,
		Status:       c.getStatusLocked(),
		Capabilities: c.capabilities,
		ServerInfo:   c.serverInfo,
		Tools:        c.tools,
		Resources:    c.resources,
		Prompts:      c.prompts,
	}
}

// getStatusLocked returns the current connection status (must be called with lock held)
func (c *Client) getStatusLocked() ServerStatus {
	if !c.connected {
		return StatusDisconnected
	}
	if c.transport != nil && c.transport.IsAlive() {
		return StatusConnected
	}
	return StatusError
}

// MarshalJSON implements json.Marshaler for debugging
func (c *Client) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.ToServer())
}
