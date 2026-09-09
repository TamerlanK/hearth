package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/TamerlanK/hearth/internal/server"
	"github.com/spf13/cobra"
)

const metricsShutdownGrace = 5 * time.Second

func newServeCmd() *cobra.Command {
	var (
		cfg         server.Config
		addr        string
		metricsAddr string
		logFormat   string
		logLevel    string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the chat server",
		Long:  "Run the chat server. Stops cleanly on SIGINT or SIGTERM, telling every client first.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			logger, err := newLogger(cmd.ErrOrStderr(), logFormat, logLevel)
			if err != nil {
				return err
			}
			cfg.Logger = logger

			ln, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}

			srv := server.New(cfg)
			logger.Info("hearth listening",
				"event", "startup",
				"addr", ln.Addr().String(),
				"metrics_addr", metricsAddr,
				"log_format", logFormat,
				"log_level", logLevel,
				"config", srv.Config())

			metricsCtx, stopMetrics := context.WithCancel(ctx)
			defer stopMetrics()
			metricsDone, err := startMetrics(metricsCtx, logger, metricsAddr)
			if err != nil {
				return err
			}

			serveErr := srv.Serve(ctx, ln)
			stopMetrics()
			<-metricsDone
			if serveErr != nil {
				return fmt.Errorf("serve: %w", serveErr)
			}
			logger.Info("hearth stopped", "event", "shutdown")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&addr, "addr", ":4000", "address to listen on")
	f.StringVar(&metricsAddr, "metrics-addr", "", "address to serve /metrics and /healthz on (empty = off)")
	f.StringVar(&logFormat, "log-format", "text", "log format: text or json")
	f.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn or error")
	f.IntVar(&cfg.MaxClients, "max-clients", 100, "maximum concurrent connections (0 = unlimited)")
	f.IntVar(&cfg.MaxClientsPerIP, "max-clients-per-ip", 10, "maximum concurrent connections from one address (0 = unlimited)")
	f.DurationVar(&cfg.IdleTimeout, "idle-timeout", 5*time.Minute, "disconnect clients silent for this long (0 = never)")
	f.IntVar(&cfg.HistorySize, "history-size", 50, "messages replayed when joining a room")
	f.StringVar(&cfg.DefaultRoom, "default-room", "general", "room every client starts in")
	f.IntVar(&cfg.MaxRooms, "max-rooms", 64, "maximum rooms that can exist at once (0 = unlimited)")
	f.Float64Var(&cfg.MessagesPerSecond, "rate", 5, "sustained lines per second per client (0 = unlimited)")
	f.IntVar(&cfg.Burst, "burst", 10, "lines a client may send back to back before the rate applies")
	f.IntVar(&cfg.MaxDropsInARow, "max-drops", 100, "consecutive dropped events before a client is disconnected (0 = never)")
	return cmd
}

func newLogger(w io.Writer, format, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("parse --log-level %q: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unknown --log-format %q, want text or json", format)
	}
}

func startMetrics(ctx context.Context, log *slog.Logger, addr string) (<-chan struct{}, error) {
	done := make(chan struct{})
	if addr == "" {
		close(done)
		return done, nil
	}
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s for metrics: %w", addr, err)
	}
	srv := &http.Server{Handler: server.MetricsHandler(log), ReadHeaderTimeout: 5 * time.Second}
	log.Info("metrics listening", "event", "startup", "metrics_addr", ln.Addr().String())

	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsShutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("shut down metrics server", "event", "shutdown", "err", err)
		}
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server failed", "event", "metrics_error", "err", err)
		}
	}()
	return done, nil
}
