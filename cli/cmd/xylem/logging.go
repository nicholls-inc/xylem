package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

const daemonLogFileName = "daemon.log"

var (
	commandLoggerCleanup func()
	commandLoggerMu      sync.Mutex
	registerFinalizeOnce sync.Once
)

func init() {
	slog.SetDefault(newTextLogger(os.Stderr))
}

func registerCommandLoggerFinalizer() {
	registerFinalizeOnce.Do(func() {
		cobra.OnFinalize(func() {
			commandLoggerMu.Lock()
			cleanup := commandLoggerCleanup
			commandLoggerCleanup = nil
			commandLoggerMu.Unlock()
			if cleanup != nil {
				cleanup()
			}
		})
	})
}

func configureCommandLogger(cmd *cobra.Command, cfg *config.Config) error {
	logger, cleanup, err := newConfiguredLogger(cfg, loggerOptions{
		daemonFile: cmd.Name() == "daemon",
	})
	if err != nil {
		return err
	}

	commandLoggerMu.Lock()
	if commandLoggerCleanup != nil {
		commandLoggerCleanup()
	}
	commandLoggerCleanup = cleanup
	commandLoggerMu.Unlock()

	slog.SetDefault(logger)
	return nil
}

type loggerOptions struct {
	daemonFile bool
}

func newConfiguredLogger(cfg *config.Config, opts loggerOptions) (*slog.Logger, func(), error) {
	handlers := []slog.Handler{newTextHandler(os.Stderr)}
	cleanups := make([]func(), 0, 1)
	restoreLogger := slog.Default()

	if opts.daemonFile {
		file, err := openDaemonLogFile(cfg.StateDir)
		if err != nil {
			return nil, nil, err
		}
		handlers = append(handlers, newTextHandler(file))
		cleanups = append(cleanups, func() {
			_ = file.Close()
		})
	}

	logger := slog.New(newMultiHandler(handlers...))
	cleanup := func() {
		slog.SetDefault(restoreLogger)
		runCleanups(cleanups)
	}

	return logger, cleanup, nil
}

func daemonLogPath(stateDir string) string {
	return config.RuntimePath(stateDir, daemonLogFileName)
}

func openDaemonLogFile(stateDir string) (*os.File, error) {
	logPath := daemonLogPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create daemon log directory: %w", err)
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open daemon log file %q: %w", logPath, err)
	}
	return file, nil
}

func newTextLogger(w io.Writer) *slog.Logger {
	return slog.New(newTextHandler(w))
}

func newTextHandler(w io.Writer) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{})
}

func runCleanups(cleanups []func()) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers ...slog.Handler) slog.Handler {
	filtered := make([]slog.Handler, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			filtered = append(filtered, handler)
		}
	}
	return &multiHandler{handlers: filtered}
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	for i, handler := range h.handlers {
		r := record
		if i < len(h.handlers)-1 {
			r = record.Clone()
		}
		err = errors.Join(err, handler.Handle(ctx, r))
	}
	return err
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithAttrs(attrs))
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithGroup(name))
	}
	return &multiHandler{handlers: handlers}
}
