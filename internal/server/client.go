package server

import (
	"bufio"
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

	"github.com/TamerlanK/hearth/internal/protocol"
	"github.com/TamerlanK/hearth/internal/ratelimit"
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
	send chan protocol.Event

	enc protocol.Encoder
	dec protocol.Decoder
	seq uint64

	bucket  *ratelimit.Bucket
	limited bool

	name string
	room *room

	maxDrops int
	overload chan struct{}

	mu         sync.Mutex
	closed     bool
	dropped    int
	inARow     int
	overloaded bool
}

func newClient(conn net.Conn, cfg Config) *client {
	c := &client{
		id:       newClientID(),
		conn:     conn,
		send:     make(chan protocol.Event, sendBuffer),
		enc:      protocol.TextCodec{},
		maxDrops: cfg.MaxDropsInARow,
		overload: make(chan struct{}),
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
	select {
	case c.send <- e:
		c.inARow = 0
		return true
	default:
		c.dropped++
		c.inARow++
		droppedMessagesTotal.Inc()
		if c.maxDrops > 0 && c.inARow >= c.maxDrops && !c.overloaded {
			c.overloaded = true
			close(c.overload)
		}
		return false
	}
}

func (c *client) trySendAll(events []protocol.Event) {
	for _, e := range events {
		c.trySend(e)
	}
}

func (c *client) closeSend() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.send)
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
	for e := range c.send {
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
