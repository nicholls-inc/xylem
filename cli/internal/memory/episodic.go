package memory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gofrs/flock"
)

// EpisodicEntry records what happened during a single workflow phase.
type EpisodicEntry struct {
	VesselID   string    `json:"vessel_id"`
	PhaseName  string    `json:"phase_name"`
	RecordedAt time.Time `json:"recorded_at"`
	// Outcome is one of "completed", "failed", or "no-op".
	Outcome   string   `json:"outcome"`
	Summary   string   `json:"summary"`
	Citations []string `json:"citations,omitempty"`
}

// EpisodicStore is an append-only JSONL-backed store for episodic phase
// records. It mirrors the AuditLog pattern in
// cli/internal/intermediary/intermediary.go.
type EpisodicStore struct {
	path     string
	lockPath string
}

// NewEpisodicStore creates an EpisodicStore writing to path.
// The parent directory must already exist (caller's responsibility).
func NewEpisodicStore(path string) *EpisodicStore {
	return &EpisodicStore{
		path:     path,
		lockPath: path + ".lock",
	}
}

// Append writes one EpisodicEntry as a JSONL line under an exclusive flock.
// INV: each Append adds exactly one JSONL line, never modifies existing lines.
func (s *EpisodicStore) Append(entry EpisodicEntry) error {
	lock := flock.New(s.lockPath)
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire episodic store lock: %w", err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			log.Printf("warn: failed to unlock episodic store: %v", err)
		}
	}()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open episodic store: %w", err)
	}
	defer f.Close()

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal episodic entry: %w", err)
	}

	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write episodic entry: %w", err)
	}
	return nil
}

// All reads every entry from the file under a shared flock.
// Returns an empty (non-nil) slice when the file does not exist.
func (s *EpisodicStore) All() ([]EpisodicEntry, error) {
	lock := flock.New(s.lockPath)
	if err := lock.RLock(); err != nil {
		return nil, fmt.Errorf("acquire episodic store read lock: %w", err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			log.Printf("warn: failed to unlock episodic store: %v", err)
		}
	}()

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []EpisodicEntry{}, nil
		}
		return nil, fmt.Errorf("open episodic store: %w", err)
	}
	defer f.Close()

	var entries []EpisodicEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry EpisodicEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			log.Printf("warn: skip malformed episodic entry: %v", err)
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read episodic store: %w", err)
	}
	if entries == nil {
		entries = []EpisodicEntry{}
	}
	return entries, nil
}

// RecentForVessel returns the last n entries whose VesselID matches vesselID.
// If n <= 0 all matching entries are returned.
func (s *EpisodicStore) RecentForVessel(vesselID string, n int) ([]EpisodicEntry, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	var matched []EpisodicEntry
	for _, e := range all {
		if e.VesselID == vesselID {
			matched = append(matched, e)
		}
	}
	if n <= 0 || len(matched) <= n {
		if matched == nil {
			return []EpisodicEntry{}, nil
		}
		return matched, nil
	}
	return matched[len(matched)-n:], nil
}
