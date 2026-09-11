package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/spf13/cobra"
)

var errNoEcho = errors.New("the server closed the connection before confirming the message")

type queryOpts struct {
	name    string
	room    string
	timeout time.Duration
	asJSON  bool
}

func (o *queryOpts) bind(cmd *cobra.Command, room bool) {
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "", "name to connect as; default is the remembered name, then your OS user name")
	f.DurationVar(&o.timeout, "timeout", 10*time.Second, "time allowed for the whole command")
	if room {
		f.StringVar(&o.room, "room", "", "room to act in (default the server's default room)")
	}
}

func (o *queryOpts) session(cmd *cobra.Command, args []string) (context.Context, context.CancelFunc, *client.Client, []string, error) {
	addr, name, err := resolveTarget(cmd, addrArg(args), o.name)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if name == "" {
		return nil, nil, nil, nil, errors.New("--name is required (or set HEARTH_NAME)")
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	c, err := client.Dial(ctx, addr, client.Options{Name: name, Room: o.room, DialTimeout: o.timeout})
	if err != nil {
		cancel()
		stop()
		if errors.Is(err, client.ErrNameTaken) {
			return nil, nil, nil, nil, fmt.Errorf("%w: %q is in use on %s, pass --name", err, name, addr)
		}
		return nil, nil, nil, nil, err
	}
	rest := args
	if len(addrArg(args)) == 1 {
		rest = args[1:]
	}
	return ctx, func() { cancel(); stop() }, c, rest, nil
}

func addrArg(args []string) []string {
	if len(args) > 0 && looksLikeAddr(args[0]) {
		return args[:1]
	}
	return nil
}

func looksLikeAddr(s string) bool {
	_, _, err := net.SplitHostPort(s)
	return err == nil
}

func newSendCmd() *cobra.Command {
	var o queryOpts
	var to string
	cmd := &cobra.Command{
		Use:   "send [host:port] [text...]",
		Short: "Send one message and exit",
		Long: `Connect, say the text, wait for the server to echo it back, disconnect.

The text is the remaining arguments joined by spaces; with none, every line
of standard input is sent as its own message. The exit status is 0 only once
the server has accepted each message. Meant for cron jobs, CI and scripts.`,
		Example: `  hearth send chat.example.com:4000 --room ops "deploy finished"
  hearth send --to alice "your build is green"
  tail -f app.log | grep ERROR | hearth send --name logbot --room alerts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop, c, rest, err := o.session(cmd, args)
			if err != nil {
				return err
			}
			defer stop()
			defer closeQuietly(cmd, c)
			if len(rest) > 0 {
				return deliver(ctx, c, to, strings.Join(rest, " "))
			}
			sc := bufio.NewScanner(cmd.InOrStdin())
			sc.Buffer(make([]byte, protocol.MaxLineBytes), protocol.MaxLineBytes)
			for sc.Scan() {
				if line := strings.TrimSpace(sc.Text()); line != "" {
					if err := deliver(ctx, c, to, line); err != nil {
						return err
					}
				}
			}
			if err := sc.Err(); err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			return nil
		},
	}
	o.bind(cmd, true)
	cmd.Flags().StringVar(&to, "to", "", "send a private message to this user instead of to the room")
	return cmd
}

func deliver(ctx context.Context, c *client.Client, to, text string) error {
	var err error
	if to != "" {
		err = c.PrivMsg(ctx, to, text)
	} else {
		err = c.Say(ctx, text)
	}
	if err != nil {
		return err
	}
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				return errNoEcho
			}
			switch {
			case e.Kind == protocol.Error:
				return &client.ServerError{Text: e.Text}
			case to == "" && e.Kind == protocol.Msg && e.From == c.Name():
				return nil
			case to != "" && e.Kind == protocol.PrivMsg && e.From == c.Name():
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("waiting for the server to confirm %q: %w", text, ctx.Err())
		}
	}
}

func newWhoCmd() *cobra.Command {
	var o queryOpts
	cmd := &cobra.Command{
		Use:   "who [host:port]",
		Short: "List who is online in a room and exit",
		Example: `  hearth who chat.example.com:4000
  hearth who --room ops --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop, c, _, err := o.session(cmd, args)
			if err != nil {
				return err
			}
			defer stop()
			defer closeQuietly(cmd, c)
			names, err := c.Who(ctx)
			if err != nil {
				return err
			}
			names = withoutSelf(names, c.Name())
			w := cmd.OutOrStdout()
			if o.asJSON {
				return writeJSON(w, struct {
					Room  string   `json:"room"`
					Names []string `json:"names"`
				}{c.Room(), names})
			}
			for _, n := range names {
				fmt.Fprintln(w, n)
			}
			return nil
		},
	}
	o.bind(cmd, true)
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print a JSON object instead of one name per line")
	return cmd
}

func newRoomsCmd() *cobra.Command {
	var o queryOpts
	cmd := &cobra.Command{
		Use:     "rooms [host:port]",
		Short:   "List the rooms and their sizes and exit",
		Example: `  hearth rooms chat.example.com:4000 --json`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop, c, _, err := o.session(cmd, args)
			if err != nil {
				return err
			}
			defer stop()
			defer closeQuietly(cmd, c)
			rooms, err := c.Rooms(ctx)
			if err != nil {
				return err
			}
			for i := range rooms {
				if rooms[i].Name == c.Room() {
					rooms[i].Members = max(0, rooms[i].Members-1)
				}
			}
			w := cmd.OutOrStdout()
			if o.asJSON {
				return writeJSON(w, rooms)
			}
			for _, r := range rooms {
				fmt.Fprintf(w, "%s %d\n", r.Name, r.Members)
			}
			return nil
		},
	}
	o.bind(cmd, false)
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print a JSON array instead of one room per line")
	return cmd
}

func withoutSelf(names []string, me string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != me {
			out = append(out, n)
		}
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func closeQuietly(cmd *cobra.Command, c *client.Client) {
	if err := c.Close(); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "hearth:", err)
	}
}
