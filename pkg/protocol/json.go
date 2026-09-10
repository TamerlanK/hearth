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
