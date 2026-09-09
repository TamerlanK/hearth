package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/internal/protocol"
)

const wait = 2 * time.Second

func startServer(t *testing.T, cfg Config) (addr string, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("listen: %v", err)
	}
	errc := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		errc <- New(cfg).Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(wait):
			t.Error("Serve did not return after cancel")
		}
	})
	return ln.Addr().String(), cancel, errc
}

type tconn struct {
	net.Conn
	r *bufio.Reader
}

func connect(t *testing.T, addr string) *tconn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	conn, err := new(net.Dialer).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Logf("close: %v", err)
		}
	})
	return &tconn{Conn: conn, r: bufio.NewReader(conn)}
}

func dial(t *testing.T, addr, name string) *tconn {
	t.Helper()
	c := connect(t, addr)
	expectLine(t, c, "Enter a name", wait)
	send(t, c, name)
	return c
}

func send(t *testing.T, c *tconn, line string) {
	t.Helper()
	if _, err := fmt.Fprintf(c, "%s\n", line); err != nil {
		t.Fatalf("send %q: %v", line, err)
	}
}

func expectLine(t *testing.T, c *tconn, contains string, timeout time.Duration) string {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for %q: %v", contains, err)
		}
		if strings.Contains(line, contains) {
			return line
		}
	}
}

func expectClosed(t *testing.T, c *tconn, timeout time.Duration) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	for {
		if _, err := c.r.ReadString('\n'); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("expected EOF, got: %v", err)
		}
	}
}

func TestTwoClientsChat(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined", wait)

	send(t, alice, "hello there")
	got := expectLine(t, bob, "alice: hello there", wait)
	if !strings.HasPrefix(got, "[") {
		t.Errorf("message lacks timestamp prefix: %q", got)
	}
	expectLine(t, alice, "alice: hello there", wait)
}

func TestJoinLeaveNotices(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined", wait)

	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined", wait)

	send(t, bob, "/quit")
	expectLine(t, bob, "* bye", wait)
	expectClosed(t, bob, wait)
	expectLine(t, alice, "* bob left", wait)
}

func TestDuplicateNameRejected(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined", wait)

	dup := dial(t, addr, "alice")
	expectLine(t, dup, "name taken", wait)
	send(t, dup, "alice2")
	expectLine(t, dup, "* alice2 joined", wait)
	expectLine(t, alice, "* alice2 joined", wait)
}

func TestWho(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined", wait)
	expectLine(t, bob, "* bob joined", wait)

	send(t, alice, "/who")
	expectLine(t, alice, "* online in #general (2): alice, bob", wait)
}

func TestMaxClients(t *testing.T) {
	addr, _, _ := startServer(t, Config{MaxClients: 1})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined", wait)

	extra := connect(t, addr)
	expectLine(t, extra, "server full", wait)
	expectClosed(t, extra, wait)
}

func TestIdleTimeout(t *testing.T) {
	addr, _, _ := startServer(t, Config{IdleTimeout: 200 * time.Millisecond})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined", wait)

	expectLine(t, alice, "idle", time.Second)
	expectClosed(t, alice, wait)
}

func TestGracefulShutdown(t *testing.T) {
	addr, cancel, done := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined", wait)

	pending := connect(t, addr)
	expectLine(t, pending, "Enter a name", wait)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return within 1s")
	}
	for _, c := range []*tconn{alice, bob} {
		expectLine(t, c, "server shutting down", wait)
		expectClosed(t, c, wait)
	}
	expectClosed(t, pending, wait)
}

func TestSlowClientDoesNotStallFastClient(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	slow := dial(t, addr, "carol")
	expectLine(t, alice, "* carol joined", wait)
	expectLine(t, bob, "* carol joined", wait)
	_ = slow

	for i := range 100 {
		msg := fmt.Sprintf("msg %d", i)
		send(t, alice, msg)
		expectLine(t, bob, "alice: "+msg, wait)
	}
}

func TestUnknownCommand(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	send(t, alice, "/dance now")
	expectLine(t, alice, "! unknown command dance (try /help)", wait)
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"simple", "alice", true},
		{"unicode", "zoë", true},
		{"max length", strings.Repeat("a", 20), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 21), false},
		{"space", "al ice", false},
		{"tab", "al\tice", false},
		{"control", "al\x1bice", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateName(tt.input)
			if (err == nil) != tt.ok {
				t.Errorf("validateName(%q) = %v, want ok=%v", tt.input, err, tt.ok)
			}
		})
	}
}

func TestTrySendDropsWhenFull(t *testing.T) {
	c := newClient(nil)
	for i := range sendBuffer {
		if !c.trySend(systemEvent("x")) {
			t.Fatalf("send %d unexpectedly dropped", i)
		}
	}
	if c.trySend(systemEvent("overflow")) {
		t.Error("send into full buffer should be dropped")
	}
	if got := c.droppedCount(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
	c.closeSend()
	c.closeSend()
	if c.trySend(systemEvent("after close")) {
		t.Error("send after close should be dropped")
	}
}

func dialJSON(t *testing.T, addr, name string) *tconn {
	t.Helper()
	c := connect(t, addr)
	expectLine(t, c, "Enter a name", wait)
	send(t, c, protocol.Hello)
	if e := expectEvent(t, c, protocol.System, wait); e.Text != "protocol json" {
		t.Fatalf("negotiation reply = %q, want %q", e.Text, "protocol json")
	}
	send(t, c, fmt.Sprintf(`{"cmd":"nick","args":[%q]}`, name))
	return c
}

func expectEvent(t *testing.T, c *tconn, kind protocol.Kind, timeout time.Duration) protocol.Event {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	var lastSeq uint64
	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("waiting for %s event: %v", kind, err)
		}
		var e protocol.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("client got a non-json line %q: %v", line, err)
		}
		if e.Seq <= lastSeq {
			t.Fatalf("seq %d is not greater than previous %d", e.Seq, lastSeq)
		}
		lastSeq = e.Seq
		if e.Kind == kind {
			return e
		}
	}
}

func TestJSONAndTextClientsShareRoom(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	jason := dialJSON(t, addr, "jason")
	if e := expectEvent(t, jason, protocol.Join, wait); e.From != "jason" || e.Room != defaultRoom {
		t.Fatalf("join event = %+v, want jason in %s", e, defaultRoom)
	}
	expectLine(t, alice, "* jason joined #general", wait)

	send(t, alice, "hello room")
	got := expectEvent(t, jason, protocol.Msg, wait)
	if got.From != "alice" || got.Text != "hello room" || got.Room != defaultRoom {
		t.Errorf("json client got %+v, want alice/hello room/%s", got, defaultRoom)
	}
	if got.Time.IsZero() {
		t.Error("event has no timestamp")
	}

	send(t, jason, `{"cmd":"say","text":"hi alice"}`)
	if line := expectLine(t, alice, "jason: hi alice", wait); !strings.HasPrefix(line, "[") {
		t.Errorf("text client message lacks timestamp prefix: %q", line)
	}

	send(t, alice, "/who")
	expectLine(t, alice, "* online in #general (2): alice, jason", wait)
	send(t, jason, `{"cmd":"who"}`)
	if e := expectEvent(t, jason, protocol.Who, wait); strings.Join(e.Names, ",") != "alice,jason" {
		t.Errorf("who names = %v, want [alice jason]", e.Names)
	}
}

func TestRoomsIsolateMessages(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)

	send(t, bob, "/join golang")
	expectLine(t, bob, "* bob joined #golang", wait)
	expectLine(t, alice, "* bob left #general", wait)

	send(t, alice, "only general hears this")
	send(t, alice, "/rooms")
	expectLine(t, alice, "* rooms (2): #general, #golang", wait)

	send(t, bob, "/who")
	line := expectLine(t, bob, "* online in #golang", wait)
	if strings.Contains(line, "alice") {
		t.Errorf("/who in #golang leaked alice: %q", line)
	}
	send(t, bob, "/msg alice psst")
	expectLine(t, bob, "bob -> alice: psst", wait)
	expectLine(t, alice, "bob -> alice: psst", wait)
}

func TestHistoryReplayedOnJoin(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)
	send(t, alice, "first")
	expectLine(t, alice, "alice: first", wait)

	bob := dialJSON(t, addr, "bob")
	expectEvent(t, bob, protocol.Join, wait)
	if e := expectEvent(t, bob, protocol.History, wait); e.Text != "first" || e.From != "alice" {
		t.Errorf("history event = %+v, want alice/first", e)
	}
	send(t, bob, `{"cmd":"history"}`)
	if e := expectEvent(t, bob, protocol.History, wait); e.Text != "first" {
		t.Errorf("/history replay = %+v, want first", e)
	}
}

func TestJSONClientErrorsAndPong(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	jason := dialJSON(t, addr, "jason")
	expectEvent(t, jason, protocol.Join, wait)

	send(t, jason, `{"cmd":"ping"}`)
	expectEvent(t, jason, protocol.Pong, wait)

	send(t, jason, `{"cmd":"dance"}`)
	if e := expectEvent(t, jason, protocol.Error, wait); !strings.Contains(e.Text, "unknown command dance") {
		t.Errorf("error event = %+v, want unknown command dance", e)
	}
	send(t, jason, `{"cmd":`)
	if e := expectEvent(t, jason, protocol.Error, wait); !strings.Contains(e.Text, "malformed") {
		t.Errorf("error event = %+v, want malformed", e)
	}
	send(t, jason, `{"cmd":"nick","args":["jason2"]}`)
	if e := expectEvent(t, jason, protocol.Nick, wait); e.From != "jason" || e.To != "jason2" {
		t.Errorf("nick event = %+v, want jason -> jason2", e)
	}
}
