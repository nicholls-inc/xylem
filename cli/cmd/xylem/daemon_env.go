package main

import (
	"fmt"
	"os"
	"strings"
)

func loadDaemonStartupEnv(workingDir, envFileName string) error {
	envFile, err := loadDaemonSupervisorEnvFile(daemonSupervisorEnvFilePath(workingDir, envFileName))
	if err != nil {
		return fmt.Errorf("load daemon startup env: %w", err)
	}
	if err := applyDaemonEnvEntries(envFile, os.Setenv); err != nil {
		return fmt.Errorf("apply daemon startup env: %w", err)
	}
	return nil
}

func applyDaemonEnvEntries(env []string, setEnv func(string, string) error) error {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return fmt.Errorf("invalid environment entry %q", entry)
		}
		if err := setEnv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}
