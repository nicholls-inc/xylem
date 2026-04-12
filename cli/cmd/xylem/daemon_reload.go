package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/observability"
	"github.com/nicholls-inc/xylem/cli/internal/policy"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
	"github.com/nicholls-inc/xylem/cli/internal/scanner"
	"github.com/nicholls-inc/xylem/cli/internal/source"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
	"github.com/spf13/cobra"
)

const daemonReloadDebounce = 30 * time.Second

type daemonReloadRequest struct {
	Trigger        string    `json:"trigger"`
	Rollback       bool      `json:"rollback,omitempty"`
	PRNumber       int       `json:"pr_number,omitempty"`
	MergeCommitSHA string    `json:"merge_commit_sha,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
}

type daemonReloadLogEntry struct {
	Trigger        string    `json:"trigger"`
	Result         string    `json:"result"`
	BeforeDigest   string    `json:"before_digest,omitempty"`
	AfterDigest    string    `json:"after_digest,omitempty"`
	PRNumber       int       `json:"pr_number,omitempty"`
	MergeCommitSHA string    `json:"merge_commit_sha,omitempty"`
	Rollback       bool      `json:"rollback,omitempty"`
	Error          string    `json:"error,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

type daemonControlPlaneSnapshot struct {
	Digest     string                           `json:"digest"`
	CapturedAt time.Time                        `json:"captured_at"`
	Files      []daemonControlPlaneSnapshotFile `json:"files"`
}

type daemonControlPlaneSnapshotFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type daemonRunnerHandle struct {
	cfg      *config.Config
	scanCmd  *realCmdRunner
	drain    *runner.Runner
	cleanup  func()
	snapshot daemonControlPlaneSnapshot
}

type daemonRuntime struct {
	rootDir    string
	configPath string
	q          *queue.Queue
	wt         *worktree.Manager

	mu            sync.Mutex
	current       *daemonRunnerHandle
	retired       []*daemonRunnerHandle
	pending       *daemonReloadRequest
	mergeDebounce time.Duration
}

func newDaemonReloadCmd() *cobra.Command {
	var rollback bool
	cmd := &cobra.Command{
		Use:   "reload",
		Short: "Request a live daemon reload",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdDaemonReload(deps.cfg, rollback, signalProcessFromPIDFile)
		},
	}
	cmd.Flags().BoolVar(&rollback, "rollback", false, "Restore the previously active daemon control-plane snapshot")
	return cmd
}

func cmdDaemonReload(cfg *config.Config, rollback bool, signaler daemonProcessSignaler) error {
	req := daemonReloadRequest{
		Trigger:     "cli",
		Rollback:    rollback,
		RequestedAt: daemonNow().UTC(),
	}
	if rollback {
		req.Trigger = "rollback"
	}
	if err := writeDaemonReloadRequest(cfg, req); err != nil {
		return err
	}
	pid, signalled, err := signaler(daemonPIDPath(cfg), syscall.SIGHUP)
	if err != nil {
		_ = clearDaemonReloadRequest(cfg)
		return fmt.Errorf("signal daemon reload: %w", err)
	}
	if !signalled {
		_ = clearDaemonReloadRequest(cfg)
		return fmt.Errorf("daemon not running")
	}
	if rollback {
		fmt.Printf("Requested daemon rollback via pid %d.\n", pid)
	} else {
		fmt.Printf("Requested daemon reload via pid %d.\n", pid)
	}
	return nil
}

func newDaemonRuntime(rootDir, configPath string, q *queue.Queue, wt *worktree.Manager, cfg *config.Config) (*daemonRuntime, error) {
	snapshot, err := captureDaemonControlPlaneSnapshot(rootDir)
	if err != nil {
		return nil, fmt.Errorf("capture daemon control-plane snapshot: %w", err)
	}
	handle, err := buildDaemonRunnerHandle(cfg, q, wt, snapshot)
	if err != nil {
		return nil, err
	}
	rt := &daemonRuntime{
		rootDir:       rootDir,
		configPath:    configPath,
		q:             q,
		wt:            wt,
		current:       handle,
		mergeDebounce: daemonReloadDebounce,
	}
	if err := saveDaemonControlPlaneSnapshot(daemonCurrentSnapshotPath(cfg), snapshot); err != nil {
		handle.cleanup()
		return nil, err
	}
	return rt, nil
}

func (r *daemonRuntime) request(req daemonReloadRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.RequestedAt.IsZero() {
		req.RequestedAt = daemonNow().UTC()
	}
	if req.Trigger == "merge" && r.pending != nil && r.pending.Trigger == "merge" {
		*r.pending = req
		return
	}
	r.pending = &req
}

func (r *daemonRuntime) requestControlPlaneMerge(event source.ControlPlaneMergeEvent) {
	r.request(daemonReloadRequest{
		Trigger:        "merge",
		PRNumber:       event.PRNumber,
		MergeCommitSHA: event.MergeCommitSHA,
		RequestedAt:    daemonNow().UTC(),
	})
}

func (r *daemonRuntime) scan(ctx context.Context) (scanner.ScanResult, error) {
	if err := r.processPendingReload(ctx); err != nil {
		slog.Warn("daemon reload failed", "error", err)
	}
	handle := r.currentHandle()
	s := scanner.New(handle.cfg, r.q, handle.scanCmd)
	s.OnControlPlaneMerge = r.requestControlPlaneMerge
	return s.Scan(ctx)
}

func (r *daemonRuntime) backlogCount(ctx context.Context) (int, error) {
	handle := r.currentHandle()
	s := scanner.New(handle.cfg, r.q, handle.scanCmd)
	s.OnControlPlaneMerge = r.requestControlPlaneMerge
	return s.BacklogCount(ctx)
}

func (r *daemonRuntime) drainOnce(ctx context.Context) (runner.DrainResult, error) {
	handle := r.currentHandle()
	scanInterval, drainInterval := parseDaemonIntervals(handle.cfg.Daemon)
	_ = scanInterval
	handle.drain.DrainBudget = drainInterval
	return handle.drain.Drain(ctx)
}

func (r *daemonRuntime) runChecks(ctx context.Context, autoMerge bool) []runner.StallFinding {
	if err := r.processPendingReload(ctx); err != nil {
		slog.Warn("daemon reload failed", "error", err)
	}
	handles := r.allHandles()
	var findings []runner.StallFinding
	for _, handle := range handles {
		findings = append(findings, handle.drain.CheckStalledVessels(ctx)...)
		handle.drain.CheckWaitingVessels(ctx)
		handle.drain.CheckHungVessels(ctx)
	}
	current := r.currentHandle()
	if removed := current.drain.PruneStaleWorktrees(ctx); removed > 0 {
		slog.Info("daemon pruned stale worktrees", "removed", removed)
	}
	current.drain.CancelStalePRVessels(ctx)
	if autoMerge {
		autoMergeXylemPRs(ctx, current.cfg.Daemon)
	}
	r.cleanupRetired()
	return findings
}

func (r *daemonRuntime) Wait() runner.DrainResult {
	for _, handle := range r.allHandles() {
		handle.drain.Wait()
	}
	return runner.DrainResult{}
}

func (r *daemonRuntime) InFlightCount() int {
	total := 0
	for _, handle := range r.allHandles() {
		total += handle.drain.InFlightCount()
	}
	return total
}

func (r *daemonRuntime) intervals() (time.Duration, time.Duration) {
	handle := r.currentHandle()
	return parseDaemonIntervals(handle.cfg.Daemon)
}

func (r *daemonRuntime) currentConfig() *config.Config {
	return r.currentHandle().cfg
}

func (r *daemonRuntime) currentHandle() *daemonRunnerHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func (r *daemonRuntime) allHandles() []*daemonRunnerHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	handles := make([]*daemonRunnerHandle, 0, 1+len(r.retired))
	handles = append(handles, r.current)
	handles = append(handles, r.retired...)
	return handles
}

func (r *daemonRuntime) processPendingReload(ctx context.Context) error {
	r.mu.Lock()
	req := r.pending
	if req == nil {
		r.mu.Unlock()
		return nil
	}
	if req.Trigger == "merge" && daemonNow().Sub(req.RequestedAt) < r.mergeDebounce {
		r.mu.Unlock()
		return nil
	}
	r.pending = nil
	r.mu.Unlock()
	return r.applyReload(ctx, *req)
}

func (r *daemonRuntime) triggerSignalReload() error {
	req, ok, err := readDaemonReloadRequest(r.currentHandle().cfg)
	if err != nil {
		return err
	}
	if ok {
		if err := clearDaemonReloadRequest(r.currentHandle().cfg); err != nil {
			return err
		}
		r.request(req)
		return nil
	}
	r.request(daemonReloadRequest{
		Trigger:     "signal",
		RequestedAt: daemonNow().UTC(),
	})
	return nil
}

func (r *daemonRuntime) applyReload(ctx context.Context, req daemonReloadRequest) error {
	before := r.currentHandle()
	beforeDigest := before.snapshot.Digest
	opDecision := policy.Evaluate(policy.Ops, policy.OpReloadDaemon)
	result := "applied"
	afterDigest := beforeDigest
	var reloadErr error
	var nextSnapshot daemonControlPlaneSnapshot
	var nextCfg *config.Config

	if !opDecision.Allowed {
		result = "denied"
		reloadErr = fmt.Errorf("reload daemon denied by policy %s", opDecision.Rule)
	} else if req.Rollback {
		nextSnapshot, nextCfg, reloadErr = r.loadRollbackCandidate()
		if reloadErr == nil {
			afterDigest = nextSnapshot.Digest
		}
		result = "rolled_back"
	} else {
		nextSnapshot, nextCfg, reloadErr = r.loadCurrentCandidate()
		if reloadErr == nil {
			afterDigest = nextSnapshot.Digest
		}
	}
	if reloadErr != nil {
		result = "rejected"
	}

	handle := before
	if reloadErr == nil {
		if err := loadDaemonStartupEnv(r.rootDir); err != nil {
			reloadErr = fmt.Errorf("reload daemon startup env: %w", err)
			result = "rejected"
		}
	}
	if reloadErr == nil {
		handle, reloadErr = buildDaemonRunnerHandle(nextCfg, r.q, r.wt, nextSnapshot)
		if reloadErr != nil {
			result = "rejected"
		}
	}

	if reloadErr == nil {
		swapErr := r.q.WithWriteLock(func() error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.current != nil {
				r.retired = append(r.retired, r.current)
			}
			r.current = handle
			if err := saveDaemonControlPlaneSnapshot(daemonRollbackSnapshotPath(nextCfg), before.snapshot); err != nil {
				return err
			}
			return saveDaemonControlPlaneSnapshot(daemonCurrentSnapshotPath(nextCfg), nextSnapshot)
		})
		if swapErr != nil {
			handle.cleanup()
			reloadErr = fmt.Errorf("swap daemon runtime: %w", swapErr)
			result = "rejected"
		}
	}

	if err := appendDaemonReloadAudit(r.currentHandle().cfg, req, result, beforeDigest, afterDigest, reloadErr); err != nil {
		slog.Warn("daemon reload audit failed", "error", err)
	}
	if err := appendDaemonReloadLog(r.currentHandle().cfg, daemonReloadLogEntry{
		Trigger:        req.Trigger,
		Result:         result,
		BeforeDigest:   beforeDigest,
		AfterDigest:    afterDigest,
		PRNumber:       req.PRNumber,
		MergeCommitSHA: req.MergeCommitSHA,
		Rollback:       req.Rollback,
		Error:          errorString(reloadErr),
		Timestamp:      daemonNow().UTC(),
	}); err != nil {
		slog.Warn("daemon reload log failed", "error", err)
	}
	recordDaemonReloadSpan(ctx, r.currentHandle().drain.Tracer, req, result, beforeDigest, afterDigest, reloadErr)
	if reloadErr != nil {
		return reloadErr
	}
	r.cleanupRetired()
	return nil
}

func (r *daemonRuntime) loadCurrentCandidate() (daemonControlPlaneSnapshot, *config.Config, error) {
	snapshot, err := captureDaemonControlPlaneSnapshot(r.rootDir)
	if err != nil {
		return daemonControlPlaneSnapshot{}, nil, err
	}
	cfg, err := validateDaemonReloadCandidate(r.rootDir, r.configPath)
	if err != nil {
		return daemonControlPlaneSnapshot{}, nil, err
	}
	return snapshot, cfg, nil
}

func (r *daemonRuntime) loadRollbackCandidate() (daemonControlPlaneSnapshot, *config.Config, error) {
	snapshot, err := loadDaemonControlPlaneSnapshot(daemonRollbackSnapshotPath(r.currentHandle().cfg))
	if err != nil {
		return daemonControlPlaneSnapshot{}, nil, fmt.Errorf("load rollback snapshot: %w", err)
	}
	currentSnapshot, err := captureDaemonControlPlaneSnapshot(r.rootDir)
	if err != nil {
		return daemonControlPlaneSnapshot{}, nil, fmt.Errorf("capture current snapshot before rollback: %w", err)
	}
	if err := restoreDaemonControlPlaneSnapshot(r.rootDir, snapshot); err != nil {
		return daemonControlPlaneSnapshot{}, nil, fmt.Errorf("restore rollback snapshot: %w", err)
	}
	cfg, cfgErr := validateDaemonReloadCandidate(r.rootDir, r.configPath)
	if cfgErr != nil {
		if restoreErr := restoreDaemonControlPlaneSnapshot(r.rootDir, currentSnapshot); restoreErr != nil {
			return daemonControlPlaneSnapshot{}, nil, fmt.Errorf("restore rollback snapshot: %w (restore current: %v)", cfgErr, restoreErr)
		}
		return daemonControlPlaneSnapshot{}, nil, cfgErr
	}
	return snapshot, cfg, nil
}

func (r *daemonRuntime) cleanupRetired() {
	r.mu.Lock()
	var active []*daemonRunnerHandle
	var idle []*daemonRunnerHandle
	for _, handle := range r.retired {
		if handle.drain.InFlightCount() == 0 {
			idle = append(idle, handle)
			continue
		}
		active = append(active, handle)
	}
	r.retired = active
	r.mu.Unlock()

	for _, handle := range idle {
		handle.drain.Wait()
		handle.cleanup()
	}
}

func buildDaemonRunnerHandle(cfg *config.Config, q *queue.Queue, wt *worktree.Manager, snapshot daemonControlPlaneSnapshot) (*daemonRunnerHandle, error) {
	cmdRunner := newCmdRunner(cfg)
	drainRunner, cleanup := buildDrainRunner(cfg, q, wt, cmdRunner)
	drainRunner.Reporter = buildReporter(cfg, cmdRunner)
	_, drainInterval := parseDaemonIntervals(cfg.Daemon)
	drainRunner.DrainBudget = drainInterval
	return &daemonRunnerHandle{
		cfg:      cfg,
		scanCmd:  newCmdRunner(cfg),
		drain:    drainRunner,
		cleanup:  cleanup,
		snapshot: snapshot,
	}, nil
}

func validateDaemonReloadCandidate(rootDir, configPath string) (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	workflowsDir := filepath.Join(rootDir, ".xylem", "workflows")
	if _, err := validateWorkflowDir(workflowsDir, nil); err != nil {
		return nil, fmt.Errorf("validate workflows: %w", err)
	}
	return cfg, nil
}

func daemonReloadRequestPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-reload-request.json")
}

func daemonReloadLogPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-reload-log.jsonl")
}

func daemonReloadAuditPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-reload-audit.jsonl")
}

func daemonCurrentSnapshotPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-control-plane-current.json")
}

func daemonRollbackSnapshotPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-control-plane-rollback.json")
}

func writeDaemonReloadRequest(cfg *config.Config, req daemonReloadRequest) error {
	if err := os.MkdirAll(filepath.Dir(daemonReloadRequestPath(cfg)), 0o755); err != nil {
		return fmt.Errorf("write daemon reload request: create state dir: %w", err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("write daemon reload request: marshal: %w", err)
	}
	if err := os.WriteFile(daemonReloadRequestPath(cfg), data, 0o644); err != nil {
		return fmt.Errorf("write daemon reload request: %w", err)
	}
	return nil
}

func readDaemonReloadRequest(cfg *config.Config) (daemonReloadRequest, bool, error) {
	data, err := os.ReadFile(daemonReloadRequestPath(cfg))
	if err != nil {
		if errorsIsNotExist(err) {
			return daemonReloadRequest{}, false, nil
		}
		return daemonReloadRequest{}, false, fmt.Errorf("read daemon reload request: %w", err)
	}
	var req daemonReloadRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return daemonReloadRequest{}, false, fmt.Errorf("read daemon reload request: decode: %w", err)
	}
	return req, true, nil
}

func clearDaemonReloadRequest(cfg *config.Config) error {
	if err := os.Remove(daemonReloadRequestPath(cfg)); err != nil && !errorsIsNotExist(err) {
		return fmt.Errorf("clear daemon reload request: %w", err)
	}
	return nil
}

func appendDaemonReloadLog(cfg *config.Config, entry daemonReloadLogEntry) error {
	if err := os.MkdirAll(filepath.Dir(daemonReloadLogPath(cfg)), 0o755); err != nil {
		return fmt.Errorf("append daemon reload log: create state dir: %w", err)
	}
	f, err := os.OpenFile(daemonReloadLogPath(cfg), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append daemon reload log: open: %w", err)
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("append daemon reload log: marshal: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append daemon reload log: write: %w", err)
	}
	return nil
}

func appendDaemonReloadAudit(cfg *config.Config, req daemonReloadRequest, result, beforeDigest, afterDigest string, reloadErr error) error {
	audit := intermediary.NewAuditLog(daemonReloadAuditPath(cfg))
	decision := intermediary.Allow
	if reloadErr != nil {
		decision = intermediary.Deny
	}
	return audit.Append(intermediary.AuditEntry{
		Intent: intermediary.Intent{
			Action:        string(policy.OpReloadDaemon),
			Resource:      "daemon",
			AgentID:       "daemon",
			Justification: req.Trigger,
			Metadata: map[string]string{
				"result":           result,
				"before_digest":    beforeDigest,
				"after_digest":     afterDigest,
				"rollback":         strconv.FormatBool(req.Rollback),
				"pr_number":        strconv.Itoa(req.PRNumber),
				"merge_commit_sha": req.MergeCommitSHA,
			},
		},
		Decision:  decision,
		Timestamp: daemonNow().UTC(),
		Error:     errorString(reloadErr),
	})
}

func recordDaemonReloadSpan(ctx context.Context, tracer *observability.Tracer, req daemonReloadRequest, result, beforeDigest, afterDigest string, err error) {
	if tracer == nil {
		return
	}
	span := tracer.StartSpan(ctx, "daemon.reload.applied", []observability.SpanAttribute{
		{Key: "reload.trigger", Value: req.Trigger},
		{Key: "reload.result", Value: result},
		{Key: "reload.before_digest", Value: beforeDigest},
		{Key: "reload.after_digest", Value: afterDigest},
		{Key: "reload.pr_number", Value: strconv.Itoa(req.PRNumber)},
		{Key: "reload.merge_commit_sha", Value: req.MergeCommitSHA},
		{Key: "reload.rollback", Value: strconv.FormatBool(req.Rollback)},
	})
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}

func captureDaemonControlPlaneSnapshot(rootDir string) (daemonControlPlaneSnapshot, error) {
	paths, err := daemonControlPlaneFiles(rootDir)
	if err != nil {
		return daemonControlPlaneSnapshot{}, err
	}
	files := make([]daemonControlPlaneSnapshotFile, 0, len(paths))
	hasher := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(rootDir, path))
		if err != nil {
			return daemonControlPlaneSnapshot{}, fmt.Errorf("read control-plane file %q: %w", path, err)
		}
		files = append(files, daemonControlPlaneSnapshotFile{Path: path, Data: data})
		_, _ = hasher.Write([]byte(path))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(data)
		_, _ = hasher.Write([]byte{0})
	}
	return daemonControlPlaneSnapshot{
		Digest:     fmt.Sprintf("cp-%x", hasher.Sum(nil)),
		CapturedAt: daemonNow().UTC(),
		Files:      files,
	}, nil
}

func restoreDaemonControlPlaneSnapshot(rootDir string, snapshot daemonControlPlaneSnapshot) error {
	existing, err := daemonControlPlaneFiles(rootDir)
	if err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(snapshot.Files))
	for _, file := range snapshot.Files {
		keep[file.Path] = struct{}{}
		fullPath := filepath.Join(rootDir, file.Path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return fmt.Errorf("restore control-plane snapshot: create dir for %q: %w", file.Path, err)
		}
		if err := os.WriteFile(fullPath, file.Data, 0o644); err != nil {
			return fmt.Errorf("restore control-plane snapshot: write %q: %w", file.Path, err)
		}
	}
	for _, path := range existing {
		if _, ok := keep[path]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(rootDir, path)); err != nil && !errorsIsNotExist(err) {
			return fmt.Errorf("restore control-plane snapshot: remove %q: %w", path, err)
		}
	}
	return nil
}

func saveDaemonControlPlaneSnapshot(path string, snapshot daemonControlPlaneSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save control-plane snapshot: create state dir: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("save control-plane snapshot: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save control-plane snapshot: %w", err)
	}
	return nil
}

func loadDaemonControlPlaneSnapshot(path string) (daemonControlPlaneSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return daemonControlPlaneSnapshot{}, err
	}
	var snapshot daemonControlPlaneSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return daemonControlPlaneSnapshot{}, fmt.Errorf("decode control-plane snapshot: %w", err)
	}
	slices.SortFunc(snapshot.Files, func(a, b daemonControlPlaneSnapshotFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	return snapshot, nil
}

func daemonControlPlaneFiles(rootDir string) ([]string, error) {
	paths := make([]string, 0, 8)
	for _, file := range []string{".xylem.yml", ".xylem/HARNESS.md"} {
		if _, err := os.Stat(filepath.Join(rootDir, file)); err == nil {
			paths = append(paths, file)
		} else if err != nil && !errorsIsNotExist(err) {
			return nil, fmt.Errorf("stat control-plane file %q: %w", file, err)
		}
	}
	for _, dir := range []string{filepath.Join(".xylem", "workflows"), filepath.Join(".xylem", "prompts")} {
		fullDir := filepath.Join(rootDir, dir)
		if _, err := os.Stat(fullDir); err != nil {
			if errorsIsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat control-plane dir %q: %w", dir, err)
		}
		if err := filepath.WalkDir(fullDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(rootDir, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk control-plane dir %q: %w", dir, err)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

type daemonIntervalSource func() (time.Duration, time.Duration)
