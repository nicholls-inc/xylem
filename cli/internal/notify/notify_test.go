package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// stubNotifier records calls and optionally returns errors.
type stubNotifier struct {
	name string

	mu          sync.Mutex
	statusCalls []StatusReport
	alertCalls  []Alert
	statusErr   error
	alertErr    error
}

func (s *stubNotifier) Name() string { return s.name }

func (s *stubNotifier) PostStatus(_ context.Context, report StatusReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCalls = append(s.statusCalls, report)
	return s.statusErr
}

func (s *stubNotifier) SendAlert(_ context.Context, alert Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertCalls = append(s.alertCalls, alert)
	return s.alertErr
}

func TestMulti_PostStatus_CallsAllNotifiers(t *testing.T) {
	n1 := &stubNotifier{name: "n1"}
	n2 := &stubNotifier{name: "n2"}
	m := &Multi{Notifiers: []Notifier{n1, n2}}

	report := StatusReport{Markdown: "test report"}
	err := m.PostStatus(context.Background(), report)
	if err != nil {
		t.Fatalf("PostStatus returned error: %v", err)
	}

	if len(n1.statusCalls) != 1 {
		t.Errorf("n1 expected 1 status call, got %d", len(n1.statusCalls))
	}
	if len(n2.statusCalls) != 1 {
		t.Errorf("n2 expected 1 status call, got %d", len(n2.statusCalls))
	}
	if n1.statusCalls[0].Markdown != "test report" {
		t.Errorf("n1 got wrong report: %q", n1.statusCalls[0].Markdown)
	}
}

func TestMulti_SendAlert_CallsAllNotifiers(t *testing.T) {
	n1 := &stubNotifier{name: "n1"}
	n2 := &stubNotifier{name: "n2"}
	m := &Multi{Notifiers: []Notifier{n1, n2}}

	alert := Alert{
		Severity: SeverityCritical,
		Code:     "test_alert",
		Title:    "Test",
		Detail:   "detail",
	}
	err := m.SendAlert(context.Background(), alert)
	if err != nil {
		t.Fatalf("SendAlert returned error: %v", err)
	}

	if len(n1.alertCalls) != 1 {
		t.Errorf("n1 expected 1 alert call, got %d", len(n1.alertCalls))
	}
	if len(n2.alertCalls) != 1 {
		t.Errorf("n2 expected 1 alert call, got %d", len(n2.alertCalls))
	}
	if n1.alertCalls[0].Code != "test_alert" {
		t.Errorf("n1 got wrong alert code: %q", n1.alertCalls[0].Code)
	}
}

func TestMulti_PostStatus_FailingNotifierDoesNotBlockOthers(t *testing.T) {
	failing := &stubNotifier{name: "failing", statusErr: errors.New("boom")}
	ok := &stubNotifier{name: "ok"}
	m := &Multi{Notifiers: []Notifier{failing, ok}}

	report := StatusReport{Markdown: "data"}
	err := m.PostStatus(context.Background(), report)
	if err != nil {
		t.Fatalf("PostStatus should not propagate notifier errors, got: %v", err)
	}

	if len(failing.statusCalls) != 1 {
		t.Errorf("failing notifier should still have been called, got %d calls", len(failing.statusCalls))
	}
	if len(ok.statusCalls) != 1 {
		t.Errorf("ok notifier should have been called despite prior failure, got %d calls", len(ok.statusCalls))
	}
}

func TestMulti_SendAlert_FailingNotifierDoesNotBlockOthers(t *testing.T) {
	failing := &stubNotifier{name: "failing", alertErr: errors.New("boom")}
	ok := &stubNotifier{name: "ok"}
	m := &Multi{Notifiers: []Notifier{failing, ok}}

	alert := Alert{Code: "test"}
	err := m.SendAlert(context.Background(), alert)
	if err != nil {
		t.Fatalf("SendAlert should not propagate notifier errors, got: %v", err)
	}

	if len(failing.alertCalls) != 1 {
		t.Errorf("failing notifier should still have been called, got %d calls", len(failing.alertCalls))
	}
	if len(ok.alertCalls) != 1 {
		t.Errorf("ok notifier should have been called despite prior failure, got %d calls", len(ok.alertCalls))
	}
}
