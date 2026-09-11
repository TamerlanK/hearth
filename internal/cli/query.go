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
	name      string
	room      string
	timeout   time.Duration
	asJSON    bool
	reconnect bool
	dial      dialOpts
}

func (o *queryOpts) bind(cmd *cobra.Command, room bool, timeout time.Duration, timeoutUsage string) {
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "", "name to connect as; default is the remembered name, then your OS user name")
	f.DurationVar(&o.timeout, "timeout", timeout, timeoutUsage)
	if room {
		f.StringVar(&o.room, "room", "", "room to act in (default the server's default room)")
	}
	o.dial.bind(cmd)
}

func (o *queryOpts) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, o.timeout)
}

func (o *queryOpts) session(cmd *cobra.Command, args []string) (context.Context, context.CancelFunc, *client.Client, []string, error) {
	addr, name, err := resolveTarget(cmd, addrArg(args), o.name)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if name == "" {
		return nil, nil, nil, nil, errors.New("--name is required (or set HEARTH_NAME)")
	}
	opts := client.Options{Name: name, Room: o.room, DialTimeout: dialTimeout(o.timeout), Reconnect: o.reconnect}
	if err := o.dial.apply(&opts); err != nil {
		return nil, nil, nil, nil, err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	c, err := client.Dial(ctx, addr, opts)
	if err != nil {
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
	return ctx, stop, c, rest, nil
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
of standard input is sent as its own message, for as long as stdin stays open.
The exit status is 0 only once the server has accepted each message; --timeout
bounds the connection and each confirmation, not the stream. Meant for cron
jobs, CI and scripts. The server's rate limit applies: a burst above it fails
with "rate limited".`,
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
				return o.deliver(ctx, c, to, strings.Join(rest, " "))
			}
			sc := bufio.NewScanner(cmd.InOrStdin())
			sc.Buffer(make([]byte, protocol.MaxLineBytes), protocol.MaxLineBytes)
			for sc.Scan() {
				if line := strings.TrimSpace(sc.Text()); line != "" {
					if err := o.deliver(ctx, c, to, line); err != nil {
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
	o.bind(cmd, true, 10*time.Second, "time allowed to connect, and then for the server to confirm each message")
	cmd.Flags().StringVar(&to, "to", "", "send a private message to this user instead of to the room")
	return cmd
}

func (o *queryOpts) deliver(ctx context.Context, c *client.Client, to, text string) error {
	ctx, cancel := o.bound(ctx)
	defer cancel()
	return deliver(ctx, c, to, text)
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
			ctx, cancel := o.bound(ctx)
			defer cancel()
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
	o.bind(cmd, true, 10*time.Second, "time allowed to connect and get the answer")
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
			ctx, cancel := o.bound(ctx)
			defer cancel()
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
	o.bind(cmd, false, 10*time.Second, "time allowed to connect and get the answer")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print a JSON array instead of one room per line")
	return cmd
}

func newTailCmd() *cobra.Command {
	o := queryOpts{reconnect: true}
	var raw bool
	cmd := &cobra.Command{
		Use:   "tail [host:port]",
		Short: "Follow a room on standard output until interrupted",
		Long: `Print everything happening in a room, one line at a time, and keep going.

The read-side counterpart to hearth send: pipe it into grep, tee it to a file,
or run it under a supervisor as a bridge. It starts from now rather than
replaying the room's history, prints only that room plus what is addressed to
it, pings the server so an idle timeout never ends it (--keepalive), and
reconnects with backoff if the server goes away. Ctrl+C or SIGTERM stops it.
Unlike connect --plain it never reads stdin, so it is safe in a pipeline.`,
		Example: `  hearth tail chat.example.com:4000 --room ops
  hearth tail --json | jq -r 'select(.kind=="msg") | .text'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop, c, _, err := o.session(cmd, args)
			if err != nil {
				return err
			}
			defer stop()
			defer closeQuietly(cmd, c)
			ctx, cancel := o.bound(ctx)
			defer cancel()
			return follow(ctx, c, cmd.OutOrStdout(), raw)
		},
	}
	o.bind(cmd, true, 0, "stop after this long; 0 follows until interrupted")
	cmd.Flags().BoolVar(&raw, "json", false, "print each event as the JSON object the server sent instead of a readable line")
	return cmd
}

func follow(ctx context.Context, c *client.Client, out io.Writer, raw bool) error {
	var codec protocol.TextCodec
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				return nil
			}
			if !shows(e, c.Room()) {
				continue
			}
			if err := emit(out, codec, e, raw); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func shows(e protocol.Event, room string) bool {
	return e.Kind != protocol.History && (e.Room == "" || e.Room == room)
}

func emit(out io.Writer, codec protocol.TextCodec, e protocol.Event, raw bool) error {
	if raw {
		if err := json.NewEncoder(out).Encode(e); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
		return nil
	}
	if err := codec.Encode(out, e); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
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
