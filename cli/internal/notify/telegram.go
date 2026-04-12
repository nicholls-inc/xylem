package notify

// Telegram sends alerts via the Telegram Bot API.
// It is send-only (no webhook listener, no callback handling).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	telegramAPIBase      = "https://api.telegram.org/bot"
	telegramMaxMsgLen    = 4096
	defaultAlertCooldown = 30 * time.Minute
)

// Telegram delivers alerts to a Telegram chat via the Bot API.
type Telegram struct {
	token  string
	chatID string
	levels map[AlertSeverity]bool
	client *http.Client

	// cooldown dedup: code -> last sent time
	mu       sync.Mutex
	lastSent map[string]time.Time
	cooldown time.Duration
}

// NewTelegram creates a Telegram notifier. levels controls which severities
// are delivered (e.g. ["critical", "warning"]).
func NewTelegram(token, chatID string, levels []string) *Telegram {
	lvl := make(map[AlertSeverity]bool, len(levels))
	for _, l := range levels {
		lvl[AlertSeverity(l)] = true
	}
	return &Telegram{
		token:    token,
		chatID:   chatID,
		levels:   lvl,
		client:   &http.Client{Timeout: 10 * time.Second},
		lastSent: make(map[string]time.Time),
		cooldown: defaultAlertCooldown,
	}
}

func (t *Telegram) Name() string { return "telegram" }

// PostStatus is a no-op -- Telegram is for alerts only.
func (t *Telegram) PostStatus(_ context.Context, _ StatusReport) error {
	return nil
}

// SendAlert delivers an alert to the configured Telegram chat. Alerts are
// suppressed if the same code was sent within the cooldown window.
func (t *Telegram) SendAlert(ctx context.Context, alert Alert) error {
	if !t.levels[alert.Severity] {
		return nil
	}
	if t.isDuplicate(alert.Code) {
		slog.Debug("notify: telegram alert suppressed by cooldown", "code", alert.Code)
		return nil
	}

	msg := formatTelegramAlert(alert)
	if err := t.sendMessage(ctx, msg); err != nil {
		return fmt.Errorf("telegram send alert: %w", err)
	}
	t.recordSent(alert.Code)
	return nil
}

func (t *Telegram) isDuplicate(code string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.lastSent[code]
	if !ok {
		return false
	}
	return time.Since(last) < t.cooldown
}

func (t *Telegram) recordSent(code string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastSent[code] = time.Now()
}

func formatTelegramAlert(alert Alert) string {
	var b strings.Builder
	severity := strings.ToUpper(string(alert.Severity))
	// Use HTML formatting (most reliable for Telegram).
	b.WriteString(fmt.Sprintf("<b>%s</b>: %s\n\n", severity, html.EscapeString(alert.Title)))
	b.WriteString(html.EscapeString(alert.Detail))
	if len(alert.VesselIDs) > 0 {
		b.WriteString("\n\n<b>Affected:</b> ")
		ids := alert.VesselIDs
		if len(ids) > 10 {
			ids = ids[:10]
		}
		for i, id := range ids {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("<code>")
			b.WriteString(html.EscapeString(id))
			b.WriteString("</code>")
		}
		if len(alert.VesselIDs) > 10 {
			b.WriteString(fmt.Sprintf(" (+%d more)", len(alert.VesselIDs)-10))
		}
	}
	msg := b.String()
	if len(msg) > telegramMaxMsgLen {
		msg = msg[:telegramMaxMsgLen-3] + "..."
	}
	return msg
}

type telegramRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
}

func (t *Telegram) sendMessage(ctx context.Context, text string) error {
	body, err := json.Marshal(telegramRequest{
		ChatID:    t.chatID,
		Text:      text,
		ParseMode: "HTML",
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := telegramAPIBase + t.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	var apiResp telegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !apiResp.OK {
		return fmt.Errorf("telegram API: %s", apiResp.Description)
	}
	return nil
}
