package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/TamerlanK/hearth/internal/protocol"
)

type sessionState int

const (
	negotiating sessionState = iota
	naming
	chatting
)

const greeting = "Welcome to hearth. Enter a name (1-20 characters, no spaces):"

func (s *Server) handshake(ctx context.Context, c *client) error {
	if err := c.writeEvent(systemEvent(greeting)); err != nil {
		return err
	}
	for state := negotiating; state != chatting; {
		line, err := c.readLine(s.cfg.IdleTimeout)
		if err != nil {
			return fmt.Errorf("handshake read: %w", err)
		}
		if state == negotiating {
			state = naming
			if bytes.Equal(bytes.TrimSuffix(line, []byte("\r")), []byte(protocol.Hello)) {
				c.useJSON()
				if err := c.writeEvent(systemEvent("protocol json")); err != nil {
					return err
				}
				continue
			}
		}
		name, err := proposedName(c.dec, line)
		if err != nil {
			if err := c.writeEvent(errorEvent(err.Error() + ", try again:")); err != nil {
				return err
			}
			continue
		}
		c.name = name
		switch err := s.hub.join(ctx, c); {
		case err == nil:
			state = chatting
		case errors.Is(err, errNameTaken):
			if err := c.writeEvent(errorEvent("name taken, try another:")); err != nil {
				return err
			}
		default:
			return fmt.Errorf("register %q: %w", name, err)
		}
	}
	return nil
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
