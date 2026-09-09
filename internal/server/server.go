package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

type Config struct {
	MaxClients int

	IdleTimeout time.Duration

	Logger *slog.Logger
}

type Server struct {
	cfg    Config
	log    *slog.Logger
	hub    *hub
	active atomic.Int32
	wg     sync.WaitGroup
}

func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{cfg: cfg, log: cfg.Logger, hub: newHub()}
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.hub.run(ctx)
	}()
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.log.Warn("close listener", "err", err)
		}
	}()

	var acceptErr error
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				acceptErr = fmt.Errorf("accept: %w", err)
			}
			break
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}

	cancel()
	s.wg.Wait()
	return acceptErr
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	log := s.log.With("remote", conn.RemoteAddr().String())

	if n := s.active.Add(1); s.cfg.MaxClients > 0 && int(n) > s.cfg.MaxClients {
		s.active.Add(-1)
		log.Info("rejected", "reason", "server full")
		if err := writeLine(conn, "! server full, try again later"); err != nil {
			log.Debug("write", "err", err)
		}
		closeConn(conn, log)
		return
	}
	defer s.active.Add(-1)

	c := newClient(conn)

	connDone := make(chan struct{})
	defer close(connDone)
	go func() {
		select {
		case <-ctx.Done():
			c.interruptRead(log)
		case <-connDone:
		}
	}()

	if err := s.handshake(ctx, c); err != nil {
		log.Info("handshake failed", "err", err)
		closeConn(conn, log)
		return
	}
	log = log.With("name", c.name)
	log.Info("joined")
	s.hub.say(ctx, "* "+c.name+" joined")

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop(log)
	}()

	s.readLoop(ctx, c)
	s.hub.leave(ctx, c)
	<-writerDone
	s.hub.say(ctx, "* "+c.name+" left")
	log.Info("left", "dropped", c.droppedCount())
}

func (s *Server) handshake(ctx context.Context, c *client) error {
	if err := c.writeLine("Welcome to hearth. Enter a name (1-20 characters, no spaces):"); err != nil {
		return err
	}
	for {
		name, err := c.readLine(s.cfg.IdleTimeout)
		if err != nil {
			return fmt.Errorf("read name: %w", err)
		}
		if err := validateName(name); err != nil {
			if err := c.writeLine("! " + err.Error() + ", try again:"); err != nil {
				return err
			}
			continue
		}
		c.name = name
		switch err := s.hub.join(ctx, c); {
		case err == nil:
			return nil
		case errors.Is(err, errNameTaken):
			if err := c.writeLine("! name taken, try another:"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("register %q: %w", name, err)
		}
	}
}

func (s *Server) readLoop(ctx context.Context, c *client) {
	for {
		line, err := c.readLine(s.cfg.IdleTimeout)
		if err != nil {
			s.reportReadError(ctx, c, err)
			return
		}
		if line == "" {
			continue
		}
		if line[0] == '/' {
			if quit := s.command(ctx, c, line); quit {
				return
			}
			continue
		}
		s.hub.say(ctx, fmt.Sprintf("[%s] %s: %s", time.Now().Format("15:04"), c.name, printable(line)))
	}
}

func (s *Server) reportReadError(ctx context.Context, c *client, err error) {
	var netErr net.Error
	switch {
	case ctx.Err() != nil:

	case errors.As(err, &netErr) && netErr.Timeout():
		c.trySend(fmt.Sprintf("* disconnected: idle for %s", s.cfg.IdleTimeout))
	case errors.Is(err, errLineTooLong):
		c.trySend("! line too long, disconnecting")
	}
}

func (s *Server) command(ctx context.Context, c *client, line string) (quit bool) {
	cmd, _, _ := strings.Cut(line, " ")
	switch cmd {
	case "/who":
		names := s.hub.who(ctx)
		c.trySend(fmt.Sprintf("* online (%d): %s", len(names), strings.Join(names, ", ")))
	case "/quit":
		c.trySend("* bye")
		return true
	case "/help":
		c.trySend("* commands: /who (list users), /quit (leave), /help (this message)")
	default:
		c.trySend("! unknown command " + cmd + " (try /help)")
	}
	return false
}

func validateName(name string) error {
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return errors.New("name is empty")
	case n > maxNameLen:
		return fmt.Errorf("name is longer than %d characters", maxNameLen)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return errors.New("name must be printable with no spaces")
		}
	}
	return nil
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}
