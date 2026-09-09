package protocol

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrLineTooLong    = errors.New("line too long")
	ErrMalformed      = errors.New("malformed line")
	ErrUnknownCommand = errors.New("unknown command")
)

type Encoder interface {
	Encode(w io.Writer, e Event) error
}

type Decoder interface {
	Decode(line []byte) (Command, error)
}

const freeTextNone = -1

var commands = map[string]int{
	"say":     0,
	"msg":     1,
	"join":    freeTextNone,
	"nick":    freeTextNone,
	"who":     freeTextNone,
	"rooms":   freeTextNone,
	"history": freeTextNone,
	"ping":    freeTextNone,
	"quit":    freeTextNone,
	"help":    freeTextNone,
}

func KnownCommand(name string) bool {
	_, ok := commands[name]
	return ok
}

func buildCommand(name, rest string) (Command, error) {
	leading, ok := commands[name]
	if !ok {
		return Command{Name: name}, fmt.Errorf("%q: %w", name, ErrUnknownCommand)
	}
	rest = strings.TrimSpace(rest)
	if leading == freeTextNone {
		if rest == "" {
			return Command{Name: name}, nil
		}
		return Command{Name: name, Args: strings.Fields(rest)}, nil
	}
	cmd := Command{Name: name}
	for range leading {
		if rest == "" {
			break
		}
		var arg string
		arg, rest, _ = strings.Cut(rest, " ")
		cmd.Args = append(cmd.Args, arg)
		rest = strings.TrimSpace(rest)
	}
	cmd.Text = rest
	return cmd, nil
}
