// Package anomaly detects and persists vessel-level run anomalies.
// It derives anomalies from a finished (or in-flight) vessel's persistent
// fields, appending them to <stateDir>/anomalies.jsonl so downstream review
// loops and the status command can surface them to operators.
package anomaly

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/queue"
)

// AnomalyType identifies the class of anomaly.
type AnomalyType string

const (
	// AnomalyGateExhausted means a command gate's retries were all consumed and
	// the phase ultimately failed.
	AnomalyGateExhausted AnomalyType = "gate_failure_exhausted"

	// AnomalyLabelTimeout means a vessel was waiting for a label gate and timed
	// out before the label was applied.
	AnomalyLabelTimeout AnomalyType = "label_gate_timeout"

	// AnomalyPolicyDenial means phase output or the vessel's error field contains
	// a pattern suggesting a policy or permission denial from the claude/gh CLI.
	AnomalyPolicyDenial AnomalyType = "policy_denial"
)

// Severity describes how urgent an anomaly is.
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Anomaly is one detected run anomaly for a vessel.
type Anomaly struct {
	Type      AnomalyType `json:"type"`
	Severity  Severity    `json:"severity"`
	VesselID  string      `json:"vessel_id"`
	Timestamp time.Time   `json:"timestamp"`
	Detail    string      `json:"detail"`
}

// policyDenialKeywords is the curated list of case-insensitive substrings that
// indicate a policy or permission rejection from the claude or gh CLI.
var policyDenialKeywords = []string{
	"permission denied",
	"not allowed",
	"policy violation",
	"disallowed",
	"tool call disallowed",
}

// DetectFromVessel derives anomalies from a vessel's persistent fields.
// It does not modify the vessel or touch the queue.
// The caller is responsible for emitting the returned anomalies via Emit.
func DetectFromVessel(v queue.Vessel) []Anomaly {
	var out []Anomaly
	now := time.Now().UTC()

	// GateExhausted: command gate ran out of retries before the phase could pass.
	if v.GateRetries <= 0 && v.FailedPhase != "" {
		out = append(out, Anomaly{
			Type:      AnomalyGateExhausted,
			Severity:  SeverityCritical,
			VesselID:  v.ID,
			Timestamp: now,
			Detail:    fmt.Sprintf("phase %q exhausted gate retries: %s", v.FailedPhase, v.GateOutput),
		})
	}

	// LabelTimeout: vessel was waiting for a label gate and timed out.
	if v.State == queue.StateTimedOut {
		detail := "label gate timed out"
		if v.WaitingFor != "" {
			detail = fmt.Sprintf("label gate timed out waiting for %q", v.WaitingFor)
		}
		out = append(out, Anomaly{
			Type:      AnomalyLabelTimeout,
			Severity:  SeverityCritical,
			VesselID:  v.ID,
			Timestamp: now,
			Detail:    detail,
		})
	}

	// PolicyDenial: scan GateOutput and Error for denial keywords.
	combined := strings.ToLower(v.GateOutput + " " + v.Error)
	for _, kw := range policyDenialKeywords {
		if strings.Contains(combined, kw) {
			out = append(out, Anomaly{
				Type:      AnomalyPolicyDenial,
				Severity:  SeverityWarning,
				VesselID:  v.ID,
				Timestamp: now,
				Detail:    fmt.Sprintf("policy denial detected in vessel output: %q", kw),
			})
			break // one PolicyDenial per vessel is enough
		}
	}

	return out
}

// Emit appends anomalies to <stateDir>/anomalies.jsonl, one JSON object per
// line. If anomalies is empty, Emit is a no-op. Concurrent callers are safe
// because O_APPEND writes under 4096 bytes are atomic on POSIX.
func Emit(stateDir string, anomalies []Anomaly) error {
	if len(anomalies) == 0 {
		return nil
	}

	path := filepath.Join(stateDir, "anomalies.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open anomalies file: %w", err)
	}
	defer f.Close()

	for _, a := range anomalies {
		b, err := json.Marshal(a)
		if err != nil {
			return fmt.Errorf("marshal anomaly: %w", err)
		}
		b = append(b, '\n')
		if _, err := f.Write(b); err != nil {
			return fmt.Errorf("write anomaly: %w", err)
		}
	}
	return nil
}

// Read reads all anomaly records from <stateDir>/anomalies.jsonl.
// If the file does not exist, Read returns an empty slice and no error.
// Malformed lines are skipped with a warning logged via stderr.
func Read(stateDir string) ([]Anomaly, error) {
	path := filepath.Join(stateDir, "anomalies.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Anomaly{}, nil
		}
		return nil, fmt.Errorf("open anomalies file: %w", err)
	}
	defer f.Close()

	var anomalies []Anomaly
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var a Anomaly
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			// Skip malformed lines rather than failing the whole read.
			fmt.Fprintf(os.Stderr, "warn: skipping malformed anomaly entry: %v\n", err)
			continue
		}
		anomalies = append(anomalies, a)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read anomalies file: %w", err)
	}
	return anomalies, nil
}
