package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	Version = "dev"

	Commit = "none"

	Date = "unknown"
)

func Execute() error {
	return newRootCmd().ExecuteContext(context.Background())
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hearth",
		Short: "A TCP chat server and terminal client in a single binary",
		Long: `Hearth is a small chat system that speaks a line-oriented protocol over TCP.

The same binary runs the server and the terminal client, so a single download
is enough to host a room or join one.`,
		SilenceUsage: true,
	}
	root.AddCommand(newServeCmd(), newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit and build date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "hearth %s (commit %s, built %s)\n", Version, Commit, Date)
			return err
		},
	}
}
