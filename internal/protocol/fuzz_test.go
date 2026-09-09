package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

var decodeSeeds = []string{
	"",
	"hello",
	"/who",
	"/",
	"//",
	"/say",
	"/join golang",
	"/msg bob hi there",
	"/dance now",
	"\r",
	"\x00\xff",
	strings.Repeat("a", MaxLineBytes+1),
	`{"cmd":"say","text":"hi"}`,
	`{"cmd":"join","args":["golang"]}`,
	`{"cmd":"msg","args":["bob"],"text":"hi"}`,
	`{"cmd":""}`,
	`{"cmd":"dance"}`,
	`{"cmd":123}`,
	`{"args":{}}`,
	`[]`,
	`{`,
}

func checkDecode(t *testing.T, dec Decoder, line []byte) {
	t.Helper()
	cmd, err := dec.Decode(line)
	switch {
	case err == nil:
		if !KnownCommand(cmd.Name) {
			t.Fatalf("decoded unknown command %q from %q", cmd.Name, line)
		}
		var buf bytes.Buffer
		if err := (TextCodec{}).Encode(&buf, Event{Kind: System, Text: cmd.Name}); err != nil {
			t.Fatalf("command name %q is not renderable: %v", cmd.Name, err)
		}
	case errors.Is(err, ErrLineTooLong), errors.Is(err, ErrMalformed), errors.Is(err, ErrUnknownCommand):
	default:
		t.Fatalf("decode %q: untyped error %v", line, err)
	}
}

func FuzzTextDecode(f *testing.F) {
	for _, s := range decodeSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		checkDecode(t, TextCodec{}, line)
	})
}

func FuzzJSONDecode(f *testing.F) {
	for _, s := range decodeSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		checkDecode(t, JSONCodec{}, line)
	})
}
