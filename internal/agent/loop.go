package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/local/picobot/internal/agent/memory"
	"github.com/local/picobot/internal/agent/tools"
	"github.com/local/picobot/internal/chat"
	"github.com/local/picobot/internal/config"
	"github.com/local/picobot/internal/cron"
	"github.com/local/picobot/internal/providers"
	"github.com/local/picobot/internal/session"
)

// ToolExecutionResult holds the result of a tool execution with error info.
type ToolExecutionResult struct {
	Content string
	Err     error
}

var rememberRE = regexp.MustCompile(`(?i)^remember(?:\s+to)?\s+(.+)$`)

// shortHash generates a short hash from the message content for identification
func shortHash(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	hash := hex.EncodeToString(h.Sum(nil))
	return hash[:8] // return first 8 characters
}

// AgentLoop is the core processing loop; it holds an LLM provider, tools, sessions and context builder.
type AgentLoop struct {
	hub           *chat.Hub
	provider      providers.LLMProvider
	tools         *tools.Registry
	sessions      *session.SessionManager
	context       *ContextBuilder
	memory        *memory.MemoryStore
	model         string
	maxIterations int
	running       bool
	dingTalkTool  *tools.DingTalkTool

	// Concurrency control
	maxConcurrency int
	semaphore      chan struct{}
	wg             sync.WaitGroup
}

// AgentLoopConfig holds configuration for AgentLoop.
type AgentLoopConfig struct {
	MaxConcurrency int // Maximum concurrent message processing (default: 10)
}

// DefaultAgentLoopConfig returns a config with sensible defaults.
func DefaultAgentLoopConfig() AgentLoopConfig {
	return AgentLoopConfig{
		MaxConcurrency: 10,
	}
}

// NewAgentLoop creates a new AgentLoop with the given provider.
func NewAgentLoop(b *chat.Hub, provider providers.LLMProvider, model string, maxIterations int, workspace string, scheduler *cron.Scheduler, cfg *config.Config) *AgentLoop {
	return NewAgentLoopWithConfig(b, provider, model, maxIterations, workspace, scheduler, cfg, DefaultAgentLoopConfig())
}

// NewAgentLoopWithConfig creates a new AgentLoop with custom configuration.
func NewAgentLoopWithConfig(b *chat.Hub, provider providers.LLMProvider, model string, maxIterations int, workspace string, scheduler *cron.Scheduler, cfg *config.Config, config AgentLoopConfig) *AgentLoop {
	if model == "" {
		model = provider.GetDefaultModel()
	}
	if workspace == "" {
		workspace = "."
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = 10
	}
	reg := tools.NewRegistry()
	// register default tools
	reg.Register(tools.NewMessageTool(b))

	// Open an os.Root anchored at the workspace for kernel-enforced sandboxing.
	root, err := os.OpenRoot(workspace)
	if err != nil {
		log.Fatalf("failed to open workspace root %q: %v", workspace, err)
	}

	fsTool, err := tools.NewFilesystemTool(workspace)
	if err != nil {
		log.Fatalf("failed to create filesystem tool: %v", err)
	}
	reg.Register(fsTool)

	reg.Register(tools.NewExecTool(60))
	reg.Register(tools.NewWebTool())
	reg.Register(tools.NewSpawnTool())
	if scheduler != nil {
		reg.Register(tools.NewCronTool(scheduler))
	}

	sm := session.NewSessionManager(workspace)
	ctx := NewContextBuilder(workspace, memory.NewLLMRanker(provider, model), 5)
	mem := memory.NewMemoryStoreWithWorkspace(workspace, 100)
	// register memory tool (needs store instance)
	reg.Register(tools.NewWriteMemoryTool(mem))

	// register skill management tools (share the same os.Root)
	skillMgr := tools.NewSkillManager(root)
	reg.Register(tools.NewCreateSkillTool(skillMgr))
	reg.Register(tools.NewListSkillsTool(skillMgr))
	reg.Register(tools.NewReadSkillTool(skillMgr))
	reg.Register(tools.NewDeleteSkillTool(skillMgr))

	// register DingTalk tool if configured
	var dingTalkTool *tools.DingTalkTool
	if cfg != nil && cfg.Channels.DingTalk.Enabled {
		dt := tools.NewDingTalkTool(
			cfg.Channels.DingTalk.ClientID,
			cfg.Channels.DingTalk.ClientSecret,
			cfg.Channels.DingTalk.UnionID,
			cfg.Channels.DingTalk.UserID,
		)
		if dt != nil {
			reg.Register(dt)
			dingTalkTool = dt
			log.Println("DingTalk tool registered")
		}
	}

	return &AgentLoop{
		hub:            b,
		provider:       provider,
		tools:          reg,
		sessions:       sm,
		context:        ctx,
		memory:         mem,
		model:          model,
		maxIterations:  maxIterations,
		dingTalkTool:   dingTalkTool,
		maxConcurrency: config.MaxConcurrency,
		semaphore:      make(chan struct{}, config.MaxConcurrency),
	}
}

// Run starts processing inbound messages. This is a blocking call until context is canceled.
func (a *AgentLoop) Run(ctx context.Context) {
	a.running = true
	log.Println("Agent loop started")

	// Ensure all goroutines complete on exit
	defer func() {
		log.Println("Waiting for pending message processing to complete...")
		a.wg.Wait()
		log.Println("Agent loop stopped")
	}()

	for a.running {
		select {
		case <-ctx.Done():
			log.Println("Agent loop received shutdown signal")
			a.running = false
			return
		case msg, ok := <-a.hub.In:
			if !ok {
				log.Println("Inbound channel closed, stopping agent loop")
				a.running = false
				return
			}

			log.Printf("Processing message from %s:%s\n", msg.Channel, msg.SenderID)

			// Send immediate acknowledgment (except for heartbeat/cron)
			if msg.Channel != "heartbeat" && msg.SenderID != "heartbeat" && msg.SenderID != "cron" {
				msgHash := shortHash(msg.Content + msg.Timestamp.Format(time.RFC3339Nano))
				ackContent := fmt.Sprintf("✅ [ID:%s] Message received, thinking...", msgHash)
				ack := chat.Outbound{Channel: msg.Channel, ChatID: msg.ChatID, Content: ackContent}
				select {
				case a.hub.Out <- ack:
					log.Printf("Sent ack to %s:%s with hash %s", msg.Channel, msg.ChatID, msgHash)
				default:
					log.Println("Outbound channel full, dropping ack")
				}
			}

			// Acquire semaphore (blocks if max concurrency reached)
			a.semaphore <- struct{}{}

			// Process message in a goroutine to avoid blocking other messages
			a.wg.Add(1)
			go func() {
				defer func() { <-a.semaphore }() // Release semaphore
				defer a.wg.Done()
				a.processMessage(ctx, msg)
			}()
		}
	}
}

// WaitForCompletion waits for all in-flight message processing to complete.
// Call this after Run() returns to ensure graceful shutdown.
func (a *AgentLoop) WaitForCompletion() {
	a.wg.Wait()
}

// processMessage handles a single message (called in a goroutine)
func (a *AgentLoop) processMessage(ctx context.Context, msg chat.Inbound) {
	// Quick heuristic: if user asks the agent to remember something explicitly,
	// store it in today's note and reply immediately without calling the LLM.
	trimmed := strings.TrimSpace(msg.Content)
	if matches := rememberRE.FindStringSubmatch(trimmed); len(matches) == 2 {
		a.handleRememberCommand(msg, matches[1])
		return
	}

	// Skip LLM for heartbeat/cron messages
	if msg.Channel == "heartbeat" || msg.SenderID == "heartbeat" || msg.SenderID == "cron" {
		log.Printf("[%s] message received, skipping LLM processing", msg.Channel)
		return
	}

	// Set tool context (so message tool knows channel+chat)
	a.setToolContext(msg.Channel, msg.ChatID)

	// Build messages from session, long-term memory, and recent memory
	session := a.sessions.GetOrCreate(msg.Channel + ":" + msg.ChatID)
	memCtx, _ := a.memory.GetMemoryContext()
	memories := a.memory.Recent(5)
	messages := a.context.BuildMessages(session.GetHistory(), msg.Content, msg.Channel, msg.ChatID, memCtx, memories)

	// Execute tool calling loop
	finalContent, _ := a.executeToolLoop(ctx, messages)

	// Fallback responses
	if finalContent == "" {
		finalContent = "I've completed processing but have no response to give."
	}

	// Save session
	session.AddMessage("user", msg.Content)
	session.AddMessage("assistant", finalContent)
	a.sessions.Save(session)

	// Send response
	a.sendResponse(msg.Channel, msg.ChatID, finalContent)
}

// handleRememberCommand handles the "remember to X" command
func (a *AgentLoop) handleRememberCommand(msg chat.Inbound, note string) {
	if err := a.memory.AppendToday(note); err != nil {
		log.Printf("error appending to memory: %v", err)
	}

	response := "OK, I've remembered that."
	a.sendResponse(msg.Channel, msg.ChatID, response)

	// Save to session as well
	session := a.sessions.GetOrCreate(msg.Channel + ":" + msg.ChatID)
	session.AddMessage("user", msg.Content)
	session.AddMessage("assistant", response)
	a.sessions.Save(session)
}

// setToolContext sets the context for message and cron tools
func (a *AgentLoop) setToolContext(channel, chatID string) {
	if mt := a.tools.Get("message"); mt != nil {
		if mtool, ok := mt.(interface{ SetContext(string, string) }); ok {
			mtool.SetContext(channel, chatID)
		}
	}
	if ct := a.tools.Get("cron"); ct != nil {
		if ctool, ok := ct.(interface{ SetContext(string, string) }); ok {
			ctool.SetContext(channel, chatID)
		}
	}
}

// executeToolLoop runs the tool calling iteration loop
// Returns final content and any error
func (a *AgentLoop) executeToolLoop(ctx context.Context, messages []providers.Message) (string, error) {
	var lastToolResult string
	toolDefs := a.tools.Definitions()

	for iteration := 0; iteration < a.maxIterations; iteration++ {
		resp, err := a.provider.Chat(ctx, messages, toolDefs, a.model)
		if err != nil {
			log.Printf("provider error: %v", err)
			return "Sorry, I encountered an error while processing your request.", err
		}

		if resp.HasToolCalls {
			// Append assistant message with tool_calls
			messages = append(messages, providers.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})

			// Execute each tool call
			for _, tc := range resp.ToolCalls {
				result, err := a.executeTool(ctx, tc)
				lastToolResult = result
				messages = append(messages, providers.Message{Role: "tool", Content: result, ToolCallID: tc.ID})
				if err != nil {
					log.Printf("tool %s execution error: %v", tc.Name, err)
				}
			}
			// Continue to next iteration
			continue
		}

		// No tool calls, return final content
		if resp.Content != "" {
			return resp.Content, nil
		}
		if lastToolResult != "" {
			return lastToolResult, nil
		}
		return resp.Content, nil
	}

	return "", fmt.Errorf("max iterations (%d) reached", a.maxIterations)
}

// executeTool executes a single tool call with error handling
func (a *AgentLoop) executeTool(ctx context.Context, tc providers.ToolCall) (string, error) {
	result, err := a.tools.Execute(ctx, tc.Name, tc.Arguments)
	if err != nil {
		return fmt.Sprintf("tool error: %v", err), err
	}
	return result, nil
}

// sendResponse sends a response to the outbound channel
func (a *AgentLoop) sendResponse(channel, chatID, content string) {
	out := chat.Outbound{Channel: channel, ChatID: chatID, Content: content}
	select {
	case a.hub.Out <- out:
	default:
		log.Println("Outbound channel full, dropping message")
	}
}

// ProcessDirect sends a message directly to the provider and returns the response.
// It supports tool calling - if the model requests tools, they will be executed.
func (a *AgentLoop) ProcessDirect(content string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Set tool context so message/cron tools know the originating channel,
	// matching what Run() does for hub-based messages.
	if mt := a.tools.Get("message"); mt != nil {
		if mtool, ok := mt.(interface{ SetContext(string, string) }); ok {
			mtool.SetContext("cli", "direct")
		}
	}
	if ct := a.tools.Get("cron"); ct != nil {
		if ctool, ok := ct.(interface{ SetContext(string, string) }); ok {
			ctool.SetContext("cli", "direct")
		}
	}

	// Build full context (bootstrap files, skills, memory) just like the main loop
	memCtx, _ := a.memory.GetMemoryContext()
	memories := a.memory.Recent(5)
	messages := a.context.BuildMessages(nil, content, "cli", "direct", memCtx, memories)

	// Support tool calling iterations (similar to main loop)
	var lastToolResult string
	for iteration := 0; iteration < a.maxIterations; iteration++ {
		resp, err := a.provider.Chat(ctx, messages, a.tools.Definitions(), a.model)
		if err != nil {
			return "", err
		}

		if !resp.HasToolCalls {
			// No tool calls, return the response (fall back to last tool result if empty)
			if resp.Content != "" {
				return resp.Content, nil
			}
			if lastToolResult != "" {
				return lastToolResult, nil
			}
			return resp.Content, nil
		}

		// Execute tool calls
		messages = append(messages, providers.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			result, err := a.tools.Execute(ctx, tc.Name, tc.Arguments)
			if err != nil {
				result = "(tool error) " + err.Error()
			}
			lastToolResult = result
			messages = append(messages, providers.Message{Role: "tool", Content: result, ToolCallID: tc.ID})
		}
	}

	return "Max iterations reached without final response", nil
}
