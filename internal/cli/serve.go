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

const shutdownGrace = 5 * time.Second

var errShutdownTimeout = errors.New("shutdown timed out, exiting with clients still connected")

type serveOpts struct {
	cfg         server.Config
	addr        string
	metricsAddr string
	logFormat   string
	logLevel    string
}

func newServeCmd() (*cobra.Command, *serveOpts) {
	o := &serveOpts{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the chat server",
		Long: `Run the chat server and log to stderr.

Ctrl+C or SIGTERM starts a graceful shutdown: every client is told the server
is going away, and the process waits up to 5s for connections to drain.`,
		Example: `  hearth serve --addr :4000 --metrics-addr :9090
  HEARTH_LOG_FORMAT=json hearth serve --log-level debug`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.addr, "addr", ":4000", "host:port to listen on; omit the host to listen on every interface")
	f.StringVar(&o.metricsAddr, "metrics-addr", "", "host:port to serve Prometheus /metrics and /healthz on; empty disables it")
	f.StringVar(&o.logFormat, "log-format", "text", "log line format: text or json")
	f.StringVar(&o.logLevel, "log-level", "info", "lowest log level to print: debug, info, warn or error")
	f.IntVar(&o.cfg.MaxClients, "max-clients", 100, "maximum concurrent connections; 0 means unlimited")
	f.IntVar(&o.cfg.MaxClientsPerIP, "max-per-ip", 10, "maximum concurrent connections from one address; 0 means unlimited")
	f.DurationVar(&o.cfg.IdleTimeout, "idle-timeout", 5*time.Minute, "disconnect a client silent for this long, e.g. 90s or 5m; 0 means never")
	f.IntVar(&o.cfg.HistorySize, "history", 50, "messages kept per room and replayed to a client that joins it")
	f.StringVar(&o.cfg.DefaultRoom, "default-room", "general", "room every client starts in")
	f.IntVar(&o.cfg.MaxRooms, "max-rooms", 64, "maximum rooms that can exist at once; 0 means unlimited")
	f.Float64Var(&o.cfg.MessagesPerSecond, "rate", 5, "sustained messages per second allowed per client; 0 means unlimited")
	f.IntVar(&o.cfg.Burst, "burst", 10, "messages a client may send back to back before --rate applies")
	f.IntVar(&o.cfg.MaxDropsInARow, "max-drops", 100, "consecutive undeliverable events before a slow client is disconnected; 0 means never")
	return cmd, o
}

func runServe(cmd *cobra.Command, o *serveOpts) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger, err := newLogger(cmd.ErrOrStderr(), o.logFormat, o.logLevel)
	if err != nil {
		return err
	}
	cfg := o.cfg
	cfg.Logger = logger

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", o.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", o.addr, err)
	}

	srv := server.New(cfg)
	logger.Info("hearth listening",
		"event", "startup",
		"addr", ln.Addr().String(),
		"metrics_addr", o.metricsAddr,
		"log_format", o.logFormat,
		"log_level", o.logLevel,
		"config", srv.Config())

	metricsCtx, stopMetrics := context.WithCancel(ctx)
	defer stopMetrics()
	metricsDone, err := startMetrics(metricsCtx, logger, o.metricsAddr)
	if err != nil {
		return err
	}

	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, ln) }()

	var serveErr error
	select {
	case serveErr = <-serveDone:
	case <-ctx.Done():
		logger.Info("shutting down, waiting for clients",
			"event", "shutdown", "clients", srv.ActiveConnections(), "deadline", shutdownGrace)
		select {
		case serveErr = <-serveDone:
		case <-time.After(shutdownGrace):
			logger.Error("shutdown deadline exceeded",
				"event", "shutdown", "clients", srv.ActiveConnections())
			return errShutdownTimeout
		}
	}
	stopMetrics()
	<-metricsDone
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	logger.Info("hearth stopped", "event", "shutdown")
	return nil
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
		return nil, fmt.Errorf("listen on %s for metrics: %w", addr, err)
	}
	srv := &http.Server{Handler: server.MetricsHandler(log), ReadHeaderTimeout: 5 * time.Second}
	log.Info("metrics listening", "event", "startup", "metrics_addr", ln.Addr().String())

	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
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
