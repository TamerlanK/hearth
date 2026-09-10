package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

type JSONCodec struct{}

type wireCommand struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args,omitempty"`
	Text string   `json:"text,omitempty"`
}

func (JSONCodec) Encode(w io.Writer, e Event) error {
	if !KnownKind(e.Kind) {
		return fmt.Errorf("kind %q: %w", e.Kind, ErrMalformed)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", e.Kind, err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write json event: %w", err)
	}
	return nil
}

func (JSONCodec) Decode(line []byte) (Command, error) {
	if len(line) > MaxLineBytes {
		return Command{}, ErrLineTooLong
	}
	line = bytes.TrimSpace(bytes.TrimSuffix(line, []byte("\r")))
	if len(line) == 0 {
		return Command{Name: "say"}, nil
	}
	if !utf8.Valid(line) {
		return Command{}, fmt.Errorf("json line is not utf-8: %w", ErrMalformed)
	}
	var wire wireCommand
	if err := json.Unmarshal(line, &wire); err != nil {
		return Command{}, fmt.Errorf("decode json line: %w: %w", ErrMalformed, err)
	}
	if wire.Cmd == "" {
		return Command{}, fmt.Errorf("json line has no cmd: %w", ErrMalformed)
	}
	if !KnownCommand(wire.Cmd) {
		return Command{Name: wire.Cmd}, fmt.Errorf("%q: %w", wire.Cmd, ErrUnknownCommand)
	}
	return Command{Name: wire.Cmd, Args: wire.Args, Text: wire.Text}, nil
}

func EncodeCommand(w io.Writer, cmd Command) error {
	if !KnownCommand(cmd.Name) {
		return fmt.Errorf("%q: %w", cmd.Name, ErrUnknownCommand)
	}
	b, err := json.Marshal(wireCommand{Cmd: cmd.Name, Args: cmd.Args, Text: cmd.Text})
	if err != nil {
		return fmt.Errorf("marshal %s command: %w", cmd.Name, err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write json command: %w", err)
	}
	return nil
}

func DecodeEvent(line []byte) (Event, error) {
	if len(line) > MaxLineBytes {
		return Event{}, ErrLineTooLong
	}
	var e Event
	if err := json.Unmarshal(bytes.TrimSpace(line), &e); err != nil {
		return Event{}, fmt.Errorf("decode json event: %w: %w", ErrMalformed, err)
	}
	if !KnownKind(e.Kind) {
		return Event{}, fmt.Errorf("kind %q: %w", e.Kind, ErrMalformed)
	}
	return e, nil
}
