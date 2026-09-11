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

	"github.com/TamerlanK/hearth/internal/tui"
	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/spf13/cobra"
)

func newConnectCmd() *cobra.Command {
	var opts client.Options
	var plain, bell bool
	cmd := &cobra.Command{
		Use:   "connect [host:port]",
		Short: "Join a chat server",
		Long: `Join a chat server as --name and start talking.

The server and name of a successful connection are remembered (see --config),
so the next time both can be left out. A name given on the command line or in
HEARTH_NAME wins over the remembered one, which wins over your OS user name.

The terminal UI opens by default and reconnects on its own if the server goes
away. Type a line to send it to the room; lines starting with / are commands
(? or F1 lists them). Ctrl+C or /quit leaves cleanly.

--plain runs a minimal line client on stdin/stdout instead, which never
reconnects and is meant for pipes and scripts.`,
		Example: `  hearth connect localhost:4000 --name alice
  echo "hello from a script" | hearth connect localhost:4000 --plain --name bot`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, name, err := resolveTarget(cmd, args, opts.Name)
			if err != nil {
				return err
			}
			if name == "" {
				return errors.New("--name is required (or set HEARTH_NAME)")
			}
			opts.Name = name
			if !plain {
				if err := requireTerminal(); err != nil {
					return err
				}
				opts.Reconnect = true
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			c, err := client.Dial(ctx, addr, opts)
			if err != nil {
				return err
			}
			defer func() {
				if err := c.Close(); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "hearth:", err)
				}
			}()
			if err := saveRemembered(cmd, remembered{Addr: addr, Name: name}); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "hearth: could not remember the server:", err)
			}
			if plain {
				return runPlain(ctx, c, cmd.InOrStdin(), cmd.OutOrStdout())
			}
			return tui.Run(ctx, c, addr, name, bell)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Name, "name", "", "display name, 1-20 characters without spaces; default is the remembered name, then your OS user name")
	f.StringVar(&opts.Room, "room", "", "room to join right after connecting (default the server's default room)")
	f.BoolVar(&plain, "plain", false, "use the minimal line client on stdin/stdout instead of the terminal UI; good for scripts and debugging")
	f.DurationVar(&opts.DialTimeout, "timeout", 10*time.Second, "time allowed to dial and complete the name handshake")
	f.BoolVar(&bell, "bell", true, "ring the terminal bell when someone mentions you or sends you a private message")
	return cmd
}

func requireTerminal() error {
	info, err := os.Stdout.Stat()
	if err != nil {
		return fmt.Errorf("stat stdout: %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("stdout is not a terminal; use --plain for pipes and scripts")
	}
	return nil
}

func runPlain(ctx context.Context, c *client.Client, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
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
	var err error
	select {
	case <-ctx.Done():
	case err = <-done:
	}
	if cerr := c.Close(); cerr != nil {
		err = errors.Join(err, cerr)
	}
	<-drained
	if err == nil || errors.Is(err, client.ErrClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}
