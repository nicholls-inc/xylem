package gate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

type fakeBrowserVerifier struct {
	verify func(ctx context.Context, worktreeDir, phaseName string, cfg *workflow.LiveBrowserGate, save artifactSaver) ([]LiveStepResult, error)
}

func (f fakeBrowserVerifier) Verify(ctx context.Context, worktreeDir, phaseName string, cfg *workflow.LiveBrowserGate, save artifactSaver) ([]LiveStepResult, error) {
	return f.verify(ctx, worktreeDir, phaseName, cfg, save)
}

func TestRunLiveGateHTTPPassesAndPersistsEvidence(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	g := &workflow.Gate{
		Type: "live",
		Live: &workflow.LiveGate{
			Mode: "http",
			HTTP: &workflow.LiveHTTPGate{
				BaseURL: server.URL,
				Steps: []workflow.LiveHTTPStep{{
					Name:            "health",
					URL:             "/health",
					ExpectStatus:    http.StatusOK,
					ExpectBodyRegex: `"status":"ok"`,
					ExpectHeaders: []workflow.LiveHeaderAssert{{
						Name:  "Content-Type",
						Regex: "application/json",
					}},
					ExpectJSON: []workflow.LiveJSONPathCheck{{
						Path:   "$.status",
						Equals: "ok",
					}},
				}},
			},
		},
	}

	verifier := NewLiveVerifier()
	verifier.HTTPClient = server.Client()

	result, err := verifier.Run(context.Background(), &mockRunner{}, LiveRequest{
		StateDir:  stateDir,
		VesselID:  "vessel-http-pass",
		PhaseName: "implement",
		Gate:      g,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; output=%q", result.Output)
	}
	if result.ReportPath == "" {
		t.Fatal("ReportPath = empty, want saved report")
	}
	if len(result.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(result.Steps))
	}
	if len(result.Steps[0].Artifacts) == 0 {
		t.Fatal("Artifacts = empty, want HTTP trace artifact")
	}

	tracePath := filepath.Join(stateDir, filepath.FromSlash(result.Steps[0].Artifacts[0].Path))
	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("ReadFile(trace) error = %v", err)
	}
	if !strings.Contains(string(traceData), "Status: 200 OK") {
		t.Fatalf("trace = %q, want response status", string(traceData))
	}
}

func TestRunLiveGateHTTPFailsOnAssertionMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	}))
	defer server.Close()

	verifier := NewLiveVerifier()
	verifier.HTTPClient = server.Client()

	result, err := verifier.Run(context.Background(), &mockRunner{}, LiveRequest{
		StateDir:  t.TempDir(),
		VesselID:  "vessel-http-fail",
		PhaseName: "implement",
		Gate: &workflow.Gate{
			Type: "live",
			Live: &workflow.LiveGate{
				Mode: "http",
				HTTP: &workflow.LiveHTTPGate{
					BaseURL: server.URL,
					Steps: []workflow.LiveHTTPStep{{
						Name:         "health",
						URL:          "/health",
						ExpectStatus: http.StatusOK,
						ExpectJSON: []workflow.LiveJSONPathCheck{{
							Path:   "$.status",
							Equals: "ok",
						}},
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if !strings.Contains(result.Output, "live gate failed") {
		t.Fatalf("Output = %q, want failure summary", result.Output)
	}
}

func TestRunLiveGateHTTPTimeoutProducesFailedStepNotSystemError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	verifier := NewLiveVerifier()
	verifier.HTTPClient = server.Client()

	result, err := verifier.Run(context.Background(), &mockRunner{}, LiveRequest{
		StateDir:  t.TempDir(),
		VesselID:  "vessel-http-timeout",
		PhaseName: "implement",
		Gate: &workflow.Gate{
			Type: "live",
			Live: &workflow.LiveGate{
				Mode:    "http",
				Timeout: "10ms",
				HTTP: &workflow.LiveHTTPGate{
					BaseURL: server.URL,
					Steps: []workflow.LiveHTTPStep{{
						Name:         "health",
						URL:          "/health",
						ExpectStatus: http.StatusOK,
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if !strings.Contains(result.Steps[0].Message, "deadline") {
		t.Fatalf("Step message = %q, want deadline context", result.Steps[0].Message)
	}
}

func TestRunLiveGateCommandAssertEvaluatesStdout(t *testing.T) {
	t.Parallel()

	result, err := NewLiveVerifier().Run(context.Background(), &mockRunner{output: []byte(`{"status":"ok"}`)}, LiveRequest{
		StateDir:  t.TempDir(),
		VesselID:  "vessel-command-assert",
		PhaseName: "implement",
		Gate: &workflow.Gate{
			Type: "live",
			Live: &workflow.LiveGate{
				Mode: "command+assert",
				CommandAssert: &workflow.LiveCommandAssertGate{
					Run: `cat status.json`,
					ExpectJSON: []workflow.LiveJSONPathCheck{{
						Path:   "$.status",
						Equals: "ok",
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; output=%q", result.Output)
	}
}

func TestRunLiveGateBrowserUsesInjectedVerifierAndPersistsArtifacts(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	verifier := &LiveVerifier{
		HTTPClient: &http.Client{},
		Browser: fakeBrowserVerifier{
			verify: func(ctx context.Context, worktreeDir, phaseName string, cfg *workflow.LiveBrowserGate, save artifactSaver) ([]LiveStepResult, error) {
				screenshot, err := save("checkpoint.screenshot.png", []byte("png"), "image/png", "checkpoint screenshot")
				if err != nil {
					return nil, err
				}
				dom, err := save("checkpoint.dom.html", []byte("<html></html>"), "text/html", "checkpoint dom")
				if err != nil {
					return nil, err
				}
				return []LiveStepResult{{
					Name:      "landing-page",
					Mode:      "browser",
					Passed:    true,
					Message:   "ok",
					Artifacts: []evidence.Artifact{screenshot, dom},
				}}, nil
			},
		},
	}

	result, err := verifier.Run(context.Background(), &mockRunner{}, LiveRequest{
		StateDir:  stateDir,
		VesselID:  "vessel-browser",
		PhaseName: "implement",
		Gate: &workflow.Gate{
			Type: "live",
			Live: &workflow.LiveGate{
				Mode: "browser",
				Browser: &workflow.LiveBrowserGate{
					Steps: []workflow.LiveBrowserStep{{
						Name:   "landing-page",
						Action: "navigate",
						URL:    "http://127.0.0.1:3000",
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; output=%q", result.Output)
	}
	if len(result.Steps) != 1 || len(result.Steps[0].Artifacts) != 2 {
		t.Fatalf("Artifacts = %#v, want two persisted browser artifacts", result.Steps)
	}
}
