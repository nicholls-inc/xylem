package daemonhealth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const fileName = "daemon-health.json"

type Level string

const (
	LevelOK       Level = "ok"
	LevelWarning  Level = "warning"
	LevelCritical Level = "critical"
)

type Check struct {
	Code      string    `json:"code"`
	Level     Level     `json:"level"`
	Message   string    `json:"message"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Snapshot struct {
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Binary        string    `json:"binary,omitempty"`
	LastUpgradeAt time.Time `json:"last_upgrade_at,omitempty"`
	Checks        []Check   `json:"checks,omitempty"`
}

func Path(stateDir string) string {
	return filepath.Join(stateDir, "state", fileName)
}

func Save(stateDir string, snapshot Snapshot) error {
	path := Path(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save daemon health: create dir: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("save daemon health: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save daemon health: write: %w", err)
	}
	return nil
}

func Load(stateDir string) (*Snapshot, error) {
	data, err := os.ReadFile(Path(stateDir))
	if err != nil {
		return nil, fmt.Errorf("load daemon health: read: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("load daemon health: unmarshal: %w", err)
	}
	if snapshot.Checks == nil {
		snapshot.Checks = []Check{}
	}
	return &snapshot, nil
}
