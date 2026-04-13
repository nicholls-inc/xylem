package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	"github.com/nicholls-inc/xylem/cli/internal/worktree"
)

type appDeps struct {
	cfg *config.Config
	q   *queue.Queue
	wt  *worktree.Manager
}

var deps *appDeps

func newRootCmd() *cobra.Command {
	registerCommandLoggerFinalizer()

	cmd := &cobra.Command{
		Use:           "xylem",
		Short:         "Autonomous Claude Code session scheduler",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			commandPath := cmd.CommandPath()
			if cmd.Name() == "init" || cmd.Name() == "shim-dispatch" || cmd.Name() == "version" || commandPath == "xylem dtu" || strings.HasPrefix(commandPath, "xylem dtu ") || commandPath == "xylem bootstrap" || strings.HasPrefix(commandPath, "xylem bootstrap ") || commandPath == "xylem config" || strings.HasPrefix(commandPath, "xylem config ") {
				return nil
			}

			// visualize (and its subcommands), review, and daemon stop are
			// local-only commands that only parse config, workflow YAML, and
			// local state; they don't shell out to git or gh.
			// continuous-improvement select is another local-only helper used
			// by a command phase. harden inventory/score/track are the same.
			skipTooling := cmd.Name() == "visualize" ||
				strings.HasPrefix(commandPath, "xylem visualize") ||
				commandPath == "xylem workflow validate" ||
				strings.HasPrefix(commandPath, "xylem workflow ") ||
				cmd.Name() == "review" ||
				cmd.Name() == "recovery" ||
				strings.HasPrefix(commandPath, "xylem recovery") ||
				commandPath == "xylem continuous-improvement select" ||
				commandPath == "xylem harden inventory" ||
				commandPath == "xylem harden score" ||
				commandPath == "xylem harden track" ||
				commandPath == "xylem field-report generate" ||
				commandPath == "xylem audit" ||
				strings.HasPrefix(commandPath, "xylem audit ") ||
				commandPath == "xylem daemon stop" ||
				commandPath == "xylem daemon reload" ||
				commandPath == "xylem eval" ||
				strings.HasPrefix(commandPath, "xylem eval ")

			if !skipTooling {
				if _, err := exec.LookPath("git"); err != nil {
					return fmt.Errorf("error: git not found on PATH")
				}
			}

			configPath := findConfigPath(viper.GetString("config"))
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("error loading config %s: %w", configPath, err)
			}
			if err := configureCommandLogger(cmd, cfg); err != nil {
				return fmt.Errorf("configure command logger: %w", err)
			}

			// Only require gh if a GitHub source is configured
			if !skipTooling && hasGitHubSource(cfg) {
				if _, err := exec.LookPath("gh"); err != nil {
					return fmt.Errorf("error: gh not found on PATH (required for github source)")
				}
			}

			queueFile := config.RuntimePath(cfg.StateDir, "queue.jsonl")
			wt := worktree.New(".", &realCmdRunner{})
			wt.DefaultBranch = cfg.DefaultBranch
			deps = &appDeps{
				cfg: cfg,
				q:   queue.New(queueFile),
				wt:  wt,
			}
			return nil
		},
	}

	cmd.PersistentFlags().String("config", ".xylem.yml", "Config file path")
	viper.BindPFlag("config", cmd.PersistentFlags().Lookup("config")) //nolint:errcheck
	viper.SetEnvPrefix("XYLEM")
	viper.AutomaticEnv()

	cmd.AddCommand(
		newInitCmd(),
		newBootstrapCmd(),
		newConfigCmd(),
		newWorkflowCmd(),
		newContinuousImprovementCmd(),
		newContinuousSimplicityCmd(),
		newReleaseCadenceCmd(),
		newHardenCmd(),
		newDtuCmd(),
		newShimDispatchCmd(),
		newScanCmd(),
		newDrainCmd(),
		newReviewCmd(),
		newGapReportCmd(),
		newLessonsCmd(),
		newRecoveryCmd(),
		newEnqueueCmd(),
		newStatusCmd(),
		newPauseCmd(),
		newResumeCmd(),
		newCancelCmd(),
		newCleanupCmd(),
		newDoctorCmd(),
		newReportCmd(),
		newDaemonCmd(),
		newDaemonSupervisorCmd(),
		newRetryCmd(),
		newVisualizeCmd(),
		newVersionCmd(),
		newFieldReportCmd(),
		newAuditCmd(),
		newEvalCmd(),
	)

	return cmd
}

// findConfigPath resolves a config file path by walking up from CWD.
// Only walks up for bare filenames (no directory component) that don't exist
// at the current working directory. Absolute paths and explicit relative paths
// (e.g. "../foo.yml") are returned unchanged. Returns the original path if
// no ancestor contains the file, preserving the error message from config.Load.
func findConfigPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path // already accessible at CWD
	}
	// Only walk up for bare filenames — explicit relative paths like
	// "../custom.yml" are left unchanged so the caller gets a clear error.
	if filepath.Base(path) != path {
		return path
	}
	dir, err := os.Getwd()
	if err != nil {
		return path // CWD unavailable; surface error at config.Load
	}
	for {
		candidate := filepath.Join(dir, path)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return path // not found; return original so error message is useful
}

func hasGitHubSource(cfg *config.Config) bool {
	for _, src := range cfg.Sources {
		switch src.Type {
		case "github", "github-pr", "github-pr-events", "github-merge", "scheduled":
			return true
		}
	}
	return false
}
