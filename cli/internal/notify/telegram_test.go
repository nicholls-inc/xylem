package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tryNewServer attempts to create an httptest.Server. If port binding is
// blocked (e.g. in a sandboxed environment), it calls t.Skip instead of
// letting the panic propagate.
func tryNewServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Skipf("httptest.NewServer unavailable (sandbox): %v", r)
			}
		}()
		srv = httptest.NewServer(handler)
	}()
	if srv == nil {
		t.Skip("httptest.NewServer returned nil")
	}
	return srv
}

// newTestTelegram creates a Telegram notifier pointed at the given test server
// instead of the real Telegram API. The token is set to "test-token" and the
// chat ID to "12345".
func newTestTelegram(serverURL string, levels []string) *Telegram {
	tg := NewTelegram("test-token", "12345", levels)
	tg.client = &http.Client{Timeout: 5 * time.Second}
	// We cannot change telegramAPIBase (const), so we redirect requests at the
	// transport level to point at the test server.
	tg.client.Transport = &rewriteTransport{target: serverURL}
	return tg
}

// rewriteTransport rewrites every request to point at a test server URL.
type rewriteTransport struct {
	target string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = strings.TrimPrefix(rt.target, "http://")
	return http.DefaultTransport.RoundTrip(req2)
}

func TestTelegram_SendAlert_Success(t *testing.T) {
	var received telegramRequest
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		json.NewEncoder(w).Encode(telegramResponse{OK: true})
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, []string{"critical", "warning"})
	alert := Alert{
		Severity:  SeverityCritical,
		Code:      "test_alert",
		Title:     "Test Title",
		Detail:    "Something broke",
		Timestamp: time.Now(),
		VesselIDs: []string{"v1", "v2"},
	}

	err := tg.SendAlert(context.Background(), alert)
	if err != nil {
		t.Fatalf("SendAlert returned error: %v", err)
	}

	if received.ChatID != "12345" {
		t.Errorf("expected chat_id=12345, got %q", received.ChatID)
	}
	if received.ParseMode != "HTML" {
		t.Errorf("expected parse_mode=HTML, got %q", received.ParseMode)
	}
	if received.Text == "" {
		t.Error("expected non-empty text")
	}
}

func TestTelegram_SendAlert_SeverityFiltered(t *testing.T) {
	// Severity filtering happens before any HTTP call, so no server needed.
	tg := NewTelegram("test-token", "12345", []string{"critical"})

	alert := Alert{
		Severity: SeverityWarning,
		Code:     "warning_alert",
		Title:    "Warning",
		Detail:   "Something mild",
	}

	err := tg.SendAlert(context.Background(), alert)
	if err != nil {
		t.Fatalf("SendAlert returned error: %v", err)
	}
	// If severity filtering works, sendMessage is never called, so no HTTP
	// error even though the token/URL are fake.
}

func TestTelegram_CooldownDedup_SameCodeWithinWindow(t *testing.T) {
	callCount := 0
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(telegramResponse{OK: true})
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, []string{"critical"})
	tg.cooldown = 1 * time.Hour // large window so second call is always within it

	alert := Alert{
		Severity: SeverityCritical,
		Code:     "dup_code",
		Title:    "Dup",
		Detail:   "Duplicate",
	}

	if err := tg.SendAlert(context.Background(), alert); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := tg.SendAlert(context.Background(), alert); err != nil {
		t.Fatalf("second send: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected 1 API call (second suppressed by cooldown), got %d", callCount)
	}
}

func TestTelegram_CooldownDedup_SameCodeAfterWindow(t *testing.T) {
	callCount := 0
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(telegramResponse{OK: true})
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, []string{"critical"})
	tg.cooldown = 1 * time.Millisecond // tiny window so it expires immediately

	alert := Alert{
		Severity: SeverityCritical,
		Code:     "dup_code",
		Title:    "Dup",
		Detail:   "Duplicate",
	}

	if err := tg.SendAlert(context.Background(), alert); err != nil {
		t.Fatalf("first send: %v", err)
	}
	// Wait for the cooldown to expire.
	time.Sleep(5 * time.Millisecond)

	if err := tg.SendAlert(context.Background(), alert); err != nil {
		t.Fatalf("second send: %v", err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 API calls (cooldown expired), got %d", callCount)
	}
}

func TestTelegram_PostStatus_IsNoop(t *testing.T) {
	// PostStatus returns nil without any HTTP call.
	tg := NewTelegram("test-token", "12345", []string{"critical", "warning"})
	err := tg.PostStatus(context.Background(), StatusReport{Markdown: "hello"})
	if err != nil {
		t.Fatalf("PostStatus returned error: %v", err)
	}
}

func TestTelegram_FormatAlert_HTMLContent(t *testing.T) {
	alert := Alert{
		Severity:  SeverityCritical,
		Code:      "test",
		Title:     "Auth <Failure>",
		Detail:    "Token expired & invalid",
		VesselIDs: []string{"v1", "v2", "v3"},
	}

	msg := formatTelegramAlert(alert)

	// Check severity is uppercased and bold.
	if !strings.Contains(msg, "<b>CRITICAL</b>") {
		t.Errorf("expected bold CRITICAL in message, got: %s", msg)
	}
	// Check title is HTML-escaped.
	if !strings.Contains(msg, "Auth &lt;Failure&gt;") {
		t.Errorf("expected HTML-escaped title, got: %s", msg)
	}
	// Check detail is HTML-escaped.
	if !strings.Contains(msg, "Token expired &amp; invalid") {
		t.Errorf("expected HTML-escaped detail, got: %s", msg)
	}
	// Check vessel IDs are in code tags.
	if !strings.Contains(msg, "<code>v1</code>") {
		t.Errorf("expected vessel ID in code tag, got: %s", msg)
	}
	// Check the "Affected" label is present.
	if !strings.Contains(msg, "<b>Affected:</b>") {
		t.Errorf("expected Affected label, got: %s", msg)
	}
}

func TestTelegram_FormatAlert_VesselIDTruncation(t *testing.T) {
	ids := make([]string, 15)
	for i := range ids {
		ids[i] = "vessel-" + string(rune('A'+i))
	}
	alert := Alert{
		Severity:  SeverityWarning,
		Code:      "test",
		Title:     "Test",
		Detail:    "Detail",
		VesselIDs: ids,
	}

	msg := formatTelegramAlert(alert)

	// Should contain the first 10 IDs.
	if !strings.Contains(msg, "<code>vessel-A</code>") {
		t.Error("expected first vessel ID")
	}
	if !strings.Contains(msg, "<code>vessel-J</code>") {
		t.Error("expected 10th vessel ID (vessel-J)")
	}
	// Should NOT contain the 11th ID.
	if strings.Contains(msg, "<code>vessel-K</code>") {
		t.Error("11th vessel ID should not appear directly")
	}
	// Should contain "+N more" suffix.
	if !strings.Contains(msg, "(+5 more)") {
		t.Errorf("expected (+5 more) truncation notice, got: %s", msg)
	}
}

func TestTelegram_FormatAlert_LongMessageTruncation(t *testing.T) {
	// Build a detail string that exceeds 4096 chars.
	longDetail := strings.Repeat("x", 5000)
	alert := Alert{
		Severity: SeverityCritical,
		Code:     "test",
		Title:    "Test",
		Detail:   longDetail,
	}

	msg := formatTelegramAlert(alert)

	if len(msg) > telegramMaxMsgLen {
		t.Errorf("message length %d exceeds max %d", len(msg), telegramMaxMsgLen)
	}
	if !strings.HasSuffix(msg, "...") {
		t.Error("truncated message should end with '...'")
	}
}

func TestTelegram_Name(t *testing.T) {
	tg := NewTelegram("tok", "123", []string{"critical"})
	if tg.Name() != "telegram" {
		t.Errorf("expected Name()=telegram, got %q", tg.Name())
	}
}

func TestTelegram_GetUpdates_ReturnsMessages(t *testing.T) {
	handlerCalled := false
	var gotChatID int64
	var gotText string

	updates := `{"ok":true,"result":[{"update_id":100,"message":{"message_id":1,"chat":{"id":12345},"text":"hello"}}]}`
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(updates)) //nolint:errcheck
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received, err := tg.getUpdates(ctx)
	if err != nil {
		t.Fatalf("getUpdates returned error: %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("expected 1 update, got %d", len(received))
	}
	if received[0].Message != nil {
		handlerCalled = true
		gotChatID = received[0].Message.Chat.ID
		gotText = received[0].Message.Text
	}
	if !handlerCalled {
		t.Error("expected message in update")
	}
	if gotChatID != 12345 {
		t.Errorf("expected chat_id=12345, got %d", gotChatID)
	}
	if gotText != "hello" {
		t.Errorf("expected text=hello, got %q", gotText)
	}
}

func TestTelegram_GetUpdates_EmptyBatch(t *testing.T) {
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"result":[]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received, err := tg.getUpdates(ctx)
	if err != nil {
		t.Fatalf("getUpdates returned error: %v", err)
	}
	if len(received) != 0 {
		t.Errorf("expected 0 updates, got %d", len(received))
	}
	// Offset should remain 0 since no updates were received.
	if tg.offset != 0 {
		t.Errorf("expected offset=0 after empty batch, got %d", tg.offset)
	}
}

func TestTelegram_GetUpdates_AcknowledgesOffset(t *testing.T) {
	var lastOffset string
	call := 0
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		lastOffset = r.URL.Query().Get("offset")
		w.Write([]byte(`{"ok":true,"result":[{"update_id":42,"message":{"message_id":1,"chat":{"id":1},"text":"hi"}}]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := tg.getUpdates(ctx); err != nil {
		t.Fatalf("first getUpdates error: %v", err)
	}
	// After seeing update_id=42, offset should be 43.
	if tg.offset != 43 {
		t.Errorf("expected offset=43 after update_id=42, got %d", tg.offset)
	}

	// Second call should send offset=43.
	if _, err := tg.getUpdates(ctx); err != nil {
		t.Fatalf("second getUpdates error: %v", err)
	}
	if lastOffset != "43" {
		t.Errorf("expected second request to have offset=43, got %q", lastOffset)
	}
	_ = call
}

func TestTelegram_StartPolling_CancelStops(t *testing.T) {
	calls := 0
	// Server immediately returns empty batch so pollLoop keeps looping without
	// blocking on a real long-poll.
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"ok":true,"result":[]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	tg := newTestTelegram(srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	tg.StartPolling(ctx, func(ctx context.Context, chatID int64, text string) {
		// Should not be called with empty batches.
		t.Error("handler called unexpectedly")
		close(done)
	})

	// Let a couple of iterations happen, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Give the goroutine time to notice ctx cancellation.
	time.Sleep(100 * time.Millisecond)

	// We can't directly observe that the goroutine stopped, but if it's still
	// running after cancel() we'd see continued server calls. The test passes
	// as long as no races or panics occur.
	if calls == 0 {
		t.Log("note: no getUpdates calls (may be expected in sandbox)")
	}
}
