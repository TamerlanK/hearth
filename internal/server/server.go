package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TamerlanK/hearth/pkg/protocol"
)

var (
	errServerFull    = errors.New("server full, try again later")
	errTooManyFromIP = errors.New("too many connections from your address")
	errPanic         = errors.New("connection handler panicked")
	errBadToken      = errors.New("bad token")
)

type Config struct {
	MaxClients int

	MaxClientsPerIP int

	IdleTimeout time.Duration

	HistorySize int

	DefaultRoom string

	MaxRooms int

	MessagesPerSecond float64

	Burst int

	MaxDropsInARow int

	MOTD string

	Token string

	Logger *slog.Logger

	decorateDecoder func(protocol.Decoder) protocol.Decoder
}

func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("max_clients", c.MaxClients),
		slog.Int("max_clients_per_ip", c.MaxClientsPerIP),
		slog.Duration("idle_timeout", c.IdleTimeout),
		slog.Duration("handshake_timeout", handshakeTimeout),
		slog.String("default_room", c.DefaultRoom),
		slog.Int("history_size", c.HistorySize),
		slog.Int("max_rooms", c.MaxRooms),
		slog.Float64("messages_per_second", c.MessagesPerSecond),
		slog.Int("burst", c.Burst),
		slog.Int("max_drops_in_a_row", c.MaxDropsInARow),
		slog.Int("max_message_runes", maxMessageRunes),
		slog.Int("motd_lines", len(motdLines(c.MOTD))),
		slog.Bool("token_required", c.Token != ""),
	)
}

type Server struct {
	cfg Config
	log *slog.Logger
	hub *hub
	wg  sync.WaitGroup

	mu     sync.Mutex
	perIP  map[string]int
	active int
}

func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HistorySize <= 0 {
		cfg.HistorySize = defaultHistorySize
	}
	if cfg.DefaultRoom == "" {
		cfg.DefaultRoom = defaultRoom
	}
	if cfg.MessagesPerSecond > 0 && cfg.Burst <= 0 {
		cfg.Burst = 1
	}
	cfg.DefaultRoom = roomName(cfg.DefaultRoom)
	if err := validateRoom(cfg.DefaultRoom); err != nil {
		cfg.Logger.Warn("default room rejected, falling back",
			"event", "config", "room", cfg.DefaultRoom, "err", err, "fallback", defaultRoom)
		cfg.DefaultRoom = defaultRoom
	}
	return &Server{cfg: cfg, log: cfg.Logger, hub: newHub(cfg), perIP: make(map[string]int)}
}

func (s *Server) Config() Config {
	return s.cfg
}

func (s *Server) ActiveConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
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
			s.log.Warn("close listener", "event", "shutdown", "err", err)
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

func (s *Server) admit(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.MaxClients > 0 && s.active >= s.cfg.MaxClients {
		return errServerFull
	}
	if s.cfg.MaxClientsPerIP > 0 && s.perIP[host] >= s.cfg.MaxClientsPerIP {
		return errTooManyFromIP
	}
	s.active++
	s.perIP[host]++
	return nil
}

func (s *Server) release(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if n := s.perIP[host] - 1; n > 0 {
		s.perIP[host] = n
	} else {
		delete(s.perIP, host)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	c := newClient(conn, s.cfg)
	host := remoteHost(conn)
	log := s.log.With("client_id", c.id, "remote_addr", conn.RemoteAddr().String())
	defer recoverPanic(log, "connection")

	if err := s.admit(host); err != nil {
		log.Info("connection rejected", "event", "reject", "reason", err.Error())
		if werr := c.writeEvent(errorEvent(err.Error())); werr != nil {
			log.Debug("write failed", "event", "write_error", "err", werr)
		}
		closeConn(conn, log)
		return
	}
	defer s.release(host)

	connectionsTotal.Inc()
	connectionsCurrent.Inc()
	defer connectionsCurrent.Dec()

	connDone := make(chan struct{})
	defer close(connDone)
	go func() {
		select {
		case <-ctx.Done():
			c.interruptRead(log)
		case <-c.overload:
			c.interruptRead(log)
		case <-connDone:
		}
	}()

	name, err := s.handshake(ctx, c, log)
	if err != nil {
		log.Info("handshake failed", "event", "handshake_failed", "err", err)
		closeConn(conn, log)
		return
	}
	log = log.With("name", name, "room", s.cfg.DefaultRoom)
	log.Info("client joined", "event", "join")

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop(log)
	}()

	s.readLoop(ctx, c, log)
	s.hub.leave(ctx, c)
	<-writerDone
	log.Info("client left", "event", "leave", "dropped", c.droppedCount(), "overloaded", c.isOverloaded())
}

func (s *Server) readLoop(ctx context.Context, c *client, log *slog.Logger) {
	defer recoverPanic(log, "read_loop")
	for {
		if c.isOverloaded() {
			log.Warn("outbox jammed, disconnecting", "event", "overload", "dropped", c.droppedCount())
			return
		}
		line, err := c.readLine(s.cfg.IdleTimeout)
		if err != nil {
			s.reportReadError(ctx, c, err)
			return
		}
		if !c.allow() {
			rateLimitedTotal.Inc()
			if !c.limited {
				c.limited = true
				c.trySend(errorEvent("rate limited"))
				log.Warn("client rate limited", "event", "rate_limited")
			}
			continue
		}
		cmd, err := c.dec.Decode(line)
		if err != nil {
			c.trySend(errorEvent(decodeError(cmd, err).Error()))
			if errors.Is(err, protocol.ErrLineTooLong) {
				return
			}
			continue
		}
		if n := utf8.RuneCountInString(cmd.Text); n > maxMessageRunes {
			c.trySend(errorEvent(fmt.Sprintf("message is longer than %d characters", maxMessageRunes)))
			continue
		}
		switch cmd.Name {
		case "quit":
			c.trySend(systemEvent("bye"))
			return
		case "help":
			c.trySend(systemEvent(protocol.HelpText()))
		default:
			c.trySendAll(s.hub.do(ctx, c, cmd))
		}
	}
}

func (s *Server) reportReadError(ctx context.Context, c *client, err error) {
	var netErr net.Error
	switch {
	case ctx.Err() != nil:

	case c.isOverloaded():

	case errors.As(err, &netErr) && netErr.Timeout():
		c.trySend(systemEvent(fmt.Sprintf("disconnected: idle for %s", s.cfg.IdleTimeout)))
	case errors.Is(err, protocol.ErrLineTooLong):
		c.trySend(errorEvent("line too long, disconnecting"))
	}
}

func remoteHost(conn net.Conn) string {
	addr := conn.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
