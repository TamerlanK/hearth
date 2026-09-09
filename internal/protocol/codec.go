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

type CommandSpec struct {
	Name    string
	Usage   string
	leading int
}

var Commands = []CommandSpec{
	{Name: "say", Usage: "/say <text>", leading: 0},
	{Name: "msg", Usage: "/msg <name> <text>", leading: 1},
	{Name: "join", Usage: "/join <room>", leading: freeTextNone},
	{Name: "nick", Usage: "/nick <name>", leading: freeTextNone},
	{Name: "who", Usage: "/who [room]", leading: freeTextNone},
	{Name: "rooms", Usage: "/rooms", leading: freeTextNone},
	{Name: "history", Usage: "/history [room]", leading: freeTextNone},
	{Name: "ping", Usage: "/ping", leading: freeTextNone},
	{Name: "quit", Usage: "/quit", leading: freeTextNone},
	{Name: "help", Usage: "/help", leading: freeTextNone},
}

var commands = func() map[string]CommandSpec {
	m := make(map[string]CommandSpec, len(Commands))
	for _, spec := range Commands {
		m[spec.Name] = spec
	}
	return m
}()

func KnownCommand(name string) bool {
	_, ok := commands[name]
	return ok
}

func Usage(name string) string {
	return "usage: " + commands[name].Usage
}

func HelpText() string {
	usages := make([]string, 0, len(Commands))
	for _, spec := range Commands {
		usages = append(usages, spec.Usage)
	}
	return "commands: " + strings.Join(usages, ", ") + " (a bare line is /say)"
}

func buildCommand(name, rest string) (Command, error) {
	spec, ok := commands[name]
	if !ok {
		return Command{Name: name}, fmt.Errorf("%q: %w", name, ErrUnknownCommand)
	}
	rest = strings.TrimSpace(rest)
	if spec.leading == freeTextNone {
		if rest == "" {
			return Command{Name: name}, nil
		}
		return Command{Name: name, Args: strings.Fields(rest)}, nil
	}
	cmd := Command{Name: name}
	for range spec.leading {
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
