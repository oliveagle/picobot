package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/local/picobot/internal/chat"
)

// DingTalkMessage represents a message from DingTalk
type DingTalkMessage struct {
	MsgType    string `json:"msgtype"`
	Text       struct {
		Content string `json:"content"`
	} `json:"text"`
	SenderStaffID string `json:"senderStaffId"`
	ConversationID string `json:"conversationId"`
	RobotCode     string `json:"robotCode"`
}

// DingTalkResponse represents the response to DingTalk
type DingTalkResponse struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

// StartDingTalk starts a HTTP server to receive DingTalk webhook messages
func StartDingTalk(ctx context.Context, hub *chat.Hub, port int, robotCode string) error {
	if port <= 0 {
		port = 8080
	}

	mux := http.NewServeMux()

	// Webhook endpoint for receiving messages
	mux.HandleFunc("/webhook/dingtalk", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var msg DingTalkMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Filter by robot code if specified
		if robotCode != "" && msg.RobotCode != robotCode {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Skip empty messages
		if msg.Text.Content == "" {
			w.WriteHeader(http.StatusOK)
			return
		}

		log.Printf("dingtalk: received message from %s: %s", msg.SenderStaffID, msg.Text.Content)

		// Send to hub
		hub.In <- chat.Inbound{
			Channel:   "dingtalk",
			SenderID:  msg.SenderStaffID,
			ChatID:    msg.ConversationID,
			Content:   msg.Text.Content,
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"robotCode": msg.RobotCode,
			},
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	})

	// Outbound sender goroutine
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		for {
			select {
			case <-ctx.Done():
				log.Println("dingtalk: stopping outbound sender")
				return
			case out := <-hub.Out:
				if out.Channel != "dingtalk" {
					continue
				}

				// For DingTalk, we need to send via webhook callback
				// This is simplified - actual implementation would need
				// to use DingTalk's message send API
				log.Printf("dingtalk: would send response: %s", out.Content)
				_ = client
			}
		}
	}()

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	// Start server in goroutine
	go func() {
		log.Printf("dingtalk: starting webhook server on port %d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("dingtalk: server error: %v", err)
		}
	}()

	// Wait for context cancellation to shutdown
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	return nil
}
