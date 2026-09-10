package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/spf13/cobra"
)

func newConnectCmd() *cobra.Command {
	var opts client.Options
	var plain bool
	cmd := &cobra.Command{
		Use:   "connect <host:port>",
		Short: "Join a chat server",
		Long: `Join a chat server as --name and start talking.

Type a line to send it to the room; lines starting with / are commands
(/help lists them). Ctrl+C or end of input leaves cleanly.`,
		Example: `  hearth connect localhost:4000 --name alice
  echo "hello from a script" | hearth connect localhost:4000 --plain --name bot`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Name == "" {
				return errors.New("--name is required (or set HEARTH_NAME)")
			}
			if !plain {
				fmt.Fprintln(cmd.ErrOrStderr(), "hearth: the terminal UI is not built yet, using the plain line client")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			c, err := client.Dial(ctx, args[0], opts)
			if err != nil {
				return err
			}
			defer func() {
				if err := c.Close(); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "hearth:", err)
				}
			}()
			return runPlain(ctx, c, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Name, "name", os.Getenv("USER"), "display name, 1-20 characters without spaces")
	f.StringVar(&opts.Room, "room", "", "room to join right after connecting (default the server's default room)")
	f.BoolVar(&plain, "plain", false, "use the minimal line client on stdin/stdout instead of the terminal UI; good for scripts and debugging")
	f.DurationVar(&opts.DialTimeout, "timeout", 10*time.Second, "time allowed to dial and complete the name handshake")
	return cmd
}

func runPlain(ctx context.Context, c *client.Client, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() {
		var codec protocol.TextCodec
		for e := range c.Events() {
			if err := codec.Encode(out, e); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	go func() {
		var codec protocol.TextCodec
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			cmd, err := codec.Decode(sc.Bytes())
			if err != nil {
				fmt.Fprintln(out, "!", err)
				continue
			}
			if err := c.Send(ctx, cmd); err != nil {
				done <- err
				return
			}
		}
		done <- sc.Err()
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if err == nil || errors.Is(err, client.ErrClosed) || ctx.Err() != nil {
			return nil
		}
		return err
	}
}
