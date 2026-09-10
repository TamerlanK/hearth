package client_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/internal/server"
	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
)

const wait = 5 * time.Second

func startServer(t *testing.T, addr string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		cancel()
		t.Fatalf("listen %s: %v", addr, err)
	}
	cfg := server.Config{HistorySize: 10, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- server.New(cfg).Serve(ctx, ln) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve: %v", err)
				}
			case <-time.After(wait):
				t.Fatal("server did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return ln.Addr().String(), stop
}

func dial(t *testing.T, addr string, opts client.Options) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c, err := client.Dial(ctx, addr, opts)
	if err != nil {
		t.Fatalf("dial as %s: %v", opts.Name, err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

func expectClosed(t *testing.T, c *client.Client) {
	t.Helper()
	for {
		select {
		case _, ok := <-c.Events():
			if !ok {
				return
			}
		case <-time.After(wait):
			t.Fatal("events not closed")
		}
	}
}

func waitFor(t *testing.T, c *client.Client, what string, match func(protocol.Event) bool) protocol.Event {
	t.Helper()
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				t.Fatalf("events closed before %s", what)
			}
			if match(e) {
				return e
			}
		case <-time.After(wait):
			t.Fatalf("no %s within %s", what, wait)
		}
	}
}

func msgFrom(name, text string) func(protocol.Event) bool {
	return func(e protocol.Event) bool {
		return e.Kind == protocol.Msg && e.From == name && e.Text == text
	}
}

func TestSayReachesAnotherClient(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	alice := dial(t, addr, client.Options{Name: "alice"})
	bob := dial(t, addr, client.Options{Name: "bob"})
	waitFor(t, alice, "bob's join", func(e protocol.Event) bool { return e.Kind == protocol.Join && e.From == "bob" })

	if err := alice.Say(context.Background(), "hello bob"); err != nil {
		t.Fatalf("say: %v", err)
	}
	if e := waitFor(t, bob, "alice's message", msgFrom("alice", "hello bob")); e.Room != "#general" {
		t.Errorf("room = %q, want #general", e.Room)
	}
	if got := alice.State(); got != client.Connected {
		t.Errorf("state = %s, want connected", got)
	}
}

func TestJoinSwitchesRoomsAndReplaysHistory(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	ctx := context.Background()
	alice := dial(t, addr, client.Options{Name: "alice", Room: "golang"})
	waitFor(t, alice, "alice's join", func(e protocol.Event) bool { return e.Kind == protocol.Join && e.Room == "#golang" })
	if err := alice.Say(ctx, "gophers unite"); err != nil {
		t.Fatalf("say: %v", err)
	}
	waitFor(t, alice, "alice's echo", msgFrom("alice", "gophers unite"))

	bob := dial(t, addr, client.Options{Name: "bob"})
	if err := bob.Join(ctx, "golang"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if e := waitFor(t, bob, "history", func(e protocol.Event) bool { return e.Kind == protocol.History }); e.Text != "gophers unite" {
		t.Errorf("history text = %q, want %q", e.Text, "gophers unite")
	}
	names, err := bob.Who(ctx)
	if err != nil {
		t.Fatalf("who: %v", err)
	}
	if len(names) != 2 || names[0] != "alice" || names[1] != "bob" {
		t.Errorf("who = %v, want [alice bob]", names)
	}
	rooms, err := bob.Rooms(ctx)
	if err != nil {
		t.Fatalf("rooms: %v", err)
	}
	want := []client.RoomInfo{{Name: "#general", Members: 0}, {Name: "#golang", Members: 2}}
	if len(rooms) != len(want) || rooms[0] != want[0] || rooms[1] != want[1] {
		t.Errorf("rooms = %v, want %v", rooms, want)
	}
	if err := bob.Join(ctx, "golang"); !errors.As(err, new(*client.ServerError)) {
		t.Errorf("joining the current room: err = %v, want a ServerError", err)
	}
}

func TestNickAndPrivMsg(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	ctx := context.Background()
	alice := dial(t, addr, client.Options{Name: "alice"})
	bob := dial(t, addr, client.Options{Name: "bob"})
	if err := alice.Nick(ctx, "bob"); !errors.Is(err, client.ErrNameTaken) {
		t.Fatalf("nick to a taken name: err = %v, want ErrNameTaken", err)
	}
	if err := alice.Nick(ctx, "alicia"); err != nil {
		t.Fatalf("nick: %v", err)
	}
	if err := alice.PrivMsg(ctx, "bob", "psst"); err != nil {
		t.Fatalf("privmsg: %v", err)
	}
	e := waitFor(t, bob, "private message", func(e protocol.Event) bool { return e.Kind == protocol.PrivMsg })
	if e.From != "alicia" || e.To != "bob" || e.Text != "psst" {
		t.Errorf("privmsg = %+v", e)
	}
}

func TestDialNameTaken(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	dial(t, addr, client.Options{Name: "alice"})
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, err := client.Dial(ctx, addr, client.Options{Name: "alice"})
	if !errors.Is(err, client.ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

func TestCloseEndsEverything(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	ctx := context.Background()
	c := dial(t, addr, client.Options{Name: "alice"})
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	expectClosed(t, c)
	if got := c.State(); got != client.Closed {
		t.Errorf("state = %s, want closed", got)
	}
	if err := c.Say(ctx, "x"); !errors.Is(err, client.ErrClosed) {
		t.Errorf("say: err = %v, want ErrClosed", err)
	}
	if _, err := c.Who(ctx); !errors.Is(err, client.ErrClosed) {
		t.Errorf("who: err = %v, want ErrClosed", err)
	}
	if err := c.Join(ctx, "x"); !errors.Is(err, client.ErrClosed) {
		t.Errorf("join: err = %v, want ErrClosed", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestServerGoneWithoutReconnectCloses(t *testing.T) {
	addr, stop := startServer(t, "127.0.0.1:0")
	c := dial(t, addr, client.Options{Name: "alice"})
	stop()
	waitFor(t, c, "shutdown notice", func(e protocol.Event) bool { return e.Kind == protocol.System && e.Text == "server shutting down" })
	expectClosed(t, c)
	if err := c.Say(context.Background(), "x"); !errors.Is(err, client.ErrClosed) {
		t.Errorf("say: err = %v, want ErrClosed", err)
	}
}

func TestReconnect(t *testing.T) {
	addr, stop := startServer(t, "127.0.0.1:0")
	ctx := context.Background()
	c := dial(t, addr, client.Options{
		Name:      "alice",
		Room:      "ops",
		Reconnect: true,
		Backoff:   client.BackoffConfig{Min: 20 * time.Millisecond, Max: 200 * time.Millisecond},
	})
	stop()
	waitFor(t, c, "shutdown notice", func(e protocol.Event) bool { return e.Kind == protocol.System && e.Text == "server shutting down" })
	startServer(t, addr)
	waitFor(t, c, "reconnected notice", func(e protocol.Event) bool { return e.Kind == protocol.System && e.Text == "reconnected" })
	if got := c.State(); got != client.Connected {
		t.Errorf("state = %s, want connected", got)
	}
	if got := c.Stats().Reconnects; got != 1 {
		t.Errorf("reconnects = %d, want 1", got)
	}
	if err := c.Say(ctx, "back again"); err != nil {
		t.Fatalf("say after reconnect: %v", err)
	}
	if e := waitFor(t, c, "echo after reconnect", msgFrom("alice", "back again")); e.Room != "#ops" {
		t.Errorf("room after reconnect = %q, want #ops", e.Room)
	}
}

func TestSlowConsumerDropsOldest(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	ctx := context.Background()
	alice := dial(t, addr, client.Options{Name: "alice"})
	bob := dial(t, addr, client.Options{Name: "bob"})
	waitFor(t, alice, "bob's join", func(e protocol.Event) bool { return e.Kind == protocol.Join && e.From == "bob" })
	n := client.EventBuffer + 20
	for i := range n {
		if _, err := alice.Who(ctx); err != nil {
			t.Fatalf("who %d: %v", i, err)
		}
	}
	if got := alice.Stats().Dropped; got < 20 {
		t.Errorf("dropped = %d, want at least 20", got)
	}
	if err := alice.Say(ctx, "still here"); err != nil {
		t.Fatalf("say: %v", err)
	}
	waitFor(t, bob, "alice's message", msgFrom("alice", "still here"))
}

func TestCancelledSendKeepsConnectionUsable(t *testing.T) {
	addr, _ := startServer(t, "127.0.0.1:0")
	alice := dial(t, addr, client.Options{Name: "alice"})
	bob := dial(t, addr, client.Options{Name: "bob"})
	waitFor(t, alice, "bob joined", func(e protocol.Event) bool { return e.Kind == protocol.Join && e.From == "bob" })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := alice.Say(cancelled, "never sent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Say with a cancelled context = %v, want context.Canceled", err)
	}

	ctx, stop := context.WithTimeout(context.Background(), wait)
	defer stop()
	if err := alice.Say(ctx, "still here"); err != nil {
		t.Fatalf("Say after a cancelled send: %v", err)
	}
	waitFor(t, bob, "alice's message", msgFrom("alice", "still here"))
	if got := alice.State(); got != client.Connected {
		t.Errorf("state after a cancelled send = %s, want connected", got)
	}
}
