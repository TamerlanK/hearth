package client

import (
	"bufio"
	"context"
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

var (
	ErrClosed       = errors.New("client closed")
	ErrNameTaken    = errors.New("name taken")
	ErrRateLimited  = errors.New("rate limited")
	ErrNotConnected = errors.New("not connected")
	ErrNegotiation  = errors.New("server did not accept the json protocol")
)

const (
	EventBuffer        = 256
	defaultDialTimeout = 10 * time.Second
	defaultBackoffMin  = 500 * time.Millisecond
	defaultBackoffMax  = 30 * time.Second
)

type ServerError struct {
	Text string
}

func (e *ServerError) Error() string {
	return "server: " + e.Text
}

func (e *ServerError) Is(target error) bool {
	switch target {
	case ErrNameTaken:
		return strings.HasPrefix(e.Text, "name taken")
	case ErrRateLimited:
		return e.Text == "rate limited"
	}
	return false
}

type State int32

const (
	Connecting State = iota
	Connected
	Reconnecting
	Closed
)

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

type BackoffConfig struct {
	Min time.Duration
	Max time.Duration
}

type Options struct {
	Name        string
	Room        string
	DialTimeout time.Duration
	Logger      *slog.Logger
	Reconnect   bool
	Backoff     BackoffConfig
}

type RoomInfo struct {
	Name    string
	Members int
}

type Stats struct {
	Dropped    uint64
	Reconnects uint64
	Attempt    uint64
}

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

func (c *Client) Events() <-chan protocol.Event {
	return c.events
}

func (c *Client) State() State {
	return State(c.state.Load())
}

func (c *Client) Stats() Stats {
	return Stats{Dropped: c.dropped.Load(), Reconnects: c.reconnects.Load(), Attempt: c.attempt.Load()}
}

func (c *Client) Close() error {
	c.cancel()
	<-c.done
	return nil
}

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

func (c *Client) Say(ctx context.Context, text string) error {
	return c.Send(ctx, protocol.Command{Name: "say", Text: text})
}

func (c *Client) PrivMsg(ctx context.Context, to, text string) error {
	return c.Send(ctx, protocol.Command{Name: "msg", Args: []string{to}, Text: text})
}

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

func (c *Client) Who(ctx context.Context) ([]string, error) {
	e, err := c.request(ctx, protocol.Command{Name: "who"}, func(e protocol.Event) bool {
		return e.Kind == protocol.Who
	})
	if err != nil {
		return nil, fmt.Errorf("who: %w", err)
	}
	return e.Names, nil
}

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
	conn, err := new(net.Dialer).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.addr, err)
	}
	if err := c.handshake(ctx, conn); err != nil {
		if cerr := conn.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return conn, nil
}

func (c *Client) handshake(ctx context.Context, conn net.Conn) error {
	stop := context.AfterFunc(ctx, func() { c.interrupt(conn) })
	defer stop()
	r := bufio.NewReaderSize(conn, protocol.MaxLineBytes)
	if _, err := io.WriteString(conn, protocol.Hello+"\n"); err != nil {
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
		c.resolve(e)
		c.push(e)
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
