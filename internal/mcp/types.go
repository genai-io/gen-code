// Package mcp implements Model Context Protocol (MCP) client functionality.
// It provides support for connecting to MCP servers via STDIO, HTTP, and SSE transports.
package mcp

import "encoding/json"

// TransportType defines the type of MCP transport
type TransportType string

const (
	TransportSTDIO TransportType = "stdio"
	TransportHTTP  TransportType = "http"
	TransportSSE   TransportType = "sse"
)

// Scope defines where the MCP configuration is stored
type Scope string

const (
	ScopeUser    Scope = "user"    // ~/.san/mcp.json (global)
	ScopeProject Scope = "project" // ./.san/mcp.json (team shared)
	ScopeLocal   Scope = "local"   // ./.san/mcp.local.json (personal, git-ignored)
)

// ServerConfig represents an MCP server configuration
type ServerConfig struct {
	// Name is the unique identifier for this server
	Name string `json:"name,omitempty"`

	// Type is the transport type (stdio, http, sse). Default is stdio.
	Type TransportType `json:"type,omitempty"`

	// STDIO transport fields
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// HTTP/SSE transport fields
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Scope indicates where this config was loaded from
	Scope Scope `json:"-"`
}

// GetType returns the transport type, defaulting to stdio if not set
func (c *ServerConfig) GetType() TransportType {
	if c.Type == "" {
		if c.URL != "" {
			return TransportHTTP
		}
		return TransportSTDIO
	}
	return c.Type
}

// MCPConfig represents the mcp.json configuration file format
type MCPConfig struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// MCPTool represents a tool exposed by an MCP server
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// MCPResource represents a resource exposed by an MCP server
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// MCPPrompt represents a prompt template exposed by an MCP server
type MCPPrompt struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Arguments   []MCPPromptArgument `json:"arguments,omitempty"`
}

// MCPPromptArgument represents an argument for an MCP prompt
type MCPPromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ToolResult represents the result of calling an MCP tool
type ToolResult struct {
	Content []ToolResultContent `json:"content,omitempty"`
	IsError bool                `json:"isError,omitempty"`
}

// ToolResultContent represents a content item in a tool result
type ToolResultContent struct {
	Type string `json:"type"` // "text", "image", "resource"
	Text string `json:"text,omitempty"`
	// Additional fields for other content types
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// PromptResult represents the result of getting an MCP prompt
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages,omitempty"`
}

// PromptMessage represents a message in a prompt result
type PromptMessage struct {
	Role    string               `json:"role"`
	Content PromptMessageContent `json:"content"`
}

// PromptMessageContent represents the content of a prompt message
type PromptMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ResourceContent represents the content of a resource
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64 encoded binary
}

// ServerCapabilities represents the capabilities of an MCP server
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

// ToolsCapability represents tool-related capabilities
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"` // Server supports tools/list_changed notification
}

// ResourcesCapability represents resource-related capabilities
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability represents prompt-related capabilities
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo represents information about an MCP server
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult represents the result of an initialize request
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerStatus represents the current status of an MCP server connection
type ServerStatus string

const (
	StatusDisconnected ServerStatus = "disconnected"
	StatusConnecting   ServerStatus = "connecting"
	StatusConnected    ServerStatus = "connected"
	StatusError        ServerStatus = "error"
)

// Server represents a connected MCP server with its current state
type Server struct {
	Config       ServerConfig       `json:"config"`
	Status       ServerStatus       `json:"status"`
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
	Error        string             `json:"error,omitempty"`
	Tools        []MCPTool          `json:"tools,omitempty"`
	Resources    []MCPResource      `json:"resources,omitempty"`
	Prompts      []MCPPrompt        `json:"prompts,omitempty"`
}

// MCP-specific methods
const (
	MethodInitialize       = "initialize"
	MethodInitialized      = "notifications/initialized"
	MethodToolsList        = "tools/list"
	MethodToolsCall        = "tools/call"
	MethodResourcesList    = "resources/list"
	MethodResourcesRead    = "resources/read"
	MethodPromptsList      = "prompts/list"
	MethodPromptsGet       = "prompts/get"
	MethodPing             = "ping"
	MethodToolsListChanged = "notifications/tools/list_changed"
)

// InitializeParams represents parameters for the initialize request
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// ClientCapabilities represents the capabilities of the MCP client
type ClientCapabilities struct {
	// Currently empty, but can be extended
}

// ClientInfo represents information about the MCP client
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsListResult represents the result of tools/list
type ToolsListResult struct {
	Tools []MCPTool `json:"tools"`
}

// ToolsCallParams represents parameters for tools/call
type ToolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolsCallResult is an alias for ToolResult (same structure)
type ToolsCallResult = ToolResult

// ResourcesListResult represents the result of resources/list
type ResourcesListResult struct {
	Resources []MCPResource `json:"resources"`
}

// ResourcesReadParams represents parameters for resources/read
type ResourcesReadParams struct {
	URI string `json:"uri"`
}

// ResourcesReadResult represents the result of resources/read
type ResourcesReadResult struct {
	Contents []ResourceContent `json:"contents"`
}

// PromptsListResult represents the result of prompts/list
type PromptsListResult struct {
	Prompts []MCPPrompt `json:"prompts"`
}

// PromptsGetParams represents parameters for prompts/get
type PromptsGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// PromptsGetResult is an alias for PromptResult (same structure)
type PromptsGetResult = PromptResult
