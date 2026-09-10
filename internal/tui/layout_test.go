package tui

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestComputeLayout(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		want layout
	}{
		{
			name: "minimum supported terminal",
			w:    80, h: 24,
			want: layout{width: 80, height: 24, sidebar: 24, body: 23, messages: 55, log: 21},
		},
		{
			name: "wide",
			w:    120, h: 40,
			want: layout{width: 120, height: 40, sidebar: 24, body: 39, messages: 95, log: 37},
		},
		{
			name: "exactly at the sidebar floor",
			w:    60, h: 24,
			want: layout{width: 60, height: 24, sidebar: 24, body: 23, messages: 35, log: 21},
		},
		{
			name: "below the sidebar floor drops the sidebar",
			w:    59, h: 20,
			want: layout{width: 59, height: 20, sidebar: 0, body: 19, messages: 59, log: 17},
		},
		{
			name: "shortest usable terminal",
			w:    24, h: 6,
			want: layout{width: 24, height: 6, sidebar: 0, body: 5, messages: 24, log: 3},
		},
		{
			name: "too narrow",
			w:    23, h: 24,
			want: layout{width: 23, height: 24, tooSmall: true},
		},
		{
			name: "too short",
			w:    80, h: 5,
			want: layout{width: 80, height: 5, tooSmall: true},
		},
		{
			name: "degenerate",
			w:    0, h: 0,
			want: layout{tooSmall: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeLayout(tt.w, tt.h)
			if got != tt.want {
				t.Errorf("computeLayout(%d, %d) = %+v\nwant                  %+v", tt.w, tt.h, got, tt.want)
			}
			if got.tooSmall {
				return
			}
			if used := got.sidebar + got.messages + boolInt(got.sidebar > 0); used != tt.w {
				t.Errorf("columns = %d, want %d", used, tt.w)
			}
			if used := got.log + 2 + 1; used != tt.h {
				t.Errorf("rows = %d, want %d", used, tt.h)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{name: "empty", in: "", width: 10, want: []string{""}},
		{name: "fits", in: "hello there", width: 11, want: []string{"hello there"}},
		{name: "breaks on the space", in: "hello there", width: 10, want: []string{"hello", "there"}},
		{name: "collapses runs of spaces", in: "a    b", width: 10, want: []string{"a b"}},
		{name: "splits a word longer than the width", in: "supercalifragilistic", width: 6, want: []string{"superc", "alifra", "gilist", "ic"}},
		{name: "keeps existing breaks", in: "one\ntwo", width: 10, want: []string{"one", "two"}},
		{name: "wide runes", in: "日本語のテキスト", width: 6, want: []string{"日本語", "のテキ", "スト"}},
		{name: "width below one is left alone", in: "hello", width: 0, want: []string{"hello"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrap(tt.in, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wrap(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
			if tt.width < 1 {
				return
			}
			for _, line := range got {
				if w := lipgloss.Width(line); w > tt.width {
					t.Errorf("line %q is %d wide, want at most %d", line, w, tt.width)
				}
			}
		})
	}
}

func TestFit(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "pads", in: "ab", width: 5, want: "ab   "},
		{name: "exact", in: "abcde", width: 5, want: "abcde"},
		{name: "truncates with an ellipsis", in: "abcdefg", width: 5, want: "abcd…"},
		{name: "single column", in: "abc", width: 1, want: "…"},
		{name: "wide rune in one column", in: "日本", width: 1, want: "…"},
		{name: "zero width", in: "abc", width: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fit(tt.in, tt.width)
			if got != tt.want {
				t.Errorf("fit(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
			if w := lipgloss.Width(got); tt.width > 0 && w != tt.width {
				t.Errorf("fit(%q, %d) is %d wide", tt.in, tt.width, w)
			}
		})
	}
}

func TestBadge(t *testing.T) {
	tests := []struct {
		unread int
		want   string
	}{
		{unread: -1, want: ""},
		{unread: 0, want: ""},
		{unread: 1, want: "•1"},
		{unread: 42, want: "•42"},
		{unread: 99, want: "•99"},
		{unread: 100, want: "•99+"},
	}
	for _, tt := range tests {
		if got := badge(tt.unread); got != tt.want {
			t.Errorf("badge(%d) = %q, want %q", tt.unread, got, tt.want)
		}
	}
}

func TestRoomLabel(t *testing.T) {
	tests := []struct {
		name    string
		room    string
		members int
		unread  int
		current bool
		width   int
		want    string
	}{
		{name: "plain", room: "#general", members: 3, width: 24, want: " #general (3)            "[:24]},
		{name: "current is marked", room: "#golang", members: 1, current: true, width: 24, want: ">#golang (1)            "},
		{name: "unread badge", room: "#golang", members: 1, unread: 2, width: 24, want: " #golang (1) •2         "},
		{name: "no members known", room: "#ops", width: 24, want: " #ops                   "},
		{name: "truncated", room: "#a-very-long-room-name", members: 12, unread: 7, width: 16, want: " #a-very-long-r…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roomLabel(tt.room, tt.members, tt.unread, tt.current, tt.width)
			if got != tt.want {
				t.Errorf("roomLabel = %q, want %q", got, tt.want)
			}
			if w := lipgloss.Width(got); w != tt.width {
				t.Errorf("roomLabel is %d wide, want %d", w, tt.width)
			}
		})
	}
}

func TestPaletteIndex(t *testing.T) {
	size := len(names)
	seen := map[string]int{}
	for _, who := range []string{"alice", "bob", "carol", "dave", "erin"} {
		got := paletteIndex(who, size)
		if got < 0 || got >= size {
			t.Fatalf("paletteIndex(%q, %d) = %d, want 0..%d", who, size, got, size-1)
		}
		for range 3 {
			if again := paletteIndex(who, size); again != got {
				t.Errorf("paletteIndex(%q) = %d then %d, want it stable", who, got, again)
			}
		}
		seen[who] = got
	}
	if seen["alice"] == seen["bob"] {
		t.Errorf("alice and bob hash to the same colour %d", seen["alice"])
	}
	if got := paletteIndex("alice", 0); got != -1 {
		t.Errorf("paletteIndex with an empty palette = %d, want -1", got)
	}
	if got := newStyles(false).palette; len(got) != 0 {
		t.Errorf("NO_COLOR palette = %v, want empty", got)
	}
}
