package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	maxNameLen   = 20
	maxLineBytes = 4096
	sendBuffer   = 32
	writeTimeout = 5 * time.Second
)

var errLineTooLong = errors.New("line too long")

type client struct {
	conn net.Conn
	sc   *bufio.Scanner
	name string
	send chan string

	mu      sync.Mutex
	closed  bool
	dropped int
}

func newClient(conn net.Conn) *client {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, maxLineBytes), maxLineBytes)
	return &client{conn: conn, sc: sc, send: make(chan string, sendBuffer)}
}

func (c *client) trySend(msg string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.send <- msg:
		return true
	default:
		c.dropped++
		return false
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

func (c *client) readLine(idle time.Duration) (string, error) {
	if idle > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return "", fmt.Errorf("set read deadline: %w", err)
		}
	}
	if !c.sc.Scan() {
		err := c.sc.Err()
		switch {
		case err == nil:
			return "", io.EOF
		case errors.Is(err, bufio.ErrTooLong):
			return "", errLineTooLong
		default:
			return "", fmt.Errorf("read: %w", err)
		}
	}
	return strings.TrimSuffix(c.sc.Text(), "\r"), nil
}

func (c *client) interruptRead(log *slog.Logger) {
	if err := c.conn.SetReadDeadline(time.Now()); err != nil {
		log.Debug("interrupt read", "err", err)
	}
}

func (c *client) writeLine(line string) error {
	return writeLine(c.conn, line)
}

func (c *client) writeLoop(log *slog.Logger) {
	defer closeConn(c.conn, log)
	for msg := range c.send {
		if err := c.writeLine(msg); err != nil {
			log.Debug("write", "err", err)
			return
		}
	}
}

func writeLine(conn net.Conn, line string) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func closeConn(conn net.Conn, log *slog.Logger) {
	if err := conn.Close(); err != nil {
		log.Debug("close conn", "err", err)
	}
}
