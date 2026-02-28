// PicoClaw - Ultra-lightweight personal AI agent
// DingTalk channel implementation using Stream Mode

package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const dingtalkAPIBase = "https://oapi.dingtalk.com"

// DingTalkAccessTokenResponse represents the response from gettoken API
type DingTalkAccessTokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// DingTalkMediaUploadResponse represents the response from media upload API
type DingTalkMediaUploadResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	MediaID string `json:"media_id"`
	Type    string `json:"type"`
}

// DingTalkChannel implements the Channel interface for DingTalk (钉钉)
// It uses WebSocket for receiving messages via stream mode and API for sending
type DingTalkChannel struct {
	*BaseChannel
	config       config.DingTalkConfig
	clientID     string
	clientSecret string
	streamClient *client.StreamClient
	ctx          context.Context
	cancel       context.CancelFunc
	// Map to store session webhooks for each chat
	sessionWebhooks sync.Map // chatID -> sessionWebhook
	// Token management for media upload
	accessToken string
	tokenExpiry time.Time
	tokenMu     sync.RWMutex
}

// NewDingTalkChannel creates a new DingTalk channel instance
func NewDingTalkChannel(cfg config.DingTalkConfig, messageBus *bus.MessageBus) (*DingTalkChannel, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("dingtalk client_id and client_secret are required")
	}

	base := NewBaseChannel("dingtalk", cfg, messageBus, cfg.AllowFrom)

	return &DingTalkChannel{
		BaseChannel:  base,
		config:       cfg,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
	}, nil
}

// Start initializes the DingTalk channel with Stream Mode
func (c *DingTalkChannel) Start(ctx context.Context) error {
	logger.InfoC("dingtalk", "Starting DingTalk channel (Stream Mode)...")

	c.ctx, c.cancel = context.WithCancel(ctx)

	// Create credential config
	cred := client.NewAppCredentialConfig(c.clientID, c.clientSecret)

	// Create the stream client with options
	c.streamClient = client.NewStreamClient(
		client.WithAppCredential(cred),
		client.WithAutoReconnect(true),
	)

	// Register chatbot callback handler (IChatBotMessageHandler is a function type)
	c.streamClient.RegisterChatBotCallbackRouter(c.onChatBotMessageReceived)

	// Start the stream client
	if err := c.streamClient.Start(c.ctx); err != nil {
		return fmt.Errorf("failed to start stream client: %w", err)
	}

	c.setRunning(true)
	logger.InfoC("dingtalk", "DingTalk channel started (Stream Mode)")
	return nil
}

// Stop gracefully stops the DingTalk channel
func (c *DingTalkChannel) Stop(ctx context.Context) error {
	logger.InfoC("dingtalk", "Stopping DingTalk channel...")

	if c.cancel != nil {
		c.cancel()
	}

	if c.streamClient != nil {
		c.streamClient.Close()
	}

	c.setRunning(false)
	logger.InfoC("dingtalk", "DingTalk channel stopped")
	return nil
}

// Send sends a message to DingTalk via the chatbot reply API
func (c *DingTalkChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("dingtalk channel not running")
	}

	// Get session webhook from storage
	sessionWebhookRaw, ok := c.sessionWebhooks.Load(msg.ChatID)
	if !ok {
		return fmt.Errorf("no session_webhook found for chat %s, cannot send message", msg.ChatID)
	}

	sessionWebhook, ok := sessionWebhookRaw.(string)
	if !ok {
		return fmt.Errorf("invalid session_webhook type for chat %s", msg.ChatID)
	}

	logger.DebugCF("dingtalk", "Sending message", map[string]any{
		"chat_id":   msg.ChatID,
		"preview":   utils.Truncate(msg.Content, 100),
		"has_media": len(msg.Media) > 0,
	})

	// Handle images (media) first
	if len(msg.Media) > 0 {
		return c.sendImageMessage(ctx, sessionWebhook, msg.Media)
	}

	// Send text/markdown message
	return c.SendDirectReply(ctx, sessionWebhook, msg.Content)
}

// onChatBotMessageReceived implements the IChatBotMessageHandler function signature
// This is called by the Stream SDK when a new message arrives
// IChatBotMessageHandler is: func(c context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error)
func (c *DingTalkChannel) onChatBotMessageReceived(
	ctx context.Context,
	data *chatbot.BotCallbackDataModel,
) ([]byte, error) {
	// Extract message content from Text field
	content := data.Text.Content
	if content == "" {
		// Try to extract from Content interface{} if Text is empty
		if contentMap, ok := data.Content.(map[string]any); ok {
			if textContent, ok := contentMap["content"].(string); ok {
				content = textContent
			}
		}
	}

	if content == "" {
		return nil, nil // Ignore empty messages
	}

	senderID := data.SenderStaffId
	senderNick := data.SenderNick
	chatID := senderID
	if data.ConversationType != "1" {
		// For group chats
		chatID = data.ConversationId
	}

	// Store the session webhook for this chat so we can reply later
	c.sessionWebhooks.Store(chatID, data.SessionWebhook)

	metadata := map[string]string{
		"sender_name":       senderNick,
		"conversation_id":   data.ConversationId,
		"conversation_type": data.ConversationType,
		"platform":          "dingtalk",
		"session_webhook":   data.SessionWebhook,
	}

	if data.ConversationType == "1" {
		metadata["peer_kind"] = "direct"
		metadata["peer_id"] = senderID
	} else {
		metadata["peer_kind"] = "group"
		metadata["peer_id"] = data.ConversationId
	}

	logger.DebugCF("dingtalk", "Received message", map[string]any{
		"sender_nick": senderNick,
		"sender_id":   senderID,
		"preview":     utils.Truncate(content, 50),
	})

	// Handle the message through the base channel
	c.HandleMessage(senderID, chatID, content, nil, metadata)

	// Return nil to indicate we've handled the message asynchronously
	// The response will be sent through the message bus
	return nil, nil
}

// SendDirectReply sends a direct reply using the session webhook
func (c *DingTalkChannel) SendDirectReply(ctx context.Context, sessionWebhook, content string) error {
	replier := chatbot.NewChatbotReplier()

	// Convert string content to []byte for the API
	contentBytes := []byte(content)
	titleBytes := []byte("PicoClaw")

	// Send markdown formatted reply
	err := replier.SimpleReplyMarkdown(
		ctx,
		sessionWebhook,
		titleBytes,
		contentBytes,
	)
	if err != nil {
		return fmt.Errorf("failed to send reply: %w", err)
	}

	return nil
}

// getAccessToken returns a valid access token for media upload
func (c *DingTalkChannel) getAccessToken() (string, error) {
	c.tokenMu.RLock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		token := c.accessToken
		c.tokenMu.RUnlock()
		return token, nil
	}
	c.tokenMu.RUnlock()

	// Token expired or missing, refresh it
	return c.refreshAccessToken()
}

// refreshAccessToken gets a new access token from DingTalk API
func (c *DingTalkChannel) refreshAccessToken() (string, error) {
	apiURL := fmt.Sprintf("%s/gettoken?appkey=%s&appsecret=%s",
		dingtalkAPIBase,
		url.QueryEscape(c.clientID),
		url.QueryEscape(c.clientSecret))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to request access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp DingTalkAccessTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.ErrCode != 0 {
		return "", fmt.Errorf("API error: %s (code: %d)", tokenResp.ErrMsg, tokenResp.ErrCode)
	}

	c.tokenMu.Lock()
	c.accessToken = tokenResp.AccessToken
	// Refresh 5 minutes before expiry
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn-300) * time.Second)
	c.tokenMu.Unlock()

	return tokenResp.AccessToken, nil
}

// uploadMedia uploads a local file to DingTalk and returns media_id
func (c *DingTalkChannel) uploadMedia(ctx context.Context, filePath string) (string, error) {
	accessToken, err := c.getAccessToken()
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}

	// Read the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	part, err := writer.CreateFormFile("media", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close writer: %w", err)
	}

	// Build upload URL
	apiURL := fmt.Sprintf("%s/media/upload?access_token=%s&type=image",
		dingtalkAPIBase, accessToken)

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &buf)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to upload media: %w", err)
	}
	defer resp.Body.Close()

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var uploadResp DingTalkMediaUploadResponse
	if err := json.Unmarshal(body, &uploadResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if uploadResp.ErrCode != 0 {
		return "", fmt.Errorf("upload API error: %s (code: %d)", uploadResp.ErrMsg, uploadResp.ErrCode)
	}

	return uploadResp.MediaID, nil
}

// sendImageMessage sends image messages via session webhook
func (c *DingTalkChannel) sendImageMessage(ctx context.Context, sessionWebhook string, mediaPaths []string) error {
	replier := chatbot.NewChatbotReplier()

	for _, filePath := range mediaPaths {
		// Upload the image to get media_id
		mediaID, err := c.uploadMedia(ctx, filePath)
		if err != nil {
			logger.ErrorCF("dingtalk", "Failed to upload image", map[string]any{
				"error": err.Error(),
				"path":  filePath,
			})
			continue
		}

		// Build image message using map for ReplyMessage
		imgMsg := map[string]interface{}{
			"msgtype": "image",
			"image": map[string]interface{}{
				"media_id": mediaID,
			},
		}

		// Use ReplyMessage to send image
		if err := replier.ReplyMessage(ctx, sessionWebhook, imgMsg); err != nil {
			logger.ErrorCF("dingtalk", "Failed to send image", map[string]any{
				"error": err.Error(),
			})
			continue
		}

		logger.DebugCF("dingtalk", "Image sent successfully", map[string]any{
			"media_id": mediaID,
		})
	}

	return nil
}
