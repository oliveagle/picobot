// Package acp implements the Agent Client Protocol (ACP) client.
// https://agentclientprotocol.com
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// =============================================================================
// ACP Protocol Messages
// =============================================================================

// JSONRPCMessage represents a JSON-RPC 2.0 message
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCError represents a JSON-RPC error
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// InitializeRequest represents the initialize request
type InitializeRequest struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// ClientInfo describes the client
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities describes client capabilities
type ClientCapabilities struct {
	TerminalAuth *bool `json:"terminal-auth,omitempty"`
	Roots        *bool `json:"roots,omitempty"`
	Sampling     *bool `json:"sampling,omitempty"`
}

// InitializeResponse represents the initialize response
type InitializeResponse struct {
	ProtocolVersion int               `json:"protocolVersion"`
	Capabilities    AgentCapabilities `json:"capabilities"`
	AgentInfo       AgentInfo         `json:"agentInfo"`
	AuthMethods     []AuthMethod      `json:"authMethods,omitempty"`
}

// AgentCapabilities describes agent capabilities
type AgentCapabilities struct {
	PromptCapabilities   *PromptCapabilities   `json:"promptCapabilities,omitempty"`
	MCPCapabilities      *MCPCapabilities      `json:"mcpCapabilities,omitempty"`
	LoadSession          *bool                 `json:"loadSession,omitempty"`
	SessionCapabilities  *SessionCapabilities  `json:"sessionCapabilities,omitempty"`
	TerminalCapabilities *TerminalCapabilities `json:"terminalCapabilities,omitempty"`
}

// PromptCapabilities describes prompt capabilities
type PromptCapabilities struct {
	Image           bool `json:"image"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// MCPCapabilities describes MCP capabilities
type MCPCapabilities struct {
	HTTP *bool `json:"http,omitempty"`
	SSE  *bool `json:"sse,omitempty"`
}

// SessionCapabilities describes session capabilities
type SessionCapabilities struct {
	Fork   *struct{} `json:"fork,omitempty"`
	List   *struct{} `json:"list,omitempty"`
	Resume *struct{} `json:"resume,omitempty"`
}

// TerminalCapabilities describes terminal capabilities
type TerminalCapabilities struct {
	Create *struct{} `json:"create,omitempty"`
}

// AgentInfo describes the agent
type AgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// AuthMethod describes an authentication method
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// NewSessionRequest represents a newSession request
type NewSessionRequest struct {
	CWD        string                 `json:"cwd"`
	MCPServers []MCPServerConfig      `json:"mcpServers,omitempty"`
	Meta       map[string]interface{} `json:"_meta,omitempty"`
}

// MCPServerConfig describes an MCP server
type MCPServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
}

// NewSessionResponse represents the newSession response
type NewSessionResponse struct {
	SessionID     string                `json:"sessionId"`
	Modes         []SessionMode         `json:"modes"`
	Models        []ModelInfo           `json:"models"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

// SessionMode describes a session mode
type SessionMode struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ModelInfo describes a model
type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MaxTokens   int    `json:"maxTokens"`
	ContextSize int    `json:"contextSize"`
}

// SessionConfigOption describes a session config option
type SessionConfigOption struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PromptRequest represents a prompt request
type PromptRequest struct {
	SessionID string       `json:"sessionId"`
	Prompt    []PromptPart `json:"prompt"`
}

// PromptPart represents a part of the prompt
type PromptPart struct {
	Type    string       `json:"type"`
	Content string       `json:"content,omitempty"`
	Image   *ImageData   `json:"image,omitempty"`
	Context *ContextData `json:"context,omitempty"`
}

// ImageData represents image data
type ImageData struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// ContextData represents embedded context
type ContextData struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// PromptResponse represents the prompt response
type PromptResponse struct {
	StopReason string `json:"stopReason"`
}

// SessionUpdate represents a session update notification
type SessionUpdate struct {
	SessionID    string                  `json:"sessionId"`
	Notification SessionNotificationData `json:"notification"`
}

// SessionNotificationData represents notification data
type SessionNotificationData struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

// CancelRequest represents a cancel request
type CancelRequest struct {
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason,omitempty"`
}

// =============================================================================
// ACP Client
// =============================================================================

// Client implements an ACP client that communicates with an ACP agent via stdio
type Client struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	mutex    sync.Mutex
	nextID   int64
	sessions map[string]*Session
	onUpdate func(*SessionUpdate)
	done     chan struct{}
}

// Session represents an ACP session
type Session struct {
	ID     string
	Client *Client
	Modes  []SessionMode
	Models []ModelInfo
}

// ClientOption configures a Client
type ClientOption func(*Client)

// WithUpdateHandler sets the update handler for session updates
func WithUpdateHandler(handler func(*SessionUpdate)) ClientOption {
	return func(c *Client) {
		c.onUpdate = handler
	}
}

// NewClient creates a new ACP client that runs the given command
func NewClient(command string, args []string, opts ...ClientOption) (*Client, error) {
	cmd := exec.Command(command, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// Forward stderr to our stderr for logging
	cmd.Stderr = nil // Will be handled by the caller if needed

	c := &Client{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReader(stdout),
		sessions: make(map[string]*Session),
		done:     make(chan struct{}),
		nextID:   1,
	}

	for _, opt := range opts {
		opt(c)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Start reading responses
	go c.readLoop()

	return c, nil
}

// Close closes the client
func (c *Client) Close() error {
	close(c.done)
	if c.stdin != nil {
		c.stdin.Close()
	}
	return c.cmd.Wait()
}

// nextID generates the next request ID
func (c *Client) nextIDLocked() int64 {
	id := c.nextID
	c.nextID++
	return id
}

// send sends a JSON-RPC message and returns the response
func (c *Client) send(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mutex.Lock()
	id := c.nextIDLocked()
	c.mutex.Unlock()

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		ID:      &id,
		Params:  paramsBytes,
	}

	// Encode and send
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	if _, err := c.stdin.Write(msgBytes); err != nil {
		return nil, fmt.Errorf("failed to write message: %w", err)
	}

	// Read response synchronously
	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var resp JSONRPCMessage
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return resp.Result, nil
}

// readLoop reads responses and notifications from the agent
func (c *Client) readLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
			line, err := c.stdout.ReadBytes('\n')
			if err != nil {
				if err != io.EOF {
					fmt.Printf("acp: read error: %v\n", err)
				}
				return
			}

			var msg JSONRPCMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				fmt.Printf("acp: failed to unmarshal: %v\n", err)
				continue
			}

			// Handle notification (no ID)
			if msg.ID == nil {
				if c.onUpdate != nil {
					// Parse as session update
					c.handleNotification(msg)
				}
				continue
			}

			// In a real implementation, we'd match the ID to pending requests
		}
	}
}

// handleNotification handles a session notification
func (c *Client) handleNotification(msg JSONRPCMessage) {
	// Parse notification based on method
	// This is simplified - real implementation would handle various notification types
	if c.onUpdate != nil {
		c.onUpdate(&SessionUpdate{})
	}
}

// Initialize sends the initialize request
func (c *Client) Initialize(ctx context.Context) (*InitializeResponse, error) {
	req := InitializeRequest{
		ProtocolVersion: 1,
		Capabilities: ClientCapabilities{
			TerminalAuth: boolPtr(true),
		},
		ClientInfo: ClientInfo{
			Name:    "picobot-acp-client",
			Version: "0.1.0",
		},
	}

	result, err := c.send(ctx, "initialize", req)
	if err != nil {
		return nil, err
	}

	var resp InitializeResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal initialize response: %w", err)
	}

	return &resp, nil
}

// NewSession creates a new session
func (c *Client) NewSession(ctx context.Context, cwd string) (*Session, error) {
	req := NewSessionRequest{
		CWD: cwd,
	}

	result, err := c.send(ctx, "newSession", req)
	if err != nil {
		return nil, err
	}

	var resp NewSessionResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal newSession response: %w", err)
	}

	session := &Session{
		ID:     resp.SessionID,
		Client: c,
		Modes:  resp.Modes,
		Models: resp.Models,
	}

	c.mutex.Lock()
	c.sessions[session.ID] = session
	c.mutex.Unlock()

	return session, nil
}

// Prompt sends a prompt to the session (simplified - returns static response)
func (s *Session) Prompt(ctx context.Context, prompt string) (<-chan string, error) {
	resultCh := make(chan string, 1)

	go func() {
		defer close(resultCh)
		resultCh <- "Prompt sent (streaming not implemented)"
	}()

	return resultCh, nil
}

// Cancel cancels a session
func (s *Session) Cancel(ctx context.Context, reason string) error {
	req := CancelRequest{
		SessionID: s.ID,
		Reason:    reason,
	}

	_, err := s.Client.send(ctx, "cancel", req)
	return err
}

// =============================================================================
// Helpers
// =============================================================================

func boolPtr(b bool) *bool {
	return &b
}
