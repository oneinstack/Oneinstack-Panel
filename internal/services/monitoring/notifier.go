package monitoring

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Sender interface {
	Send(context.Context, *models.NotificationChannel, *models.MonitorAlertEvent) error
}

type ChannelInput struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	WebhookURL  string `json:"webhookUrl"`
	Secret      string `json:"secret"`
	ClearSecret bool   `json:"clearSecret"`
}

type webhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

type WebhookSender struct {
	client *http.Client
}

var blockedWebhookPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func NewWebhookSender() *WebhookSender {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	resolver := net.DefaultResolver
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(addresses) == 0 {
				return nil, errors.New("webhook host has no address")
			}
			for _, candidate := range addresses {
				if !publicWebhookAddress(candidate) {
					return nil, errors.New("webhook resolved to a non-public address")
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
		},
		ForceAttemptHTTP2: true, MaxIdleConns: 10,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
	}
	return &WebhookSender{client: &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("webhook redirects are not allowed")
		},
	}}
}

func (sender *WebhookSender) Send(
	ctx context.Context,
	channel *models.NotificationChannel,
	event *models.MonitorAlertEvent,
) error {
	if channel == nil || event == nil || channel.Type != "webhook" {
		return errors.New("unsupported notification channel")
	}
	config, err := decryptWebhookConfig(channel)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]interface{}{
		"source": "oneinstack-panel", "event": event.EventType,
		"severity": event.Severity, "rule": event.RuleName, "metric": event.Metric,
		"value": event.Value, "threshold": event.Threshold,
		"startedAt": event.StartedAt, "occurredAt": event.OccurredAt,
		"message": event.Message,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Oneinstack-Panel/monitor")
	request.Header.Set("X-Oneinstack-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	if config.Secret != "" {
		mac := hmac.New(sha256.New, []byte(config.Secret))
		_, _ = mac.Write(payload)
		request.Header.Set("X-Oneinstack-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := sender.client.Do(request)
	if err != nil {
		// net/http errors can contain the full URL, including query-string tokens.
		return errors.New("webhook request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (manager *Manager) CreateChannel(input ChannelInput) (*models.NotificationChannel, error) {
	if err := validateChannelInput(input, false); err != nil {
		return nil, err
	}
	channel := &models.NotificationChannel{
		ID: uuid.NewString(), Name: strings.TrimSpace(input.Name),
		Type: input.Type, Enabled: input.Enabled,
	}
	if err := setChannelConfig(channel, input, nil); err != nil {
		return nil, err
	}
	if err := manager.db.Create(channel).Error; err != nil {
		return nil, err
	}
	return channel, nil
}

func (manager *Manager) UpdateChannel(id string, input ChannelInput) (*models.NotificationChannel, error) {
	var channel models.NotificationChannel
	if err := manager.db.First(&channel, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if input.Type == "" {
		input.Type = channel.Type
	}
	if err := validateChannelInput(input, input.WebhookURL == ""); err != nil {
		return nil, err
	}
	existing, err := decryptWebhookConfig(&channel)
	if err != nil {
		return nil, err
	}
	channel.Name = strings.TrimSpace(input.Name)
	channel.Type = input.Type
	channel.Enabled = input.Enabled
	if err := setChannelConfig(&channel, input, existing); err != nil {
		return nil, err
	}
	if err := manager.db.Save(&channel).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

func (manager *Manager) ListChannels() ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	err := manager.db.Order("created_at DESC").Find(&channels).Error
	return channels, err
}

func (manager *Manager) DeleteChannel(id string) error {
	result := manager.db.Delete(&models.NotificationChannel{}, "id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (manager *Manager) TestChannel(ctx context.Context, id string) error {
	var channel models.NotificationChannel
	if err := manager.db.First(&channel, "id = ?", id).Error; err != nil {
		return err
	}
	now := manager.now().UTC()
	return manager.sender.Send(ctx, &channel, &models.MonitorAlertEvent{
		RuleName: "通知通道测试", Metric: MetricCPU, Severity: "info",
		EventType: "test", Value: 42, Threshold: 90,
		StartedAt: now, OccurredAt: now,
		Message: "Oneinstack Panel 通知通道测试成功",
	})
}

func setChannelConfig(
	channel *models.NotificationChannel,
	input ChannelInput,
	existing *webhookConfig,
) error {
	config := webhookConfig{}
	if existing != nil {
		config = *existing
	}
	if strings.TrimSpace(input.WebhookURL) != "" {
		config.URL = strings.TrimSpace(input.WebhookURL)
	}
	if input.Secret != "" {
		config.Secret = input.Secret
	} else if input.ClearSecret {
		config.Secret = ""
	}
	parsed, err := validateWebhookURL(config.URL)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	encrypted, err := utils.EncryptCredential(
		string(encoded), notificationPurpose(channel.ID),
	)
	if err != nil {
		return err
	}
	channel.ConfigEncrypted = encrypted
	channel.TargetHint = parsed.Hostname()
	channel.HasSecret = config.Secret != ""
	return nil
}

func decryptWebhookConfig(channel *models.NotificationChannel) (*webhookConfig, error) {
	if channel == nil || channel.ConfigEncrypted == "" {
		return nil, errors.New("notification channel configuration is missing")
	}
	plaintext, err := utils.DecryptCredential(
		channel.ConfigEncrypted, notificationPurpose(channel.ID),
	)
	if err != nil {
		return nil, err
	}
	var config webhookConfig
	if err := json.Unmarshal([]byte(plaintext), &config); err != nil {
		return nil, errors.New("notification channel configuration is invalid")
	}
	if _, err := validateWebhookURL(config.URL); err != nil {
		return nil, err
	}
	return &config, nil
}

func notificationPurpose(id string) string {
	return utils.CredentialPurposeNotification + ":" + id
}

func validateChannelInput(input ChannelInput, allowEmptyURL bool) error {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 120 {
		return errors.New("channel name must contain 1 to 120 characters")
	}
	if input.Type != "webhook" {
		return errors.New("only webhook notification channels are supported")
	}
	if !allowEmptyURL || strings.TrimSpace(input.WebhookURL) != "" {
		_, err := validateWebhookURL(input.WebhookURL)
		return err
	}
	return nil
}

func validateWebhookURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return nil, errors.New("webhook URL is invalid")
	}
	if parsed.Scheme != "https" {
		return nil, errors.New("webhook URL must use HTTPS")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("webhook URL cannot contain credentials or a fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, errors.New("webhook host is not allowed")
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && !publicWebhookAddress(address) {
		return nil, errors.New("webhook address must be public")
	}
	return parsed, nil
}

func publicWebhookAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() ||
		address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedWebhookPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
