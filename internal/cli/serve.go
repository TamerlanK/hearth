package cli

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TamerlanK/hearth/internal/server"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var (
		addr        string
		maxClients  int
		idleTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the chat server",
		Long:  "Run the chat server. Stops cleanly on SIGINT or SIGTERM, telling every client first.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			ln, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}
			logger.Info("listening", "addr", ln.Addr().String(), "max_clients", maxClients, "idle_timeout", idleTimeout)

			srv := server.New(server.Config{
				MaxClients:  maxClients,
				IdleTimeout: idleTimeout,
				Logger:      logger,
			})
			if err := srv.Serve(ctx, ln); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			logger.Info("stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":4000", "address to listen on")
	cmd.Flags().IntVar(&maxClients, "max-clients", 100, "maximum concurrent connections (0 = unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 5*time.Minute, "disconnect clients silent for this long (0 = never)")
	return cmd
}
