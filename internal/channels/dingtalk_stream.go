package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/local/picobot/internal/chat"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/logger"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

// =============================================================================
// SDK Logger
// =============================================================================

// sdkLogger forwards SDK logs to Go's standard log
type sdkLogger struct{}

func (l *sdkLogger) Debugf(format string, args ...interface{}) {
	log.Printf("[dingtalk-sdk] "+format, args...)
}
func (l *sdkLogger) Infof(format string, args ...interface{}) {
	log.Printf("[dingtalk-sdk] "+format, args...)
}
func (l *sdkLogger) Warningf(format string, args ...interface{}) {
	log.Printf("[dingtalk-sdk] WARN: "+format, args...)
}
func (l *sdkLogger) Errorf(format string, args ...interface{}) {
	log.Printf("[dingtalk-sdk] ERROR: "+format, args...)
}
func (l *sdkLogger) Fatalf(format string, args ...interface{}) {
	log.Printf("[dingtalk-sdk] FATAL: "+format, args...)
}

func init() {
	logger.SetLogger(&sdkLogger{})
}

// =============================================================================
// Configuration
// =============================================================================

// DingTalkStreamConfig holds the configuration for the stream client
type DingTalkStreamConfig struct {
	ClientID              string
	ClientSecret          string
	RobotCode             string
	CorpID                string
	AgentID               string
	DMPolicy              string   // "open", "pairing", "allowlist"
	GroupPolicy           string   // "open", "allowlist"
	AllowFrom             []string // Allowed sender/group IDs
	MessageType           string   // "markdown" or "card"
	CardTemplateID        string   // AI Card template ID
	CardTemplateKey       string   // AI Card template content key
	MaxConnectionAttempts int      // Default: 10
	InitialReconnectDelay int      // Default: 1000ms
	MaxReconnectDelay     int      // Default: 60000ms
	ReconnectJitter       float64  // Default: 0.3
	Debug                 bool     // Debug logging
	ShowThinking          bool     // Show thinking status
	WorkspaceDir          string   // Agent workspace directory
}

// DefaultDingTalkStreamConfig returns default configuration
func DefaultDingTalkStreamConfig() DingTalkStreamConfig {
	return DingTalkStreamConfig{
		MaxConnectionAttempts: 10,
		InitialReconnectDelay: 1000,
		MaxReconnectDelay:     60000,
		ReconnectJitter:       0.3,
		DMPolicy:              "open",
		GroupPolicy:           "open",
		MessageType:           "markdown",
		CardTemplateKey:       "msgContent",
		ShowThinking:          true,
	}
}

// =============================================================================
// Session Webhook Cache
// =============================================================================

// sessionWebhookCache stores session webhooks for each conversation
type sessionWebhookCache struct {
	mu       sync.RWMutex
	webhooks map[string]string // chatID -> sessionWebhook
}

func newSessionWebhookCache() *sessionWebhookCache {
	return &sessionWebhookCache{
		webhooks: make(map[string]string),
	}
}

func (c *sessionWebhookCache) Set(chatID, webhook string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.webhooks[chatID] = webhook
}

// SetBoth stores webhook with both keys for flexible routing (zeroclaw compatible)
// This allows reply routing to work for both group and private chats
func (c *sessionWebhookCache) SetBoth(chatID, senderID, webhook string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.webhooks[chatID] = webhook
	c.webhooks[senderID] = webhook
}

func (c *sessionWebhookCache) Get(chatID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	webhook, ok := c.webhooks[chatID]
	return webhook, ok
}

// =============================================================================
// Authorization Helpers
// =============================================================================

// normalizedAllowFrom normalizes allowFrom list to standardized format
func normalizedAllowFrom(list []string) (entries []string, entriesLower []string, hasWildcard bool) {
	for _, value := range list {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if value == "*" {
			hasWildcard = true
			continue
		}
		// Strip dingtalk/dd/ding prefix
		normalized := value
		if strings.HasPrefix(normalized, "dingtalk:") || strings.HasPrefix(normalized, "dd:") || strings.HasPrefix(normalized, "ding:") {
			normalized = normalized[strings.Index(normalized, ":")+1:]
		}
		entries = append(entries, normalized)
		entriesLower = append(entriesLower, strings.ToLower(normalized))
	}
	return
}

// isSenderAllowed checks if sender is allowed based on allowFrom list
func isSenderAllowed(senderId string, entries []string, entriesLower []string, hasWildcard bool) bool {
	if len(entries) == 0 {
		return true // Empty allowFrom = allow all
	}
	if hasWildcard {
		return true
	}
	// Strip prefix from sender ID
	normalizedSender := senderId
	if strings.HasPrefix(normalizedSender, "dingtalk:") || strings.HasPrefix(normalizedSender, "dd:") || strings.HasPrefix(normalizedSender, "ding:") {
		normalizedSender = normalizedSender[strings.Index(normalizedSender, ":")+1:]
	}
	normalizedSenderLower := strings.ToLower(normalizedSender)

	for _, entryLower := range entriesLower {
		if entryLower == normalizedSenderLower {
			return true
		}
	}
	return false
}

// =============================================================================
// Media Helpers
// =============================================================================

// MediaFile represents a downloaded media file
type MediaFile struct {
	Path     string
	MimeType string
}

// downloadMedia downloads media file from DingTalk
func downloadMedia(ctx context.Context, httpClient *http.Client, robotCode, downloadCode, workspaceDir string, accessToken string, log *log.Logger) (*MediaFile, error) {
	if downloadCode == "" {
		return nil, fmt.Errorf("downloadCode is required")
	}
	if robotCode == "" {
		return nil, fmt.Errorf("robotCode is required")
	}

	// Get download URL
	downloadReq := map[string]string{
		"downloadCode": downloadCode,
		"robotCode":    robotCode,
	}
	downloadBody, _ := json.Marshal(downloadReq)

	downloadURL := "https://api.dingtalk.com/v1.0/robot/messageFiles/download"
	req, err := http.NewRequestWithContext(ctx, "POST", downloadURL, bytes.NewReader(downloadBody))
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accessToken != "" {
		req.Header.Set("x-acs-dingtalk-access-token", accessToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download failed: %d - %s", resp.StatusCode, string(body))
	}

	var downloadResp struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&downloadResp); err != nil {
		return nil, fmt.Errorf("decode download response: %w", err)
	}

	if downloadResp.DownloadURL == "" {
		return nil, fmt.Errorf("downloadUrl is empty in response")
	}

	// Download the actual file
	fileResp, err := httpClient.Get(downloadResp.DownloadURL)
	if err != nil {
		return nil, fmt.Errorf("fetch file failed: %w", err)
	}
	defer fileResp.Body.Close()

	contentType := fileResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Save to workspace media/inbound/ directory
	mediaDir := filepath.Join(workspaceDir, "media", "inbound")
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		return nil, fmt.Errorf("create media dir: %w", err)
	}

	ext := strings.Split(contentType, "/")[1]
	ext = strings.Split(ext, ";")[0]
	if ext == "" {
		ext = "bin"
	}
	filename := fmt.Sprintf("%d_%s.%s", time.Now().UnixMilli(), randomString(8), ext)
	mediaPath := filepath.Join(mediaDir, filename)

	fileData, err := io.ReadAll(fileResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read file data: %w", err)
	}

	if err := os.WriteFile(mediaPath, fileData, 0644); err != nil {
		return nil, fmt.Errorf("save file: %w", err)
	}

	log.Printf("[dingtalk] Media saved to: %s", mediaPath)
	return &MediaFile{Path: mediaPath, MimeType: contentType}, nil
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
		time.Sleep(time.Nanosecond) // Ensure different values
	}
	return string(b)
}

// =============================================================================
// DingTalk Stream Client
// =============================================================================

// DingTalkStreamClient wraps with official DingTalk stream SDK with zeroclaw-compatible features
type DingTalkStreamClient struct {
	client       *client.StreamClient
	hub          *chat.Hub
	config       DingTalkStreamConfig
	webhookCache *sessionWebhookCache
	httpClient   *http.Client
	dedup        *MessageDedup
	accountId    string

	// Token cache
	tokenMu     sync.RWMutex
	accessToken string
	tokenExpiry time.Time

	// Connection manager
	connManager *ConnectionManager
}

// NewDingTalkStreamClient creates a new stream client with with given configuration
func NewDingTalkStreamClient(hub *chat.Hub, config DingTalkStreamConfig, accountId string) *DingTalkStreamClient {
	return &DingTalkStreamClient{
		hub:          hub,
		config:       config,
		webhookCache: newSessionWebhookCache(),
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		dedup:        NewMessageDedup(1000, 60*time.Second),
		accountId:    accountId,
	}
}

// getAccessToken retrieves and caches an access token from DingTalk
func (d *DingTalkStreamClient) getAccessToken(ctx context.Context) (string, error) {
	d.tokenMu.RLock()
	now := time.Now()
	if d.accessToken != "" && now.Before(d.tokenExpiry.Add(-5*time.Minute)) {
		d.tokenMu.RUnlock()
		return d.accessToken, nil
	}
	d.tokenMu.RUnlock()

	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()

	// Double-check after acquiring write lock
	if d.accessToken != "" && now.Before(d.tokenExpiry.Add(-5*time.Minute)) {
		return d.accessToken, nil
	}

	// Fetch new token using DingTalk OAuth2 API
	// URL: https://api.dingtalk.com/v1.0/oauth2/accessToken
	// Method: POST
	// Content-Type: application/x-www-form-urlencoded
	// Body: appKey=xxx&appSecret=xxx
	tokenURL := "https://api.dingtalk.com/v1.0/oauth2/accessToken"
	formData := url.Values{}
	formData.Set("appKey", d.config.ClientID)
	formData.Set("appSecret", d.config.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token request failed: %d - %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int    `json:"expiresIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	d.accessToken = result.AccessToken
	d.tokenExpiry = now.Add(time.Duration(result.ExpiresIn) * time.Second)

	log.Printf("[dingtalk] Token refreshed, expires in %ds", result.ExpiresIn)
	return d.accessToken, nil
}

// Start starts the stream client with robust connection management
func (d *DingTalkStreamClient) Start(ctx context.Context) error {
	if d.config.ClientID == "" || d.config.ClientSecret == "" {
		log.Println("dingtalk-stream: clientID or clientSecret not configured, skipping")
		return nil
	}

	log.Printf("[dingtalk] Starting with config: clientID=%s, robotCode=%s, dmPolicy=%s, groupPolicy=%s",
		d.config.ClientID, d.config.RobotCode, d.config.DMPolicy, d.config.GroupPolicy)

	// Create credential config
	cred := client.NewAppCredentialConfig(d.config.ClientID, d.config.ClientSecret)

	// Build extras map
	extras := make(map[string]string)
	if d.config.RobotCode != "" {
		extras["robotCode"] = d.config.RobotCode
	}
	if d.config.CorpID != "" {
		extras["corpId"] = d.config.CorpID
	}
	if d.config.AgentID != "" {
		extras["agentId"] = d.config.AgentID
	}

	// Create stream client with auto-reconnect disabled (we manage it ourselves)
	d.client = client.NewStreamClient(
		client.WithAppCredential(cred),
		client.WithAutoReconnect(false), // We use our own ConnectionManager
		client.WithExtras(extras),
	)

	// Register chatbot callback handler
	d.client.RegisterChatBotCallbackRouter(func(ctx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		log.Printf("[dingtalk-sdk] ChatBot callback: sender=%s, content=%s", data.SenderStaffId, data.Text.Content)
		d.handleMessage(ctx, data)
		return []byte("{}"), nil
	})

	// Register all events for debugging
	d.client.RegisterAllEventRouter(func(ctx context.Context, df *payload.DataFrame) (*payload.DataFrameResponse, error) {
		log.Printf("[dingtalk-sdk] Event: type=%s, topic=%s", df.Type, df.GetTopic())
		return payload.NewSuccessDataFrameResponse(), nil
	})

	// Create connection manager config
	connConfig := ConnectionManagerConfig{
		MaxAttempts:   d.config.MaxConnectionAttempts,
		InitialDelay:  time.Duration(d.config.InitialReconnectDelay) * time.Millisecond,
		MaxDelay:      time.Duration(d.config.MaxReconnectDelay) * time.Millisecond,
		Jitter:        d.config.ReconnectJitter,
		OnStateChange: d.onConnectionStateChange,
	}

	// Create connection manager
	d.connManager = NewConnectionManager(d.client, d.accountId, connConfig, log.New(os.Stderr, "[dingtalk-conn] ", log.LstdFlags))

	// Start outbound handler
	go d.outboundHandler(ctx)

	// Connect with robust retry
	if err := d.connManager.connect(ctx); err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	// Wait for context cancellation
	<-ctx.Done()
	log.Println("dingtalk-stream: shutting down")
	return ctx.Err()
}

// onConnectionStateChange handles connection state changes
func (d *DingTalkStreamClient) onConnectionStateChange(state ConnectionState, err string) {
	log.Printf("[dingtalk] Connection state: %s%s", state, func() string {
		if err != "" {
			return " (" + err + ")"
		}
		return ""
	}())
}

// handleMessage processes incoming messages with deduplication and authorization
func (d *DingTalkStreamClient) handleMessage(ctx context.Context, msg *chatbot.BotCallbackDataModel) {
	if msg == nil {
		log.Println("[dingtalk] handleMessage: nil message")
		return
	}

	content := msg.Text.Content
	if content == "" {
		log.Println("[dingtalk] handleMessage: empty content, skipping")
		return
	}

	// Message deduplication
	robotKey := d.config.RobotCode
	if robotKey == "" {
		robotKey = d.config.ClientID
	}
	if robotKey == "" {
		robotKey = d.accountId
	}
	dedupKey := fmt.Sprintf("%s:%s", robotKey, msg.MsgId)

	if d.dedup.IsProcessed(dedupKey) {
		log.Printf("[dingtalk] Skipping duplicate message: %s", dedupKey)
		return
	}
	d.dedup.MarkProcessed(dedupKey)

	// Extract message info
	senderId := msg.SenderStaffId
	senderNick := msg.SenderNick
	if senderNick == "" {
		senderNick = "Unknown"
	}
	conversationId := msg.ConversationId
	conversationType := msg.ConversationType // "1" = direct, "2" = group
	isDirect := conversationType == "1"

	log.Printf("[dingtalk] Received message: type=%s, sender=%s, content=%q", conversationType, senderNick, content)

	// Authorization check
	if !d.checkAuthorization(msg, senderId, conversationId, isDirect) {
		return
	}

	// Store session webhook for replies (zeroclaw compatible: store both keys)
	d.webhookCache.SetBoth(conversationId, senderId, msg.SessionWebhook)

	// Send acknowledgment message (zeroclaw style)
	if d.config.ShowThinking {
		d.sendAcknowledgment(senderId, msg.SessionWebhook, content, msg.MsgId)
	}

	// Handle different message types
	var messageContent string
	var mediaFile *MediaFile

	msgType := "text"
	if msg.Msgtype != "" {
		msgType = msg.Msgtype
	}

	switch msgType {
	case "text":
		messageContent = content
	case "richText":
		messageContent = d.extractRichText(msg)
	case "picture", "audio", "video", "file":
		messageContent = fmt.Sprintf("<media:%s>", msgType)
		// Download media
		accessToken, _ := d.getAccessToken(ctx)
		if downloadCode := d.getDownloadCode(msg); downloadCode != "" {
			var err error
			mediaFile, err = downloadMedia(ctx, d.httpClient, d.config.RobotCode, downloadCode, d.config.WorkspaceDir, accessToken, log.Default())
			if err != nil {
				log.Printf("[dingtalk] Failed to download media: %v", err)
			}
		}
	default:
		messageContent = content
	}

	// Build inbound message
	inbound := chat.Inbound{
		Channel:   "dingtalk",
		SenderID:  senderId,
		ChatID:    conversationId,
		Content:   messageContent,
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"senderCorpId":              msg.SenderCorpId,
			"chatbotCorpId":             msg.ChatbotCorpId,
			"chatbotUserId":             msg.ChatbotUserId,
			"msgId":                     msg.MsgId,
			"sessionWebhook":            msg.SessionWebhook,
			"conversationType":          conversationType,
			"senderNick":                senderNick,
			"isAdmin":                   msg.IsAdmin,
			"sessionWebhookExpiredTime": msg.SessionWebhookExpiredTime,
			"msgType":                   msgType,
		},
	}

	if mediaFile != nil {
		inbound.Metadata["mediaPath"] = mediaFile.Path
		inbound.Metadata["mediaType"] = mediaFile.MimeType
	}

	// Send to hub
	select {
	case d.hub.In <- inbound:
		log.Printf("[dingtalk] Message sent to hub: sender=%s", senderNick)
	default:
		log.Printf("[dingtalk] hub.In channel full, dropping message")
	}
}

// sendAcknowledgment sends an acknowledgment message (zeroclaw style)
func (d *DingTalkStreamClient) sendAcknowledgment(senderId, webhook, content, msgId string) {
	// Generate a short hash for message ID (like zeroclaw)
	msgHash := shortHash(fmt.Sprintf("%s:%d", content, time.Now().Unix()))

	ackText := fmt.Sprintf("✅ [ID:%s] Message received, thinking...", msgHash)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		accessToken, _ := d.getAccessToken(ctx)

		body := map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": "Clawdbot",
				"text":  ackText,
			},
		}

		jsonData, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, "POST", webhook, bytes.NewReader(jsonData))
		if err != nil {
			log.Printf("[dingtalk] Failed to create ack request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if accessToken != "" {
			req.Header.Set("x-acs-dingtalk-access-token", accessToken)
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			log.Printf("[dingtalk] Failed to send ack: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			log.Printf("[dingtalk] Ack failed: %d - %s", resp.StatusCode, string(respBody))
		} else {
			log.Printf("[dingtalk] Ack sent with ID: %s", msgHash)
		}
	}()
}

// shortHash generates a short hash for message identification (like zeroclaw)
func shortHash(content string) string {
	h := crc32.ChecksumIEEE([]byte(content))
	return fmt.Sprintf("%08x", h)[:8]
}

// checkAuthorization verifies if sender is authorized
func (d *DingTalkStreamClient) checkAuthorization(msg *chatbot.BotCallbackDataModel, senderId, conversationId string, isDirect bool) bool {
	entries, entriesLower, hasWildcard := normalizedAllowFrom(d.config.AllowFrom)

	if isDirect {
		// Check dmPolicy
		switch d.config.DMPolicy {
		case "allowlist":
			if !isSenderAllowed(senderId, entries, entriesLower, hasWildcard) {
				log.Printf("[dingtalk] DM blocked: senderId=%s not in allowlist", senderId)
				// Notify user
				d.sendAccessDenied(senderId, msg.SessionWebhook, fmt.Sprintf("您的用户 ID: `%s`", senderId))
				return false
			}
		case "pairing":
			// Pairing mode - SDK handles it
		case "open":
			// Allow all
		}
	} else {
		// Group message - check groupPolicy
		if d.config.GroupPolicy == "allowlist" {
			if !isSenderAllowed(conversationId, entries, entriesLower, hasWildcard) {
				log.Printf("[dingtalk] Group blocked: conversationId=%s not in allowlist", conversationId)
				d.sendAccessDenied(senderId, msg.SessionWebhook, fmt.Sprintf("您的群聊 ID: `%s`", conversationId))
				return false
			}
		}
	}

	return true
}

// sendAccessDenied sends an access denied message
func (d *DingTalkStreamClient) sendAccessDenied(userId, webhook, message string) {
	content := fmt.Sprintf("⛔ 访问受限\n\n%s\n\n请联系管理员将此 ID 添加到允许列表中。", message)
	d.sendMessageToWebhook(webhook, content, "")
}

// extractRichText extracts text from richText messages
func (d *DingTalkStreamClient) extractRichText(msg *chatbot.BotCallbackDataModel) string {
	// Content is interface{}, need to handle based on msgtype
	if msg.Msgtype == "richText" {
		// Try to extract text from content
		if contentMap, ok := msg.Content.(map[string]interface{}); ok {
			if content, ok := contentMap["content"].(string); ok {
				return content
			}
		}
	}
	// Fallback to text content
	return msg.Text.Content
}

// getDownloadCode extracts download code from message
func (d *DingTalkStreamClient) getDownloadCode(msg *chatbot.BotCallbackDataModel) string {
	// Content is interface{}, need to type assert
	if contentMap, ok := msg.Content.(map[string]interface{}); ok {
		if code, ok := contentMap["downloadCode"].(string); ok {
			return code
		}
		if code, ok := contentMap["download_link"].(string); ok {
			return code
		}
	}
	return ""
}

// outboundHandler listens for outbound messages and sends them
func (d *DingTalkStreamClient) outboundHandler(ctx context.Context) {
	log.Println("dingtalk-stream: outbound handler started")
	for {
		select {
		case <-ctx.Done():
			log.Println("dingtalk-stream: outbound handler stopping")
			return
		case out := <-d.hub.Out:
			if out.Channel != "dingtalk" {
				continue
			}
			log.Printf("[dingtalk] Sending response: chatID=%s, content=%q", out.ChatID, out.Content)
			d.sendResponse(out.ChatID, out.Content)
		}
	}
}

// sendResponse sends a response via session webhook
func (d *DingTalkStreamClient) sendResponse(chatID, content string) {
	webhook, ok := d.webhookCache.Get(chatID)
	if !ok {
		log.Printf("[dingtalk] No session webhook for chatID: %s", chatID)
		return
	}

	if err := d.sendMessageToWebhook(webhook, content, ""); err != nil {
		log.Printf("[dingtalk] Send response failed: %v", err)
	} else {
		log.Printf("[dingtalk] Response sent successfully: chatID=%s", chatID)
	}
}

// sendMessageToWebhook sends a message to the session webhook
func (d *DingTalkStreamClient) sendMessageToWebhook(webhook, content string, atUserId string) error {
	ctx := context.Background()
	accessToken, err := d.getAccessToken(ctx)
	if err != nil {
		log.Printf("[dingtalk] Failed to get token: %v", err)
		// Continue without token
	}

	// Detect markdown
	useMarkdown := strings.Contains(content, "#") || strings.Contains(content, "*") ||
		strings.Contains(content, ">") || strings.Contains(content, "-") ||
		strings.Contains(content, "[") || strings.Contains(content, "\n")

	var body map[string]interface{}
	if useMarkdown {
		title := "Clawdbot 消息"
		if lines := strings.Split(content, "\n"); len(lines) > 0 {
			firstLine := strings.TrimSpace(lines[0])
			firstLine = strings.TrimLeft(firstLine, "#*-> ")
			if len(firstLine) > 0 {
				if len(firstLine) > 20 {
					firstLine = firstLine[:20]
				}
				title = firstLine
			}
		}
		body = map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": title,
				"text":  content,
			},
		}
	} else {
		body = map[string]interface{}{
			"msgtype": "text",
			"text": map[string]string{
				"content": content,
			},
		}
	}

	if atUserId != "" {
		body["at"] = map[string]interface{}{
			"atUserIds": []string{atUserId},
			"isAtAll":   false,
		}
	}

	jsonData, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", webhook, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accessToken != "" {
		req.Header.Set("x-acs-dingtalk-access-token", accessToken)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send failed: %d - %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// =============================================================================
// Exported Function
// =============================================================================

// StartDingTalkStream starts DingTalk stream client with full zeroclaw-compatible features
func StartDingTalkStream(ctx context.Context, hub *chat.Hub, config DingTalkStreamConfig, accountId string) {
	if config.ClientID == "" || config.ClientSecret == "" {
		log.Println("dingtalk-stream: not configured (missing clientID or clientSecret)")
		return
	}

	if config.WorkspaceDir == "" {
		// Default workspace
		homeDir, _ := os.UserHomeDir()
		config.WorkspaceDir = filepath.Join(homeDir, ".openclaw", "workspace")
	}

	go func() {
		client := NewDingTalkStreamClient(hub, config, accountId)
		if err := client.Start(ctx); err != nil && err != context.Canceled {
			log.Printf("dingtalk-stream: error: %v", err)
		}
	}()
}
