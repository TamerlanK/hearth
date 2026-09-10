package protocol

import (
	"io"
	"testing"
)

func BenchmarkJSONEncode(b *testing.B) {
	e := Event{Kind: Msg, Room: "#general", From: "alice", Text: "hello everyone, how is it going today?", Time: stamp, Seq: 42}
	codec := JSONCodec{}
	b.ReportAllocs()
	for b.Loop() {
		if err := codec.Encode(io.Discard, e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONDecode(b *testing.B) {
	line := []byte(`{"cmd":"msg","args":["bob"],"text":"hello everyone, how is it going today?"}`)
	codec := JSONCodec{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := codec.Decode(line); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeEvent(b *testing.B) {
	line := []byte(`{"kind":"msg","room":"#general","from":"alice","text":"hello everyone, how is it going today?","time":"2026-09-09T15:04:05.123456789+04:00","seq":42}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DecodeEvent(line); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTextEncode(b *testing.B) {
	e := Event{Kind: Msg, Room: "#general", From: "alice", Text: "hello everyone, how is it going today?", Time: stamp, Seq: 42}
	codec := TextCodec{}
	b.ReportAllocs()
	for b.Loop() {
		if err := codec.Encode(io.Discard, e); err != nil {
			b.Fatal(err)
		}
	}
}
