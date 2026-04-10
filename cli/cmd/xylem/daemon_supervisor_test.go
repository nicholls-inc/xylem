package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDaemonSupervisorProcess struct {
	pid      int
	signalFn func(os.Signal) error
	waitFn   func() error
}

func (p *fakeDaemonSupervisorProcess) PID() int {
	return p.pid
}

func (p *fakeDaemonSupervisorProcess) Signal(sig os.Signal) error {
	if p.signalFn != nil {
		return p.signalFn(sig)
	}
	return nil
}

func (p *fakeDaemonSupervisorProcess) Wait() error {
	if p.waitFn != nil {
		return p.waitFn()
	}
	return nil
}

func TestRunDaemonSupervisorRestartsAfterUnexpectedExitAndReloadsEnv(t *testing.T) {
	repoDir := t.TempDir()
	cfg := &config.Config{StateDir: filepath.Join(repoDir, ".xylem")}
	envPath := daemonSupervisorEnvFilePath(repoDir)
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(envPath), err)
	}
	if err := os.WriteFile(envPath, []byte("API_TOKEN=first\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", envPath, err)
	}

	logBuf := withBufferedDefaultLogger(t)
	var launches []daemonSupervisorLaunch
	var sleepCalls []time.Duration

	err := runDaemonSupervisor(context.Background(), daemonSupervisorOptions{
		Cfg:            cfg,
		ConfigPath:     ".xylem.yml",
		ExecutablePath: "/tmp/xylem",
		WorkingDir:     repoDir,
		Start: func(launch daemonSupervisorLaunch) (daemonSupervisorProcess, error) {
			launches = append(launches, launch)
			switch len(launches) {
			case 1:
				return &fakeDaemonSupervisorProcess{
					pid: 101,
					waitFn: func() error {
						if err := os.WriteFile(envPath, []byte("API_TOKEN=second\n"), 0o644); err != nil {
							t.Fatalf("WriteFile(%q): %v", envPath, err)
						}
						return nil
					},
				}, nil
			case 2:
				return &fakeDaemonSupervisorProcess{
					pid: 102,
					waitFn: func() error {
						if err := requestDaemonSupervisorStop(cfg); err != nil {
							t.Fatalf("requestDaemonSupervisorStop() error = %v", err)
						}
						return nil
					},
				}, nil
			default:
				t.Fatalf("unexpected extra launch %d", len(launches))
				return nil, nil
			}
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleepCalls = append(sleepCalls, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("runDaemonSupervisor() error = %v", err)
	}

	if len(launches) != 2 {
		t.Fatalf("len(launches) = %d, want 2", len(launches))
	}
	if len(sleepCalls) != 1 || sleepCalls[0] != daemonRestartDelay {
		t.Fatalf("sleepCalls = %v, want [%s]", sleepCalls, daemonRestartDelay)
	}
	if got := daemonEnvValue(launches[0].Env, "API_TOKEN"); got != "first" {
		t.Fatalf("first launch API_TOKEN = %q, want %q", got, "first")
	}
	if got := daemonEnvValue(launches[1].Env, "API_TOKEN"); got != "second" {
		t.Fatalf("second launch API_TOKEN = %q, want %q", got, "second")
	}
	if want := []string{"--config", ".xylem.yml", "daemon"}; strings.Join(launches[0].Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("launch args = %v, want %v", launches[0].Args, want)
	}
	if daemonSupervisorStopRequested(cfg) {
		t.Fatal("stop marker still present after clean supervisor shutdown")
	}
	if !strings.Contains(logBuf.String(), "restart_count=1") {
		t.Fatalf("log output %q does not include restart_count=1", logBuf.String())
	}
}

func TestRunDaemonSupervisorStopRequestedDoesNotRestart(t *testing.T) {
	repoDir := t.TempDir()
	cfg := &config.Config{StateDir: filepath.Join(repoDir, ".xylem")}
	var launches int

	err := runDaemonSupervisor(context.Background(), daemonSupervisorOptions{
		Cfg:            cfg,
		ExecutablePath: "/tmp/xylem",
		WorkingDir:     repoDir,
		Start: func(launch daemonSupervisorLaunch) (daemonSupervisorProcess, error) {
			launches++
			return &fakeDaemonSupervisorProcess{
				pid: 201,
				waitFn: func() error {
					return requestDaemonSupervisorStop(cfg)
				},
			}, nil
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			t.Fatalf("unexpected sleep call with %s", delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("runDaemonSupervisor() error = %v", err)
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
	if daemonSupervisorStopRequested(cfg) {
		t.Fatal("stop marker still present after supervisor exit")
	}
}

func TestStopDaemonProcessesSignalsSupervisorAndDaemon(t *testing.T) {
	repoDir := t.TempDir()
	cfg := &config.Config{StateDir: filepath.Join(repoDir, ".xylem")}

	type signalCall struct {
		path string
		sig  syscall.Signal
	}
	var calls []signalCall
	result, err := stopDaemonProcesses(cfg, func(pidPath string, sig syscall.Signal) (int, bool, error) {
		calls = append(calls, signalCall{path: filepath.Base(pidPath), sig: sig})
		switch filepath.Base(pidPath) {
		case "daemon-supervisor.pid":
			return 22, true, nil
		case "daemon.pid":
			return 11, true, nil
		default:
			return 0, false, nil
		}
	})
	if err != nil {
		t.Fatalf("stopDaemonProcesses() error = %v", err)
	}
	if !result.supervisorStopped || !result.daemonStopped {
		t.Fatalf("result = %+v, want both daemon and supervisor stopped", result)
	}
	if result.supervisorPID != 22 || result.daemonPID != 11 {
		t.Fatalf("result PIDs = %+v, want supervisor=22 daemon=11", result)
	}
	wantCalls := []signalCall{
		{path: "daemon-supervisor.pid", sig: syscall.Signal(0)},
		{path: "daemon.pid", sig: syscall.SIGTERM},
		{path: "daemon-supervisor.pid", sig: syscall.SIGTERM},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("len(calls) = %d, want %d (%v)", len(calls), len(wantCalls), calls)
	}
	for i, want := range wantCalls {
		if calls[i] != want {
			t.Fatalf("calls[%d] = %+v, want %+v", i, calls[i], want)
		}
	}
	if !daemonSupervisorStopRequested(cfg) {
		t.Fatal("expected stop marker to be written when supervisor is running")
	}
}

func TestStopDaemonProcessesWithoutSupervisorClearsStopMarker(t *testing.T) {
	repoDir := t.TempDir()
	cfg := &config.Config{StateDir: filepath.Join(repoDir, ".xylem")}
	if err := requestDaemonSupervisorStop(cfg); err != nil {
		t.Fatalf("requestDaemonSupervisorStop() error = %v", err)
	}

	result, err := stopDaemonProcesses(cfg, func(pidPath string, sig syscall.Signal) (int, bool, error) {
		switch filepath.Base(pidPath) {
		case "daemon-supervisor.pid":
			return 0, false, nil
		case "daemon.pid":
			return 11, true, nil
		default:
			return 0, false, nil
		}
	})
	if err != nil {
		t.Fatalf("stopDaemonProcesses() error = %v", err)
	}
	if result.supervisorStopped {
		t.Fatalf("result.supervisorStopped = true, want false")
	}
	if !result.daemonStopped {
		t.Fatalf("result.daemonStopped = false, want true")
	}
	if daemonSupervisorStopRequested(cfg) {
		t.Fatal("expected stale stop marker to be removed when supervisor is absent")
	}
}

func TestLoadDaemonSupervisorEnvFile(t *testing.T) {
	t.Run("parses exports quotes and inline comments", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		content := strings.Join([]string{
			"# comment",
			"export API_TOKEN=abc123",
			`NAME="xylem daemon"`,
			`SINGLE='quoted value'`,
			`INLINE=value # ignored`,
		}, "\n")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}

		env, err := loadDaemonSupervisorEnvFile(path)
		if err != nil {
			t.Fatalf("loadDaemonSupervisorEnvFile(%q) error = %v", path, err)
		}
		if got := daemonEnvValue(env, "API_TOKEN"); got != "abc123" {
			t.Fatalf("API_TOKEN = %q, want %q", got, "abc123")
		}
		if got := daemonEnvValue(env, "NAME"); got != "xylem daemon" {
			t.Fatalf("NAME = %q, want %q", got, "xylem daemon")
		}
		if got := daemonEnvValue(env, "SINGLE"); got != "quoted value" {
			t.Fatalf("SINGLE = %q, want %q", got, "quoted value")
		}
		if got := daemonEnvValue(env, "INLINE"); got != "value" {
			t.Fatalf("INLINE = %q, want %q", got, "value")
		}
	})

	t.Run("missing file returns empty env", func(t *testing.T) {
		env, err := loadDaemonSupervisorEnvFile(filepath.Join(t.TempDir(), ".env"))
		if err != nil {
			t.Fatalf("loadDaemonSupervisorEnvFile(missing) error = %v", err)
		}
		if len(env) != 0 {
			t.Fatalf("len(env) = %d, want 0", len(env))
		}
	})

	t.Run("invalid assignment returns error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte("not-an-assignment\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
		if _, err := loadDaemonSupervisorEnvFile(path); err == nil {
			t.Fatal("loadDaemonSupervisorEnvFile() error = nil, want error")
		}
	})
}

type daemonSupervisorSmokeResult struct {
	cfg        *config.Config
	logOutput  string
	launches   []daemonSupervisorLaunch
	sleepCalls []time.Duration
}

func TestSmoke_S1_DaemonSupervisorRestartsWithinThirtySecondsAfterUnexpectedExit(t *testing.T) {
	result := runDaemonSupervisorUnexpectedExitSmoke(t)

	require.Len(t, result.launches, 2)
	require.Len(t, result.sleepCalls, 1)
	assert.Equal(t, daemonRestartDelay, result.sleepCalls[0])
	assert.LessOrEqual(t, result.sleepCalls[0], 30*time.Second)
	assert.Equal(t, []string{"--config", ".xylem.yml", "daemon"}, result.launches[0].Args)
}

func TestSmoke_S2_DaemonSupervisorReloadsEnvOnEachRestart(t *testing.T) {
	result := runDaemonSupervisorUnexpectedExitSmoke(t)

	require.Len(t, result.launches, 2)
	assert.Equal(t, "first", daemonEnvValue(result.launches[0].Env, "API_TOKEN"))
	assert.Equal(t, "second", daemonEnvValue(result.launches[1].Env, "API_TOKEN"))
	assert.False(t, daemonSupervisorStopRequested(result.cfg))
}

func TestSmoke_S3_DaemonSupervisorLogsRestartCount(t *testing.T) {
	result := runDaemonSupervisorUnexpectedExitSmoke(t)

	assert.Contains(t, result.logOutput, "daemon supervisor restarting daemon after exit")
	assert.Contains(t, result.logOutput, "restart_count=1")
}

func TestSmoke_S4_ManualDaemonStopDoesNotTriggerRestart(t *testing.T) {
	repoDir := t.TempDir()
	cfg := &config.Config{StateDir: filepath.Join(repoDir, ".xylem")}
	started := make(chan struct{}, 1)
	stopProcess := make(chan struct{})
	supervisorDone := make(chan error, 1)
	var launches []daemonSupervisorLaunch
	var sleepCalls []time.Duration

	go func() {
		supervisorDone <- runDaemonSupervisor(context.Background(), daemonSupervisorOptions{
			Cfg:            cfg,
			ConfigPath:     ".xylem.yml",
			ExecutablePath: "/tmp/xylem",
			WorkingDir:     repoDir,
			Start: func(launch daemonSupervisorLaunch) (daemonSupervisorProcess, error) {
				launches = append(launches, launch)
				started <- struct{}{}
				return &fakeDaemonSupervisorProcess{
					pid: 501,
					waitFn: func() error {
						<-stopProcess
						return nil
					},
				}, nil
			},
			Sleep: func(_ context.Context, delay time.Duration) error {
				sleepCalls = append(sleepCalls, delay)
				return nil
			},
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon supervisor did not start")
	}

	var signals []string
	result, err := stopDaemonProcesses(cfg, func(pidPath string, sig syscall.Signal) (int, bool, error) {
		signals = append(signals, filepath.Base(pidPath)+":"+sig.String())
		switch filepath.Base(pidPath) {
		case "daemon-supervisor.pid":
			if sig == syscall.Signal(0) {
				return 22, true, nil
			}
			close(stopProcess)
			return 22, true, nil
		case "daemon.pid":
			return 11, true, nil
		default:
			return 0, false, nil
		}
	})
	require.NoError(t, err)
	assert.True(t, result.supervisorStopped)
	assert.True(t, result.daemonStopped)
	assert.Equal(t, 22, result.supervisorPID)
	assert.Equal(t, 11, result.daemonPID)

	select {
	case err := <-supervisorDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("daemon supervisor did not exit after stop request")
	}

	assert.Equal(t, []string{
		"daemon-supervisor.pid:signal 0",
		"daemon.pid:terminated",
		"daemon-supervisor.pid:terminated",
	}, signals)
	assert.Len(t, launches, 1)
	assert.Empty(t, sleepCalls)
	assert.False(t, daemonSupervisorStopRequested(cfg))
}

func daemonEnvValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	return value
}

func runDaemonSupervisorUnexpectedExitSmoke(t *testing.T) daemonSupervisorSmokeResult {
	t.Helper()

	repoDir := t.TempDir()
	cfg := &config.Config{StateDir: filepath.Join(repoDir, ".xylem")}
	envPath := daemonSupervisorEnvFilePath(repoDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("API_TOKEN=first\n"), 0o644))

	logBuf := withBufferedDefaultLogger(t)
	var launches []daemonSupervisorLaunch
	var sleepCalls []time.Duration

	err := runDaemonSupervisor(context.Background(), daemonSupervisorOptions{
		Cfg:            cfg,
		ConfigPath:     ".xylem.yml",
		ExecutablePath: "/tmp/xylem",
		WorkingDir:     repoDir,
		Start: func(launch daemonSupervisorLaunch) (daemonSupervisorProcess, error) {
			launches = append(launches, launch)
			switch len(launches) {
			case 1:
				return &fakeDaemonSupervisorProcess{
					pid: 101,
					waitFn: func() error {
						require.NoError(t, os.WriteFile(envPath, []byte("API_TOKEN=second\n"), 0o644))
						return errors.New("exit status 1")
					},
				}, nil
			case 2:
				return &fakeDaemonSupervisorProcess{
					pid: 102,
					waitFn: func() error {
						require.NoError(t, requestDaemonSupervisorStop(cfg))
						return nil
					},
				}, nil
			default:
				t.Fatalf("unexpected extra launch %d", len(launches))
				return nil, nil
			}
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleepCalls = append(sleepCalls, delay)
			return nil
		},
	})
	require.NoError(t, err)

	return daemonSupervisorSmokeResult{
		cfg:        cfg,
		logOutput:  logBuf.String(),
		launches:   launches,
		sleepCalls: sleepCalls,
	}
}
