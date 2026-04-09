package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"pgregory.net/rapid"
)

type rapidFataler interface {
	Fatalf(format string, args ...any)
}

func writeSinglePhaseWorkflow(f rapidFataler, dir, workflowName string) {
	workflowDir := filepath.Join(dir, ".xylem", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		f.Fatalf("MkdirAll(workflowDir) error = %v", err)
	}

	promptPath := filepath.Join(dir, ".xylem", "prompts", workflowName, "analyze.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
		f.Fatalf("MkdirAll(promptDir) error = %v", err)
	}
	if err := os.WriteFile(promptPath, []byte("Analyze"), 0o644); err != nil {
		f.Fatalf("WriteFile(promptPath) error = %v", err)
	}

	workflowContent := fmt.Sprintf("name: %s\nphases:\n  - name: analyze\n    prompt_file: %s\n    max_turns: 5\n", workflowName, promptPath)
	if err := os.WriteFile(filepath.Join(workflowDir, workflowName+".yaml"), []byte(workflowContent), 0o644); err != nil {
		f.Fatalf("WriteFile(workflow) error = %v", err)
	}
}

func propEndedSpanByName(f rapidFataler, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	f.Fatalf("no ended span named %q", name)
	return nil
}

func newPropTestTracer() (*observability.Tracer, *tracetest.SpanRecorder, func()) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(rec),
	)
	return observability.NewTracerFromProvider(tp), rec, func() {
		_ = tp.Shutdown(context.Background())
	}
}

func TestProp_SpanHierarchyMaintainedAcrossVessels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		vesselCount := rapid.IntRange(1, 10).Draw(t, "vessel_count")
		dir, err := os.MkdirTemp("", "xylem-trace-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, vesselCount)
		cfg.StateDir = filepath.Join(dir, ".xylem")

		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		for i := 1; i <= vesselCount; i++ {
			if _, err := q.Enqueue(makeVessel(i, "trace-prop")); err != nil {
				t.Fatalf("Enqueue(%d) error = %v", i, err)
			}
		}
		writeSinglePhaseWorkflow(t, dir, "trace-prop")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd() error = %v", err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("Chdir(%q) error = %v", dir, err)
		}
		defer os.Chdir(oldWd)

		tracer, rec, cleanup := newPropTestTracer()
		defer cleanup()

		cmdRunner := &mockCmdRunner{
			phaseOutputs: map[string][]byte{
				"Analyze": []byte("analysis complete"),
			},
		}
		r := New(cfg, q, &mockWorktree{}, cmdRunner)
		r.Tracer = tracer
		r.Sources = map[string]source.Source{
			"github-issue": makeGitHubSource(),
		}

		result, err := r.DrainAndWait(context.Background())
		if err != nil {
			t.Fatalf("Drain() error = %v", err)
		}
		if result.Completed != vesselCount {
			t.Fatalf("DrainResult.Completed = %d, want %d", result.Completed, vesselCount)
		}

		drainSpan := propEndedSpanByName(t, rec.Ended(), "drain_run")
		for i := 1; i <= vesselCount; i++ {
			vesselSpan := propEndedSpanByName(t, rec.Ended(), fmt.Sprintf("vessel:issue-%d", i))
			if vesselSpan.Parent().TraceID() != drainSpan.SpanContext().TraceID() {
				t.Fatalf("vessel %d trace ID = %s, want %s", i, vesselSpan.Parent().TraceID(), drainSpan.SpanContext().TraceID())
			}
		}
	})
}

func TestProp_PhaseSpanAttributesNeverEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringMatching(`[a-z0-9-]{1,20}`).Draw(t, "name")
		index := rapid.IntRange(0, 1000).Draw(t, "index")
		provider := rapid.StringMatching(`[a-z0-9-]{0,20}`).Draw(t, "provider")
		model := rapid.StringMatching(`[a-z0-9-]{0,20}`).Draw(t, "model")
		inputTokens := rapid.IntRange(0, 1_000_000).Draw(t, "input_tokens")
		outputTokens := rapid.IntRange(0, 1_000_000).Draw(t, "output_tokens")
		costUSDEst := rapid.Float64Range(0, 100).Draw(t, "cost_usd_est")
		durationMS := rapid.Int64Range(0, 600_000).Draw(t, "duration_ms")

		attrs := append(
			observability.PhaseSpanAttributes(observability.PhaseSpanData{
				Name:     name,
				Index:    index,
				Type:     "prompt",
				Provider: provider,
				Model:    model,
			}),
			observability.PhaseResultAttributes(observability.PhaseResultData{
				InputTokensEst:  inputTokens,
				OutputTokensEst: outputTokens,
				CostUSDEst:      costUSDEst,
				DurationMS:      durationMS,
			})...,
		)

		if len(attrs) == 0 {
			t.Fatal("expected non-empty phase attributes")
		}
		for _, attr := range attrs {
			if !strings.HasPrefix(attr.Key, "xylem.phase.") {
				t.Fatalf("attribute key %q does not use xylem.phase namespace", attr.Key)
			}
		}
	})
}

func TestProp_NilTracerNeverPanicsUnderConcurrency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		vesselCount := rapid.IntRange(1, 8).Draw(t, "vessel_count")
		dir, err := os.MkdirTemp("", "xylem-nil-trace-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		cfg := makeTestConfig(dir, vesselCount)
		cfg.StateDir = filepath.Join(dir, ".xylem")

		q := queue.New(filepath.Join(dir, "queue.jsonl"))
		for i := 1; i <= vesselCount; i++ {
			if _, err := q.Enqueue(makeVessel(i, "trace-nil")); err != nil {
				t.Fatalf("Enqueue(%d) error = %v", i, err)
			}
		}
		writeSinglePhaseWorkflow(t, dir, "trace-nil")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd() error = %v", err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("Chdir(%q) error = %v", dir, err)
		}
		defer os.Chdir(oldWd)

		cmdRunner := &mockCmdRunner{
			phaseOutputs: map[string][]byte{
				"Analyze": []byte("analysis complete"),
			},
		}
		r := New(cfg, q, &mockWorktree{}, cmdRunner)
		r.Sources = map[string]source.Source{
			"github-issue": makeGitHubSource(),
		}

		result, err := r.DrainAndWait(context.Background())
		if err != nil {
			t.Fatalf("Drain() error = %v", err)
		}
		if result.Completed != vesselCount {
			t.Fatalf("DrainResult.Completed = %d, want %d", result.Completed, vesselCount)
		}
	})
}
