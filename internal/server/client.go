package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/TamerlanK/hearth/internal/protocol"
)

const (
	maxNameLen   = 20
	maxRoomLen   = 24
	sendBuffer   = 32
	writeTimeout = 5 * time.Second
	defaultRoom  = "#general"
)

type client struct {
	conn net.Conn
	sc   *bufio.Scanner
	send chan protocol.Event

	enc protocol.Encoder
	dec protocol.Decoder
	seq uint64

	name string
	room string

	mu      sync.Mutex
	closed  bool
	dropped int
}

func newClient(conn net.Conn) *client {
	c := &client{
		conn: conn,
		send: make(chan protocol.Event, sendBuffer),
		enc:  protocol.TextCodec{},
		dec:  protocol.TextCodec{},
		room: defaultRoom,
	}
	if conn != nil {
		c.sc = bufio.NewScanner(conn)
		c.sc.Buffer(make([]byte, protocol.MaxLineBytes), protocol.MaxLineBytes)
	}
	return c
}

func (c *client) useJSON() {
	c.enc = protocol.JSONCodec{}
	c.dec = protocol.JSONCodec{}
}

func (c *client) trySend(e protocol.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.send <- e:
		return true
	default:
		c.dropped++
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

func (c *client) interruptRead(log *slog.Logger) {
	if err := c.conn.SetReadDeadline(time.Now()); err != nil {
		log.Debug("interrupt read", "err", err)
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
	for e := range c.send {
		if err := c.writeEvent(e); err != nil {
			log.Debug("write", "err", err)
			return
		}
	}
}

func closeConn(conn net.Conn, log *slog.Logger) {
	if err := conn.Close(); err != nil {
		log.Debug("close conn", "err", err)
	}
}
