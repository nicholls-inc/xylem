package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubRunner is a test stub for CommandRunner that captures calls and returns
// preset output.
type stubRunner struct {
	name   string
	args   []string
	output []byte
	err    error
	// block causes Run to block until the context is cancelled (for timeout tests).
	block bool
}

func (s *stubRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	s.name = name
	s.args = args
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.output, s.err
}

// captureDispatchReplies sets up a test Telegram + BotDispatcher and returns
// the replies sent via Send.
func captureDispatchReplies(t *testing.T, authorizedChatID int64, allowedCmds []string, runner CommandRunner) (*BotDispatcher, *[]string) {
	t.Helper()
	var replies []string
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegramRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			replies = append(replies, req.Text)
		}
		json.NewEncoder(w).Encode(telegramResponse{OK: true}) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	tg := newTestTelegram(srv.URL, nil)
	disp := NewBotDispatcher(tg, authorizedChatID, allowedCmds, runner)
	return disp, &replies
}

func TestBotDispatcher_Dispatch_AllowedCommand(t *testing.T) {
	stub := &stubRunner{output: []byte("queue is empty")}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem queue list"}, stub)

	disp.Dispatch(context.Background(), 99, "xylem queue list")

	if stub.name != "xylem" {
		t.Errorf("expected command=xylem, got %q", stub.name)
	}
	if len(stub.args) != 2 || stub.args[0] != "queue" || stub.args[1] != "list" {
		t.Errorf("unexpected args: %v", stub.args)
	}
	if len(*replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(*replies))
	}
	if !strings.Contains((*replies)[0], "queue is empty") {
		t.Errorf("reply should contain command output, got: %s", (*replies)[0])
	}
}

func TestBotDispatcher_Dispatch_DisallowedCommand(t *testing.T) {
	stub := &stubRunner{}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem status"}, stub)

	disp.Dispatch(context.Background(), 99, "rm -rf /")

	if stub.name != "" {
		t.Error("runner should NOT have been called for disallowed command")
	}
	if len(*replies) != 1 {
		t.Fatalf("expected 1 error reply, got %d", len(*replies))
	}
	if !strings.Contains((*replies)[0], "not allowed") {
		t.Errorf("expected 'not allowed' in reply, got: %s", (*replies)[0])
	}
}

func TestBotDispatcher_Dispatch_UnauthorizedChat(t *testing.T) {
	stub := &stubRunner{}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem status"}, stub)

	disp.Dispatch(context.Background(), 1234, "xylem status")

	if stub.name != "" {
		t.Error("runner should NOT have been called for unauthorized chat")
	}
	if len(*replies) != 0 {
		t.Errorf("expected no replies to unauthorized chat, got %d", len(*replies))
	}
}

func TestBotDispatcher_Dispatch_CommandError(t *testing.T) {
	stub := &stubRunner{err: errors.New("command failed")}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem doctor"}, stub)

	disp.Dispatch(context.Background(), 99, "xylem doctor")

	if len(*replies) != 1 {
		t.Fatalf("expected 1 error reply, got %d", len(*replies))
	}
	if !strings.Contains((*replies)[0], "command failed") {
		t.Errorf("expected error message in reply, got: %s", (*replies)[0])
	}
}

func TestBotDispatcher_Dispatch_CommandTimeout(t *testing.T) {
	stub := &stubRunner{block: true}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem status"}, stub)
	disp.timeout = 50 * time.Millisecond

	disp.Dispatch(context.Background(), 99, "xylem status")

	if len(*replies) != 1 {
		t.Fatalf("expected 1 error reply after timeout, got %d", len(*replies))
	}
	if !strings.Contains((*replies)[0], "Error") {
		t.Errorf("expected error reply after timeout, got: %s", (*replies)[0])
	}
}

func TestBotDispatcher_Dispatch_EmptyOutput(t *testing.T) {
	stub := &stubRunner{output: []byte{}}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem status"}, stub)

	disp.Dispatch(context.Background(), 99, "xylem status")

	if len(*replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(*replies))
	}
	if !strings.Contains((*replies)[0], "(no output)") {
		t.Errorf("expected '(no output)' for empty command output, got: %s", (*replies)[0])
	}
}

func TestBotDispatcher_Dispatch_LongOutput(t *testing.T) {
	longOut := strings.Repeat("x", 5000)
	stub := &stubRunner{output: []byte(longOut)}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem status"}, stub)

	disp.Dispatch(context.Background(), 99, "xylem status")

	if len(*replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(*replies))
	}
	// The raw HTML reply contains <pre> tags plus HTML-escaped content.
	// The text portion should be truncated.
	if !strings.Contains((*replies)[0], "...") {
		t.Errorf("expected truncation '...' in long output reply, got length %d", len((*replies)[0]))
	}
}

func TestBotDispatcher_Dispatch_WhitespaceOnlyIgnored(t *testing.T) {
	stub := &stubRunner{}
	disp, replies := captureDispatchReplies(t, 99, []string{"xylem status"}, stub)

	disp.Dispatch(context.Background(), 99, "   ")

	if stub.name != "" {
		t.Error("runner should NOT have been called for whitespace-only input")
	}
	if len(*replies) != 0 {
		t.Errorf("expected no reply for whitespace-only message, got %d", len(*replies))
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"xylem status", []string{"xylem", "status"}},
		{"gh pr list", []string{"gh", "pr", "list"}},
		{"  xylem  queue  list  ", []string{"xylem", "queue", "list"}},
		{"", nil},
		{"   ", nil},
	}
	for _, tt := range tests {
		got := parseCommand(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("parseCommand(%q): got %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("parseCommand(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}
