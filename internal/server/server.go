package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TamerlanK/hearth/internal/protocol"
)

const helpText = "commands: /say <text> (or just type), /msg <name> <text>, /join <room>, " +
	"/nick <name>, /who [room], /rooms, /history [room], /ping, /quit, /help"

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
	c := newClient(conn)

	if n := s.active.Add(1); s.cfg.MaxClients > 0 && int(n) > s.cfg.MaxClients {
		s.active.Add(-1)
		log.Info("rejected", "reason", "server full")
		if err := c.writeEvent(errorEvent("server full, try again later")); err != nil {
			log.Debug("write", "err", err)
		}
		closeConn(conn, log)
		return
	}
	defer s.active.Add(-1)

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
	name := c.name
	log = log.With("name", name)
	log.Info("joined")

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop(log)
	}()

	s.readLoop(ctx, c)
	s.hub.leave(ctx, c)
	<-writerDone
	log.Info("left", "dropped", c.droppedCount())
}

func (s *Server) readLoop(ctx context.Context, c *client) {
	for {
		line, err := c.readLine(s.cfg.IdleTimeout)
		if err != nil {
			s.reportReadError(ctx, c, err)
			return
		}
		cmd, err := c.dec.Decode(line)
		if err != nil {
			c.trySend(errorEvent(decodeError(cmd, err).Error()))
			if errors.Is(err, protocol.ErrLineTooLong) {
				return
			}
			continue
		}
		switch cmd.Name {
		case "quit":
			c.trySend(systemEvent("bye"))
			return
		case "help":
			c.trySend(systemEvent(helpText))
		default:
			c.trySendAll(s.hub.do(ctx, c, cmd))
		}
	}
}

func (s *Server) reportReadError(ctx context.Context, c *client, err error) {
	var netErr net.Error
	switch {
	case ctx.Err() != nil:

	case errors.As(err, &netErr) && netErr.Timeout():
		c.trySend(systemEvent(fmt.Sprintf("disconnected: idle for %s", s.cfg.IdleTimeout)))
	case errors.Is(err, protocol.ErrLineTooLong):
		c.trySend(errorEvent("line too long, disconnecting"))
	}
}
