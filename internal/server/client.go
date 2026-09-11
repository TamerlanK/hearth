package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/TamerlanK/hearth/internal/ratelimit"
	"github.com/TamerlanK/hearth/pkg/protocol"
)

const (
	maxNameLen         = 20
	maxRoomLen         = 24
	maxMessageRunes    = 1024
	sendBuffer         = 32
	writeTimeout       = 5 * time.Second
	handshakeTimeout   = 10 * time.Second
	defaultRoom        = "#general"
	defaultHistorySize = 50
)

type client struct {
	id   string
	conn net.Conn
	sc   *bufio.Scanner

	enc protocol.Encoder
	dec protocol.Decoder
	seq uint64

	bucket  *ratelimit.Bucket
	limited bool

	name string
	room *room
	away string

	maxDrops   int
	overload   chan struct{}
	wake       chan struct{}
	slot       chan struct{}
	writerDone chan struct{}

	mu         sync.Mutex
	queue      []protocol.Event
	head       int
	closed     bool
	dropped    int
	inARow     int
	overloaded bool
}

func newClient(conn net.Conn, cfg Config) *client {
	c := &client{
		id:         newClientID(),
		conn:       conn,
		enc:        protocol.TextCodec{},
		maxDrops:   cfg.MaxDropsInARow,
		overload:   make(chan struct{}),
		wake:       make(chan struct{}, 1),
		slot:       make(chan struct{}, 1),
		writerDone: make(chan struct{}),
	}
	if cfg.MessagesPerSecond > 0 {
		c.bucket = ratelimit.New(cfg.MessagesPerSecond, cfg.Burst, nil)
	}
	c.dec = protocol.TextCodec{}
	if cfg.decorateDecoder != nil {
		c.dec = cfg.decorateDecoder(c.dec)
	}
	if conn != nil {
		c.sc = bufio.NewScanner(conn)
		c.sc.Buffer(make([]byte, protocol.MaxLineBytes), protocol.MaxLineBytes)
	}
	return c
}

func newClientID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

func (c *client) useJSON(cfg Config) {
	c.enc = protocol.JSONCodec{}
	c.dec = protocol.Decoder(protocol.JSONCodec{})
	if cfg.decorateDecoder != nil {
		c.dec = cfg.decorateDecoder(c.dec)
	}
}

func (c *client) allow() bool {
	return c.bucket == nil || c.bucket.Allow()
}

func (c *client) trySend(e protocol.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if len(c.queue)-c.head < sendBuffer {
		c.queue = append(c.queue, e)
		c.inARow = 0
		signal(c.wake)
		return true
	}
	c.dropped++
	c.inARow++
	droppedMessagesTotal.Inc()
	if c.maxDrops > 0 && c.inARow >= c.maxDrops && !c.overloaded {
		c.overloaded = true
		close(c.overload)
	}
	return false
}

func (c *client) push(events ...protocol.Event) {
	if len(events) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.queue = append(c.queue, events...)
	signal(c.wake)
}

func (c *client) pop() (protocol.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.head == len(c.queue) {
		return protocol.Event{}, false
	}
	e := c.queue[c.head]
	c.queue[c.head] = protocol.Event{}
	c.head++
	if c.head == len(c.queue) {
		c.head = 0
		if cap(c.queue) > 4*sendBuffer {
			c.queue = nil
		} else {
			c.queue = c.queue[:0]
		}
	}
	signal(c.slot)
	return e, true
}

func (c *client) backlog() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue) - c.head
}

func (c *client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *client) closeSend() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		signal(c.wake)
	}
}

func (c *client) throttle(ctx context.Context) bool {
	for c.backlog() >= sendBuffer {
		select {
		case <-c.slot:
		case <-c.writerDone:
			return false
		case <-c.overload:
			return false
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *client) droppedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

func (c *client) isOverloaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overloaded
}

func (c *client) readLine(idle time.Duration) ([]byte, error) {
	if idle > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return nil, fmt.Errorf("set read deadline: %w", err)
		}
	}
	if !c.sc.Scan() {
		switch err := c.sc.Err(); {
		case err == nil:
			return nil, io.EOF
		case errors.Is(err, bufio.ErrTooLong):
			return nil, protocol.ErrLineTooLong
		default:
			return nil, fmt.Errorf("read: %w", err)
		}
	}
	return c.sc.Bytes(), nil
}

func (c *client) setReadDeadline(t time.Time) error {
	if err := c.conn.SetReadDeadline(t); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}
	return nil
}

func (c *client) interruptRead(log *slog.Logger) {
	if err := c.conn.SetReadDeadline(time.Now()); err != nil {
		log.Debug("interrupt read", "event", "interrupt_read", "err", err)
	}
}

func (c *client) writeEvent(e protocol.Event) error {
	c.seq++
	e.Seq = c.seq
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if err := c.enc.Encode(c.conn, e); err != nil {
		return fmt.Errorf("encode %s event: %w", e.Kind, err)
	}
	return nil
}

func (c *client) writeLoop(log *slog.Logger) {
	defer closeConn(c.conn, log)
	defer recoverPanic(log, "write_loop")
	for {
		e, ok := c.pop()
		if !ok {
			if c.isClosed() {
				break
			}
			<-c.wake
			continue
		}
		if err := c.writeEvent(e); err != nil {
			log.Debug("write failed", "event", "write_error", "err", err)
			return
		}
	}
	if c.isOverloaded() {
		if err := c.writeEvent(errorEvent("too many dropped messages, disconnecting")); err != nil {
			log.Debug("write failed", "event", "write_error", "err", err)
		}
	}
}

func closeConn(conn net.Conn, log *slog.Logger) {
	if err := conn.Close(); err != nil {
		log.Debug("close conn", "event", "close_error", "err", err)
	}
}

func recoverPanic(log *slog.Logger, event string) {
	if r := recover(); r != nil {
		logPanic(log, event, r)
	}
}

func logPanic(log *slog.Logger, event string, r any) {
	log.Error("recovered from panic", "event", event, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
}
