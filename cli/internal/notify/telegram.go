package notify

// Telegram sends alerts via the Telegram Bot API and optionally polls for
// inbound messages via getUpdates long-polling.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	telegramAPIBase      = "https://api.telegram.org/bot"
	telegramMaxMsgLen    = 4096
	defaultAlertCooldown = 30 * time.Minute
)

// telegramUpdate is the wire format for a single update from getUpdates.
type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramInbound `json:"message,omitempty"`
}

// telegramInbound is the message part of a Telegram update.
type telegramInbound struct {
	MessageID int64        `json:"message_id"`
	Chat      telegramChat `json:"chat"`
	Text      string       `json:"text"`
}

// telegramChat holds the chat ID from an inbound message.
type telegramChat struct {
	ID int64 `json:"id"`
}

// telegramUpdatesResponse is the response envelope for getUpdates.
type telegramUpdatesResponse struct {
	OK     bool             `json:"ok"`
	Result []telegramUpdate `json:"result"`
}

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

	// offset tracks the next update_id to request from getUpdates.
	offset int64
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
	fmt.Fprintf(&b, "<b>%s</b>: %s\n\n", severity, html.EscapeString(alert.Title))
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
			fmt.Fprintf(&b, " (+%d more)", len(alert.VesselIDs)-10)
		}
	}
	msg := b.String()
	if len(msg) > telegramMaxMsgLen {
		msg = msg[:telegramMaxMsgLen-3] + "..."
	}
	return msg
}

// Send delivers a plain text message to the configured chat. It is the
// public equivalent of sendMessage and is used by the bot dispatcher to reply
// to inbound commands.
func (t *Telegram) Send(ctx context.Context, text string) error {
	return t.sendMessage(ctx, text)
}

// StartPolling starts a background goroutine that calls getUpdates in a
// long-poll loop. Each received message is delivered to handler. The goroutine
// stops when ctx is cancelled.
func (t *Telegram) StartPolling(ctx context.Context, handler func(ctx context.Context, chatID int64, text string)) {
	go t.pollLoop(ctx, handler)
}

// pollLoop is the internal long-poll loop. It runs until ctx is cancelled.
func (t *Telegram) pollLoop(ctx context.Context, handler func(ctx context.Context, chatID int64, text string)) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := t.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("telegram: getUpdates failed; backing off", "error", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			continue
		}
		backoff = time.Second

		for _, update := range updates {
			if update.Message == nil || update.Message.Text == "" {
				continue
			}
			handler(ctx, update.Message.Chat.ID, update.Message.Text)
		}
	}
}

// getUpdates calls the Telegram getUpdates API with long-polling (25s timeout).
// It returns the slice of updates and advances the internal offset to
// acknowledge them.
func (t *Telegram) getUpdates(ctx context.Context) ([]telegramUpdate, error) {
	t.mu.Lock()
	offset := t.offset
	t.mu.Unlock()

	// Use a separate context for the HTTP call so the long-poll timeout does
	// not interfere with the overall daemon context.
	reqCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	url := telegramAPIBase + t.token + "/getUpdates?timeout=25&offset=" + strconv.FormatInt(offset, 10)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create getUpdates request: %w", err)
	}

	// Use a client without a short timeout for long-polling.
	client := &http.Client{}
	if t.client != nil && t.client.Transport != nil {
		client.Transport = t.client.Transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getUpdates: %w", err)
	}
	defer resp.Body.Close()

	var apiResp telegramUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("telegram getUpdates API not ok")
	}

	// Advance offset to acknowledge all received updates.
	if len(apiResp.Result) > 0 {
		last := apiResp.Result[len(apiResp.Result)-1].UpdateID
		t.mu.Lock()
		t.offset = last + 1
		t.mu.Unlock()
	}

	return apiResp.Result, nil
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
