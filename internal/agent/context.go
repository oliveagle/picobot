package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/local/picobot/internal/agent/memory"
	"github.com/local/picobot/internal/agent/skills"
	"github.com/local/picobot/internal/providers"
)

// BootstrapCache holds cached bootstrap file contents with expiry.
type BootstrapCache struct {
	mu        sync.RWMutex
	files     map[string]string
	expiresAt time.Time
}

// SkillsCache holds cached skills with expiry.
type SkillsCache struct {
	skills    []skills.Skill
	expiresAt time.Time
}

// ContextBuilder builds messages for the LLM from session history and current message.
type ContextBuilder struct {
	workspace    string
	ranker       memory.Ranker
	topK         int
	skillsLoader *skills.Loader
	bootCache    *BootstrapCache
	cacheMu      sync.RWMutex // protects bootCache and skillsCache
	skillsCache  *SkillsCache
}

func NewContextBuilder(workspace string, r memory.Ranker, topK int) *ContextBuilder {
	return &ContextBuilder{
		workspace:    workspace,
		ranker:       r,
		topK:         topK,
		skillsLoader: skills.NewLoader(workspace),
		bootCache:    &BootstrapCache{files: make(map[string]string)},
		skillsCache:  &SkillsCache{},
	}
}

// getBootstrapFile reads a bootstrap file with caching (5 minute expiry).
func (cb *ContextBuilder) getBootstrapFile(name string) string {
	// Check cache first
	cb.cacheMu.RLock()
	if content, ok := cb.bootCache.files[name]; ok && time.Now().Before(cb.bootCache.expiresAt) {
		cb.cacheMu.RUnlock()
		return content
	}
	cb.cacheMu.RUnlock()

	// Cache miss or expired, read file
	cb.cacheMu.Lock()
	defer cb.cacheMu.Unlock()

	p := filepath.Join(cb.workspace, name)
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content != "" {
		cb.bootCache.files[name] = content
		// Set expiry to 5 minutes from now
		cb.bootCache.expiresAt = time.Now().Add(5 * time.Minute)
	}
	return content
}

// invalidateBootstrapCache invalidates the bootstrap file cache.
func (cb *ContextBuilder) invalidateBootstrapCache() {
	cb.cacheMu.Lock()
	defer cb.cacheMu.Unlock()
	cb.bootCache.files = make(map[string]string)
	cb.bootCache.expiresAt = time.Time{}
}

// getSkills loads skills with caching (5 minute expiry).
func (cb *ContextBuilder) getSkills() []skills.Skill {
	cb.cacheMu.RLock()
	if time.Now().Before(cb.skillsCache.expiresAt) {
		skills := cb.skillsCache.skills
		cb.cacheMu.RUnlock()
		return skills
	}
	cb.cacheMu.RUnlock()

	// Cache miss or expired, load skills
	cb.cacheMu.Lock()
	defer cb.cacheMu.Unlock()

	loadedSkills, err := cb.skillsLoader.LoadAll()
	if err != nil {
		log.Printf("error loading skills: %v", err)
		return nil
	}
	cb.skillsCache.skills = loadedSkills
	cb.skillsCache.expiresAt = time.Now().Add(5 * time.Minute)
	return loadedSkills
}

// invalidateSkillsCache invalidates the skills cache.
func (cb *ContextBuilder) invalidateSkillsCache() {
	cb.cacheMu.Lock()
	defer cb.cacheMu.Unlock()
	cb.skillsCache.skills = nil
	cb.skillsCache.expiresAt = time.Time{}
}

func (cb *ContextBuilder) BuildMessages(history []string, currentMessage string, channel, chatID string, memoryContext string, memories []memory.MemoryItem) []providers.Message {
	msgs := make([]providers.Message, 0, len(history)+8)
	// system prompt
	msgs = append(msgs, providers.Message{Role: "system", Content: "You are Picobot, a helpful assistant."})

	// Load workspace bootstrap files with caching (SOUL.md, AGENTS.md, USER.md, TOOLS.md)
	bootstrapFiles := []string{"SOUL.md", "AGENTS.md", "USER.md", "TOOLS.md"}
	for _, name := range bootstrapFiles {
		content := cb.getBootstrapFile(name)
		if content != "" {
			msgs = append(msgs, providers.Message{Role: "system", Content: fmt.Sprintf("## %s\n\n%s", name, content)})
		}
	}

	// Channel info
	msgs = append(msgs, providers.Message{Role: "system", Content: fmt.Sprintf(
		"You are operating on channel=%q chatID=%q. You have full access to all registered tools.",
		channel, chatID)})

	// Load and include skills context with caching
	loadedSkills := cb.getSkills()
	if len(loadedSkills) > 0 {
		var sb strings.Builder
		sb.WriteString("Available Skills:\n")
		for _, skill := range loadedSkills {
			sb.WriteString(fmt.Sprintf("\n## %s\n%s\n\n%s\n", skill.Name, skill.Description, skill.Content))
		}
		msgs = append(msgs, providers.Message{Role: "system", Content: sb.String()})
	}

	// Include file-based memory context
	if memoryContext != "" {
		msgs = append(msgs, providers.Message{Role: "system", Content: "Memory:\n" + memoryContext})
	}

	// Select top-K memories
	selected := memories
	if cb.ranker != nil && len(memories) > 0 {
		selected = cb.ranker.Rank(currentMessage, memories, cb.topK)
	}
	if len(selected) > 0 {
		var sb strings.Builder
		sb.WriteString("Relevant memories:\n")
		for _, m := range selected {
			sb.WriteString(fmt.Sprintf("- %s (%s)\n", m.Text, m.Kind))
		}
		msgs = append(msgs, providers.Message{Role: "system", Content: sb.String()})
	}

	// Replay history (limit to last 20 messages)
	maxHistory := 20
	startIdx := 0
	if len(history) > maxHistory {
		startIdx = len(history) - maxHistory
	}
	for i := startIdx; i < len(history); i++ {
		h := history[i]
		if len(h) > 0 {
			msgs = append(msgs, providers.Message{Role: "user", Content: h})
		}
	}

	// Current message
	msgs = append(msgs, providers.Message{Role: "user", Content: currentMessage})
	return msgs
}
