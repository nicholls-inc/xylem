package dtushim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/dtu"
)

const (
	envStatePath  = "XYLEM_DTU_STATE_PATH"
	envStateDir   = "XYLEM_DTU_STATE_DIR"
	envUniverseID = "XYLEM_DTU_UNIVERSE_ID"
	envPhase      = "XYLEM_DTU_PHASE"
	envScript     = "XYLEM_DTU_SCRIPT"
	envAttempt    = "XYLEM_DTU_ATTEMPT"
	envFault      = "XYLEM_DTU_FAULT"
)

type options struct {
	StatePath  string
	StateDir   string
	UniverseID string
	Phase      string
	ScriptName string
	Attempt    int
	FaultName  string
}

type countingWriter struct {
	writer io.Writer
	bytes  int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil {
		return len(p), nil
	}
	n, err := w.writer.Write(p)
	w.bytes += n
	return n, err
}

// Execute runs a DTU shim entrypoint for the given binary name.
func Execute(ctx context.Context, binary string, args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	binaryPath := strings.TrimSpace(binary)
	binary = filepath.Base(binaryPath)
	opts, remaining, err := parseOptions(args, env)
	if err != nil {
		return writeError(stderr, 2, err)
	}

	store, err := resolveStore(opts)
	if err != nil {
		return writeError(stderr, 2, err)
	}

	switch binary {
	case "gh":
		return executeShim(ctx, store, binaryPath, dtu.ShimCommandGH, opts, remaining, "", "", "", stdout, stderr, func(ctx context.Context, state *dtu.State, _ *dtu.ShimEvent, shimStdout, shimStderr io.Writer) (int, error) {
			return runGH(ctx, store, state, remaining, shimStdout, shimStderr), nil
		})
	case "git":
		return executeShim(ctx, store, binaryPath, dtu.ShimCommandGit, opts, remaining, "", "", "", stdout, stderr, func(ctx context.Context, state *dtu.State, _ *dtu.ShimEvent, shimStdout, shimStderr io.Writer) (int, error) {
			return runGit(ctx, store, state, remaining, shimStdout, shimStderr), nil
		})
	case "claude":
		return runProvider(ctx, store, binaryPath, dtu.ProviderClaude, opts, remaining, stdin, stdout, stderr)
	case "copilot":
		return runProvider(ctx, store, binaryPath, dtu.ProviderCopilot, opts, remaining, stdin, stdout, stderr)
	default:
		return writeError(stderr, 2, fmt.Errorf("unsupported DTU shim %q", binary))
	}
}

func parseOptions(args []string, env []string) (options, []string, error) {
	attempt, err := parseOptionalInt(lookupEnv(env, envAttempt), envAttempt)
	if err != nil {
		return options{}, nil, err
	}
	opts := options{
		StatePath:  lookupEnv(env, envStatePath),
		StateDir:   lookupEnv(env, envStateDir),
		UniverseID: lookupEnv(env, envUniverseID),
		Phase:      lookupEnv(env, envPhase),
		ScriptName: lookupEnv(env, envScript),
		Attempt:    attempt,
		FaultName:  lookupEnv(env, envFault),
	}

	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dtu-state-path":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.StatePath = value
			i = next
		case "--dtu-state-dir":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.StateDir = value
			i = next
		case "--dtu-universe":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.UniverseID = value
			i = next
		case "--dtu-phase":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.Phase = value
			i = next
		case "--dtu-script":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.ScriptName = value
			i = next
		case "--dtu-attempt":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			parsed, err := parseOptionalInt(value, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.Attempt = parsed
			i = next
		case "--dtu-fault":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return options{}, nil, err
			}
			opts.FaultName = value
			i = next
		default:
			remaining = append(remaining, args[i])
		}
	}
	return opts, remaining, nil
}

func parseOptionalInt(value string, name string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", name, value)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must be non-negative, got %d", name, parsed)
	}
	return parsed, nil
}

func resolveStore(opts options) (*dtu.Store, error) {
	stateDir := strings.TrimSpace(opts.StateDir)
	universeID := strings.TrimSpace(opts.UniverseID)
	if statePath := strings.TrimSpace(opts.StatePath); statePath != "" {
		var err error
		stateDir, universeID, err = deriveStoreLocation(statePath)
		if err != nil {
			return nil, err
		}
	}
	if stateDir == "" || universeID == "" {
		return nil, fmt.Errorf("DTU state is not configured; set %s or both %s and %s", envStatePath, envStateDir, envUniverseID)
	}
	store, err := dtu.NewStore(stateDir, universeID)
	if err != nil {
		return nil, fmt.Errorf("resolve DTU store: %w", err)
	}
	return store, nil
}

func deriveStoreLocation(statePath string) (string, string, error) {
	cleaned := filepath.Clean(statePath)
	if filepath.Base(cleaned) != "state.json" {
		return "", "", fmt.Errorf("DTU state path %q must point to state.json", cleaned)
	}
	universeID := filepath.Base(filepath.Dir(cleaned))
	dtuDir := filepath.Base(filepath.Dir(filepath.Dir(cleaned)))
	if dtuDir != "dtu" {
		return "", "", fmt.Errorf("DTU state path %q must live under <stateDir>/dtu/<universe>/state.json", cleaned)
	}
	stateDir := filepath.Dir(filepath.Dir(filepath.Dir(cleaned)))
	return stateDir, universeID, nil
}

func runProvider(ctx context.Context, store *dtu.Store, binaryPath string, provider dtu.Provider, opts options, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	inv, stdinDigest, err := parseProviderInvocation(provider, opts, args, stdin)
	if err != nil {
		return writeError(stderr, 2, err)
	}
	return executeShim(ctx, store, binaryPath, providerShimCommand(provider), opts, args, provider, inv.Prompt, stdinDigest, stdout, stderr, func(ctx context.Context, state *dtu.State, event *dtu.ShimEvent, shimStdout, shimStderr io.Writer) (int, error) {
		script, err := state.SelectProviderScript(inv)
		if err != nil {
			return 1, err
		}
		if event.Script == "" {
			event.Script = script.Name
		}
		return applyProviderScript(ctx, store, script, shimStdout, shimStderr)
	})
}

func parseProviderInvocation(provider dtu.Provider, opts options, args []string, stdin io.Reader) (dtu.ProviderInvocation, string, error) {
	inv := dtu.ProviderInvocation{
		Provider:   provider,
		ScriptName: strings.TrimSpace(opts.ScriptName),
		Phase:      strings.TrimSpace(opts.Phase),
		Attempt:    opts.Attempt,
	}
	var stdinDigest string
	switch provider {
	case dtu.ProviderClaude:
		prompt, model, allowedTools, digest, err := parseClaudeArgs(args, stdin)
		if err != nil {
			return dtu.ProviderInvocation{}, "", err
		}
		inv.Prompt = prompt
		inv.Model = model
		inv.AllowedTools = allowedTools
		stdinDigest = digest
	case dtu.ProviderCopilot:
		prompt, model, allowedTools, err := parseCopilotArgs(args)
		if err != nil {
			return dtu.ProviderInvocation{}, "", err
		}
		inv.Prompt = prompt
		inv.Model = model
		inv.AllowedTools = allowedTools
	default:
		return dtu.ProviderInvocation{}, "", fmt.Errorf("unsupported provider %q", provider)
	}
	return inv, stdinDigest, nil
}

func parseClaudeArgs(args []string, stdin io.Reader) (string, string, []string, string, error) {
	var (
		promptFlag   bool
		prompt       string
		model        string
		allowedTools []string
		stdinDigest  string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p":
			promptFlag = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				prompt = args[i+1]
				i++
			}
		case "--max-turns":
			_, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, "", err
			}
			i = next
		case "--model":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, "", err
			}
			model = value
			i = next
		case "--allowedTools":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, "", err
			}
			allowedTools = append(allowedTools, value)
			i = next
		case "--append-system-prompt":
			_, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, "", err
			}
			i = next
		}
	}
	if !promptFlag {
		return "", "", nil, "", fmt.Errorf("claude shim requires -p")
	}
	if prompt == "" && stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", "", nil, "", fmt.Errorf("read claude prompt: %w", err)
		}
		prompt = string(data)
		stdinDigest = hashPrompt(prompt)
	}
	return prompt, model, normalizeStrings(allowedTools), stdinDigest, nil
}

func parseCopilotArgs(args []string) (string, string, []string, error) {
	var (
		prompt       string
		promptSet    bool
		model        string
		allowedTools []string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--prompt":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, err
			}
			prompt = value
			promptSet = true
			i = next
		case "--model":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, err
			}
			model = value
			i = next
		case "--available-tools":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return "", "", nil, err
			}
			allowedTools = append(allowedTools, value)
			i = next
		case "-s", "--silent", "--headless", "--allow-all-tools":
		}
	}
	if !promptSet {
		return "", "", nil, fmt.Errorf("copilot shim requires -p/--prompt")
	}
	return prompt, model, normalizeStrings(allowedTools), nil
}

func providerShimCommand(provider dtu.Provider) dtu.ShimCommand {
	switch provider {
	case dtu.ProviderCopilot:
		return dtu.ShimCommandCopilot
	default:
		return dtu.ShimCommandClaude
	}
}

func executeShim(
	ctx context.Context,
	store *dtu.Store,
	binaryPath string,
	command dtu.ShimCommand,
	opts options,
	args []string,
	provider dtu.Provider,
	prompt string,
	stdinDigest string,
	stdout io.Writer,
	stderr io.Writer,
	run func(context.Context, *dtu.State, *dtu.ShimEvent, io.Writer, io.Writer) (int, error),
) int {
	state, err := store.Load()
	if err != nil {
		return writeError(stderr, 1, fmt.Errorf("load DTU state: %w", err))
	}
	clock, err := dtu.ResolveClock(state.Clock, nil)
	if err != nil {
		return writeError(stderr, 1, fmt.Errorf("resolve DTU clock: %w", err))
	}

	invocation := dtu.ShimInvocation{
		Command:   command,
		FaultName: strings.TrimSpace(opts.FaultName),
		Args:      normalizeArgsForMatch(args),
		Phase:     strings.TrimSpace(opts.Phase),
		Script:    strings.TrimSpace(opts.ScriptName),
		Attempt:   opts.Attempt,
	}
	stdoutCounter := &countingWriter{writer: stdout}
	stderrCounter := &countingWriter{writer: stderr}
	invocationEvent := buildShimEvent(binaryPath, command, args, provider, opts, prompt, stdinDigest)
	if err := recordShimEvent(store, dtu.EventKindShimInvocation, invocationEvent); err != nil {
		return writeError(stderr, 1, fmt.Errorf("record DTU shim invocation: %w", err))
	}
	resultEvent := cloneShimEvent(invocationEvent)
	runState := state
	if shouldRecordObservation(command, args) {
		previewState, _, previewErr := dtu.PreviewObservation(state, invocation)
		if previewErr != nil {
			return writeError(stderr, 1, fmt.Errorf("preview DTU observation: %w", previewErr))
		}
		runState = previewState
	}

	start := clock.Now()
	fault, err := findShimFault(state, invocation)
	var (
		code   int
		runErr error
	)
	switch {
	case err != nil:
		code = 1
		runErr = err
	case fault != nil:
		code, runErr = applyShimFault(ctx, store, fault, stdoutCounter, stderrCounter)
	default:
		code, runErr = run(ctx, runState, resultEvent, stdoutCounter, stderrCounter)
	}
	if runErr == nil && fault == nil && shouldRecordObservation(command, args) {
		if _, obsErr := store.RecordObservation(invocation); obsErr != nil {
			code = 1
			runErr = fmt.Errorf("record DTU observation: %w", obsErr)
		}
	}

	endClock, clockErr := currentStoreClock(store)
	if clockErr != nil {
		return writeError(stderr, 1, clockErr)
	}
	if runErr != nil {
		code = writeError(stderrCounter, code, runErr)
	}
	resultEvent.Duration = endClock.Since(start).String()
	resultEvent.ExitCode = intPtr(code)
	resultEvent.StdoutBytes = stdoutCounter.bytes
	resultEvent.StderrBytes = stderrCounter.bytes
	if runErr != nil {
		resultEvent.Error = runErr.Error()
	}
	if err := recordShimEvent(store, dtu.EventKindShimResult, resultEvent); err != nil {
		return writeError(stderr, 1, fmt.Errorf("record DTU shim result: %w", err))
	}
	return code
}

func buildShimEvent(binaryPath string, command dtu.ShimCommand, args []string, provider dtu.Provider, opts options, prompt string, stdinDigest string) *dtu.ShimEvent {
	workingDir, err := os.Getwd()
	if err != nil {
		workingDir = ""
	}
	binaryName := filepath.Base(strings.TrimSpace(binaryPath))
	if binaryName == "." || binaryName == string(filepath.Separator) {
		binaryName = ""
	}
	if binaryName == "" {
		binaryName = string(command)
	}
	return &dtu.ShimEvent{
		Command:     string(command),
		Args:        append([]string(nil), args...),
		Provider:    provider,
		Phase:       strings.TrimSpace(opts.Phase),
		Attempt:     opts.Attempt,
		BinaryPath:  strings.TrimSpace(binaryPath),
		BinaryName:  binaryName,
		WorkingDir:  workingDir,
		StdinDigest: strings.TrimSpace(stdinDigest),
		Prompt:      prompt,
		PromptHash:  hashPrompt(prompt),
		Script:      strings.TrimSpace(opts.ScriptName),
	}
}

func recordShimEvent(store *dtu.Store, kind dtu.EventKind, shim *dtu.ShimEvent) error {
	if shim == nil {
		return nil
	}
	return store.RecordEvent(&dtu.Event{
		Kind: kind,
		Shim: shim,
	})
}

func cloneShimEvent(event *dtu.ShimEvent) *dtu.ShimEvent {
	if event == nil {
		return nil
	}
	cloned := *event
	cloned.Args = append([]string(nil), event.Args...)
	if event.ExitCode != nil {
		cloned.ExitCode = intPtr(*event.ExitCode)
	}
	return &cloned
}

func findShimFault(state *dtu.State, inv dtu.ShimInvocation) (*dtu.ShimFault, error) {
	fault, err := state.SelectShimFault(inv)
	if err == nil {
		return fault, nil
	}
	if inv.FaultName != "" {
		return nil, err
	}
	if strings.Contains(err.Error(), "no matching shim fault") {
		return nil, nil
	}
	return nil, err
}

func shouldRecordObservation(command dtu.ShimCommand, args []string) bool {
	switch command {
	case dtu.ShimCommandGH:
		if len(args) >= 2 && args[0] == "search" && args[1] == "issues" {
			return true
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
			return true
		}
		if len(args) >= 2 && args[0] == "pr" && (args[1] == "checks" || args[1] == "list" || args[1] == "view") {
			return true
		}
		if len(args) >= 1 && args[0] == "api" {
			return true
		}
	case dtu.ShimCommandGit:
		if len(args) >= 1 && (args[0] == "fetch" || args[0] == "ls-remote") {
			return true
		}
	}
	return false
}

func applyProviderScript(ctx context.Context, store *dtu.Store, script *dtu.ProviderScript, stdout, stderr io.Writer) (int, error) {
	if script.Delay != "" {
		delay, err := time.ParseDuration(script.Delay)
		if err != nil {
			return 1, fmt.Errorf("parse DTU provider delay: %w", err)
		}
		if err := waitContext(ctx, store, delay); err != nil {
			return 124, err
		}
	}
	if script.Hang {
		<-ctx.Done()
		if err := ctx.Err(); err != nil {
			return 124, err
		}
		return 124, nil
	}
	stdoutContent := withNoOpMarker(script.Stdout, script.NoOpMarker)
	if stdoutContent != "" {
		_, _ = io.WriteString(stdout, stdoutContent)
	}
	if script.Stderr != "" {
		_, _ = io.WriteString(stderr, script.Stderr)
	}
	if script.ExitCode != 0 {
		return script.ExitCode, nil
	}
	return 0, nil
}

func withNoOpMarker(stdout string, marker string) string {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return stdout
	}
	trimmed := strings.TrimRight(stdout, "\n")
	switch {
	case trimmed == "":
		return marker + "\n"
	case trimmed == marker:
		return marker + "\n"
	case strings.HasSuffix(trimmed, "\n\n"+marker):
		return trimmed + "\n"
	default:
		return trimmed + "\n\n" + marker + "\n"
	}
}

func applyShimFault(ctx context.Context, store *dtu.Store, fault *dtu.ShimFault, stdout, stderr io.Writer) (int, error) {
	if fault.Delay != "" {
		delay, err := time.ParseDuration(fault.Delay)
		if err != nil {
			return 1, fmt.Errorf("parse DTU shim delay: %w", err)
		}
		if err := waitContext(ctx, store, delay); err != nil {
			return 124, err
		}
	}
	if fault.Hang {
		<-ctx.Done()
		if err := ctx.Err(); err != nil {
			return 124, err
		}
		return 124, nil
	}
	if fault.Stdout != "" {
		_, _ = io.WriteString(stdout, fault.Stdout)
	}
	if fault.Stderr != "" {
		_, _ = io.WriteString(stderr, fault.Stderr)
	}
	return fault.ExitCode, nil
}

func runGH(ctx context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		return writeError(stderr, 2, fmt.Errorf("gh shim requires a supported subcommand"))
	}
	switch args[0] {
	case "search":
		if args[1] == "issues" {
			return runGHSearchIssues(ctx, store, state, args[2:], stdout, stderr)
		}
	case "issue":
		switch args[1] {
		case "create":
			return runGHIssueCreate(ctx, store, args[2:], stdout, stderr)
		case "edit":
			return runGHIssueEdit(ctx, store, args[2:], stderr)
		case "view":
			return runGHIssueView(ctx, store, state, args[2:], stdout, stderr)
		case "comment":
			return runGHIssueComment(ctx, store, args[2:], stderr)
		}
	case "pr":
		switch args[1] {
		case "list":
			return runGHPRList(ctx, store, state, args[2:], stdout, stderr)
		case "edit":
			return runGHPRModify(ctx, store, args[2:], stderr)
		case "merge":
			return runGHPRMerge(ctx, store, args[2:], stderr)
		case "view":
			return runGHPRView(ctx, store, state, args[2:], stdout, stderr)
		case "checks":
			return runGHPRChecks(ctx, store, state, args[2:], stdout, stderr)
		}
	case "repo":
		if args[1] == "view" {
			return runGHRepoView(ctx, store, state, args[2:], stdout, stderr)
		}
	case "api":
		return runGHAPI(ctx, store, state, args[1:], stdout, stderr)
	}
	return writeError(stderr, 2, fmt.Errorf("unsupported gh shim command: %s", strings.Join(args, " ")))
}

func runGit(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("git shim requires a supported subcommand"))
	}
	switch args[0] {
	case "symbolic-ref":
		return runGitSymbolicRef(store, state, args[1:], stdout, stderr)
	case "remote":
		if len(args) < 3 {
			return writeError(stderr, 2, fmt.Errorf("git remote requires a supported subcommand"))
		}
		switch args[1] {
		case "get-url":
			return runGitRemoteGetURL(store, state, args[2:], stdout, stderr)
		case "show":
			return runGitRemoteShow(store, state, args[2:], stdout, stderr)
		}
	case "fetch":
		return runGitFetch(store, state, args[1:], stderr)
	case "worktree":
		if len(args) < 2 {
			return writeError(stderr, 2, fmt.Errorf("git worktree requires a supported subcommand"))
		}
		switch args[1] {
		case "add":
			return runGitWorktreeAdd(store, args[2:], stderr)
		case "list":
			return runGitWorktreeList(store, state, args[2:], stdout, stderr)
		case "remove":
			return runGitWorktreeRemove(store, args[2:], stderr)
		}
	case "branch":
		if len(args) >= 2 && (args[1] == "-d" || args[1] == "-D") {
			return runGitBranchDelete(store, args[2:], stderr)
		}
	case "ls-remote":
		return runGitLsRemote(store, state, args[1:], stdout, stderr)
	}
	return writeError(stderr, 2, fmt.Errorf("unsupported git shim command: %s", strings.Join(args, " ")))
}

func runGitSymbolicRef(store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return writeError(stderr, 2, fmt.Errorf("git symbolic-ref requires exactly one ref"))
	}
	repo, _, code := loadRepo(store, state, "", stderr)
	if code != 0 {
		return code
	}
	switch args[0] {
	case "refs/remotes/origin/HEAD":
		_, _ = io.WriteString(stdout, "refs/remotes/origin/"+repo.DefaultBranch+"\n")
		return 0
	case "HEAD":
		_, _ = io.WriteString(stdout, "refs/heads/"+repo.DefaultBranch+"\n")
		return 0
	default:
		return writeError(stderr, 2, fmt.Errorf("unsupported git symbolic-ref target %q", args[0]))
	}
}

func runGitRemoteGetURL(store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "origin" {
		return writeError(stderr, 2, fmt.Errorf("git remote get-url requires origin"))
	}
	repo, _, code := loadRepo(store, state, "", stderr)
	if code != 0 {
		return code
	}
	_, _ = io.WriteString(stdout, syntheticRemoteURL(repo)+"\n")
	return 0
}

func runGitRemoteShow(store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "origin" {
		return writeError(stderr, 2, fmt.Errorf("git remote show requires origin"))
	}
	repo, _, code := loadRepo(store, state, "", stderr)
	if code != 0 {
		return code
	}
	_, _ = fmt.Fprintf(stdout, "  HEAD branch: %s\n", repo.DefaultBranch)
	return 0
}

func runGitFetch(store *dtu.Store, state *dtu.State, args []string, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "origin" {
		return writeError(stderr, 2, fmt.Errorf("git fetch requires origin and at least one branch name"))
	}
	repo, _, code := loadRepo(store, state, "", stderr)
	if code != 0 {
		return code
	}
	for _, branch := range args[1:] {
		if !repoHasBranch(repo, branch) {
			return writeError(stderr, 1, fmt.Errorf("branch %q not found", branch))
		}
	}
	return 0
}

func runGitWorktreeAdd(store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) != 4 || args[1] != "-B" {
		return writeError(stderr, 2, fmt.Errorf("git worktree add requires <path> -B <branch> <start-point>"))
	}
	worktreePath, err := resolveShimPath(args[0])
	if err != nil {
		return writeError(stderr, 1, err)
	}
	branchName := strings.TrimSpace(args[2])
	startPoint := strings.TrimSpace(args[3])
	sourceBranch := strings.TrimPrefix(startPoint, "origin/")
	if branchName == "" || sourceBranch == "" {
		return writeError(stderr, 2, fmt.Errorf("git worktree add requires non-empty branch and start-point"))
	}
	err = store.Update(func(state *dtu.State) error {
		repo, err := singleRepository(state)
		if err != nil {
			return err
		}
		if !repoHasBranch(repo, sourceBranch) {
			return fmt.Errorf("start point %q not found", startPoint)
		}
		if existing := findWorktreeByPath(repo, worktreePath); existing != nil {
			return fmt.Errorf("worktree %q already exists", worktreePath)
		}
		repo.Worktrees = append(repo.Worktrees, dtu.Worktree{Path: worktreePath, Branch: branchName})
		upsertBranch(repo, dtu.Branch{Name: branchName, SHA: branchSHA(repo, sourceBranch)})
		return nil
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		return writeError(stderr, 1, fmt.Errorf("create worktree directory %q: %w", worktreePath, err))
	}
	return 0
}

func runGitWorktreeList(store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--porcelain" {
		return writeError(stderr, 2, fmt.Errorf("git worktree list requires --porcelain"))
	}
	repo, _, code := loadRepo(store, state, "", stderr)
	if code != 0 {
		return code
	}
	worktrees := append([]dtu.Worktree(nil), repo.Worktrees...)
	sort.Slice(worktrees, func(i, j int) bool {
		return resolvedWorktreePath(worktrees[i].Path) < resolvedWorktreePath(worktrees[j].Path)
	})
	for _, wt := range worktrees {
		path := resolvedWorktreePath(wt.Path)
		_, _ = fmt.Fprintf(stdout, "worktree %s\n", path)
		_, _ = fmt.Fprintf(stdout, "HEAD %s\n", branchSHA(repo, wt.Branch))
		if wt.Branch != "" {
			_, _ = fmt.Fprintf(stdout, "branch refs/heads/%s\n", wt.Branch)
		}
		_, _ = io.WriteString(stdout, "\n")
	}
	return 0
}

func runGitWorktreeRemove(store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) != 2 || args[1] != "--force" {
		return writeError(stderr, 2, fmt.Errorf("git worktree remove requires <path> --force"))
	}
	worktreePath, err := resolveShimPath(args[0])
	if err != nil {
		return writeError(stderr, 1, err)
	}
	err = store.Update(func(state *dtu.State) error {
		repo, err := singleRepository(state)
		if err != nil {
			return err
		}
		for i := range repo.Worktrees {
			if sameWorktreePath(repo.Worktrees[i].Path, worktreePath) {
				repo.Worktrees = append(repo.Worktrees[:i], repo.Worktrees[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("worktree %q not found", worktreePath)
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		return writeError(stderr, 1, fmt.Errorf("remove worktree directory %q: %w", worktreePath, err))
	}
	return 0
}

func runGitBranchDelete(store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) != 1 {
		return writeError(stderr, 2, fmt.Errorf("git branch -d/-D requires exactly one branch"))
	}
	branchName := strings.TrimSpace(args[0])
	err := store.Update(func(state *dtu.State) error {
		repo, err := singleRepository(state)
		if err != nil {
			return err
		}
		for i := range repo.Branches {
			if repo.Branches[i].Name == branchName {
				repo.Branches = append(repo.Branches[:i], repo.Branches[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("branch %q not found", branchName)
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	return 0
}

func runGitLsRemote(store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "--heads" || args[1] != "origin" {
		return writeError(stderr, 2, fmt.Errorf("git ls-remote requires --heads origin <pattern>"))
	}
	pattern := strings.TrimSpace(args[2])
	repo, _, code := loadRepo(store, state, "", stderr)
	if code != 0 {
		return code
	}
	branches := uniqueBranchNames(repo)
	for _, branch := range branches {
		matched, err := path.Match(pattern, branch)
		if err != nil {
			return writeError(stderr, 2, fmt.Errorf("invalid ls-remote pattern %q: %w", pattern, err))
		}
		if matched {
			_, _ = fmt.Fprintf(stdout, "%s\trefs/heads/%s\n", branchSHA(repo, branch), branch)
		}
	}
	return 0
}

func singleRepository(state *dtu.State) (*dtu.Repository, error) {
	if state == nil {
		return nil, fmt.Errorf("DTU state must not be nil")
	}
	if len(state.Repositories) == 1 {
		return &state.Repositories[0], nil
	}
	if len(state.Repositories) == 0 {
		return nil, fmt.Errorf("DTU state has no repositories")
	}
	return nil, fmt.Errorf("git shim requires a single DTU repository")
}

func repoHasBranch(repo *dtu.Repository, name string) bool {
	if repo == nil {
		return false
	}
	if repo.BranchByName(name) != nil {
		return true
	}
	return name == repo.DefaultBranch
}

func branchSHA(repo *dtu.Repository, name string) string {
	if repo == nil {
		return syntheticSHA()
	}
	if branch := repo.BranchByName(name); branch != nil && strings.TrimSpace(branch.SHA) != "" {
		return branch.SHA
	}
	if name == repo.DefaultBranch {
		return syntheticSHA()
	}
	return syntheticSHA()
}

func upsertBranch(repo *dtu.Repository, branch dtu.Branch) {
	if repo == nil {
		return
	}
	if existing := repo.BranchByName(branch.Name); existing != nil {
		existing.SHA = branch.SHA
		return
	}
	repo.Branches = append(repo.Branches, branch)
}

func uniqueBranchNames(repo *dtu.Repository) []string {
	if repo == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(repo.Branches)+1)
	out := make([]string, 0, len(repo.Branches)+1)
	if repo.DefaultBranch != "" {
		seen[repo.DefaultBranch] = struct{}{}
		out = append(out, repo.DefaultBranch)
	}
	for _, branch := range repo.Branches {
		if branch.Name == "" {
			continue
		}
		if _, ok := seen[branch.Name]; ok {
			continue
		}
		seen[branch.Name] = struct{}{}
		out = append(out, branch.Name)
	}
	sort.Strings(out)
	return out
}

func findWorktreeByPath(repo *dtu.Repository, worktreePath string) *dtu.Worktree {
	if repo == nil {
		return nil
	}
	for i := range repo.Worktrees {
		if sameWorktreePath(repo.Worktrees[i].Path, worktreePath) {
			return &repo.Worktrees[i]
		}
	}
	return nil
}

func sameWorktreePath(left string, right string) bool {
	return resolvedWorktreePath(left) == resolvedWorktreePath(right)
}

func resolveShimPath(pathValue string) (string, error) {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(pathValue) {
		return filepath.Clean(pathValue), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Clean(filepath.Join(wd, pathValue)), nil
}

func resolvedWorktreePath(pathValue string) string {
	resolved, err := resolveShimPath(pathValue)
	if err != nil {
		return filepath.Clean(pathValue)
	}
	return resolved
}

func syntheticRemoteURL(repo *dtu.Repository) string {
	return fmt.Sprintf("https://github.com/%s.git", repoSlug(nil, repo))
}

func syntheticSHA() string {
	return "0000000000000000000000000000000000000000"
}

func normalizeArgsForMatch(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, strings.TrimSpace(arg))
	}
	return out
}

func hashPrompt(prompt string) string {
	if prompt == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

func intPtr(value int) *int {
	return &value
}

func runGHSearchIssues(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	opts, err := parseGHFlags(args)
	if err != nil {
		return writeError(stderr, 2, err)
	}
	repo, state, code := loadRepo(store, state, opts.repo, stderr)
	if code != 0 {
		return code
	}
	if opts.state != "" && opts.state != string(dtu.IssueStateOpen) {
		_, _ = io.WriteString(stdout, "[]")
		return 0
	}
	fields := splitCSV(opts.jsonFields)
	var items []map[string]any
	for _, issue := range repo.Issues {
		if issue.State != dtu.IssueStateOpen {
			continue
		}
		if len(opts.labels) > 0 && !containsAll(issue.Labels, opts.labels) {
			continue
		}
		if opts.search != "" && !matchesSearchQuery(issue.Title, issue.Body, opts.search) {
			continue
		}
		items = append(items, encodeIssue(state, repo, issue, fields))
		if opts.limit > 0 && len(items) >= opts.limit {
			break
		}
	}
	return writeJSON(stdout, stderr, items)
}

func runGHIssueCreate(_ context.Context, store *dtu.Store, args []string, stdout, stderr io.Writer) int {
	opts, err := parseGHFlags(args)
	if err != nil {
		return writeError(stderr, 2, err)
	}
	if opts.repo == "" {
		return writeError(stderr, 2, fmt.Errorf("gh issue create requires --repo"))
	}
	if strings.TrimSpace(opts.title) == "" {
		return writeError(stderr, 2, fmt.Errorf("gh issue create requires --title"))
	}
	if strings.TrimSpace(opts.body) == "" {
		return writeError(stderr, 2, fmt.Errorf("gh issue create requires --body"))
	}

	var createdURL string
	err = store.Update(func(state *dtu.State) error {
		repo := state.RepositoryBySlug(opts.repo)
		if repo == nil {
			return fmt.Errorf("repository %q not found", opts.repo)
		}
		number := nextIssueNumber(repo)
		ensureRepoLabels(repo, opts.labels)
		issue := dtu.Issue{
			Number: number,
			Title:  opts.title,
			Body:   opts.body,
			State:  dtu.IssueStateOpen,
			Labels: append([]string(nil), opts.labels...),
		}
		createdURL = issueURL(state, repo, issue)
		issue.URL = createdURL
		repo.Issues = append(repo.Issues, issue)
		return nil
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	_, _ = io.WriteString(stdout, createdURL+"\n")
	return 0
}

func runGHIssueEdit(_ context.Context, store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh issue edit requires an issue number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid issue number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	if opts.repo == "" {
		return writeError(stderr, 2, fmt.Errorf("gh issue edit requires --repo"))
	}
	err = store.Update(func(state *dtu.State) error {
		repo := state.RepositoryBySlug(opts.repo)
		if repo == nil {
			return fmt.Errorf("repository %q not found", opts.repo)
		}
		issue := repo.IssueByNumber(number)
		if issue == nil {
			return fmt.Errorf("issue %d not found in %s", number, opts.repo)
		}
		issue.Labels = mutateLabels(issue.Labels, opts.addLabels, opts.removeLabels)
		return nil
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	return 0
}

func runGHIssueView(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh issue view requires an issue number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid issue number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	repo, state, code := loadRepo(store, state, opts.repo, stderr)
	if code != 0 {
		return code
	}
	issue := repo.IssueByNumber(number)
	if issue == nil {
		return writeError(stderr, 1, fmt.Errorf("issue %d not found in %s", number, repo.Slug()))
	}
	return writeJSON(stdout, stderr, encodeIssue(state, repo, *issue, splitCSV(opts.jsonFields)))
}

func runGHIssueComment(_ context.Context, store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh issue comment requires a number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid issue number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	if opts.repo == "" {
		return writeError(stderr, 2, fmt.Errorf("gh issue comment requires --repo"))
	}
	if opts.body == "" {
		return writeError(stderr, 2, fmt.Errorf("gh issue comment requires --body"))
	}
	err = store.Update(func(state *dtu.State) error {
		repo := state.RepositoryBySlug(opts.repo)
		if repo == nil {
			return fmt.Errorf("repository %q not found", opts.repo)
		}
		comment := dtu.Comment{ID: state.AdvanceCommentID(), Body: opts.body}
		if issue := repo.IssueByNumber(number); issue != nil {
			issue.Comments = append(issue.Comments, comment)
			return nil
		}
		if pr := repo.PullRequestByNumber(number); pr != nil {
			pr.Comments = append(pr.Comments, comment)
			return nil
		}
		return fmt.Errorf("issue or pull request %d not found in %s", number, opts.repo)
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	return 0
}

func runGHPRList(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	opts, err := parseGHFlags(args)
	if err != nil {
		return writeError(stderr, 2, err)
	}
	repo, state, code := loadRepo(store, state, opts.repo, stderr)
	if code != 0 {
		return code
	}
	fields := splitCSV(opts.jsonFields)
	var items []map[string]any
	for _, pr := range repo.PullRequests {
		if !matchesPRState(pr, opts.state) {
			continue
		}
		if len(opts.labels) > 0 && !containsAll(pr.Labels, opts.labels) {
			continue
		}
		if search := strings.TrimPrefix(opts.search, "head:"); opts.search != "" && !strings.HasPrefix(pr.HeadBranch, search) {
			continue
		}
		items = append(items, encodePR(state, repo, pr, fields))
		if opts.limit > 0 && len(items) >= opts.limit {
			break
		}
	}
	return writeJSON(stdout, stderr, items)
}

func runGHPRModify(_ context.Context, store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh pr edit requires a pull request number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid pull request number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	if opts.repo == "" {
		return writeError(stderr, 2, fmt.Errorf("gh pr edit requires --repo"))
	}
	err = store.Update(func(state *dtu.State) error {
		repo := state.RepositoryBySlug(opts.repo)
		if repo == nil {
			return fmt.Errorf("repository %q not found", opts.repo)
		}
		pr := repo.PullRequestByNumber(number)
		if pr == nil {
			return fmt.Errorf("pull request %d not found in %s", number, opts.repo)
		}
		pr.Labels = mutateLabels(pr.Labels, opts.addLabels, opts.removeLabels)
		return nil
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	return 0
}

func runGHPRView(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh pr view requires a pull request number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid pull request number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	repo, state, code := loadRepo(store, state, opts.repo, stderr)
	if code != 0 {
		return code
	}
	pr := repo.PullRequestByNumber(number)
	if pr == nil {
		return writeError(stderr, 1, fmt.Errorf("pull request %d not found in %s", number, repo.Slug()))
	}
	if opts.jq == ".headRefOid" {
		_, _ = io.WriteString(stdout, pr.HeadSHA)
		return 0
	}
	return writeJSON(stdout, stderr, encodePR(state, repo, *pr, splitCSV(opts.jsonFields)))
}

func runGHPRChecks(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh pr checks requires a pull request number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid pull request number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	repo, _, code := loadRepo(store, state, opts.repo, stderr)
	if code != 0 {
		return code
	}
	pr := repo.PullRequestByNumber(number)
	if pr == nil {
		return writeError(stderr, 1, fmt.Errorf("pull request %d not found in %s", number, repo.Slug()))
	}
	items := make([]map[string]any, 0, len(pr.Checks))
	for _, check := range pr.Checks {
		items = append(items, map[string]any{"name": check.Name, "state": ghCheckState(check.State)})
	}
	return writeJSON(stdout, stderr, items)
}

func runGHPRMerge(_ context.Context, store *dtu.Store, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh pr merge requires a pull request number"))
	}
	number, err := strconv.Atoi(args[0])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid pull request number %q", args[0]))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}

	err = store.Update(func(state *dtu.State) error {
		var repo *dtu.Repository
		if opts.repo == "" {
			var repoErr error
			repo, repoErr = singleRepository(state)
			if repoErr != nil {
				return fmt.Errorf("resolve repository for gh pr merge: %w", repoErr)
			}
		} else {
			repo = state.RepositoryBySlug(opts.repo)
			if repo == nil {
				return fmt.Errorf("repository %q not found", opts.repo)
			}
		}
		return repo.MergePullRequest(number, dtu.MergePullRequestOptions{
			DeleteHeadBranch: opts.deleteBranch,
			AutoMerge:        opts.autoMerge,
			AdminMerge:       opts.adminMerge,
		})
	})
	if err != nil {
		return writeError(stderr, 1, err)
	}
	return 0
}

func runGHRepoView(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	opts, err := parseGHFlags(args)
	if err != nil {
		return writeError(stderr, 2, err)
	}
	repo, _, code := loadRepo(store, state, opts.repo, stderr)
	if code != 0 {
		return code
	}
	return writeJSON(stdout, stderr, map[string]any{"defaultBranchRef": map[string]any{"name": repo.DefaultBranch}})
}

func runGHAPI(_ context.Context, store *dtu.Store, state *dtu.State, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeError(stderr, 2, fmt.Errorf("gh api requires a path"))
	}
	opts, err := parseGHFlags(args[1:])
	if err != nil {
		return writeError(stderr, 2, err)
	}
	parts := strings.Split(strings.Trim(args[0], "/"), "/")
	if len(parts) != 6 || parts[0] != "repos" {
		return writeError(stderr, 2, fmt.Errorf("unsupported gh api path %q", args[0]))
	}
	repoSlug := parts[1] + "/" + parts[2]
	repo, _, code := loadRepo(store, state, repoSlug, stderr)
	if code != 0 {
		return code
	}
	number, err := strconv.Atoi(parts[4])
	if err != nil {
		return writeError(stderr, 2, fmt.Errorf("invalid resource number in %q", args[0]))
	}
	switch {
	case parts[3] == "pulls" && opts.jq == ".[].id":
		pr := repo.PullRequestByNumber(number)
		if pr == nil {
			return writeError(stderr, 1, fmt.Errorf("pull request %d not found in %s", number, repo.Slug()))
		}
		for _, review := range pr.Reviews {
			_, _ = fmt.Fprintf(stdout, "%d\n", review.ID)
		}
		return 0
	case parts[3] == "issues" && opts.jq == ".[].id":
		if pr := repo.PullRequestByNumber(number); pr != nil {
			for _, comment := range pr.Comments {
				_, _ = fmt.Fprintf(stdout, "%d\n", comment.ID)
			}
			return 0
		}
		issue := repo.IssueByNumber(number)
		if issue == nil {
			return writeError(stderr, 1, fmt.Errorf("issue %d not found in %s", number, repo.Slug()))
		}
		for _, comment := range issue.Comments {
			_, _ = fmt.Fprintf(stdout, "%d\n", comment.ID)
		}
		return 0
	default:
		return writeError(stderr, 2, fmt.Errorf("unsupported gh api path %q", args[0]))
	}
}

type ghOptions struct {
	repo         string
	state        string
	jsonFields   string
	title        string
	labels       []string
	search       string
	jq           string
	body         string
	limit        int
	deleteBranch bool
	autoMerge    bool
	adminMerge   bool
	addLabels    []string
	removeLabels []string
}

func parseGHFlags(args []string) (ghOptions, error) {
	var opts ghOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.repo = value
			i = next
		case "--state":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.state = value
			i = next
		case "--json":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.jsonFields = value
			i = next
		case "--label":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.labels = append(opts.labels, value)
			i = next
		case "--title":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.title = value
			i = next
		case "--search":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.search = value
			i = next
		case "--jq":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.jq = value
			i = next
		case "--body":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.body = value
			i = next
		case "--limit":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			limit, convErr := strconv.Atoi(value)
			if convErr != nil {
				return ghOptions{}, fmt.Errorf("invalid --limit value %q", value)
			}
			opts.limit = limit
			i = next
		case "--delete-branch", "-d":
			opts.deleteBranch = true
		case "--auto":
			opts.autoMerge = true
		case "--admin":
			opts.adminMerge = true
		case "--add-label":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.addLabels = append(opts.addLabels, value)
			i = next
		case "--remove-label":
			value, next, err := requireValue(args, i, args[i])
			if err != nil {
				return ghOptions{}, err
			}
			opts.removeLabels = append(opts.removeLabels, value)
			i = next
		}
	}
	return opts, nil
}

func loadRepo(store *dtu.Store, state *dtu.State, slug string, stderr io.Writer) (*dtu.Repository, *dtu.State, int) {
	if state == nil {
		loaded, err := store.Load()
		if err != nil {
			return nil, nil, writeError(stderr, 1, fmt.Errorf("load DTU state: %w", err))
		}
		state = loaded
	}
	if slug == "" {
		if len(state.Repositories) == 1 {
			return &state.Repositories[0], state, 0
		}
		return nil, state, writeError(stderr, 2, fmt.Errorf("gh shim requires --repo when DTU state has multiple repositories"))
	}
	repo := state.RepositoryBySlug(slug)
	if repo == nil {
		return nil, state, writeError(stderr, 1, fmt.Errorf("repository %q not found", slug))
	}
	return repo, state, 0
}

func encodeIssue(state *dtu.State, repo *dtu.Repository, issue dtu.Issue, fields []string) map[string]any {
	if len(fields) == 0 {
		fields = []string{"number", "title", "body", "url", "labels"}
	}
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		switch field {
		case "number":
			out[field] = issue.Number
		case "title":
			out[field] = issue.Title
		case "body":
			out[field] = issue.Body
		case "url":
			out[field] = issueURL(state, repo, issue)
		case "labels":
			out[field] = encodeLabels(issue.Labels)
		}
	}
	return out
}

func encodePR(state *dtu.State, repo *dtu.Repository, pr dtu.PullRequest, fields []string) map[string]any {
	if len(fields) == 0 {
		fields = []string{"number", "title", "body", "url", "labels", "headRefName"}
	}
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		switch field {
		case "number":
			out[field] = pr.Number
		case "title":
			out[field] = pr.Title
		case "body":
			out[field] = pr.Body
		case "url":
			out[field] = prURL(state, repo, pr)
		case "labels":
			out[field] = encodeLabels(pr.Labels)
		case "state":
			out[field] = prState(pr)
		case "createdAt":
			if !pr.CreatedAt.IsZero() {
				out[field] = pr.CreatedAt.UTC().Format(time.RFC3339)
			} else {
				out[field] = time.Time{}.Format(time.RFC3339)
			}
		case "headRefName":
			out[field] = pr.HeadBranch
		case "headRefOid":
			out[field] = pr.HeadSHA
		case "commits":
			out[field] = encodePRCommits(pr)
		case "mergeable":
			out[field] = prMergeable(pr)
		case "reviewDecision":
			out[field] = prReviewDecision(pr)
		case "autoMergeRequest":
			if pr.AutoMergeEnabled {
				out[field] = map[string]any{}
			} else {
				out[field] = nil
			}
		case "statusCheckRollup":
			out[field] = encodeStatusCheckRollup(pr.Checks)
		case "reviewRequests":
			out[field] = encodeReviewRequests(pr.ReviewRequests)
		case "latestReviews":
			out[field] = encodeLatestReviews(pr.Reviews)
		case "reviewThreads":
			out[field] = encodeReviewThreads(pr.ReviewThreads)
		case "mergeCommit":
			out[field] = map[string]any{"oid": pr.HeadSHA}
		}
	}
	return out
}

func encodePRCommits(pr dtu.PullRequest) []map[string]any {
	if len(pr.Commits) == 0 {
		if strings.TrimSpace(pr.HeadSHA) == "" {
			return []map[string]any{}
		}
		return []map[string]any{{"oid": pr.HeadSHA}}
	}
	out := make([]map[string]any, 0, len(pr.Commits))
	for _, commit := range pr.Commits {
		oid := strings.TrimSpace(commit.OID)
		if oid == "" {
			continue
		}
		out = append(out, map[string]any{"oid": oid})
	}
	return out
}

func issueURL(state *dtu.State, repo *dtu.Repository, issue dtu.Issue) string {
	if strings.TrimSpace(issue.URL) != "" {
		return issue.URL
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", repoSlug(state, repo), issue.Number)
}

func prURL(state *dtu.State, repo *dtu.Repository, pr dtu.PullRequest) string {
	if strings.TrimSpace(pr.URL) != "" {
		return pr.URL
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repoSlug(state, repo), pr.Number)
}

func repoSlug(_ *dtu.State, repo *dtu.Repository) string {
	if repo == nil {
		return ""
	}
	return repo.Slug()
}

func encodeLabels(labels []string) []map[string]string {
	out := make([]map[string]string, 0, len(labels))
	for _, label := range labels {
		out = append(out, map[string]string{"name": label})
	}
	return out
}

func ghCheckState(state dtu.CheckState) string {
	switch state {
	case dtu.CheckStateSuccess:
		return "SUCCESS"
	case dtu.CheckStateFailure:
		return "FAILURE"
	case dtu.CheckStateCancelled:
		return "CANCELLED"
	case dtu.CheckStateSkipped:
		return "SKIPPED"
	default:
		return "PENDING"
	}
}

func prState(pr dtu.PullRequest) string {
	if pr.State != "" {
		return strings.ToUpper(string(pr.State))
	}
	return "OPEN"
}

func prMergeable(pr dtu.PullRequest) string {
	if trimmed := strings.TrimSpace(pr.Mergeable); trimmed != "" {
		return trimmed
	}
	return "MERGEABLE"
}

func prReviewDecision(pr dtu.PullRequest) string {
	return strings.TrimSpace(pr.ReviewDecision)
}

func encodeStatusCheckRollup(checks []dtu.Check) []map[string]string {
	out := make([]map[string]string, 0, len(checks))
	for _, check := range checks {
		status := "COMPLETED"
		if check.State == dtu.CheckStatePending {
			status = "IN_PROGRESS"
		}
		out = append(out, map[string]string{
			"conclusion": ghCheckState(check.State),
			"status":     status,
		})
	}
	return out
}

func encodeReviewRequests(requests []string) []map[string]string {
	out := make([]map[string]string, 0, len(requests))
	for _, request := range requests {
		out = append(out, map[string]string{"login": request})
	}
	return out
}

func encodeLatestReviews(reviews []dtu.Review) []map[string]any {
	out := make([]map[string]any, 0, len(reviews))
	for _, review := range reviews {
		out = append(out, map[string]any{
			"author": map[string]string{"login": review.Author},
			"state":  string(review.State),
		})
	}
	return out
}

func encodeReviewThreads(threads []dtu.ReviewThread) []map[string]any {
	out := make([]map[string]any, 0, len(threads))
	for _, thread := range threads {
		out = append(out, map[string]any{"isResolved": thread.IsResolved})
	}
	return out
}

func matchesPRState(pr dtu.PullRequest, want string) bool {
	if want == "" {
		return true
	}
	switch want {
	case string(dtu.PullRequestStateMerged):
		return pr.State == dtu.PullRequestStateMerged || pr.Merged
	case string(dtu.PullRequestStateOpen):
		return pr.State == dtu.PullRequestStateOpen && !pr.Merged
	case string(dtu.PullRequestStateClosed):
		return pr.State == dtu.PullRequestStateClosed && !pr.Merged
	default:
		return false
	}
}

func mutateLabels(existing, add, remove []string) []string {
	labelSet := make(map[string]struct{}, len(existing)+len(add))
	for _, label := range existing {
		if trimmed := strings.TrimSpace(label); trimmed != "" {
			labelSet[trimmed] = struct{}{}
		}
	}
	for _, label := range add {
		if trimmed := strings.TrimSpace(label); trimmed != "" {
			labelSet[trimmed] = struct{}{}
		}
	}
	for _, label := range remove {
		delete(labelSet, strings.TrimSpace(label))
	}
	out := make([]string, 0, len(labelSet))
	for label := range labelSet {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func requireValue(args []string, index int, flag string) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("%s requires a value", flag)
	}
	return args[index+1], index + 1, nil
}

func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsAll(values []string, wants []string) bool {
	for _, want := range wants {
		if !contains(values, want) {
			return false
		}
	}
	return true
}

func matchesSearchQuery(title, body, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(title + "\n" + body)
	return strings.Contains(haystack, query)
}

func nextIssueNumber(repo *dtu.Repository) int {
	next := 1
	for _, issue := range repo.Issues {
		if issue.Number >= next {
			next = issue.Number + 1
		}
	}
	return next
}

func ensureRepoLabels(repo *dtu.Repository, labels []string) {
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		found := false
		for _, existing := range repo.Labels {
			if existing.Name == label {
				found = true
				break
			}
		}
		if !found {
			repo.Labels = append(repo.Labels, dtu.Label{Name: label})
		}
	}
}

func waitContext(ctx context.Context, store *dtu.Store, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	if store != nil {
		advanced, err := advanceStoreClock(store, delay)
		if err != nil {
			return err
		}
		if advanced {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}
	}
	return dtu.RuntimeSleep(ctx, delay)
}

func advanceStoreClock(store *dtu.Store, delay time.Duration) (bool, error) {
	if store == nil {
		return false, nil
	}
	if err := store.Update(func(state *dtu.State) error {
		clock, err := dtu.ResolveClock(state.Clock, dtu.SystemClock{})
		if err != nil {
			return fmt.Errorf("advance DTU shim clock: resolve clock: %w", err)
		}
		state.Clock = dtu.ClockState{
			Now: clock.Now().UTC().Add(delay).Format(time.RFC3339Nano),
		}
		return nil
	}); err != nil {
		return false, fmt.Errorf("advance DTU shim clock: %w", err)
	}
	return true, nil
}

func currentStoreClock(store *dtu.Store) (dtu.Clock, error) {
	if store == nil {
		return dtu.SystemClock{}, nil
	}
	state, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("resolve DTU shim clock: %w", err)
	}
	clock, err := dtu.ResolveClock(state.Clock, dtu.SystemClock{})
	if err != nil {
		return nil, fmt.Errorf("resolve DTU shim clock: %w", err)
	}
	return clock, nil
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return writeError(stderr, 1, fmt.Errorf("marshal JSON: %w", err))
	}
	_, _ = stdout.Write(data)
	return 0
}

func writeError(stderr io.Writer, code int, err error) int {
	if err != nil && stderr != nil {
		_, _ = io.WriteString(stderr, err.Error())
		if !strings.HasSuffix(err.Error(), "\n") {
			_, _ = io.WriteString(stderr, "\n")
		}
	}
	return code
}
