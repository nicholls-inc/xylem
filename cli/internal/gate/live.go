package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"github.com/nicholls-inc/xylem/cli/internal/workflow"
)

var liveArtifactNamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// LiveGateRunner executes live verification gates.
type LiveGateRunner interface {
	Run(ctx context.Context, runner Runner, req LiveRequest) (*LiveGateResult, error)
}

// LiveRequest carries the runtime context required to execute a live gate.
type LiveRequest struct {
	StateDir    string
	VesselID    string
	PhaseName   string
	WorktreeDir string
	Gate        *workflow.Gate
}

// LiveGateResult is the outcome of a live verification gate.
type LiveGateResult struct {
	Mode       string           `json:"mode"`
	Passed     bool             `json:"passed"`
	Output     string           `json:"output"`
	ReportPath string           `json:"report_path,omitempty"`
	Steps      []LiveStepResult `json:"steps"`
}

// LiveStepResult captures one live verification step.
type LiveStepResult struct {
	Name       string              `json:"name"`
	Mode       string              `json:"mode"`
	Passed     bool                `json:"passed"`
	Message    string              `json:"message,omitempty"`
	DurationMS int64               `json:"duration_ms"`
	Artifacts  []evidence.Artifact `json:"artifacts,omitempty"`
}

type artifactSaver func(name string, data []byte, mediaType, description string) (evidence.Artifact, error)

// BrowserVerifier executes browser live-gate scripts.
type BrowserVerifier interface {
	Verify(ctx context.Context, worktreeDir, phaseName string, cfg *workflow.LiveBrowserGate, save artifactSaver) ([]LiveStepResult, error)
}

// LiveVerifier is the default live-gate runtime.
type LiveVerifier struct {
	HTTPClient *http.Client
	Browser    BrowserVerifier
}

// NewLiveVerifier constructs the default live-gate runtime.
func NewLiveVerifier() *LiveVerifier {
	return &LiveVerifier{
		HTTPClient: &http.Client{},
		Browser:    chromedpBrowserVerifier{},
	}
}

// Run executes one live gate and persists its evidence report.
func (v *LiveVerifier) Run(ctx context.Context, runner Runner, req LiveRequest) (*LiveGateResult, error) {
	if req.Gate == nil || req.Gate.Live == nil {
		return nil, fmt.Errorf("run live gate: live gate config is required")
	}
	if v == nil {
		v = NewLiveVerifier()
	}

	save := func(name string, data []byte, mediaType, description string) (evidence.Artifact, error) {
		artifactName := filepath.ToSlash(filepath.Join(req.PhaseName, name))
		return evidence.SaveArtifact(req.StateDir, req.VesselID, artifactName, data, mediaType, description)
	}

	runCtx := ctx
	if req.Gate.Live.Timeout != "" {
		timeout, err := time.ParseDuration(req.Gate.Live.Timeout)
		if err != nil {
			return nil, fmt.Errorf("run live gate: parse timeout: %w", err)
		}
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var (
		steps []LiveStepResult
		err   error
	)
	switch req.Gate.Live.Mode {
	case "http":
		steps, err = v.runHTTP(runCtx, req.Gate.Live.HTTP, save)
	case "browser":
		if v.Browser == nil {
			return nil, fmt.Errorf("run live gate: browser verifier is not configured")
		}
		steps, err = v.Browser.Verify(runCtx, req.WorktreeDir, req.PhaseName, req.Gate.Live.Browser, save)
	case "command+assert":
		steps, err = v.runCommandAssert(runCtx, runner, req.WorktreeDir, req.Gate.Live.CommandAssert, save)
	default:
		return nil, fmt.Errorf("run live gate: unsupported mode %q", req.Gate.Live.Mode)
	}
	if err != nil {
		return nil, err
	}

	result := &LiveGateResult{
		Mode:   req.Gate.Live.Mode,
		Passed: true,
		Steps:  steps,
	}
	for _, step := range steps {
		if !step.Passed {
			result.Passed = false
			break
		}
	}
	result.Output = summarizeLiveSteps(result.Mode, result.Steps)

	reportArtifact, err := save("live-gate.json", marshalLiveReport(req.PhaseName, result), "application/json", "Live gate report")
	if err != nil {
		return nil, fmt.Errorf("run live gate: save report: %w", err)
	}
	result.ReportPath = reportArtifact.Path

	return result, nil
}

func (v *LiveVerifier) runHTTP(ctx context.Context, cfg *workflow.LiveHTTPGate, save artifactSaver) ([]LiveStepResult, error) {
	client := v.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	steps := make([]LiveStepResult, 0, len(cfg.Steps))
	for i, stepCfg := range cfg.Steps {
		stepName := liveStepName(stepCfg.Name, "http", i)
		stepCtx := ctx
		if stepCfg.Timeout != "" {
			timeout, err := time.ParseDuration(stepCfg.Timeout)
			if err != nil {
				return nil, fmt.Errorf("run live gate http %q: parse timeout: %w", stepName, err)
			}
			var cancel context.CancelFunc
			stepCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		reqURL, err := resolveLiveURL(cfg.BaseURL, stepCfg.URL)
		if err != nil {
			return nil, fmt.Errorf("run live gate http %q: resolve url: %w", stepName, err)
		}

		method := stepCfg.Method
		if method == "" {
			method = http.MethodGet
		}
		httpReq, err := http.NewRequestWithContext(stepCtx, method, reqURL, strings.NewReader(stepCfg.Body))
		if err != nil {
			return nil, fmt.Errorf("run live gate http %q: create request: %w", stepName, err)
		}
		for key, value := range stepCfg.Headers {
			httpReq.Header.Set(key, value)
		}

		startedAt := time.Now()
		resp, reqErr := client.Do(httpReq)
		duration := time.Since(startedAt)
		step := LiveStepResult{
			Name:       stepName,
			Mode:       "http",
			Passed:     false,
			DurationMS: duration.Milliseconds(),
		}

		var (
			respBody []byte
			status   string
			headers  http.Header
		)
		if reqErr != nil {
			step.Message = reqErr.Error()
		} else {
			status = resp.Status
			headers = resp.Header.Clone()
			respBody, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("run live gate http %q: read response: %w", stepName, err)
			}

			passed, message := evaluateHTTPStep(stepCfg, resp.StatusCode, headers, respBody, duration)
			step.Passed = passed
			step.Message = message
		}

		traceArtifact, err := save(
			filepath.Join(sanitizeArtifactName(stepName)+".http.txt"),
			buildHTTPTrace(httpReq, status, headers, respBody, duration, reqErr),
			"text/plain",
			fmt.Sprintf("HTTP trace for %s", stepName),
		)
		if err != nil {
			return nil, fmt.Errorf("run live gate http %q: save trace: %w", stepName, err)
		}
		step.Artifacts = append(step.Artifacts, traceArtifact)
		steps = append(steps, step)
		if !step.Passed {
			break
		}
	}

	return steps, nil
}

func (v *LiveVerifier) runCommandAssert(ctx context.Context, runner Runner, dir string, cfg *workflow.LiveCommandAssertGate, save artifactSaver) ([]LiveStepResult, error) {
	stepCtx := ctx
	if cfg.Timeout != "" {
		timeout, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("run live gate command+assert: parse timeout: %w", err)
		}
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	startedAt := time.Now()
	stdout, commandPassed, err := RunCommandGate(stepCtx, runner, dir, cfg.Run)
	if err != nil {
		return nil, fmt.Errorf("run live gate command+assert: %w", err)
	}

	step := LiveStepResult{
		Name:       "command_assert",
		Mode:       "command+assert",
		Passed:     commandPassed,
		DurationMS: time.Since(startedAt).Milliseconds(),
	}

	if step.Passed {
		passed, message := evaluateCommandAssertions(cfg, []byte(stdout))
		step.Passed = passed
		step.Message = message
	} else {
		step.Message = "command exited non-zero"
	}

	artifact, saveErr := save(
		"command_assert.stdout.txt",
		[]byte(stdout),
		"text/plain",
		"Command+assert stdout",
	)
	if saveErr != nil {
		return nil, fmt.Errorf("run live gate command+assert: save stdout: %w", saveErr)
	}
	step.Artifacts = append(step.Artifacts, artifact)

	return []LiveStepResult{step}, nil
}

type chromedpBrowserVerifier struct{}

func (chromedpBrowserVerifier) Verify(ctx context.Context, worktreeDir, phaseName string, cfg *workflow.LiveBrowserGate, save artifactSaver) ([]LiveStepResult, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if cfg.Headless != nil && !*cfg.Headless {
		opts = append(opts, chromedp.Flag("headless", false))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	steps := make([]LiveStepResult, 0, len(cfg.Steps))
	for i, stepCfg := range cfg.Steps {
		stepName := liveStepName(stepCfg.Name, "browser", i)
		stepCtx := browserCtx
		if stepCfg.Timeout != "" {
			timeout, err := time.ParseDuration(stepCfg.Timeout)
			if err != nil {
				return nil, fmt.Errorf("run live gate browser %q: parse timeout: %w", stepName, err)
			}
			var cancel context.CancelFunc
			stepCtx, cancel = context.WithTimeout(browserCtx, timeout)
			defer cancel()
		}

		startedAt := time.Now()
		step := LiveStepResult{
			Name:   stepName,
			Mode:   "browser",
			Passed: true,
		}

		if err := runBrowserStep(stepCtx, cfg.BaseURL, stepCfg); err != nil {
			step.Passed = false
			step.Message = err.Error()
		}
		step.DurationMS = time.Since(startedAt).Milliseconds()

		artifacts, err := captureBrowserArtifacts(stepCtx, stepName, save)
		if err != nil {
			return nil, fmt.Errorf("run live gate browser %q: capture artifacts: %w", stepName, err)
		}
		step.Artifacts = append(step.Artifacts, artifacts...)
		steps = append(steps, step)
		if !step.Passed {
			break
		}
	}

	return steps, nil
}

func runBrowserStep(ctx context.Context, baseURL string, step workflow.LiveBrowserStep) error {
	switch step.Action {
	case "navigate":
		target, err := resolveLiveURL(baseURL, step.URL)
		if err != nil {
			return fmt.Errorf("resolve navigate URL: %w", err)
		}
		return chromedp.Run(ctx, chromedp.Navigate(target))
	case "click":
		return chromedp.Run(ctx,
			chromedp.WaitVisible(step.Selector, chromedp.ByQuery),
			chromedp.Click(step.Selector, chromedp.ByQuery),
		)
	case "type":
		return chromedp.Run(ctx,
			chromedp.WaitVisible(step.Selector, chromedp.ByQuery),
			chromedp.SetValue(step.Selector, "", chromedp.ByQuery),
			chromedp.SendKeys(step.Selector, step.Value, chromedp.ByQuery),
		)
	case "wait_visible", "assert_visible":
		return chromedp.Run(ctx, chromedp.WaitVisible(step.Selector, chromedp.ByQuery))
	case "assert_text":
		var text string
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(step.Selector, chromedp.ByQuery),
			chromedp.Text(step.Selector, &text, chromedp.ByQuery),
		); err != nil {
			return err
		}
		if !strings.Contains(text, step.Text) {
			return fmt.Errorf("text %q does not contain %q", text, step.Text)
		}
		return nil
	default:
		return fmt.Errorf("unsupported browser action %q", step.Action)
	}
}

func captureBrowserArtifacts(ctx context.Context, stepName string, save artifactSaver) ([]evidence.Artifact, error) {
	var (
		domHTML     string
		screenshot  []byte
		artifacts   []evidence.Artifact
		safeStep    = sanitizeArtifactName(stepName)
		description = fmt.Sprintf("Browser checkpoint for %s", stepName)
	)

	if err := chromedp.Run(ctx,
		chromedp.OuterHTML("html", &domHTML, chromedp.ByQuery),
		chromedp.FullScreenshot(&screenshot, 90),
	); err != nil {
		return nil, err
	}

	domArtifact, err := save(safeStep+".dom.html", []byte(domHTML), "text/html", description+" DOM snapshot")
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, domArtifact)

	screenshotArtifact, err := save(safeStep+".screenshot.png", screenshot, "image/png", description+" screenshot")
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, screenshotArtifact)

	return artifacts, nil
}

func evaluateHTTPStep(step workflow.LiveHTTPStep, statusCode int, headers http.Header, body []byte, duration time.Duration) (bool, string) {
	if step.ExpectStatus > 0 && statusCode != step.ExpectStatus {
		return false, fmt.Sprintf("expected status %d, got %d", step.ExpectStatus, statusCode)
	}
	if step.MaxLatency != "" {
		maxLatency, err := time.ParseDuration(step.MaxLatency)
		if err != nil {
			return false, fmt.Sprintf("invalid max latency %q", step.MaxLatency)
		}
		if duration > maxLatency {
			return false, fmt.Sprintf("expected latency <= %s, got %s", maxLatency, duration)
		}
	}
	for _, header := range step.ExpectHeaders {
		value := headers.Get(header.Name)
		if header.Equals != "" && value != header.Equals {
			return false, fmt.Sprintf("expected header %s=%q, got %q", header.Name, header.Equals, value)
		}
		if header.Regex != "" {
			matched, err := regexp.MatchString(header.Regex, value)
			if err != nil {
				return false, fmt.Sprintf("invalid header regex %q", header.Regex)
			}
			if !matched {
				return false, fmt.Sprintf("expected header %s to match %q, got %q", header.Name, header.Regex, value)
			}
		}
	}
	if step.ExpectBodyRegex != "" {
		matched, err := regexp.MatchString(step.ExpectBodyRegex, string(body))
		if err != nil {
			return false, fmt.Sprintf("invalid body regex %q", step.ExpectBodyRegex)
		}
		if !matched {
			return false, fmt.Sprintf("expected body to match %q", step.ExpectBodyRegex)
		}
	}
	if len(step.ExpectJSON) > 0 {
		passed, message := evaluateJSONAssertions(step.ExpectJSON, body)
		if !passed {
			return false, message
		}
	}
	return true, "all assertions passed"
}

func evaluateCommandAssertions(cfg *workflow.LiveCommandAssertGate, stdout []byte) (bool, string) {
	if cfg.ExpectStdoutRegex != "" {
		matched, err := regexp.MatchString(cfg.ExpectStdoutRegex, string(stdout))
		if err != nil {
			return false, fmt.Sprintf("invalid stdout regex %q", cfg.ExpectStdoutRegex)
		}
		if !matched {
			return false, fmt.Sprintf("expected stdout to match %q", cfg.ExpectStdoutRegex)
		}
	}
	if len(cfg.ExpectJSON) > 0 {
		return evaluateJSONAssertions(cfg.ExpectJSON, stdout)
	}
	return true, "all assertions passed"
}

func evaluateJSONAssertions(checks []workflow.LiveJSONPathCheck, raw []byte) (bool, string) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return false, fmt.Sprintf("parse JSON for assertion: %v", err)
	}

	for _, check := range checks {
		actualValue, exists, err := jsonPathLookup(value, check.Path)
		if err != nil {
			return false, fmt.Sprintf("evaluate JSONPath %q: %v", check.Path, err)
		}
		if check.Exists != nil {
			if exists != *check.Exists {
				return false, fmt.Sprintf("expected JSONPath %q exists=%t, got %t", check.Path, *check.Exists, exists)
			}
			if !exists {
				continue
			}
		}
		if !exists {
			return false, fmt.Sprintf("JSONPath %q did not resolve", check.Path)
		}

		actual := stringifyJSONValue(actualValue)
		if check.Equals != "" && actual != check.Equals {
			return false, fmt.Sprintf("expected JSONPath %q = %q, got %q", check.Path, check.Equals, actual)
		}
		if check.Regex != "" {
			matched, err := regexp.MatchString(check.Regex, actual)
			if err != nil {
				return false, fmt.Sprintf("invalid JSONPath regex %q", check.Regex)
			}
			if !matched {
				return false, fmt.Sprintf("expected JSONPath %q to match %q, got %q", check.Path, check.Regex, actual)
			}
		}
	}

	return true, "all assertions passed"
}

func decodeJSONValue(raw []byte) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func jsonPathLookup(value any, path string) (any, bool, error) {
	if path == "$" {
		return value, true, nil
	}
	if !strings.HasPrefix(path, "$") {
		return nil, false, fmt.Errorf("path must start with '$'")
	}

	current := value
	for i := 1; i < len(path); {
		switch path[i] {
		case '.':
			i++
			start := i
			for i < len(path) && (path[i] == '_' || path[i] == '-' || path[i] == '$' ||
				(path[i] >= 'a' && path[i] <= 'z') || (path[i] >= 'A' && path[i] <= 'Z') || (path[i] >= '0' && path[i] <= '9')) {
				i++
			}
			if start == i {
				return nil, false, fmt.Errorf("missing field after '.'")
			}
			key := path[start:i]
			obj, ok := current.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			next, ok := obj[key]
			if !ok {
				return nil, false, nil
			}
			current = next
		case '[':
			i++
			start := i
			for i < len(path) && path[i] >= '0' && path[i] <= '9' {
				i++
			}
			if start == i || i >= len(path) || path[i] != ']' {
				return nil, false, fmt.Errorf("invalid array index in %q", path)
			}
			idx, err := strconv.Atoi(path[start:i])
			if err != nil {
				return nil, false, fmt.Errorf("parse array index: %w", err)
			}
			i++
			arr, ok := current.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false, nil
			}
			current = arr[idx]
		default:
			return nil, false, fmt.Errorf("unexpected token %q in %q", string(path[i]), path)
		}
	}

	return current, true, nil
}

func stringifyJSONValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return "null"
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}

func resolveLiveURL(baseURL, target string) (string, error) {
	parsedTarget, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	if parsedTarget.IsAbs() {
		return parsedTarget.String(), nil
	}
	if baseURL == "" {
		return "", fmt.Errorf("relative URL %q requires base_url", target)
	}
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	return parsedBase.ResolveReference(parsedTarget).String(), nil
}

func buildHTTPTrace(req *http.Request, status string, headers http.Header, body []byte, duration time.Duration, reqErr error) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s %s\n", req.Method, req.URL.String())
	for key, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\n", key, value)
		}
	}
	if req.Body != nil {
		fmt.Fprintln(&buf)
		if req.GetBody != nil {
			bodyReader, err := req.GetBody()
			if err == nil {
				payload, _ := io.ReadAll(bodyReader)
				buf.Write(payload)
				bodyReader.Close()
			}
		}
	}
	fmt.Fprintf(&buf, "\n\nDuration: %s\n", duration)
	if reqErr != nil {
		fmt.Fprintf(&buf, "Error: %v\n", reqErr)
		return buf.Bytes()
	}
	fmt.Fprintf(&buf, "Status: %s\n", status)
	for key, values := range headers {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\n", key, value)
		}
	}
	fmt.Fprintln(&buf)
	buf.Write(body)
	return buf.Bytes()
}

func summarizeLiveSteps(mode string, steps []LiveStepResult) string {
	for _, step := range steps {
		if !step.Passed {
			if step.Message == "" {
				return fmt.Sprintf("live gate failed (%s): %s", mode, step.Name)
			}
			return fmt.Sprintf("live gate failed (%s): %s: %s", mode, step.Name, step.Message)
		}
	}
	if len(steps) == 0 {
		return fmt.Sprintf("live gate passed (%s)", mode)
	}
	return fmt.Sprintf("live gate passed (%s): %d step(s)", mode, len(steps))
}

func marshalLiveReport(phaseName string, result *LiveGateResult) []byte {
	report := struct {
		Phase      string           `json:"phase"`
		Mode       string           `json:"mode"`
		Passed     bool             `json:"passed"`
		Output     string           `json:"output"`
		Generated  time.Time        `json:"generated_at"`
		ReportPath string           `json:"report_path,omitempty"`
		Steps      []LiveStepResult `json:"steps"`
	}{
		Phase:     phaseName,
		Mode:      result.Mode,
		Passed:    result.Passed,
		Output:    result.Output,
		Generated: time.Now().UTC(),
		Steps:     result.Steps,
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	return data
}

func liveStepName(name, prefix string, idx int) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return fmt.Sprintf("%s-%d", prefix, idx+1)
}

func sanitizeArtifactName(name string) string {
	safe := liveArtifactNamePattern.ReplaceAllString(strings.ToLower(name), "-")
	safe = strings.Trim(safe, "-")
	if safe == "" {
		return "artifact"
	}
	return safe
}
