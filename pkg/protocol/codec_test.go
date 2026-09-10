package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var stamp = time.Date(2026, 9, 9, 15, 4, 5, 0, time.UTC)

func sample() []Event {
	return []Event{
		{Kind: Msg, Room: "#general", From: "alice", Text: "hi", Time: stamp, Seq: 1},
		{Kind: PrivMsg, From: "alice", To: "bob", Text: "psst", Time: stamp, Seq: 2},
		{Kind: System, Text: "protocol json", Time: stamp, Seq: 3},
		{Kind: Join, Room: "#general", From: "bob", Time: stamp, Seq: 4},
		{Kind: Leave, Room: "#general", From: "bob", Time: stamp, Seq: 5},
		{Kind: Nick, Room: "#general", From: "bob", To: "robert", Time: stamp, Seq: 6},
		{Kind: Who, Room: "#general", Names: []string{"alice", "bob"}, Time: stamp, Seq: 7},
		{Kind: Rooms, Names: []string{"#general", "#golang"}, Time: stamp, Seq: 8},
		{Kind: History, Room: "#general", From: "alice", Text: "earlier", Time: stamp, Seq: 9},
		{Kind: Error, Text: "name taken", Time: stamp, Seq: 10},
		{Kind: Pong, Time: stamp, Seq: 11},
	}
}

func TestJSONEventRoundTrip(t *testing.T) {
	for _, want := range sample() {
		t.Run(string(want.Kind), func(t *testing.T) {
			var buf bytes.Buffer
			if err := (JSONCodec{}).Encode(&buf, want); err != nil {
				t.Fatalf("encode: %v", err)
			}
			line := buf.Bytes()
			if n := bytes.Count(line, []byte("\n")); n != 1 || line[len(line)-1] != '\n' {
				t.Fatalf("encode produced %d newlines, want one trailing: %q", n, line)
			}
			var got Event
			if err := json.Unmarshal(line, &got); err != nil {
				t.Fatalf("unmarshal %q: %v", line, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("round trip = %+v, want %+v", got, want)
			}
		})
	}
}

func TestTextEncode(t *testing.T) {
	want := []string{
		"[15:04] alice: hi",
		"[15:04] alice -> bob: psst",
		"* protocol json",
		"* bob joined #general",
		"* bob left #general",
		"* bob is now known as robert",
		"* online in #general (2): alice, bob",
		"* rooms (2): #general, #golang",
		"[15:04] alice: earlier",
		"! name taken",
		"* pong",
	}
	events := sample()
	for i, e := range events {
		t.Run(string(e.Kind), func(t *testing.T) {
			var buf bytes.Buffer
			if err := (TextCodec{}).Encode(&buf, e); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := buf.String(); got != want[i]+"\n" {
				t.Errorf("encode = %q, want %q", got, want[i]+"\n")
			}
		})
	}
}

func TestEncodeRejectsBadEvents(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{"unknown kind", Event{Kind: "shout", Text: "hi", Time: stamp}},
		{"empty kind", Event{Text: "hi", Time: stamp}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, enc := range []Encoder{TextCodec{}, JSONCodec{}} {
				if err := enc.Encode(new(bytes.Buffer), tt.event); !errors.Is(err, ErrMalformed) {
					t.Errorf("%T encode = %v, want ErrMalformed", enc, err)
				}
			}
		})
	}
	injected := Event{Kind: System, Text: "one\nfake: line", Time: stamp}
	if err := (TextCodec{}).Encode(new(bytes.Buffer), injected); !errors.Is(err, ErrMalformed) {
		t.Errorf("text encode of embedded newline = %v, want ErrMalformed", err)
	}
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		text string
		json string
		want Command
	}{
		{"say", "hello", `{"cmd":"say","text":"hello"}`, Command{Name: "say", Text: "hello"}},
		{"say slashless", "/say hello", `{"cmd":"say","text":"hello"}`, Command{Name: "say", Text: "hello"}},
		{"empty", "", ``, Command{Name: "say"}},
		{"join", "/join golang", `{"cmd":"join","args":["golang"]}`, Command{Name: "join", Args: []string{"golang"}}},
		{"nick", "/nick bob", `{"cmd":"nick","args":["bob"]}`, Command{Name: "nick", Args: []string{"bob"}}},
		{"who bare", "/who", `{"cmd":"who"}`, Command{Name: "who"}},
		{"who room", "/who golang", `{"cmd":"who","args":["golang"]}`, Command{Name: "who", Args: []string{"golang"}}},
		{"rooms", "/rooms", `{"cmd":"rooms"}`, Command{Name: "rooms"}},
		{"history", "/history golang", `{"cmd":"history","args":["golang"]}`, Command{Name: "history", Args: []string{"golang"}}},
		{"msg", "/msg bob hi there", `{"cmd":"msg","args":["bob"],"text":"hi there"}`, Command{Name: "msg", Args: []string{"bob"}, Text: "hi there"}},
		{"ping", "/ping", `{"cmd":"ping"}`, Command{Name: "ping"}},
		{"quit", "/quit", `{"cmd":"quit"}`, Command{Name: "quit"}},
		{"help", "/help", `{"cmd":"help"}`, Command{Name: "help"}},
		{"uppercase command", "/JOIN golang", `{"cmd":"join","args":["golang"]}`, Command{Name: "join", Args: []string{"golang"}}},
		{"carriage return", "/who\r", `{"cmd":"who"}` + "\r", Command{Name: "who"}},
		{"extra spaces", "/join   golang  ", `{"cmd":"join","args":["golang"]}`, Command{Name: "join", Args: []string{"golang"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (TextCodec{}).Decode([]byte(tt.text))
			if err != nil {
				t.Fatalf("text decode %q: %v", tt.text, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("text decode %q = %+v, want %+v", tt.text, got, tt.want)
			}
			got, err = (JSONCodec{}).Decode([]byte(tt.json))
			if err != nil {
				t.Fatalf("json decode %q: %v", tt.json, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("json decode %q = %+v, want %+v", tt.json, got, tt.want)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	long := strings.Repeat("a", MaxLineBytes+1)
	tests := []struct {
		name  string
		codec Decoder
		line  []byte
		want  error
	}{
		{"text oversized", TextCodec{}, []byte(long), ErrLineTooLong},
		{"json oversized", JSONCodec{}, []byte(`{"cmd":"say","text":"` + long + `"}`), ErrLineTooLong},
		{"text invalid utf8", TextCodec{}, []byte{'h', 'i', 0xff, 0xfe}, ErrMalformed},
		{"json invalid utf8", JSONCodec{}, []byte{'{', 0xff, '}'}, ErrMalformed},
		{"json not an object", JSONCodec{}, []byte(`["say"]`), ErrMalformed},
		{"json truncated", JSONCodec{}, []byte(`{"cmd":"say"`), ErrMalformed},
		{"json no cmd", JSONCodec{}, []byte(`{"text":"hi"}`), ErrMalformed},
		{"text unknown command", TextCodec{}, []byte("/dance now"), ErrUnknownCommand},
		{"json unknown command", JSONCodec{}, []byte(`{"cmd":"dance"}`), ErrUnknownCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.codec.Decode(tt.line)
			if !errors.Is(err, tt.want) {
				t.Errorf("decode = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecodeUnknownCommandKeepsName(t *testing.T) {
	for _, tt := range []struct {
		codec Decoder
		line  string
	}{{TextCodec{}, "/dance now"}, {JSONCodec{}, `{"cmd":"dance"}`}} {
		cmd, err := tt.codec.Decode([]byte(tt.line))
		if !errors.Is(err, ErrUnknownCommand) {
			t.Fatalf("%T decode = %v, want ErrUnknownCommand", tt.codec, err)
		}
		if cmd.Name != "dance" {
			t.Errorf("%T name = %q, want \"dance\"", tt.codec, cmd.Name)
		}
	}
}
