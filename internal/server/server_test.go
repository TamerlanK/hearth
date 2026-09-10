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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/pkg/protocol"
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

func readUntil(t *testing.T, c *tconn, marker string) []string {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for %q: %v", marker, err)
		}
		lines = append(lines, line)
		if strings.Contains(line, marker) {
			return lines
		}
	}
}

func expectAbsent(t *testing.T, c *tconn, marker string, forbidden ...string) {
	t.Helper()
	for _, line := range readUntil(t, c, marker) {
		for _, f := range forbidden {
			if strings.Contains(line, f) {
				t.Errorf("delivered %q to a client it should not reach: %s", f, line)
			}
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
	addr, _, _ := startServer(t, Config{MessagesPerSecond: 10000, Burst: 10000})
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
	c := newClient(nil, Config{})
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

func TestOverloadAfterConsecutiveDrops(t *testing.T) {
	c := newClient(nil, Config{MaxDropsInARow: 3})
	for range sendBuffer {
		if !c.trySend(systemEvent("fill")) {
			t.Fatal("send into a free buffer was dropped")
		}
	}
	for i := range 2 {
		if c.trySend(systemEvent("drop")) {
			t.Fatalf("send %d into a full buffer was accepted", i)
		}
		if c.isOverloaded() {
			t.Fatalf("overloaded after %d consecutive drops, want 3", i+1)
		}
	}
	c.trySend(systemEvent("drop"))
	if !c.isOverloaded() {
		t.Error("still not overloaded after 3 consecutive drops")
	}
	select {
	case <-c.overload:
	default:
		t.Error("overload channel was not closed")
	}
}

func TestConsecutiveDropCountResets(t *testing.T) {
	c := newClient(nil, Config{MaxDropsInARow: 3})
	for range sendBuffer {
		c.trySend(systemEvent("fill"))
	}
	c.trySend(systemEvent("drop"))
	c.trySend(systemEvent("drop"))

	<-c.send
	if !c.trySend(systemEvent("room freed up")) {
		t.Fatal("send into a freed slot was dropped")
	}
	for range sendBuffer {
		c.trySend(systemEvent("drop"))
		if c.isOverloaded() {
			break
		}
	}
	if got := c.droppedCount(); got < 3 {
		t.Errorf("dropped = %d, want at least 3", got)
	}
}

func TestZeroMaxDropsNeverOverloads(t *testing.T) {
	c := newClient(nil, Config{})
	for range sendBuffer + 500 {
		c.trySend(systemEvent("x"))
	}
	if c.isOverloaded() {
		t.Error("a client overloaded with MaxDropsInARow unset")
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
	expectLine(t, alice, "* rooms (2): #general (1), #golang (1)", wait)

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

func TestJoinReplaysHistoryToJoinerOnly(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	carol := dial(t, addr, "carol")
	expectLine(t, alice, "* carol joined #general", wait)

	send(t, alice, "/join golang")
	expectLine(t, alice, "* alice joined #golang", wait)
	for _, m := range []string{"g1", "g2", "g3"} {
		send(t, alice, m)
		expectLine(t, alice, "alice: "+m, wait)
	}

	bob := dialJSON(t, addr, "bob")
	expectEvent(t, bob, protocol.Join, wait)
	send(t, bob, `{"cmd":"join","args":["golang"]}`)
	if e := expectEvent(t, bob, protocol.Join, wait); e.Room != "#golang" {
		t.Fatalf("join event = %+v, want #golang", e)
	}
	for _, want := range []string{"g1", "g2", "g3"} {
		if e := expectEvent(t, bob, protocol.History, wait); e.Text != want || e.Room != "#golang" {
			t.Errorf("history event = %+v, want %q in #golang", e, want)
		}
	}

	send(t, carol, "/who")
	expectAbsent(t, carol, "online in #general", "g1", "g2", "g3")
}

func TestRoomMessagesDoNotLeak(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)

	send(t, bob, "/join golang")
	expectLine(t, bob, "* bob joined #golang", wait)
	expectLine(t, alice, "* bob left #general", wait)

	send(t, bob, "golang only")
	expectLine(t, bob, "bob: golang only", wait)

	send(t, alice, "general only")
	expectAbsent(t, alice, "alice: general only", "golang only")

	send(t, bob, "/who")
	expectAbsent(t, bob, "online in #golang", "general only", "alice")
}

func TestPrivateMessageReachesBothPartiesOnly(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	carol := dial(t, addr, "carol")
	expectLine(t, alice, "* carol joined #general", wait)
	expectLine(t, bob, "* carol joined #general", wait)

	send(t, alice, "/msg bob the secret")
	expectLine(t, alice, "alice -> bob: the secret", wait)
	expectLine(t, bob, "alice -> bob: the secret", wait)

	send(t, alice, "marker")
	expectAbsent(t, carol, "alice: marker", "the secret")

	send(t, alice, "/msg nobody hello")
	expectLine(t, alice, "! no such user nobody", wait)
}

func TestNickCollisionAndBroadcast(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)

	send(t, bob, "/nick alice")
	expectLine(t, bob, "! name taken", wait)

	send(t, bob, "/nick bobby")
	expectLine(t, alice, "* bob is now known as bobby", wait)
	expectLine(t, bob, "* bob is now known as bobby", wait)

	send(t, bob, "after the rename")
	expectLine(t, alice, "bobby: after the rename", wait)

	send(t, alice, "/who")
	expectLine(t, alice, "* online in #general (2): alice, bobby", wait)

	other := dial(t, addr, "bob")
	expectLine(t, other, "* bob joined #general", wait)
}

func TestEmptyRoomsAreCollected(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	send(t, alice, "/join golang")
	expectLine(t, alice, "* alice joined #golang", wait)
	send(t, alice, "hello golang")
	expectLine(t, alice, "alice: hello golang", wait)

	send(t, alice, "/rooms")
	expectLine(t, alice, "* rooms (2): #general (0), #golang (1)", wait)

	send(t, alice, "/join general")
	expectLine(t, alice, "* alice joined #general", wait)
	send(t, alice, "/rooms")
	expectLine(t, alice, "* rooms (1): #general (1)", wait)
	send(t, alice, "/history golang")
	expectLine(t, alice, "* no history for #golang", wait)
}

func TestMaxRooms(t *testing.T) {
	addr, _, _ := startServer(t, Config{MaxRooms: 2})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	send(t, alice, "/join golang")
	expectLine(t, alice, "* alice joined #golang", wait)
	send(t, alice, "/join rust")
	expectLine(t, alice, "! too many rooms, limit is 2", wait)
}

func TestHistorySizeIsConfigurable(t *testing.T) {
	addr, _, _ := startServer(t, Config{HistorySize: 2})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)
	for _, m := range []string{"one", "two", "three"} {
		send(t, alice, m)
		expectLine(t, alice, "alice: "+m, wait)
	}

	bob := dialJSON(t, addr, "bob")
	expectEvent(t, bob, protocol.Join, wait)
	for _, want := range []string{"two", "three"} {
		if e := expectEvent(t, bob, protocol.History, wait); e.Text != want {
			t.Errorf("history event = %+v, want %q", e, want)
		}
	}
}

func TestValidateRoom(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"simple", "#golang", true},
		{"digits and dash", "#go-1", true},
		{"max length", "#" + strings.Repeat("a", 24), true},
		{"empty", "#", false},
		{"too long", "#" + strings.Repeat("a", 25), false},
		{"uppercase", "#Golang", false},
		{"space", "#go lang", false},
		{"punctuation", "#go_lang", false},
		{"unicode", "#zoë", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRoom(tt.input)
			if (err == nil) != tt.ok {
				t.Errorf("validateRoom(%q) = %v, want ok=%v", tt.input, err, tt.ok)
			}
		})
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	send(t, alice, "/help")
	line := expectLine(t, alice, "commands:", wait)
	for _, spec := range protocol.Commands {
		if !strings.Contains(line, spec.Usage) {
			t.Errorf("/help omits %q: %s", spec.Usage, line)
		}
	}
}

type panicCodec struct {
	protocol.Decoder
	on string
}

func (p panicCodec) Decode(line []byte) (protocol.Command, error) {
	if strings.Contains(string(line), p.on) {
		panic("codec exploded on purpose")
	}
	return p.Decoder.Decode(line)
}

func TestRateLimitDropsExcessAndWarnsOnce(t *testing.T) {
	addr, _, _ := startServer(t, Config{MessagesPerSecond: 1, Burst: 3})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)

	for i := range 40 {
		send(t, alice, fmt.Sprintf("flood %d", i))
	}
	expectLine(t, alice, "! rate limited", wait)

	send(t, bob, "bob still works")
	extraWarnings := 0
	for _, line := range readUntil(t, alice, "bob: bob still works") {
		if strings.Contains(line, "! rate limited") {
			extraWarnings++
		}
	}
	if extraWarnings != 0 {
		t.Errorf("a sustained flood produced %d extra warnings; want one in total", extraWarnings)
	}

	delivered := 0
	for _, line := range readUntil(t, bob, "bob: bob still works") {
		if strings.Contains(line, "alice: flood ") {
			delivered++
		}
	}
	if delivered == 0 {
		t.Fatal("the burst was not delivered at all")
	}
	if delivered > 10 {
		t.Errorf("%d of 40 flooded messages were broadcast; the rate limit did not hold", delivered)
	}
}

func TestRateLimitCountsButKeepsConnectionUsable(t *testing.T) {
	addr, _, _ := startServer(t, Config{MessagesPerSecond: 1, Burst: 2})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	for range 20 {
		send(t, alice, "spam")
	}
	expectLine(t, alice, "! rate limited", wait)

	time.Sleep(1100 * time.Millisecond)
	send(t, alice, "recovered")
	expectLine(t, alice, "alice: recovered", wait)
}

func TestRateWithoutBurstStillAllowsTraffic(t *testing.T) {
	addr, _, _ := startServer(t, Config{MessagesPerSecond: 100})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)
	send(t, alice, "a rate with no burst must not lock me out")
	expectLine(t, alice, "alice: a rate with no burst must not lock me out", wait)
}

func TestMaxClientsPerIP(t *testing.T) {
	addr, _, _ := startServer(t, Config{MaxClientsPerIP: 2})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)

	extra := connect(t, addr)
	expectLine(t, extra, "too many connections from your address", wait)
	expectClosed(t, extra, wait)

	send(t, alice, "still here")
	expectLine(t, bob, "alice: still here", wait)
}

func TestPanickingConnectionDoesNotAffectOthers(t *testing.T) {
	addr, _, _ := startServer(t, Config{
		decorateDecoder: func(d protocol.Decoder) protocol.Decoder {
			return panicCodec{Decoder: d, on: "boom"}
		},
	})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)

	send(t, alice, "boom")
	expectClosed(t, alice, wait)
	expectLine(t, bob, "* alice left #general", wait)

	carol := dial(t, addr, "carol")
	expectLine(t, bob, "* carol joined #general", wait)
	send(t, carol, "server survived")
	expectLine(t, bob, "carol: server survived", wait)
}

func TestPanicDuringHandshakeClosesOnlyThatConnection(t *testing.T) {
	addr, _, _ := startServer(t, Config{
		decorateDecoder: func(d protocol.Decoder) protocol.Decoder {
			return panicCodec{Decoder: d, on: "boom"}
		},
	})
	bad := connect(t, addr)
	expectLine(t, bad, "Enter a name", wait)
	send(t, bad, "boom")
	expectClosed(t, bad, wait)

	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)
}

func TestMessageTooLongIsRejected(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	send(t, alice, strings.Repeat("a", maxMessageRunes+1))
	expectLine(t, alice, fmt.Sprintf("! message is longer than %d characters", maxMessageRunes), wait)

	send(t, alice, strings.Repeat("b", maxMessageRunes))
	expectLine(t, alice, "alice: "+strings.Repeat("b", maxMessageRunes), wait)
}

func TestControlCharactersStrippedExceptTab(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	expectLine(t, alice, "* alice joined #general", wait)

	send(t, alice, "before\x1b[31mafter\tkept")
	line := expectLine(t, alice, "alice: ", wait)
	if strings.Contains(line, "\x1b") {
		t.Errorf("escape sequence survived: %q", line)
	}
	if !strings.Contains(line, "after\tkept") {
		t.Errorf("tab was stripped: %q", line)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	silent := connect(t, addr)
	expectLine(t, silent, "Enter a name", wait)
	expectClosed(t, silent, handshakeTimeout+wait)
}

func TestMetricsEndpoint(t *testing.T) {
	addr, _, _ := startServer(t, Config{})
	alice := dial(t, addr, "alice")
	bob := dial(t, addr, "bob")
	expectLine(t, alice, "* bob joined #general", wait)
	send(t, alice, "traffic for the counters")
	expectLine(t, bob, "alice: traffic for the counters", wait)

	ln, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: MetricsHandler(slog.New(slog.NewTextHandler(io.Discard, nil))), ReadHeaderTimeout: wait}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("metrics serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Logf("close metrics server: %v", err)
		}
	})

	body := httpGet(t, "http://"+ln.Addr().String()+"/metrics")
	for _, want := range []string{
		"hearth_connections_current",
		"hearth_connections_total",
		"hearth_messages_total",
		"hearth_dropped_messages_total",
		"hearth_rate_limited_total",
		"hearth_rooms_current",
		"hearth_message_fanout_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics does not expose %s", want)
		}
	}
	if health := httpGet(t, "http://"+ln.Addr().String()+"/healthz"); !strings.Contains(health, "ok") {
		t.Errorf("/healthz = %q, want ok", health)
	}
	if index := httpGet(t, "http://"+ln.Addr().String()+"/debug/pprof/"); !strings.Contains(index, "goroutine") {
		t.Errorf("/debug/pprof/ does not list the goroutine profile")
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("close body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
