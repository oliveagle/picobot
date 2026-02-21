package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/local/picobot/internal/chat"
)

// =============================================================================
// ACP Tool for Claude Code
// =============================================================================

// ACPTool provides access to Claude Code via ACP protocol
type ACPTool struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	stderr   io.ReadCloser
	mutex    sync.Mutex
	nextID   int64
	sessions map[string]*ACPSession
	hub      *chat.Hub
}

// ACPSession represents an ACP session
type ACPSession struct {
	ID        string
	Tool      *ACPTool
	Modes     []string
	Models    []string
	CreatedAt time.Time
}

// ACPToolConfig holds configuration for the ACP tool
type ACPToolConfig struct {
	Command string   // Command to run (default: "claude")
	Args    []string // Additional arguments
	Env     []string // Environment variables
	WorkDir string   // Working directory
}

// NewACPTool creates a new ACP tool
func NewACPTool(hub *chat.Hub, config ACPToolConfig) (*ACPTool, error) {
	if config.Command == "" {
		config.Command = "claude"
	}

	// Build command args for ACP mode
	args := []string{"-y", "@zed-industries/claude-agent-acp"}
	args = append(args, config.Args...)

	cmd := exec.Command(config.Command, args...)
	cmd.Env = append(os.Environ(), config.Env...)
	if config.WorkDir != "" {
		cmd.Dir = config.WorkDir
	}

	// Get pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	t := &ACPTool{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReader(stdout),
		stderr:   stderr,
		sessions: make(map[string]*ACPSession),
		hub:      hub,
		nextID:   1,
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Start stderr reader (for logging)
	go t.readStderr()

	// Start response reader
	go t.readLoop()

	return t, nil
}

// Close closes the ACP tool
func (t *ACPTool) Close() error {
	if t.stdin != nil {
		t.stdin.Close()
	}
	return t.cmd.Wait()
}

// readStderr reads stderr for logging
func (t *ACPTool) readStderr() {
	buf := make([]byte, 1024)
	for {
		n, err := t.stderr.Read(buf)
		if n > 0 {
			fmt.Fprintf(os.Stderr, "[acp-stderr] %s", buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "[acp-stderr] read error: %v\n", err)
			}
			return
		}
	}
}

// nextID generates the next request ID
func (t *ACPTool) nextIDLocked() int64 {
	id := t.nextID
	t.nextID++
	return id
}

// ACPMessage represents a JSON-RPC 2.0 message
type ACPMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ACPError       `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ACPError represents a JSON-RPC error
type ACPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// send sends a message and returns the response
func (t *ACPTool) send(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	t.mutex.Lock()
	id := t.nextIDLocked()
	t.mutex.Unlock()

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	msg := ACPMessage{
		JSONRPC: "2.0",
		Method:  method,
		ID:      &id,
		Params:  paramsBytes,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	t.mutex.Lock()
	_, err = t.stdin.Write(msgBytes)
	t.mutex.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write message: %w", err)
	}

	// Read response with timeout
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	respCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	go func() {
		line, err := t.readJSONLine()
		if err != nil {
			errCh <- err
			return
		}
		respCh <- line
	}()

	select {
	case <-readCtx.Done():
		return nil, fmt.Errorf("timeout waiting for response")
	case err := <-errCh:
		return nil, err
	case resp := <-respCh:
		var acpResp ACPMessage
		if err := json.Unmarshal(resp, &acpResp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		if acpResp.Error != nil {
			return nil, fmt.Errorf("ACP error %d: %s", acpResp.Error.Code, acpResp.Error.Message)
		}
		return acpResp.Result, nil
	}
}

// readJSONLine reads a line and parses it as JSON
func (t *ACPTool) readJSONLine() (json.RawMessage, error) {
	line, err := t.stdout.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return json.RawMessage(line), nil
}

// readLoop reads notifications from the agent
func (t *ACPTool) readLoop() {
	for {
		line, err := t.stdout.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "[acp] read error: %v\n", err)
			}
			return
		}

		var msg ACPMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			fmt.Fprintf(os.Stderr, "[acp] unmarshal error: %v\n", err)
			continue
		}

		// Handle notification (no ID)
		if msg.ID == nil {
			t.handleNotification(msg)
			continue
		}
	}
}

// handleNotification handles a session notification
func (t *ACPTool) handleNotification(msg ACPMessage) {
	// Parse and forward to hub if needed
	fmt.Fprintf(os.Stderr, "[acp] notification: %s\n", string(msg.Params))
}

// Initialize initializes the ACP connection
func (t *ACPTool) Initialize(ctx context.Context) error {
	params := map[string]interface{}{
		"protocolVersion": 1,
		"capabilities": map[string]interface{}{
			"terminal-auth": true,
		},
		"clientInfo": map[string]string{
			"name":    "picobot",
			"version": "0.1.0",
		},
	}

	_, err := t.send(ctx, "initialize", params)
	return err
}

// NewSession creates a new session
func (t *ACPTool) NewSession(ctx context.Context, workDir string) (*ACPSession, error) {
	params := map[string]interface{}{
		"cwd": workDir,
	}

	result, err := t.send(ctx, "newSession", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		SessionID string `json:"sessionId"`
		Modes     []struct {
			Name string `json:"name"`
		} `json:"modes"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal newSession response: %w", err)
	}

	session := &ACPSession{
		ID:        resp.SessionID,
		Tool:      t,
		CreatedAt: time.Now(),
	}

	for _, m := range resp.Modes {
		session.Modes = append(session.Modes, m.Name)
	}
	for _, m := range resp.Models {
		session.Models = append(session.Models, m.ID)
	}

	t.mutex.Lock()
	t.sessions[session.ID] = session
	t.mutex.Unlock()

	return session, nil
}

// Prompt sends a prompt to the session
func (t *ACPTool) Prompt(ctx context.Context, sessionID, prompt string) (string, error) {
	t.mutex.Lock()
	_, ok := t.sessions[sessionID]
	t.mutex.Unlock()
	if !ok {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	params := map[string]interface{}{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "content": prompt},
		},
	}

	result, err := t.send(ctx, "prompt", params)
	if err != nil {
		return "", err
	}

	var resp struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("unmarshal prompt response: %w", err)
	}

	return fmt.Sprintf("Completed: %s", resp.StopReason), nil
}

// =============================================================================
// Tool Integration
// =============================================================================

// Name returns the tool name
func (t *ACPTool) Name() string { return "claude_code" }

// Description returns the tool description
func (t *ACPTool) Description() string {
	return "Execute coding tasks via Claude Code using ACP protocol"
}

// Parameters returns the tool parameters
func (t *ACPTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"init", "new_session", "prompt", "cancel"},
				"description": "The action to perform",
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Session ID for existing sessions",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The prompt to send to Claude Code",
			},
			"work_dir": map[string]interface{}{
				"type":        "string",
				"description": "Working directory for the session",
			},
		},
		"required": []string{"action"},
	}
}

// Execute executes the tool
func (t *ACPTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	action, _ := args["action"].(string)

	switch action {
	case "init":
		err := t.Initialize(ctx)
		if err != nil {
			return "", fmt.Errorf("init failed: %w", err)
		}
		return "ACP initialized", nil

	case "new_session":
		workDir, _ := args["work_dir"].(string)
		if workDir == "" {
			workDir = "."
		}
		session, err := t.NewSession(ctx, workDir)
		if err != nil {
			return "", fmt.Errorf("new_session failed: %w", err)
		}
		return fmt.Sprintf("Session created: %s (modes: %v, models: %v)",
			session.ID, session.Modes, session.Models), nil

	case "prompt":
		sessionID, _ := args["session_id"].(string)
		prompt, _ := args["prompt"].(string)
		if prompt == "" {
			return "", fmt.Errorf("prompt is required")
		}
		if sessionID == "" {
			// Create new session if none provided
			newSession, err := t.NewSession(ctx, ".")
			if err != nil {
				return "", err
			}
			sessionID = newSession.ID
		}
		result, err := t.Prompt(ctx, sessionID, prompt)
		if err != nil {
			return "", fmt.Errorf("prompt failed: %w", err)
		}
		return result, nil

	case "cancel":
		sessionID, _ := args["session_id"].(string)
		reason, _ := args["reason"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session_id is required")
		}
		params := map[string]interface{}{
			"sessionId": sessionID,
			"reason":    reason,
		}
		_, err := t.send(ctx, "cancel", params)
		if err != nil {
			return "", fmt.Errorf("cancel failed: %w", err)
		}
		return "Session cancelled", nil

	default:
		return "", fmt.Errorf("unknown action: %s", action)
	}
}
