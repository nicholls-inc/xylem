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
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otelglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/nicholls-inc/xylem/cli/internal/config"
)

const daemonLogFileName = "daemon.log"

var (
	commandLoggerCleanup func()
	commandLoggerMu      sync.Mutex
	registerFinalizeOnce sync.Once
	buildOTelLogHandler  = newOTelLogHandler
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
	cleanups := make([]func(), 0, 3)
	restoreLogger := slog.Default()
	restoreProvider := otelglobal.GetLoggerProvider()

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

	if cfg.ObservabilityEnabled() && cfg.Observability.Endpoint != "" {
		handler, provider, err := buildOTelLogHandler(cfg)
		if err != nil {
			slog.New(newTextHandler(os.Stderr)).Warn("initialize OTel log exporter", "error", err)
		} else {
			otelglobal.SetLoggerProvider(provider)
			handlers = append(handlers, handler)
			cleanups = append(cleanups, func() {
				otelglobal.SetLoggerProvider(restoreProvider)
				if err := provider.Shutdown(context.Background()); err != nil {
					slog.New(newTextHandler(os.Stderr)).Warn("shutdown OTel log provider", "error", err)
				}
			})
		}
	}

	logger := slog.New(newMultiHandler(handlers...))
	cleanup := func() {
		slog.SetDefault(restoreLogger)
		runCleanups(cleanups)
	}

	return logger, cleanup, nil
}

func daemonLogPath(stateDir string) string {
	return filepath.Join(stateDir, daemonLogFileName)
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

func newOTelLogHandler(cfg *config.Config) (slog.Handler, *sdklog.LoggerProvider, error) {
	opts := []otlploggrpc.Option{
		otlploggrpc.WithEndpoint(cfg.Observability.Endpoint),
	}
	if cfg.Observability.Insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}

	exporter, err := otlploggrpc.New(context.Background(), opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize OTel log exporter: %w", err)
	}

	res := resource.NewWithAttributes("", attribute.String("service.name", "xylem"))
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	handler := otelslog.NewHandler("github.com/nicholls-inc/xylem/cli/cmd/xylem", otelslog.WithLoggerProvider(provider))
	return handler, provider, nil
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
