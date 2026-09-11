package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/TamerlanK/hearth/pkg/protocol"
)

const (
	greeting    = "Welcome to hearth. Enter a name (1-20 characters, no spaces):"
	tokenPrompt = "This server requires a token. Enter it:"
	badToken    = "bad token"
)

func (s *Server) authorised(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) == 1
}

func (s *Server) handshake(ctx context.Context, c *client, log *slog.Logger) (name string, err error) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(log, "handshake", r)
			name, err = "", errPanic
		}
	}()
	if err := c.setReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return "", err
	}
	authed := s.cfg.Token == ""
	prompt := greeting
	if !authed {
		prompt = tokenPrompt
	}
	if err := c.writeEvent(systemEvent(prompt)); err != nil {
		return "", err
	}
	for first := true; ; first = false {
		line, err := c.readLine(0)
		if err != nil {
			return "", fmt.Errorf("handshake read: %w", err)
		}
		if first {
			if token, ok := protocol.ParseHello(string(line)); ok {
				c.useJSON(s.cfg)
				if !authed && !s.authorised(token) {
					return "", s.refuse(c)
				}
				authed = true
				if err := c.writeEvent(systemEvent("protocol json")); err != nil {
					return "", err
				}
				continue
			}
			if !authed {
				if !s.authorised(strings.TrimSuffix(string(line), "\r")) {
					return "", s.refuse(c)
				}
				if err := c.writeEvent(systemEvent(greeting)); err != nil {
					return "", err
				}
				continue
			}
		}
		candidate, err := proposedName(c.dec, line)
		if err != nil {
			if err := c.writeEvent(errorEvent(err.Error() + ", try again:")); err != nil {
				return "", err
			}
			continue
		}
		switch err := s.hub.join(ctx, c, candidate); {
		case err == nil:
			if err := c.setReadDeadline(time.Time{}); err != nil {
				return "", err
			}
			return candidate, nil
		case errors.Is(err, errNameTaken):
			if err := c.writeEvent(errorEvent("name taken, try another:")); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("register %q: %w", candidate, err)
		}
	}
}

func (s *Server) refuse(c *client) error {
	if err := c.writeEvent(errorEvent(badToken)); err != nil {
		return err
	}
	return errBadToken
}

func proposedName(dec protocol.Decoder, line []byte) (string, error) {
	cmd, err := dec.Decode(line)
	if err != nil {
		return "", decodeError(cmd, err)
	}
	var name string
	switch {
	case cmd.Name == "say":
		name = cmd.Text
	case cmd.Name == "nick" && len(cmd.Args) == 1:
		name = cmd.Args[0]
	default:
		return "", errors.New("expected a name")
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	return name, nil
}

func decodeError(cmd protocol.Command, err error) error {
	switch {
	case errors.Is(err, protocol.ErrUnknownCommand):
		return fmt.Errorf("unknown command %s (try /help)", cmd.Name)
	case errors.Is(err, protocol.ErrLineTooLong):
		return protocol.ErrLineTooLong
	default:
		return protocol.ErrMalformed
	}
}
