package gapreport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	StatusWired          = "wired"
	StatusDormant        = "dormant"
	StatusNotImplemented = "not-implemented"
)

type Snapshot struct {
	Version      int          `json:"version"`
	Repo         string       `json:"repo"`
	GeneratedAt  string       `json:"generated_at"`
	Capabilities []Capability `json:"capabilities"`
}

type Capability struct {
	Key            string         `json:"key"`
	Name           string         `json:"name"`
	Layer          string         `json:"layer"`
	Status         string         `json:"status"`
	Summary        string         `json:"summary"`
	Recommendation string         `json:"recommendation,omitempty"`
	Priority       int            `json:"priority"`
	SpecSections   []string       `json:"spec_sections"`
	CodeEvidence   []CodeEvidence `json:"code_evidence"`
}

type CodeEvidence struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end,omitempty"`
	Summary   string `json:"summary"`
}

type Delta struct {
	GeneratedAt   string            `json:"generated_at"`
	Repo          string            `json:"repo"`
	Previous      SnapshotSummary   `json:"previous"`
	Current       SnapshotSummary   `json:"current"`
	NewGaps       []CapabilityDelta `json:"new_gaps"`
	Improvements  []CapabilityDelta `json:"improvements"`
	Regressions   []CapabilityDelta `json:"regressions"`
	UnchangedGaps []CapabilityDelta `json:"unchanged_gaps"`
}

type SnapshotSummary struct {
	GeneratedAt string         `json:"generated_at"`
	Counts      map[string]int `json:"counts"`
}

type CapabilityDelta struct {
	Key            string         `json:"key"`
	Name           string         `json:"name"`
	Layer          string         `json:"layer"`
	Priority       int            `json:"priority"`
	PreviousStatus string         `json:"previous_status,omitempty"`
	CurrentStatus  string         `json:"current_status"`
	Summary        string         `json:"summary"`
	Recommendation string         `json:"recommendation,omitempty"`
	SpecSections   []string       `json:"spec_sections"`
	CodeEvidence   []CodeEvidence `json:"code_evidence"`
}

func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot %q: %w", path, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("parse snapshot %q: %w", path, err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("validate snapshot %q: %w", path, err)
	}
	return &snapshot, nil
}

func (s *Snapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("snapshot must not be nil")
	}
	if s.Version <= 0 {
		return fmt.Errorf("version must be greater than 0")
	}
	if strings.TrimSpace(s.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(s.GeneratedAt) == "" {
		return fmt.Errorf("generated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, s.GeneratedAt); err != nil {
		return fmt.Errorf("generated_at must be RFC3339: %w", err)
	}
	if len(s.Capabilities) == 0 {
		return fmt.Errorf("at least one capability is required")
	}
	seen := make(map[string]struct{}, len(s.Capabilities))
	for i := range s.Capabilities {
		if err := s.Capabilities[i].Validate(); err != nil {
			return fmt.Errorf("capabilities[%d]: %w", i, err)
		}
		if _, ok := seen[s.Capabilities[i].Key]; ok {
			return fmt.Errorf("duplicate capability key %q", s.Capabilities[i].Key)
		}
		seen[s.Capabilities[i].Key] = struct{}{}
	}
	return nil
}

func (c *Capability) Validate() error {
	if strings.TrimSpace(c.Key) == "" {
		return fmt.Errorf("key is required")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(c.Layer) == "" {
		return fmt.Errorf("layer is required")
	}
	switch c.Status {
	case StatusWired, StatusDormant, StatusNotImplemented:
	default:
		return fmt.Errorf("status %q is invalid", c.Status)
	}
	if strings.TrimSpace(c.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if c.Priority < 0 {
		return fmt.Errorf("priority must be non-negative")
	}
	if len(c.SpecSections) == 0 {
		return fmt.Errorf("spec_sections must not be empty")
	}
	if len(c.CodeEvidence) == 0 {
		return fmt.Errorf("code_evidence must not be empty")
	}
	for i := range c.CodeEvidence {
		if err := c.CodeEvidence[i].Validate(); err != nil {
			return fmt.Errorf("code_evidence[%d]: %w", i, err)
		}
	}
	return nil
}

func (e *CodeEvidence) Validate() error {
	if strings.TrimSpace(e.Path) == "" {
		return fmt.Errorf("path is required")
	}
	if e.LineStart <= 0 {
		return fmt.Errorf("line_start must be greater than 0")
	}
	if e.LineEnd < 0 {
		return fmt.Errorf("line_end must be non-negative")
	}
	if e.LineEnd > 0 && e.LineEnd < e.LineStart {
		return fmt.Errorf("line_end must be greater than or equal to line_start")
	}
	if strings.TrimSpace(e.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

func Diff(previous, current *Snapshot) (*Delta, error) {
	if previous == nil || current == nil {
		return nil, fmt.Errorf("previous and current snapshots are required")
	}
	if previous.Repo != current.Repo {
		return nil, fmt.Errorf("repo mismatch: %q vs %q", previous.Repo, current.Repo)
	}

	previousCaps := make(map[string]Capability, len(previous.Capabilities))
	for _, capability := range previous.Capabilities {
		previousCaps[capability.Key] = capability
	}

	delta := &Delta{
		GeneratedAt: current.GeneratedAt,
		Repo:        current.Repo,
		Previous: SnapshotSummary{
			GeneratedAt: previous.GeneratedAt,
			Counts:      snapshotCounts(previous),
		},
		Current: SnapshotSummary{
			GeneratedAt: current.GeneratedAt,
			Counts:      snapshotCounts(current),
		},
	}

	for _, capability := range current.Capabilities {
		prev, found := previousCaps[capability.Key]
		item := newCapabilityDelta(prev, capability, found)
		switch {
		case !found && capability.Status != StatusWired:
			delta.NewGaps = append(delta.NewGaps, item)
		case found && statusRank(capability.Status) > statusRank(prev.Status):
			delta.Improvements = append(delta.Improvements, item)
		case found && statusRank(capability.Status) < statusRank(prev.Status):
			delta.NewGaps = append(delta.NewGaps, item)
			delta.Regressions = append(delta.Regressions, item)
		case capability.Status != StatusWired:
			delta.UnchangedGaps = append(delta.UnchangedGaps, item)
		}
	}

	sortCapabilityDeltas(delta.NewGaps)
	sortCapabilityDeltas(delta.Improvements)
	sortCapabilityDeltas(delta.Regressions)
	sortCapabilityDeltas(delta.UnchangedGaps)
	return delta, nil
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %q: %w", path, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json for %q: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write json %q: %w", path, err)
	}
	return nil
}

func CopySnapshot(dst string, snapshot *Snapshot) error {
	return WriteJSON(dst, snapshot)
}

func ImprovementCount(delta *Delta) int {
	if delta == nil {
		return 0
	}
	return len(delta.Improvements)
}

func statusRank(status string) int {
	switch status {
	case StatusWired:
		return 2
	case StatusDormant:
		return 1
	default:
		return 0
	}
}

func snapshotCounts(snapshot *Snapshot) map[string]int {
	counts := map[string]int{
		StatusWired:          0,
		StatusDormant:        0,
		StatusNotImplemented: 0,
	}
	for _, capability := range snapshot.Capabilities {
		counts[capability.Status]++
	}
	return counts
}

func newCapabilityDelta(previous Capability, current Capability, found bool) CapabilityDelta {
	item := CapabilityDelta{
		Key:            current.Key,
		Name:           current.Name,
		Layer:          current.Layer,
		Priority:       current.Priority,
		CurrentStatus:  current.Status,
		Summary:        current.Summary,
		Recommendation: current.Recommendation,
		SpecSections:   append([]string(nil), current.SpecSections...),
		CodeEvidence:   append([]CodeEvidence(nil), current.CodeEvidence...),
	}
	if found {
		item.PreviousStatus = previous.Status
	}
	return item
}

func sortCapabilityDeltas(items []CapabilityDelta) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].Key < items[j].Key
	})
}
