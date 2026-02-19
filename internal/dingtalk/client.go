package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	BaseURL       = "https://api.dingtalk.com/v1.0"
	OldBaseURL    = "https://oapi.dingtalk.com"
	TokenEndpoint = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
)

// Client provides access to DingTalk APIs
type Client struct {
	clientID     string
	clientSecret string
	accessToken  string
	expiresAt    time.Time
	httpClient   *http.Client
	mu           sync.RWMutex
}

// NewClient creates a new DingTalk client
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// tokenResponse represents the OAuth2 token response
type tokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int    `json:"expiresIn"`
}

// ensureToken refreshes the access token if expired
func (c *Client) ensureToken(ctx context.Context) error {
	c.mu.RLock()
	if c.accessToken != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if c.accessToken != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		return nil
	}

	payload := map[string]string{
		"appKey":    c.clientID,
		"appSecret": c.clientSecret,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token request failed: %d - %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}

	c.accessToken = tokenResp.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return nil
}

// doRequest makes an authenticated request to DingTalk API
func (c *Client) doRequest(ctx context.Context, method, url string, body interface{}) ([]byte, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	c.mu.RLock()
	req.Header.Set("x-acs-dingtalk-access-token", c.accessToken)
	c.mu.RUnlock()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// doOldAPIRequest makes a request to the old DingTalk API (oapi)
func (c *Client) doOldAPIRequest(ctx context.Context, method, url string, body interface{}) ([]byte, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	// Old API uses access_token as query param
	c.mu.RLock()
	token := c.accessToken
	c.mu.RUnlock()

	url = fmt.Sprintf("%s?access_token=%s", url, token)

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
