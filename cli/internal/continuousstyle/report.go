package continuousstyle

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
	ReportVersion = 1
)

type Report struct {
	Version       int       `json:"version"`
	Repo          string    `json:"repo"`
	GeneratedAt   string    `json:"generated_at"`
	TargetSurface string    `json:"target_surface"`
	Findings      []Finding `json:"findings"`
}

type Finding struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Category       string     `json:"category"`
	Summary        string     `json:"summary"`
	Recommendation string     `json:"recommendation"`
	Priority       int        `json:"priority"`
	Paths          []string   `json:"paths"`
	Evidence       []Evidence `json:"evidence"`
}

type Evidence struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end,omitempty"`
	Summary   string `json:"summary"`
}

func LoadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read report %q: %w", path, err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parse report %q: %w", path, err)
	}
	if err := report.Validate(); err != nil {
		return nil, fmt.Errorf("validate report %q: %w", path, err)
	}
	return &report, nil
}

func (r *Report) Validate() error {
	if r == nil {
		return fmt.Errorf("report must not be nil")
	}
	if r.Version <= 0 {
		return fmt.Errorf("version must be greater than 0")
	}
	if strings.TrimSpace(r.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(r.GeneratedAt) == "" {
		return fmt.Errorf("generated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, r.GeneratedAt); err != nil {
		return fmt.Errorf("generated_at must be RFC3339: %w", err)
	}
	if strings.TrimSpace(r.TargetSurface) == "" {
		return fmt.Errorf("target_surface is required")
	}
	seen := make(map[string]struct{}, len(r.Findings))
	for i := range r.Findings {
		if err := r.Findings[i].Validate(); err != nil {
			return fmt.Errorf("findings[%d]: %w", i, err)
		}
		if _, ok := seen[r.Findings[i].ID]; ok {
			return fmt.Errorf("duplicate finding id %q", r.Findings[i].ID)
		}
		seen[r.Findings[i].ID] = struct{}{}
	}
	return nil
}

func (f *Finding) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(f.Category) == "" {
		return fmt.Errorf("category is required")
	}
	if strings.TrimSpace(f.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if strings.TrimSpace(f.Recommendation) == "" {
		return fmt.Errorf("recommendation is required")
	}
	if f.Priority < 0 {
		return fmt.Errorf("priority must be non-negative")
	}
	if len(f.Paths) == 0 {
		return fmt.Errorf("paths must not be empty")
	}
	for i, path := range f.Paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("paths[%d] is required", i)
		}
	}
	if len(f.Evidence) == 0 {
		return fmt.Errorf("evidence must not be empty")
	}
	for i := range f.Evidence {
		if err := f.Evidence[i].Validate(); err != nil {
			return fmt.Errorf("evidence[%d]: %w", i, err)
		}
	}
	return nil
}

func (e *Evidence) Validate() error {
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

func SortedFindings(report *Report) []Finding {
	if report == nil {
		return nil
	}
	items := append([]Finding(nil), report.Findings...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
	return items
}
