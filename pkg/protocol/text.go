package protocol

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const clockLayout = "15:04"

type TextCodec struct{}

func (TextCodec) Encode(w io.Writer, e Event) error {
	line, err := renderText(e)
	if err != nil {
		return err
	}
	if strings.ContainsAny(line, "\r\n") {
		return fmt.Errorf("event contains a line break: %w", ErrMalformed)
	}
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		return fmt.Errorf("write text event: %w", err)
	}
	return nil
}

func (TextCodec) Decode(line []byte) (Command, error) {
	if len(line) > MaxLineBytes {
		return Command{}, ErrLineTooLong
	}
	if !utf8.Valid(line) {
		return Command{}, fmt.Errorf("text line is not utf-8: %w", ErrMalformed)
	}
	s := strings.TrimSpace(strings.TrimSuffix(string(line), "\r"))
	if s == "" || !strings.HasPrefix(s, "/") {
		return Command{Name: "say", Text: s}, nil
	}
	name, rest, _ := strings.Cut(s[1:], " ")
	return buildCommand(strings.ToLower(name), rest)
}

func renderText(e Event) (string, error) {
	switch e.Kind {
	case Msg, History:
		return fmt.Sprintf("[%s] %s: %s", e.Time.Format(clockLayout), e.From, e.Text), nil
	case PrivMsg:
		return fmt.Sprintf("[%s] %s -> %s: %s", e.Time.Format(clockLayout), e.From, e.To, e.Text), nil
	case System:
		return "* " + e.Text, nil
	case Join:
		return fmt.Sprintf("* %s joined %s", e.From, e.Room), nil
	case Leave:
		return fmt.Sprintf("* %s left %s", e.From, e.Room), nil
	case Nick:
		return fmt.Sprintf("* %s is now known as %s", e.From, e.To), nil
	case Who:
		return fmt.Sprintf("* online in %s (%d): %s", e.Room, len(e.Names), strings.Join(e.Names, ", ")), nil
	case Rooms:
		return fmt.Sprintf("* rooms (%d): %s", len(e.Names), strings.Join(e.Names, ", ")), nil
	case Error:
		return "! " + e.Text, nil
	case Pong:
		return "* pong", nil
	default:
		return "", fmt.Errorf("kind %q: %w", e.Kind, ErrMalformed)
	}
}
