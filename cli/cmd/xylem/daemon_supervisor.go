package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

const daemonRestartDelay = 10 * time.Second

var daemonEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type daemonSupervisorProcess interface {
	PID() int
	Signal(os.Signal) error
	Wait() error
}

type daemonSupervisorLaunch struct {
	ExecutablePath string
	Args           []string
	Env            []string
	WorkingDir     string
}

type daemonSupervisorStarter func(daemonSupervisorLaunch) (daemonSupervisorProcess, error)
type daemonSupervisorSleep func(context.Context, time.Duration) error

type daemonSupervisorOptions struct {
	Cfg            *config.Config
	ConfigPath     string
	ExecutablePath string
	WorkingDir     string
	Start          daemonSupervisorStarter
	Sleep          daemonSupervisorSleep
}

type execDaemonSupervisorProcess struct {
	cmd *exec.Cmd
}

func (p *execDaemonSupervisorProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execDaemonSupervisorProcess) Signal(sig os.Signal) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(sig)
}

func (p *execDaemonSupervisorProcess) Wait() error {
	if p == nil || p.cmd == nil {
		return fmt.Errorf("wait daemon process: nil command")
	}
	return p.cmd.Wait()
}

func newDaemonSupervisorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon-supervisor",
		Short: "Restart the daemon after unexpected exits",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdDaemonSupervisor(deps.cfg)
		},
	}
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon and disable supervisor restarts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdDaemonStop(deps.cfg)
		},
	}
}

func cmdDaemonSupervisor(cfg *config.Config) error {
	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve xylem executable: %w", err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runDaemonSupervisor(ctx, daemonSupervisorOptions{
		Cfg:            cfg,
		ConfigPath:     viper.GetString("config"),
		ExecutablePath: executablePath,
		WorkingDir:     workingDir,
		Start:          startDaemonSupervisorProcess,
		Sleep:          daemonSupervisorSleepWithContext,
	})
}

func runDaemonSupervisor(ctx context.Context, opts daemonSupervisorOptions) error {
	if opts.Cfg == nil {
		return fmt.Errorf("run daemon supervisor: nil config")
	}
	if opts.ExecutablePath == "" {
		return fmt.Errorf("run daemon supervisor: executable path is required")
	}
	if opts.WorkingDir == "" {
		return fmt.Errorf("run daemon supervisor: working directory is required")
	}
	if opts.Start == nil {
		return fmt.Errorf("run daemon supervisor: start function is required")
	}
	if opts.Sleep == nil {
		opts.Sleep = daemonSupervisorSleepWithContext
	}
	if err := os.MkdirAll(opts.Cfg.StateDir, 0o755); err != nil {
		return fmt.Errorf("run daemon supervisor: create state dir: %w", err)
	}
	if err := clearDaemonSupervisorStopRequest(opts.Cfg); err != nil {
		return err
	}

	unlock, err := acquireDaemonSupervisorLock(daemonSupervisorPIDPath(opts.Cfg))
	if err != nil {
		return err
	}
	defer unlock()

	restartCount := 0
	for {
		if daemonSupervisorStopRequested(opts.Cfg) {
			return clearDaemonSupervisorStopRequest(opts.Cfg)
		}

		env, err := daemonSupervisorProcessEnv(opts.WorkingDir)
		if err != nil {
			return err
		}
		launch := daemonSupervisorLaunch{
			ExecutablePath: opts.ExecutablePath,
			Args:           daemonSupervisorCommandArgs(opts.ConfigPath),
			Env:            env,
			WorkingDir:     opts.WorkingDir,
		}
		proc, err := opts.Start(launch)
		if err != nil {
			if daemonSupervisorStopRequested(opts.Cfg) || ctx.Err() != nil {
				if clearErr := clearDaemonSupervisorStopRequest(opts.Cfg); clearErr != nil {
					return clearErr
				}
				return nil
			}
			restartCount++
			slog.Warn("daemon supervisor failed to start daemon; retrying",
				"restart_count", restartCount,
				"retry_in", daemonRestartDelay,
				"error", err)
			if err := opts.Sleep(ctx, daemonRestartDelay); err != nil {
				if daemonSupervisorStopRequested(opts.Cfg) || ctx.Err() != nil {
					if clearErr := clearDaemonSupervisorStopRequest(opts.Cfg); clearErr != nil {
						return clearErr
					}
					return nil
				}
				return err
			}
			continue
		}

		slog.Info("daemon supervisor started daemon",
			"pid", proc.PID(),
			"restart_count", restartCount)

		waitErr := waitForDaemonSupervisorProcess(ctx, proc)
		if daemonSupervisorStopRequested(opts.Cfg) || ctx.Err() != nil {
			if clearErr := clearDaemonSupervisorStopRequest(opts.Cfg); clearErr != nil {
				return clearErr
			}
			return nil
		}

		restartCount++
		slog.Warn("daemon supervisor restarting daemon after exit",
			"restart_count", restartCount,
			"retry_in", daemonRestartDelay,
			"error", waitErr)
		if err := opts.Sleep(ctx, daemonRestartDelay); err != nil {
			if daemonSupervisorStopRequested(opts.Cfg) || ctx.Err() != nil {
				if clearErr := clearDaemonSupervisorStopRequest(opts.Cfg); clearErr != nil {
					return clearErr
				}
				return nil
			}
			return err
		}
	}
}

func cmdDaemonStop(cfg *config.Config) error {
	result, err := stopDaemonProcesses(cfg, signalProcessFromPIDFile)
	if err != nil {
		return err
	}
	switch {
	case result.supervisorStopped && result.daemonStopped:
		fmt.Printf("Stopping daemon pid %d and supervisor pid %d.\n", result.daemonPID, result.supervisorPID)
	case result.supervisorStopped:
		fmt.Printf("Stopping supervisor pid %d.\n", result.supervisorPID)
	case result.daemonStopped:
		fmt.Printf("Stopping daemon pid %d.\n", result.daemonPID)
	default:
		fmt.Println("Daemon not running.")
	}
	return nil
}

type daemonStopResult struct {
	daemonPID         int
	supervisorPID     int
	daemonStopped     bool
	supervisorStopped bool
}

type daemonProcessSignaler func(pidPath string, sig syscall.Signal) (int, bool, error)

func stopDaemonProcesses(cfg *config.Config, signaler daemonProcessSignaler) (daemonStopResult, error) {
	if cfg == nil {
		return daemonStopResult{}, fmt.Errorf("stop daemon: nil config")
	}
	if signaler == nil {
		return daemonStopResult{}, fmt.Errorf("stop daemon: nil process signaler")
	}

	supervisorPID, supervisorStopped, err := signaler(daemonSupervisorPIDPath(cfg), syscall.Signal(0))
	if err != nil {
		return daemonStopResult{}, err
	}
	if supervisorStopped {
		if err := requestDaemonSupervisorStop(cfg); err != nil {
			return daemonStopResult{}, err
		}
	}

	daemonPID, daemonStopped, err := signaler(daemonPIDPath(cfg), syscall.SIGTERM)
	if err != nil {
		return daemonStopResult{}, err
	}
	if supervisorStopped {
		supervisorPID, supervisorStopped, err = signaler(daemonSupervisorPIDPath(cfg), syscall.SIGTERM)
		if err != nil {
			return daemonStopResult{}, err
		}
	} else if err := clearDaemonSupervisorStopRequest(cfg); err != nil {
		return daemonStopResult{}, err
	}

	return daemonStopResult{
		daemonPID:         daemonPID,
		supervisorPID:     supervisorPID,
		daemonStopped:     daemonStopped,
		supervisorStopped: supervisorStopped,
	}, nil
}

func waitForDaemonSupervisorProcess(ctx context.Context, proc daemonSupervisorProcess) error {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- proc.Wait()
	}()

	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		if err := proc.Signal(syscall.SIGTERM); err != nil && !isMissingProcessError(err) {
			return fmt.Errorf("stop daemon process %d: %w", proc.PID(), err)
		}
		select {
		case err := <-waitCh:
			return err
		case <-time.After(drainShutdownTimeout):
			return ctx.Err()
		}
	}
}

func startDaemonSupervisorProcess(launch daemonSupervisorLaunch) (daemonSupervisorProcess, error) {
	cmd := exec.Command(launch.ExecutablePath, launch.Args...)
	cmd.Dir = launch.WorkingDir
	cmd.Env = launch.Env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start daemon process: %w", err)
	}
	return &execDaemonSupervisorProcess{cmd: cmd}, nil
}

func daemonSupervisorProcessEnv(workingDir string) ([]string, error) {
	envFile, err := loadDaemonSupervisorEnvFile(daemonSupervisorEnvFilePath(workingDir))
	if err != nil {
		return nil, err
	}
	return append(os.Environ(), envFile...), nil
}

func daemonSupervisorCommandArgs(configPath string) []string {
	args := make([]string, 0, 3)
	if strings.TrimSpace(configPath) != "" {
		args = append(args, "--config", configPath)
	}
	args = append(args, "daemon")
	return args
}

func daemonSupervisorEnvFilePath(workingDir string) string {
	return filepath.Join(workingDir, ".env")
}

func loadDaemonSupervisorEnvFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load daemon env file %q: %w", path, err)
	}
	defer file.Close()

	env := make([]string, 0)
	scanner := bufio.NewScanner(file)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		key, value, ok, err := parseDaemonEnvLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("load daemon env file %q line %d: %w", path, lineNum, err)
		}
		if !ok {
			continue
		}
		env = append(env, key+"="+value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("load daemon env file %q: scan: %w", path, err)
	}
	return env, nil
}

func parseDaemonEnvLine(line string) (key, value string, ok bool, err error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false, nil
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq < 0 {
		return "", "", false, fmt.Errorf("expected KEY=VALUE assignment")
	}
	key = strings.TrimSpace(trimmed[:eq])
	if !daemonEnvKeyPattern.MatchString(key) {
		return "", "", false, fmt.Errorf("invalid env key %q", key)
	}
	value, err = parseDaemonEnvValue(strings.TrimSpace(trimmed[eq+1:]))
	if err != nil {
		return "", "", false, err
	}
	return key, value, true, nil
}

func parseDaemonEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '"':
		unquoted, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted env value: %w", err)
		}
		return unquoted, nil
	case '\'':
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted env value")
		}
		return raw[1 : len(raw)-1], nil
	default:
		return stripDaemonEnvInlineComment(raw), nil
	}
}

func stripDaemonEnvInlineComment(raw string) string {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '#' {
			continue
		}
		if i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t' {
			return strings.TrimSpace(raw[:i])
		}
	}
	return strings.TrimSpace(raw)
}

func daemonSupervisorSleepWithContext(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func daemonPIDPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon.pid")
}

func daemonSupervisorPIDPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-supervisor.pid")
}

func daemonSupervisorStopPath(cfg *config.Config) string {
	return config.RuntimePath(cfg.StateDir, "daemon-supervisor.stop")
}

func daemonSupervisorStopRequested(cfg *config.Config) bool {
	_, err := os.Stat(daemonSupervisorStopPath(cfg))
	return err == nil
}

func requestDaemonSupervisorStop(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("request daemon supervisor stop: nil config")
	}
	if err := os.MkdirAll(filepath.Dir(daemonSupervisorStopPath(cfg)), 0o755); err != nil {
		return fmt.Errorf("request daemon supervisor stop: create state dir: %w", err)
	}
	if err := os.WriteFile(daemonSupervisorStopPath(cfg), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
		return fmt.Errorf("request daemon supervisor stop: write stop marker: %w", err)
	}
	return nil
}

func clearDaemonSupervisorStopRequest(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("clear daemon supervisor stop: nil config")
	}
	if err := os.Remove(daemonSupervisorStopPath(cfg)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear daemon supervisor stop: %w", err)
	}
	return nil
}

func signalProcessFromPIDFile(pidPath string, sig syscall.Signal) (int, bool, error) {
	pid, err := readPIDFile(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false, fmt.Errorf("signal process from %q: find pid %d: %w", pidPath, pid, err)
	}
	if err := proc.Signal(sig); err != nil {
		if isMissingProcessError(err) {
			if removeErr := os.Remove(pidPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return pid, false, fmt.Errorf("signal process from %q: remove stale pid file: %w", pidPath, removeErr)
			}
			return pid, false, nil
		}
		return pid, false, fmt.Errorf("signal process from %q: signal pid %d: %w", pidPath, pid, err)
	}
	return pid, true, nil
}

func readPIDFile(pidPath string) (int, error) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("read pid file %q: parse pid: %w", pidPath, err)
	}
	return pid, nil
}

func isMissingProcessError(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}
