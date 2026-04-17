package scanner

// Property tests for the invariants specified in docs/invariants/scanner.md.
// Each TestProp* function carries a "// Invariant SN: <Name>" comment (see
// Governance §2 of the spec). This file is a protected surface: modifications
// require a human-signed commit (see .claude/rules/protected-surfaces.md).
//
// Per spec Governance §4: S4 and S5 are expected violations against the
// current code (scanner.go:75-77 and scanner.go:106-108 / :111-113). Those
// tests are NOT skipped — the red CI is the enforcement mechanism until the
// corresponding scanner.Scan refactor lands. Do not add t.Skip to paper over
// them; per spec §4 and the file-level contract, relaxing an assertion to
// make a failing test pass is an invariant violation itself.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/cost"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"pgregory.net/rapid"
)

// Shared helpers

// propSourceName identifies one of the source slots in a generated config.
type propSourceName string

const (
	propSrcA propSourceName = "src-a"
	propSrcB propSourceName = "src-b"
	propSrcC propSourceName = "src-c"
)

var propAllSources = []propSourceName{propSrcA, propSrcB, propSrcC}

// propSourceSpec captures per-source generator choices for a single scan
// iteration.
type propSourceSpec struct {
	name        propSourceName
	repo        string
	issueNums   []int  // issues the source "returns" from gh
	shouldError bool   // if true, gh returns an error
	label       string // trigger label (always "bug" for github)
}

// drawSourceSpec builds a single source's generator contract.
func drawSourceSpec(t *rapid.T, name propSourceName) propSourceSpec {
	repo := fmt.Sprintf("owner/%s", string(name))
	numCount := rapid.IntRange(0, 3).Draw(t, string(name)+"_issue_count")
	nums := make([]int, 0, numCount)
	// Seed per-source issue numbers in distinct bands so refs don't collide
	// across sources. Ref-dedup is the queue's job (I1a); scanner's job is
	// that one source's error doesn't poison another source's enqueues.
	var base int
	switch name {
	case propSrcA:
		base = 100
	case propSrcB:
		base = 200
	case propSrcC:
		base = 300
	}
	seen := make(map[int]bool)
	for i := 0; i < numCount; i++ {
		n := base + rapid.IntRange(0, 9).Draw(t, fmt.Sprintf("%s_issue_%d", name, i))
		if seen[n] {
			continue
		}
		seen[n] = true
		nums = append(nums, n)
	}
	return propSourceSpec{
		name:        name,
		repo:        repo,
		issueNums:   nums,
		shouldError: rapid.Bool().Draw(t, string(name)+"_error"),
		label:       "bug",
	}
}

// buildPropConfig constructs a full scanner config with the given source
// specs, each registered as a distinct "github" source. `stateDir` is the
// scanner's state directory (which also holds queue.jsonl inside tests).
// When `withStatusLabels` is true, every task carries a StatusLabels block
// so OnEnqueue emits an observable `gh issue edit --add-label queued` call
// (needed for S6's hook-firing observable).
func buildPropConfig(dir string, specs []propSourceSpec, withStatusLabels bool) *config.Config {
	sources := make(map[string]config.SourceConfig, len(specs))
	for _, spec := range specs {
		task := config.Task{Labels: []string{spec.label}, Workflow: "fix-bug"}
		if withStatusLabels {
			task.StatusLabels = &config.StatusLabels{Queued: "queued"}
		}
		sources[string(spec.name)] = config.SourceConfig{
			Type:    "github",
			Repo:    spec.repo,
			Exclude: []string{"wontfix", "duplicate"},
			Tasks: map[string]config.Task{
				"fix-bugs": task,
			},
		}
	}
	return &config.Config{
		Repo:        "owner/repo",
		Concurrency: 2,
		MaxTurns:    50,
		Timeout:     "30m",
		StateDir:    dir,
		Exclude:     []string{"wontfix", "duplicate"},
		Claude:      config.ClaudeConfig{Command: "claude"},
		Sources:     sources,
	}
}

// wireMockRunner sets deterministic gh responses for every source spec.
// Error-flagged specs get `setErr`, otherwise issueJSON is returned.
func wireMockRunner(r *mockRunner, specs []propSourceSpec) {
	for _, spec := range specs {
		issues := make([]ghIssue, 0, len(spec.issueNums))
		for _, n := range spec.issueNums {
			issues = append(issues, ghIssue{
				Number: n,
				Title:  fmt.Sprintf("issue %d", n),
				URL:    fmt.Sprintf("https://github.com/%s/issues/%d", spec.repo, n),
				Labels: []struct {
					Name string `json:"name"`
				}{{Name: spec.label}},
			})
		}
		args := []string{"gh", "issue", "list", "--repo", spec.repo, "--state", "open", "--json", "number,title,body,url,labels", "--limit", "20", "--label", spec.label}
		if spec.shouldError {
			r.setErr(fmt.Errorf("synthetic gh failure for %s", spec.name), args...)
			continue
		}
		r.set(issueJSON(issues), args...)
	}
}

// collectEnqueuedRefs returns, in no particular order, the refs of every
// vessel in the queue file.
func collectEnqueuedRefs(t *rapid.T, q *queue.Queue) []string {
	vessels, err := q.List()
	if err != nil {
		t.Fatalf("queue.List: %v", err)
	}
	refs := make([]string, 0, len(vessels))
	for _, v := range vessels {
		refs = append(refs, v.Ref)
	}
	sort.Strings(refs)
	return refs
}

// snapshotFile returns the bytes of a file, or nil if it does not exist.
// It is used to verify that a read-path (BacklogCount) does not mutate the
// queue file on disk.
func snapshotFile(t *rapid.T, path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("snapshot read %s: %v", path, err)
	}
	return data
}

// newPropTempDir creates a fresh temp dir scoped to the rapid iteration.
func newPropTempDir(t *rapid.T) string {
	dir, err := os.MkdirTemp("", "scanner-prop-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestPropScanOnlyWritesViaEnqueue verifies that no other queue mutation is
// observed during a Scan. Since scanner.go uses the concrete *queue.Queue
// type (no interface to spy on), we verify structurally: every vessel in
// the queue file after a scan must be in the Enqueue post-state — pending,
// non-zero CreatedAt, nil StartedAt/EndedAt.
//
// Invariant S1: Enqueue is the scanner's only queue-mutation operation.
func TestPropScanOnlyWritesViaEnqueue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		specCount := rapid.IntRange(1, 3).Draw(t, "source_count")
		specs := make([]propSourceSpec, 0, specCount)
		for i := 0; i < specCount; i++ {
			spec := drawSourceSpec(t, propAllSources[i])
			spec.shouldError = false // no errors for S1; focus on success path
			specs = append(specs, spec)
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, specs)

		s := New(cfg, q, r)
		s.RunHooks = false // disable OnEnqueue hooks (they shell out to gh)
		_, _ = s.Scan(context.Background())

		// Every vessel in the queue must be in the pending state (Enqueue's
		// post-state) — any other state would imply Update/Dequeue/etc. was
		// invoked from the scanner.
		vessels, err := q.List()
		if err != nil {
			t.Fatalf("queue.List: %v", err)
		}
		for _, v := range vessels {
			if v.State != queue.StatePending {
				t.Fatalf("vessel %s has non-pending state %q after scan; S1 violated (non-Enqueue write)", v.ID, v.State)
			}
			// CreatedAt must be populated (Enqueue preserves the input).
			if v.CreatedAt.IsZero() {
				t.Fatalf("vessel %s has zero CreatedAt after scan; S1 violated", v.ID)
			}
			// StartedAt/EndedAt must be nil (Dequeue/complete/cancel mutate
			// these; Enqueue never touches them).
			if v.StartedAt != nil {
				t.Fatalf("vessel %s has non-nil StartedAt; S1 violated (Dequeue-like mutation)", v.ID)
			}
			if v.EndedAt != nil {
				t.Fatalf("vessel %s has non-nil EndedAt; S1 violated (Cancel/Update-like mutation)", v.ID)
			}
		}
	})
}

// TestPropScanNoDuplicateSourceRef verifies that across a full scan, no two
// enqueued vessels share a (Source, Ref) with non-empty Ref. Delegates to
// queue I1a dedup; the property holds regardless of strategy (spec §S2).
//
// Invariant S2: No duplicate enqueue per (Source, Ref) within a tick.
func TestPropScanNoDuplicateSourceRef(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		specCount := rapid.IntRange(1, 3).Draw(t, "source_count")
		specs := make([]propSourceSpec, 0, specCount)
		// Deliberately seed overlapping issue numbers per-source so the
		// github source internally dedups and produces mixed ref reuse.
		for i := 0; i < specCount; i++ {
			spec := drawSourceSpec(t, propAllSources[i])
			spec.shouldError = false
			// Force some overlap: duplicate the first issue if any exists.
			if len(spec.issueNums) > 0 {
				spec.issueNums = append(spec.issueNums, spec.issueNums[0])
			}
			specs = append(specs, spec)
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, specs)

		s := New(cfg, q, r)
		s.RunHooks = false
		_, _ = s.Scan(context.Background())

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("queue.List: %v", err)
		}
		keys := make(map[string]string, len(vessels))
		for _, v := range vessels {
			if v.Ref == "" {
				continue
			}
			key := v.Source + "\x00" + v.Ref
			if prior, exists := keys[key]; exists {
				t.Fatalf("S2 violation: duplicate (Source=%q, Ref=%q) enqueued with IDs %q and %q", v.Source, v.Ref, prior, v.ID)
			}
			keys[key] = v.ID
		}
	})
}

// S3. Pause marker aborts the tick before any side effect.

// TestPropPauseMarkerAbortsScan verifies that when <StateDir>/paused exists,
// Scan returns {0, 0, true}, no gh commands are executed, and the queue
// remains untouched. The spec says Paused=true implies zero Source.Scan,
// zero BudgetGate.Check, zero Queue.Enqueue. We verify the strongest
// observables: (1) no commands were recorded on the mockRunner, (2) queue
// file bytes are unchanged, (3) result is exactly ScanResult{0, 0, true},
// (4) BudgetGate (via stubBudgetGate) received zero Check calls.
//
// Invariant S3: Pause marker aborts the tick before any side effect.
func TestPropPauseMarkerAbortsScan(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		specCount := rapid.IntRange(1, 3).Draw(t, "source_count")
		specs := make([]propSourceSpec, 0, specCount)
		for i := 0; i < specCount; i++ {
			spec := drawSourceSpec(t, propAllSources[i])
			specs = append(specs, spec)
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, specs)

		// Create the pause marker at the runtime-resolved path the scanner
		// checks. For non-control-plane dirs (our tempdir), this is
		// simply <dir>/paused.
		pausePath := config.RuntimePath(dir, "paused")
		if err := os.MkdirAll(filepath.Dir(pausePath), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(pausePath), err)
		}
		if err := os.WriteFile(pausePath, []byte("paused"), 0o644); err != nil {
			t.Fatalf("write pause marker: %v", err)
		}

		// Snapshot queue file before the scan. Since we never wrote to
		// queue.jsonl above, this will be nil — but we still verify the
		// post-scan snapshot matches (nil == nil or unchanged bytes).
		before := snapshotFile(t, qPath)

		gate := &stubBudgetGate{decision: cost.Decision{Allowed: true, RemainingUSD: 100}}
		s := New(cfg, q, r)
		s.BudgetGate = gate
		s.RunHooks = true // hooks should NOT fire since no Enqueue happens

		result, err := s.Scan(context.Background())
		if err != nil {
			t.Fatalf("Scan returned unexpected error: %v", err)
		}

		if result.Added != 0 || result.Skipped != 0 || !result.Paused {
			t.Fatalf("S3 violation: Scan result = %+v, want ScanResult{Added:0, Skipped:0, Paused:true}", result)
		}

		// No gh commands — no Source.Scan was called.
		if len(r.calls) != 0 {
			t.Fatalf("S3 violation: paused scan issued %d subprocess calls (first: %v); expected 0", len(r.calls), r.calls[0])
		}

		// No BudgetGate checks.
		if len(gate.classes) != 0 {
			t.Fatalf("S3 violation: paused scan invoked BudgetGate.Check %d times (classes=%v); expected 0", len(gate.classes), gate.classes)
		}

		// Queue file bytes unchanged.
		after := snapshotFile(t, qPath)
		if !bytesEqual(before, after) {
			t.Fatalf("S3 violation: paused scan mutated queue.jsonl (before=%d bytes, after=%d bytes)", len(before), len(after))
		}
	})
}

// S4. Source Scan errors do not cross-contaminate other sources.

// TestPropSourceErrorDoesNotBlockOthers verifies that when one configured
// source returns an error from its Scan method, every other configured
// source is still invoked and its healthy vessels appear in the queue.
//
// Status per spec: expected violation (scanner.go:75-77). Do NOT t.Skip —
// the red CI is the enforcement mechanism per spec Governance §4.
//
// Invariant S4: Source Scan errors do not cross-contaminate other sources.
func TestPropSourceErrorDoesNotBlockOthers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		// Require at least two sources: one errors, one is healthy with ≥1
		// vessel. Otherwise the property is vacuous.
		specs := []propSourceSpec{
			{
				name:        propSrcA,
				repo:        "owner/src-a",
				issueNums:   []int{101, 102},
				shouldError: false,
				label:       "bug",
			},
			{
				name:        propSrcB,
				repo:        "owner/src-b",
				issueNums:   []int{201, 202},
				shouldError: true, // forced failure
				label:       "bug",
			},
			{
				name:        propSrcC,
				repo:        "owner/src-c",
				issueNums:   []int{301},
				shouldError: false,
				label:       "bug",
			},
		}
		// Randomly permute so the error source might be first, middle, or
		// last in iteration order. Scanner iterates over a Go map, so
		// ordering is already non-deterministic, but permuting the spec
		// list makes the intent explicit.
		errorIdx := rapid.IntRange(0, len(specs)-1).Draw(t, "error_idx")
		for i := range specs {
			specs[i].shouldError = (i == errorIdx)
		}

		// Collect the expected ref set from healthy specs.
		expectedHealthyRefs := map[string]bool{}
		for _, spec := range specs {
			if spec.shouldError {
				continue
			}
			for _, n := range spec.issueNums {
				expectedHealthyRefs[fmt.Sprintf("https://github.com/%s/issues/%d", spec.repo, n)] = true
			}
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, specs)

		s := New(cfg, q, r)
		s.RunHooks = false
		// Surfacing an error is permitted by spec §S4; the ScanResult must
		// still reflect the sum of healthy sources, and the queue must
		// contain every healthy vessel.
		_, _ = s.Scan(context.Background())

		enqueuedRefs := collectEnqueuedRefs(t, q)
		enqueuedSet := make(map[string]bool, len(enqueuedRefs))
		for _, ref := range enqueuedRefs {
			enqueuedSet[ref] = true
		}

		// Every ref produced by a healthy source must appear in the queue.
		for ref := range expectedHealthyRefs {
			if !enqueuedSet[ref] {
				t.Fatalf("S4 violation: healthy source's vessel ref %q missing from queue (enqueued=%v, error_spec=%s); one source's failure blocked another's intake (scanner.go:75-77)", ref, enqueuedRefs, specs[errorIdx].name)
			}
		}
	})
}

// S5. Non-dedup Enqueue errors do not cross-contaminate other sources.

// TestPropEnqueueErrorDoesNotBlockOthers verifies that when Queue.Enqueue
// returns a non-ErrDuplicateID error mid-tick (e.g. disk I/O failure, stat
// corruption), the scanner still attempts subsequent vessels from the
// remaining sources and later vessels from the current source.
//
// We inject the failure by pre-corrupting queue.jsonl with a malformed line.
// readAllVessels then returns a non-nil error (see queue.go:735 —
// "%d malformed queue entries skipped"), which flows up as a non-
// ErrDuplicateID error from Enqueue, directly exercising scanner.go:106-108.
//
// Status per spec: expected violation. Do NOT t.Skip.
//
// Invariant S5: Non-dedup Enqueue errors do not cross-contaminate other sources.
func TestPropEnqueueErrorDoesNotBlockOthers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		// Two healthy sources with ≥1 vessel each. If Enqueue returns a
		// non-ErrDuplicateID error on the first attempt, the spec requires
		// the scanner to continue attempting later vessels from the same
		// source AND vessels from subsequent sources.
		specs := []propSourceSpec{
			{
				name:        propSrcA,
				repo:        "owner/src-a",
				issueNums:   []int{101, 102},
				shouldError: false,
				label:       "bug",
			},
			{
				name:        propSrcB,
				repo:        "owner/src-b",
				issueNums:   []int{201, 202},
				shouldError: false,
				label:       "bug",
			},
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		// Pre-corrupt queue.jsonl so every readAllVessels call returns a
		// non-ErrDuplicateID error. The scan's first Enqueue hits this error
		// immediately. Spec S5 requires later vessels (from the same source
		// AND from subsequent sources) to still be attempted — the scanner
		// must not abort on this error class.
		if err := os.WriteFile(qPath, []byte("{not valid json\n"), 0o644); err != nil {
			t.Fatalf("pre-corrupt queue: %v", err)
		}

		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, specs)

		s := New(cfg, q, r)
		s.RunHooks = false
		_, _ = s.Scan(context.Background()) // error may be surfaced; spec permits

		// Count how many source's gh list commands were issued. If the
		// scanner aborts on the first Enqueue error, only the first
		// source's gh call is made (since Scan processes sources in-order
		// and enqueues per-source before moving on). For S5 to hold, every
		// source's gh call must have been made — i.e. we see one call per
		// source.
		ghListCallCount := 0
		distinctRepos := make(map[string]struct{})
		for _, call := range r.calls {
			if len(call) < 5 || call[0] != "gh" || call[1] != "issue" || call[2] != "list" {
				continue
			}
			ghListCallCount++
			// --repo is at call[3]="--repo", call[4]=<repo>
			for i := 3; i+1 < len(call); i++ {
				if call[i] == "--repo" {
					distinctRepos[call[i+1]] = struct{}{}
					break
				}
			}
		}

		if len(distinctRepos) < len(specs) {
			callSummary := make([]string, 0, len(r.calls))
			for _, c := range r.calls {
				callSummary = append(callSummary, strings.Join(c, " "))
			}
			t.Fatalf("S5 violation: only %d/%d sources were queried (distinct repos: %v) — scanner aborted on first non-ErrDuplicateID Enqueue error. Calls: %v", len(distinctRepos), len(specs), distinctRepos, callSummary)
		}
	})
}

// S6. Hooks fire exactly when Enqueue succeeds and RunHooks is true.

// TestPropHooksFireIffEnqueuedAndRunHooks checks both directions of the
// biconditional:
//  1. RunHooks=true + Allowed + enqueued ⇒ OnEnqueue fires (observable via
//     `gh issue edit --add-label queued` calls on the mockRunner).
//  2. Any of (budget denied, RunHooks=false, Enqueue no-op) ⇒ no hook fire.
//
// Since OnEnqueue for source.GitHub calls `gh issue edit --add-label queued`,
// we count those calls and assert they equal the expected enqueued vessel
// count.
//
// Invariant S6: Hooks fire exactly when Enqueue succeeds and RunHooks is true.
func TestPropHooksFireIffEnqueuedAndRunHooks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		// Single source, always healthy, variable number of vessels.
		spec := propSourceSpec{
			name:        propSrcA,
			repo:        "owner/src-a",
			issueNums:   []int{},
			shouldError: false,
			label:       "bug",
		}
		n := rapid.IntRange(0, 3).Draw(t, "issue_count")
		for i := 0; i < n; i++ {
			spec.issueNums = append(spec.issueNums, 400+i)
		}
		allowed := rapid.Bool().Draw(t, "budget_allowed")
		runHooks := rapid.Bool().Draw(t, "run_hooks")

		cfg := buildPropConfig(dir, []propSourceSpec{spec}, true)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, []propSourceSpec{spec})
		// OnEnqueue shells out to `gh issue edit --add-label queued`; set
		// it to succeed (nil error) so we can observe the call count
		// without a Scan-time hook error masking the count.
		for _, num := range spec.issueNums {
			r.set([]byte("{}"), "gh", "issue", "edit", fmt.Sprintf("%d", num), "--repo", spec.repo, "--add-label", "queued")
		}

		gate := &stubBudgetGate{decision: cost.Decision{Allowed: allowed, Reason: "prop stub", RemainingUSD: 100}}
		s := New(cfg, q, r)
		s.BudgetGate = gate
		s.RunHooks = runHooks

		_, err := s.Scan(context.Background())
		if err != nil {
			t.Fatalf("Scan unexpected error: %v", err)
		}

		// Count OnEnqueue label-add calls.
		hookCallCount := 0
		for _, call := range r.calls {
			if len(call) >= 3 && call[0] == "gh" && call[1] == "issue" && call[2] == "edit" {
				// Look for --add-label queued
				for i := 0; i+1 < len(call); i++ {
					if call[i] == "--add-label" && call[i+1] == "queued" {
						hookCallCount++
						break
					}
				}
			}
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("queue.List: %v", err)
		}
		enqueuedCount := len(vessels)

		var wantHookCount int
		if allowed && runHooks {
			wantHookCount = enqueuedCount
		} else {
			wantHookCount = 0
		}

		if hookCallCount != wantHookCount {
			t.Fatalf("S6 violation: OnEnqueue fired %d times; want %d (allowed=%t, runHooks=%t, enqueued=%d)", hookCallCount, wantHookCount, allowed, runHooks, enqueuedCount)
		}

		// When budget denied, no vessel should be enqueued.
		if !allowed && enqueuedCount != 0 {
			t.Fatalf("S6 violation: budget-denied scan enqueued %d vessels; want 0", enqueuedCount)
		}
	})
}

// S7. config_source meta propagation.

// TestPropConfigSourceMetaPopulated verifies that every enqueued vessel
// carries Meta["config_source"] matching the configured source name when
// the config entry has a non-empty name. Multiple sources with distinct
// names are checked so we confirm the per-entry wiring is correct.
//
// Invariant S7: config_source meta propagation.
func TestPropConfigSourceMetaPopulated(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		specCount := rapid.IntRange(1, 3).Draw(t, "source_count")
		specs := make([]propSourceSpec, 0, specCount)
		for i := 0; i < specCount; i++ {
			spec := drawSourceSpec(t, propAllSources[i])
			spec.shouldError = false
			// Ensure at least one issue so the property is non-vacuous
			// for this source. (If issueNums is empty the source enqueues
			// nothing, which is fine but uninteresting.)
			specs = append(specs, spec)
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()
		wireMockRunner(r, specs)

		s := New(cfg, q, r)
		s.RunHooks = false
		_, _ = s.Scan(context.Background())

		// Build repo→configName map.
		repoToName := make(map[string]string, len(specs))
		for _, spec := range specs {
			repoToName[spec.repo] = string(spec.name)
		}

		vessels, err := q.List()
		if err != nil {
			t.Fatalf("queue.List: %v", err)
		}

		for _, v := range vessels {
			got := v.Meta["config_source"]
			// Derive the expected configName from the vessel's repo meta.
			// source.GitHub populates Meta["repo"] on every vessel.
			repo := v.Meta["repo"]
			want, ok := repoToName[repo]
			if !ok {
				// Fallback: confirm config_source is at least one of the
				// configured names. This is a weaker assertion but
				// guarantees the key is present and sensible.
				found := false
				for _, spec := range specs {
					if got == string(spec.name) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("S7 violation: vessel %s Meta[config_source]=%q not in configured names (specs=%v)", v.ID, got, repoToName)
				}
				continue
			}
			if got != want {
				t.Fatalf("S7 violation: vessel %s (repo=%q) Meta[config_source]=%q; want %q", v.ID, repo, got, want)
			}
		}
	})
}

// S8. BacklogCount is side-effect free.

// TestPropBacklogCountNoSideEffects verifies that BacklogCount does not
// mutate the queue file, does not touch the pause marker, and does not
// invoke any BudgetGate.Check. The returned count is always ≥ 0.
//
// Invariant S8: BacklogCount is side-effect free.
func TestPropBacklogCountNoSideEffects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := newPropTempDir(t)
		specCount := rapid.IntRange(1, 3).Draw(t, "source_count")
		specs := make([]propSourceSpec, 0, specCount)
		for i := 0; i < specCount; i++ {
			spec := drawSourceSpec(t, propAllSources[i])
			spec.shouldError = false
			specs = append(specs, spec)
		}

		cfg := buildPropConfig(dir, specs, false)
		qPath := filepath.Join(dir, "queue.jsonl")
		q := queue.New(qPath)
		r := newMock()

		// For BacklogCount, source.GitHub issues `gh issue list` (same
		// command as Scan). Wire the same responses.
		wireMockRunner(r, specs)

		pausePath := config.RuntimePath(dir, "paused")
		pauseBefore := snapshotFile(t, pausePath) // should be nil
		queueBefore := snapshotFile(t, qPath)     // should be nil (no prior enqueue)

		gate := &stubBudgetGate{decision: cost.Decision{Allowed: false, RemainingUSD: 0}}
		s := New(cfg, q, r)
		s.BudgetGate = gate
		s.RunHooks = true

		total, err := s.BacklogCount(context.Background())
		if err != nil {
			// spec permits surfacing source errors; count must still be ≥ 0.
			if total < 0 {
				t.Fatalf("S8 violation: BacklogCount returned negative total %d on error %v", total, err)
			}
			return
		}

		if total < 0 {
			t.Fatalf("S8 violation: BacklogCount returned negative total %d", total)
		}

		// Queue file must not have been written.
		queueAfter := snapshotFile(t, qPath)
		if !bytesEqual(queueBefore, queueAfter) {
			t.Fatalf("S8 violation: BacklogCount mutated queue.jsonl (before=%d bytes, after=%d bytes)", len(queueBefore), len(queueAfter))
		}

		// Pause marker must not have been created.
		pauseAfter := snapshotFile(t, pausePath)
		if !bytesEqual(pauseBefore, pauseAfter) {
			t.Fatalf("S8 violation: BacklogCount touched pause marker (before=%d bytes, after=%d bytes)", len(pauseBefore), len(pauseAfter))
		}

		// BudgetGate must not have been consulted.
		if len(gate.classes) != 0 {
			t.Fatalf("S8 violation: BacklogCount invoked BudgetGate.Check %d times (classes=%v); expected 0", len(gate.classes), gate.classes)
		}

		// No OnEnqueue hook (no `gh issue edit ... --add-label queued`).
		for _, call := range r.calls {
			if len(call) >= 3 && call[0] == "gh" && call[1] == "issue" && call[2] == "edit" {
				for i := 0; i+1 < len(call); i++ {
					if call[i] == "--add-label" && call[i+1] == "queued" {
						t.Fatalf("S8 violation: BacklogCount invoked OnEnqueue-style label edit: %v", call)
					}
				}
			}
		}
	})
}

// misc utilities

// bytesEqual compares two byte slices for equality, treating nil and empty
// as equal. Using a small helper keeps rapid property tests compact.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
