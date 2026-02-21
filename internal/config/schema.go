package config

// Config holds picobot configuration (minimal for v0).
type Config struct {
	Agents    AgentsConfig    `json:"agents"`
	Channels  ChannelsConfig  `json:"channels"`
	Providers ProvidersConfig `json:"providers"`
}

type AgentsConfig struct {
	Defaults AgentDefaults `json:"defaults"`
}

type AgentDefaults struct {
	Workspace          string  `json:"workspace"`
	Model              string  `json:"model"`
	MaxTokens          int     `json:"maxTokens"`
	Temperature        float64 `json:"temperature"`
	MaxToolIterations  int     `json:"maxToolIterations"`
	HeartbeatIntervalS int     `json:"heartbeatIntervalS"`
}

type ChannelsConfig struct {
	Telegram TelegramConfig `json:"telegram"`
	DingTalk DingTalkConfig `json:"dingtalk"`
}

// DingTalkConfig holds DingTalk channel configuration
// Compatible with zeroclaw/openclaw DingTalk channel plugin
type DingTalkConfig struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"clientId"`     // AppKey
	ClientSecret string `json:"clientSecret"` // AppSecret
	UnionID      string `json:"unionId"`      // User UnionID (for API calls)
	UserID       string `json:"userId"`       // User ID (for API calls)
	RobotCode    string `json:"robotCode"`    // Robot code (for media download, proactive messages)
	CorpID       string `json:"corpId"`       // Corp ID
	AgentID      string `json:"agentId"`      // Agent ID

	// Authorization policies (zeroclaw compatible)
	DMPolicy    string   `json:"dmPolicy"`    // "open", "pairing", "allowlist"
	GroupPolicy string   `json:"groupPolicy"` // "open", "allowlist"
	AllowFrom   []string `json:"allowFrom"`   // Allowed sender/group IDs

	// Message type preference
	MessageType     string `json:"messageType"`     // "markdown" (default) or "card"
	CardTemplateID  string `json:"cardTemplateId"`  // AI Card template ID
	CardTemplateKey string `json:"cardTemplateKey"` // AI Card template content key

	// Connection robustness settings (zeroclaw compatible)
	MaxConnectionAttempts int     `json:"maxConnectionAttempts"` // Default: 10
	InitialReconnectDelay int     `json:"initialReconnectDelay"` // Default: 1000ms
	MaxReconnectDelay     int     `json:"maxReconnectDelay"`     // Default: 60000ms
	ReconnectJitter       float64 `json:"reconnectJitter"`       // Default: 0.3

	// Debug logging
	Debug bool `json:"debug"`

	// Show thinking status
	ShowThinking bool `json:"showThinking"`
}

type TelegramConfig struct {
	Enabled   bool     `json:"enabled"`
	Token     string   `json:"token"`
	AllowFrom []string `json:"allowFrom"`
}

type ProvidersConfig struct {
	OpenAI *ProviderConfig `json:"openai,omitempty"`
}

type ProviderConfig struct {
	APIKey  string `json:"apiKey"`
	APIBase string `json:"apiBase"`
}
