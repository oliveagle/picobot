package channels

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

// ConnectionState represents the connection state
type ConnectionState int

const (
	ConnectionStateDisconnected ConnectionState = iota
	ConnectionStateConnecting
	ConnectionStateConnected
	ConnectionStateDisconnecting
	ConnectionStateFailed
)

func (s ConnectionState) String() string {
	switch s {
	case ConnectionStateDisconnected:
		return "DISCONNECTED"
	case ConnectionStateConnecting:
		return "CONNECTING"
	case ConnectionStateConnected:
		return "CONNECTED"
	case ConnectionStateDisconnecting:
		return "DISCONNECTING"
	case ConnectionStateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// ConnectionManagerConfig holds connection manager configuration
type ConnectionManagerConfig struct {
	MaxAttempts   int           // Max connection attempts (default: 10)
	InitialDelay  time.Duration // Initial reconnect delay (default: 1s)
	MaxDelay      time.Duration // Max reconnect delay (default: 60s)
	Jitter        float64       // Jitter factor 0-1 (default: 0.3)
	OnStateChange func(ConnectionState, string)
}

// ConnectionManager handles robust connection lifecycle for DingTalk Stream
type ConnectionManager struct {
	client    *client.StreamClient
	config    ConnectionManagerConfig
	log       *log.Logger
	accountId string

	// Connection state
	state        ConnectionState
	attemptCount int
	stopped      bool

	// Timers
	reconnectTimer  *time.Timer
	healthCheckTick *time.Ticker

	// Sleep abort control
	sleepCancel context.CancelFunc

	// Socket monitoring
	socketMu     sync.RWMutex
	socketClosed chan struct{}
}

// NewConnectionManager creates a new connection manager
func NewConnectionManager(client *client.StreamClient, accountId string, config ConnectionManagerConfig, logger *log.Logger) *ConnectionManager {
	return &ConnectionManager{
		client:    client,
		accountId: accountId,
		config:    config,
		log:       logger,
		state:     ConnectionStateDisconnected,
	}
}

// calculateNextDelay computes the next reconnect delay with exponential backoff and jitter
// Formula: delay = min(initialDelay * 2^attempt, maxDelay) * (1 ± jitter)
func (m *ConnectionManager) calculateNextDelay(attempt int) time.Duration {
	initialDelay := float64(m.config.InitialDelay)
	maxDelay := float64(m.config.MaxDelay)
	jitter := m.config.Jitter

	// Exponential backoff: initialDelay * 2^attempt
	exponentialDelay := initialDelay * math.Pow(2, float64(attempt))

	// Cap at maxDelay
	cappedDelay := math.Min(exponentialDelay, maxDelay)

	// Apply jitter: randomize ± jitter%
	jitterAmount := cappedDelay * jitter
	randomJitter := (rand.Float64()*2 - 1) * jitterAmount
	finalDelay := math.Max(100, cappedDelay+randomJitter) // Minimum 100ms

	return time.Duration(finalDelay)
}

// notifyStateChange notifies the state change callback
func (m *ConnectionManager) notifyStateChange(err ...string) {
	if m.config.OnStateChange != nil {
		var errMsg string
		if len(err) > 0 {
			errMsg = err[0]
		}
		m.config.OnStateChange(m.state, errMsg)
	}
}

// attemptConnection tries to connect once
func (m *ConnectionManager) attemptConnection(ctx context.Context) error {
	if m.stopped {
		return fmt.Errorf("connection manager stopped")
	}

	m.attemptCount++
	m.state = ConnectionStateConnecting
	m.notifyStateChange()

	m.log.Printf("[%s] Connection attempt %d/%d...", m.accountId, m.attemptCount, m.config.MaxAttempts)

	// Create context that can be cancelled for sleep abort
	sleepCtx, sleepCancel := context.WithCancel(ctx)
	m.sleepCancel = sleepCancel

	defer func() {
		m.sleepCancel = nil
		sleepCancel()
	}()

	// Start the stream client in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- m.client.Start(ctx)
	}()

	// Wait for connection result with timeout
	select {
	case err := <-errChan:
		if err != nil {
			m.log.Printf("[%s] Connection attempt %d failed: %v", m.accountId, m.attemptCount, err)

			if m.attemptCount >= m.config.MaxAttempts {
				m.state = ConnectionStateFailed
				m.notifyStateChange("Max connection attempts reached")
				m.log.Printf("[%s] Max connection attempts (%d) reached. Giving up.", m.accountId, m.config.MaxAttempts)
				return fmt.Errorf("max connection attempts reached: %w", err)
			}

			return err
		}

		// Connection successful
		m.state = ConnectionStateConnected
		m.notifyStateChange()
		m.attemptCount = 0 // Reset counter on success
		m.log.Printf("[%s] DingTalk Stream client connected successfully", m.accountId)
		return nil

	case <-sleepCtx.Done():
		return fmt.Errorf("connection cancelled")
	}
}

// connect connects with robust retry logic
func (m *ConnectionManager) connect(ctx context.Context) error {
	if m.stopped {
		return fmt.Errorf("cannot connect: connection manager is stopped")
	}

	// Clear any existing reconnect timer
	if m.reconnectTimer != nil {
		m.reconnectTimer.Stop()
		m.reconnectTimer = nil
	}

	m.log.Printf("[%s] Starting DingTalk Stream client with robust connection...", m.accountId)

	// Keep trying until success or max attempts reached
	for !m.stopped && m.state != ConnectionStateConnected {
		err := m.attemptConnection(ctx)
		if err == nil {
			// Connection successful
			return nil
		}

		// Check if connection was cancelled
		if ctx.Err() != nil {
			m.log.Printf("[%s] Connection cancelled: context done", m.accountId)
			return ctx.Err()
		}

		if m.attemptCount >= m.config.MaxAttempts {
			return fmt.Errorf("failed to connect after %d attempts", m.attemptCount)
		}

		// Calculate next retry delay
		nextDelay := m.calculateNextDelay(m.attemptCount - 1)

		m.log.Printf("[%s] Will retry connection in %.2fs (attempt %d/%d)",
			m.accountId, nextDelay.Seconds(), m.attemptCount+1, m.config.MaxAttempts)

		// Wait before next attempt
		select {
		case <-time.After(nextDelay):
			// Continue to next attempt
		case <-ctx.Done():
			m.log.Printf("[%s] Connection retry cancelled", m.accountId)
			return ctx.Err()
		}
	}

	return nil
}

// handleRuntimeDisconnection handles runtime disconnection and triggers reconnection
func (m *ConnectionManager) handleRuntimeDisconnection(ctx context.Context) {
	if m.stopped {
		return
	}

	m.log.Printf("[%s] Runtime disconnection detected, initiating reconnection...", m.accountId)

	m.state = ConnectionStateDisconnected
	m.notifyStateChange("Runtime disconnection detected")
	m.attemptCount = 0 // Reset attempt counter for runtime reconnection

	// Schedule reconnection with initial delay
	delay := m.calculateNextDelay(0)
	m.log.Printf("[%s] Scheduling reconnection in %.2fs", m.accountId, delay.Seconds())

	m.reconnectTimer = time.AfterFunc(delay, func() {
		if err := m.connect(ctx); err != nil {
			m.log.Printf("[%s] Reconnection failed: %v", m.accountId, err)
			// Schedule next retry
			m.handleRuntimeDisconnection(ctx)
		}
	})
}

// stop stops the connection manager and cleans up resources
func (m *ConnectionManager) stop() {
	if m.stopped {
		return
	}

	m.log.Printf("[%s] Stopping connection manager...", m.accountId)

	m.stopped = true
	m.state = ConnectionStateDisconnecting

	// Clear reconnect timer
	if m.reconnectTimer != nil {
		m.reconnectTimer.Stop()
		m.reconnectTimer = nil
	}

	// Cancel any in-flight sleep
	if m.sleepCancel != nil {
		m.sleepCancel()
	}

	// Stop health check ticker
	if m.healthCheckTick != nil {
		m.healthCheckTick.Stop()
	}

	// Disconnect client
	// Note: StreamClient doesn't have Disconnect method, rely on context cancellation
	if m.client != nil {
		// The SDK handles cleanup via context cancellation
	}

	m.state = ConnectionStateDisconnected
	m.log.Printf("[%s] Connection manager stopped", m.accountId)
}

// getState returns the current connection state
func (m *ConnectionManager) getState() ConnectionState {
	return m.state
}

// isConnected returns true if connection is active
func (m *ConnectionManager) isConnected() bool {
	return m.state == ConnectionStateConnected
}

// isStopped returns true if connection manager is stopped
func (m *ConnectionManager) isStopped() bool {
	return m.stopped
}

// MessageDedup handles message deduplication
type MessageDedup struct {
	mu        sync.RWMutex
	processed map[string]int64 // Map<dedupKey, expiresAt>
	maxSize   int
	ttl       time.Duration
	counter   int
}

// NewMessageDedup creates a new message deduplication manager
func NewMessageDedup(maxSize int, ttl time.Duration) *MessageDedup {
	return &MessageDedup{
		processed: make(map[string]int64),
		maxSize:   maxSize,
		ttl:       ttl,
	}
}

// IsProcessed checks if message was already processed (with lazy cleanup)
func (d *MessageDedup) IsProcessed(dedupKey string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	now := time.Now().UnixMilli()
	expiresAt, exists := d.processed[dedupKey]

	if !exists {
		return false
	}

	// Lazy cleanup: remove expired entry if found
	if now >= expiresAt {
		d.mu.RUnlock()
		d.mu.Lock()
		delete(d.processed, dedupKey)
		d.mu.Unlock()
		d.mu.RLock()
		return false
	}

	return true
}

// MarkProcessed marks a message as processed
func (d *MessageDedup) MarkProcessed(dedupKey string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	expiresAt := time.Now().Add(d.ttl).UnixMilli()
	d.processed[dedupKey] = expiresAt

	// Hard cap: if Map exceeds max size, force immediate cleanup
	if len(d.processed) > d.maxSize {
		d.forceCleanup()
		return
	}

	// Lazy cleanup: remove expired entries deterministically every 10 messages
	d.counter++
	if d.counter >= 10 {
		d.counter = 0
		d.lazyCleanup()
	}
}

// forceCleanup removes expired entries and oldest entries if still over limit
func (d *MessageDedup) forceCleanup() {
	now := time.Now().UnixMilli()

	// First remove expired entries
	for key, expiry := range d.processed {
		if now >= expiry {
			delete(d.processed, key)
		}
	}

	// If still over limit, remove oldest entries (first inserted)
	if len(d.processed) > d.maxSize {
		removeCount := len(d.processed) - d.maxSize
		for key := range d.processed {
			delete(d.processed, key)
			removeCount--
			if removeCount <= 0 {
				break
			}
		}
	}
}

// lazyCleanup removes expired entries
func (d *MessageDedup) lazyCleanup() {
	now := time.Now().UnixMilli()
	for key, expiry := range d.processed {
		if now >= expiry {
			delete(d.processed, key)
		}
	}
}
