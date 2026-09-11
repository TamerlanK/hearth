// Package client is a Go client for a hearth server. It speaks the JSON form
// of the hearth/1 protocol, delivers every server event on a bounded channel,
// matches the reply to Join, Nick, Who and Rooms with the request that caused
// it, and can reconnect with jittered exponential backoff when the connection
// drops. A longer guide is docs/CLIENT.md in the repository.
package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TamerlanK/hearth/pkg/protocol"
)

// Errors a [Client] returns. ErrNameTaken and ErrRateLimited are matched with
// errors.Is against the [ServerError] the server actually sent.
var (
	ErrClosed       = errors.New("client closed")
	ErrNameTaken    = errors.New("name taken")
	ErrRateLimited  = errors.New("rate limited")
	ErrNotConnected = errors.New("not connected")
	ErrNegotiation  = errors.New("server did not accept the json protocol")
	ErrBadToken     = errors.New("server rejected the token")
)

// EventBuffer is the capacity of the channel returned by [Client.Events].
// When it is full the oldest event is dropped and counted in [Stats].
const (
	EventBuffer        = 256
	defaultDialTimeout = 10 * time.Second
	defaultBackoffMin  = 500 * time.Millisecond
	defaultBackoffMax  = 30 * time.Second
	defaultKeepAlive   = 30 * time.Second
)

// ServerError is an error event the server sent in reply to a request. It
// matches [ErrNameTaken] and [ErrRateLimited] through errors.Is.
type ServerError struct {
	Text string
}

// Error returns the server's text prefixed with "server: ".
func (e *ServerError) Error() string {
	return "server: " + e.Text
}

// Is reports whether the server's text corresponds to the sentinel target.
func (e *ServerError) Is(target error) bool {
	switch target {
	case ErrNameTaken:
		return strings.HasPrefix(e.Text, "name taken")
	case ErrRateLimited:
		return e.Text == "rate limited"
	case ErrBadToken:
		return e.Text == "bad token"
	}
	return false
}

// State is the connection state reported by [Client.State].
type State int32

// The states a Client moves through. Reconnecting is reached only when
// [Options].Reconnect is set; Closed is final.
const (
	Connecting State = iota
	Connected
	Reconnecting
	Closed
)

// String returns the lower-case name of the state.
func (s State) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Connected:
		return "connected"
	case Reconnecting:
		return "reconnecting"
	case Closed:
		return "closed"
	}
	return "state(" + strconv.Itoa(int(s)) + ")"
}

// BackoffConfig bounds the delay between reconnect attempts. Each attempt
// waits a random duration between half the current delay and all of it, and
// the delay doubles from Min until it reaches Max.
type BackoffConfig struct {
	Min time.Duration
	Max time.Duration
}

// Options configures [Dial]. Name is required; every other field has a
// default.
type Options struct {
	// Name is the display name to claim: 1-20 characters, no spaces.
	Name string
	// Room is joined right after connecting; empty means the server's default.
	Room string
	// Token authenticates the connection when the server requires one.
	Token string
	// TLS dials with TLS when set. Use a zero &tls.Config{} for a server with
	// a certificate your system trusts.
	TLS *tls.Config
	// DialTimeout bounds the TCP dial plus the name handshake; 10s when zero.
	DialTimeout time.Duration
	// Logger receives connection and reconnect events; discarded when nil.
	Logger *slog.Logger
	// Reconnect redials with backoff when the connection drops instead of
	// closing the client.
	Reconnect bool
	// Backoff bounds the reconnect delay; 500ms to 30s when zero.
	Backoff BackoffConfig
	// KeepAlive is the longest the connection may go without a write before
	// the client sends a ping, so the server's idle timeout never fires. Zero
	// means 30s; a negative value disables it. The pong that answers a
	// keep-alive ping is consumed by the client and never appears on Events.
	KeepAlive time.Duration
}

// RoomInfo is one entry from [Client.Rooms].
type RoomInfo struct {
	Name    string
	Members int
}

// Stats are counters a [Client] keeps for its lifetime: events dropped from
// the Events channel, reconnects that succeeded, and the reconnect attempt in
// progress (zero while connected).
type Stats struct {
	Dropped    uint64
	Reconnects uint64
	Attempt    uint64
}

// A Client is one connection to a hearth server, created by [Dial]. Its
// methods are safe for concurrent use. Join, Nick, Who and Rooms wait for the
// server's reply and run one at a time, because hearth/1 has no request ids
// to tell concurrent replies apart.
type Client struct {
	addr   string
	opts   Options
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	events chan protocol.Event

	state      atomic.Int32
	dropped    atomic.Uint64
	reconnects atomic.Uint64
	attempt    atomic.Uint64
	lastWrite  atomic.Int64
	ownPongs   atomic.Int64

	reqMu sync.Mutex

	mu     sync.Mutex
	conn   net.Conn
	name   string
	room   string
	waiter *waiter
}

type waiter struct {
	match func(protocol.Event) bool
	reply chan result
}

type result struct {
	event protocol.Event
	err   error
}

// Dial connects to addr, negotiates the JSON protocol, claims opts.Name and
// joins opts.Room. ctx bounds only the dial and handshake; the connection
// outlives it and is released by [Client.Close].
func Dial(ctx context.Context, addr string, opts Options) (*Client, error) {
	if opts.Name == "" {
		return nil, errors.New("dial: Options.Name is required")
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.Backoff.Min <= 0 {
		opts.Backoff.Min = defaultBackoffMin
	}
	if opts.Backoff.Max < opts.Backoff.Min {
		opts.Backoff.Max = max(opts.Backoff.Min, defaultBackoffMax)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.KeepAlive == 0 {
		opts.KeepAlive = defaultKeepAlive
	}
	c := &Client{
		addr:   addr,
		opts:   opts,
		log:    opts.Logger.With("addr", addr),
		done:   make(chan struct{}),
		events: make(chan protocol.Event, EventBuffer),
		name:   opts.Name,
		room:   roomName(opts.Room),
	}
	c.ctx, c.cancel = context.WithCancel(context.WithoutCancel(ctx))

	dialCtx, cancel := context.WithTimeout(ctx, opts.DialTimeout)
	defer cancel()
	conn, err := c.connect(dialCtx)
	if err != nil {
		c.cancel()
		close(c.done)
		return nil, err
	}
	c.state.Store(int32(Connected))
	go c.run(conn)
	return c, nil
}

// Events returns the channel every server event is delivered on. It is
// closed after [Client.Close], or when the connection drops and Reconnect is
// off. A consumer that falls behind loses the oldest events; see [EventBuffer].
func (c *Client) Events() <-chan protocol.Event {
	return c.events
}

// Name returns the display name the server currently knows this client by.
func (c *Client) Name() string {
	return c.currentName()
}

// Room returns the room the client is in, with its leading #.
func (c *Client) Room() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.room
}

// State reports the current connection state.
func (c *Client) State() State {
	return State(c.state.Load())
}

// Stats returns a snapshot of the client's counters.
func (c *Client) Stats() Stats {
	return Stats{Dropped: c.dropped.Load(), Reconnects: c.reconnects.Load(), Attempt: c.attempt.Load()}
}

// Close disconnects, stops any reconnect in progress and waits for the reader
// to exit. It always returns nil and is safe to call more than once.
func (c *Client) Close() error {
	c.cancel()
	<-c.done
	return nil
}

// Send writes cmd and returns without waiting for a reply. ctx bounds the
// write alone: cancelling it interrupts a blocked write and leaves the
// connection usable. Send returns [ErrNotConnected] while a reconnect is in
// progress and [ErrClosed] after Close.
func (c *Client) Send(ctx context.Context, cmd protocol.Command) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if c.ctx.Err() != nil {
		return ErrClosed
	}
	if conn == nil {
		return ErrNotConnected
	}
	return c.write(ctx, conn, cmd)
}

// Say sends text to the current room.
func (c *Client) Say(ctx context.Context, text string) error {
	return c.Send(ctx, protocol.Command{Name: "say", Text: text})
}

// PrivMsg sends text to one user by name.
func (c *Client) PrivMsg(ctx context.Context, to, text string) error {
	return c.Send(ctx, protocol.Command{Name: "msg", Args: []string{to}, Text: text})
}

// Join moves to room, with or without its leading #, and waits for the
// server to confirm.
func (c *Client) Join(ctx context.Context, room string) error {
	room = roomName(room)
	me := c.currentName()
	_, err := c.request(ctx, protocol.Command{Name: "join", Args: []string{room}}, func(e protocol.Event) bool {
		return e.Kind == protocol.Join && e.From == me && e.Room == room
	})
	if err != nil {
		return fmt.Errorf("join %s: %w", room, err)
	}
	c.mu.Lock()
	c.room = room
	c.mu.Unlock()
	return nil
}

// Nick changes the display name and waits for the server to confirm.
func (c *Client) Nick(ctx context.Context, name string) error {
	me := c.currentName()
	if name == me {
		return nil
	}
	_, err := c.request(ctx, protocol.Command{Name: "nick", Args: []string{name}}, func(e protocol.Event) bool {
		return e.Kind == protocol.Nick && e.From == me && e.To == name
	})
	if err != nil {
		return fmt.Errorf("nick %s: %w", name, err)
	}
	c.mu.Lock()
	c.name = name
	c.mu.Unlock()
	return nil
}

// Who returns the names of everyone in the current room, sorted.
func (c *Client) Who(ctx context.Context) ([]string, error) {
	e, err := c.request(ctx, protocol.Command{Name: "who"}, func(e protocol.Event) bool {
		return e.Kind == protocol.Who
	})
	if err != nil {
		return nil, fmt.Errorf("who: %w", err)
	}
	return e.Names, nil
}

// Rooms lists every room on the server with its member count.
func (c *Client) Rooms(ctx context.Context) ([]RoomInfo, error) {
	e, err := c.request(ctx, protocol.Command{Name: "rooms"}, func(e protocol.Event) bool {
		return e.Kind == protocol.Rooms
	})
	if err != nil {
		return nil, fmt.Errorf("rooms: %w", err)
	}
	rooms := make([]RoomInfo, 0, len(e.Names))
	for _, entry := range e.Names {
		rooms = append(rooms, parseRoom(entry))
	}
	return rooms, nil
}

func (c *Client) request(ctx context.Context, cmd protocol.Command, match func(protocol.Event) bool) (protocol.Event, error) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	w := &waiter{match: match, reply: make(chan result, 1)}
	c.mu.Lock()
	c.waiter = w
	c.mu.Unlock()
	if err := c.Send(ctx, cmd); err != nil {
		c.clearWaiter(w)
		return protocol.Event{}, err
	}
	select {
	case r := <-w.reply:
		return r.event, r.err
	case <-ctx.Done():
		c.clearWaiter(w)
		return protocol.Event{}, ctx.Err()
	case <-c.done:
		return protocol.Event{}, ErrClosed
	}
}

func (c *Client) clearWaiter(w *waiter) {
	c.mu.Lock()
	if c.waiter == w {
		c.waiter = nil
	}
	c.mu.Unlock()
}

func (c *Client) resolve(e protocol.Event) {
	c.mu.Lock()
	w := c.waiter
	if w == nil {
		c.mu.Unlock()
		return
	}
	switch {
	case e.Kind == protocol.Error:
		c.waiter = nil
		w.reply <- result{err: &ServerError{Text: e.Text}}
	case w.match(e):
		c.waiter = nil
		w.reply <- result{event: e}
	}
	c.mu.Unlock()
}

func (c *Client) failWaiter(err error) {
	c.mu.Lock()
	if w := c.waiter; w != nil {
		c.waiter = nil
		w.reply <- result{err: err}
	}
	c.mu.Unlock()
}

func (c *Client) currentName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.name
}

func (c *Client) write(ctx context.Context, conn net.Conn, cmd protocol.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(fired)
		c.interruptWrite(conn)
	})
	err := protocol.EncodeCommand(conn, cmd)
	if err == nil {
		c.lastWrite.Store(time.Now().UnixNano())
	}
	if !stop() {
		<-fired
		if derr := conn.SetWriteDeadline(time.Time{}); derr != nil && !errors.Is(derr, net.ErrClosed) {
			c.log.Warn("clear write deadline", "event", "client_error", "err", derr)
		}
		if err != nil {
			return ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("send %s: %w", cmd.Name, err)
	}
	return nil
}

func (c *Client) interruptWrite(conn net.Conn) {
	if err := conn.SetWriteDeadline(time.Now()); err != nil && !errors.Is(err, net.ErrClosed) {
		c.log.Warn("interrupt blocked write", "event", "client_error", "err", err)
	}
}

func (c *Client) interrupt(conn net.Conn) {
	if err := conn.SetDeadline(time.Now()); err != nil && !errors.Is(err, net.ErrClosed) {
		c.log.Warn("interrupt blocked io", "event", "client_error", "err", err)
	}
}

func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.handshake(ctx, conn); err != nil {
		if cerr := conn.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, err
	}
	c.ownPongs.Store(0)
	c.lastWrite.Store(time.Now().UnixNano())
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return conn, nil
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	if c.opts.TLS == nil {
		conn, err := d.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", c.addr, err)
		}
		return conn, nil
	}
	conn, err := (&tls.Dialer{NetDialer: &d, Config: c.opts.TLS}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s with tls: %w", c.addr, err)
	}
	return conn, nil
}

func (c *Client) handshake(ctx context.Context, conn net.Conn) error {
	stop := context.AfterFunc(ctx, func() { c.interrupt(conn) })
	defer stop()
	r := bufio.NewReaderSize(conn, protocol.MaxLineBytes)
	if _, err := io.WriteString(conn, protocol.HelloWith(c.opts.Token)+"\n"); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("negotiate: %w", err)
		}
		e, err := protocol.DecodeEvent(line)
		if err != nil {
			continue
		}
		if e.Kind == protocol.System && e.Text == "protocol json" {
			break
		}
		if e.Kind == protocol.Error {
			return fmt.Errorf("negotiate: %w", &ServerError{Text: e.Text})
		}
		return fmt.Errorf("negotiate: got %s %q: %w", e.Kind, e.Text, ErrNegotiation)
	}
	c.mu.Lock()
	name, room := c.name, c.room
	c.mu.Unlock()
	if err := protocol.EncodeCommand(conn, protocol.Command{Name: "nick", Args: []string{name}}); err != nil {
		return fmt.Errorf("send name: %w", err)
	}
	joined, err := c.expect(r, func(e protocol.Event) bool { return e.Kind == protocol.Join && e.From == name })
	if err != nil {
		return fmt.Errorf("register %q: %w", name, err)
	}
	if room == "" || room == joined.Room {
		c.mu.Lock()
		c.room = joined.Room
		c.mu.Unlock()
		return c.clearDeadline(conn)
	}
	if err := protocol.EncodeCommand(conn, protocol.Command{Name: "join", Args: []string{room}}); err != nil {
		return fmt.Errorf("send join: %w", err)
	}
	if _, err := c.expect(r, func(e protocol.Event) bool { return e.Kind == protocol.Join && e.From == name && e.Room == room }); err != nil {
		return fmt.Errorf("join %s: %w", room, err)
	}
	return c.clearDeadline(conn)
}

func (c *Client) expect(r *bufio.Reader, match func(protocol.Event) bool) (protocol.Event, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return protocol.Event{}, fmt.Errorf("read: %w", err)
		}
		e, err := protocol.DecodeEvent(line)
		if err != nil {
			return protocol.Event{}, err
		}
		switch {
		case e.Kind == protocol.Error:
			return protocol.Event{}, &ServerError{Text: e.Text}
		case match(e):
			c.push(e)
			return e, nil
		default:
			c.push(e)
		}
	}
}

func (c *Client) clearDeadline(conn net.Conn) error {
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear deadline: %w", err)
	}
	return nil
}

func (c *Client) run(conn net.Conn) {
	defer close(c.done)
	defer close(c.events)
	defer c.state.Store(int32(Closed))
	defer c.startKeepAlive()()
	for {
		err := c.readLoop(conn)
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		c.failWaiter(err)
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			c.log.Warn("close connection", "event", "client_error", "err", cerr)
		}
		if c.ctx.Err() != nil {
			return
		}
		c.log.Warn("connection lost", "event", "disconnected", "err", err)
		if !c.opts.Reconnect {
			c.cancel()
			return
		}
		c.state.Store(int32(Reconnecting))
		conn, err = c.reconnect()
		if err != nil {
			return
		}
		c.reconnects.Add(1)
		c.state.Store(int32(Connected))
		c.push(protocol.Event{Kind: protocol.System, Text: "reconnected", Time: time.Now()})
	}
}

func (c *Client) readLoop(conn net.Conn) error {
	stop := context.AfterFunc(c.ctx, func() { c.interrupt(conn) })
	defer stop()
	r := bufio.NewReaderSize(conn, protocol.MaxLineBytes)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		e, err := protocol.DecodeEvent(line)
		if err != nil {
			c.log.Warn("bad line from server", "event", "client_error", "err", err)
			continue
		}
		if e.Kind == protocol.Pong && c.consumeOwnPong() {
			continue
		}
		c.resolve(e)
		c.push(e)
	}
}

func (c *Client) startKeepAlive() (stop func()) {
	interval := c.opts.KeepAlive
	if interval <= 0 {
		return func() {}
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.keepAlive(interval, quit)
	}()
	return func() {
		close(quit)
		<-done
	}
}

func (c *Client) keepAlive(interval time.Duration, quit <-chan struct{}) {
	tick := time.NewTicker(interval / 2)
	defer tick.Stop()
	for {
		select {
		case <-quit:
			return
		case <-tick.C:
		}
		if time.Since(time.Unix(0, c.lastWrite.Load())) < interval/2 {
			continue
		}
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			continue
		}
		c.ownPongs.Add(1)
		ctx, cancel := context.WithTimeout(c.ctx, interval)
		err := c.write(ctx, conn, protocol.Command{Name: "ping"})
		cancel()
		if err != nil {
			c.ownPongs.Add(-1)
			c.log.Debug("keep-alive ping failed", "event", "client_error", "err", err)
		}
	}
}

func (c *Client) consumeOwnPong() bool {
	for {
		n := c.ownPongs.Load()
		if n <= 0 {
			return false
		}
		if c.ownPongs.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

func (c *Client) reconnect() (net.Conn, error) {
	delay := c.opts.Backoff.Min
	for attempt := 1; ; attempt++ {
		c.attempt.Store(uint64(attempt))
		wait := delay/2 + rand.N(delay/2+1)
		c.log.Info("reconnecting", "event", "reconnect", "attempt", attempt, "in", wait)
		select {
		case <-time.After(wait):
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		}
		ctx, cancel := context.WithTimeout(c.ctx, c.opts.DialTimeout)
		conn, err := c.connect(ctx)
		cancel()
		if err == nil {
			c.attempt.Store(0)
			return conn, nil
		}
		if c.ctx.Err() != nil {
			return nil, c.ctx.Err()
		}
		c.log.Warn("reconnect failed", "event", "reconnect", "attempt", attempt, "err", err)
		delay = min(delay*2, c.opts.Backoff.Max)
	}
}

func (c *Client) push(e protocol.Event) {
	for {
		select {
		case c.events <- e:
			return
		default:
		}
		select {
		case <-c.events:
			c.dropped.Add(1)
		default:
		}
	}
}

func roomName(s string) string {
	if s == "" || strings.HasPrefix(s, "#") {
		return s
	}
	return "#" + s
}

func parseRoom(entry string) RoomInfo {
	name, count, ok := strings.Cut(entry, " (")
	if !ok {
		return RoomInfo{Name: entry}
	}
	n, err := strconv.Atoi(strings.TrimSuffix(count, ")"))
	if err != nil {
		return RoomInfo{Name: entry}
	}
	return RoomInfo{Name: name, Members: n}
}
