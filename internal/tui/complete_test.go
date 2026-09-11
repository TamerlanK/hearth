package tui

import (
	"slices"
	"testing"

	"github.com/TamerlanK/hearth/pkg/protocol"
	tea "github.com/charmbracelet/bubbletea"
)

func TestCandidates(t *testing.T) {
	rooms := []string{"#general", "#golang", "#ops"}
	users := []string{"bill", "bob", "carol"}
	tests := []struct {
		name  string
		line  string
		pos   int
		start int
		want  []string
	}{
		{"empty line", "", 0, 0, nil},
		{"command prefix", "/j", 2, 0, []string{"/join"}},
		{"command prefix is case-insensitive", "/J", 2, 0, []string{"/join"}},
		{"bare slash lists every command", "/", 1, 0, []string{"/close", "/help", "/history", "/join", "/msg", "/nick", "/ping", "/quit", "/rooms", "/say", "/who"}},
		{"join completes rooms without the hash", "/join go", 8, 6, []string{"#golang"}},
		{"join completes rooms with the hash", "/join #g", 8, 6, []string{"#general", "#golang"}},
		{"who and history take rooms", "/history o", 10, 9, []string{"#ops"}},
		{"msg completes users", "/msg b", 6, 5, []string{"bill", "bob"}},
		{"a bare word completes users anywhere", "hey ca", 6, 4, []string{"carol"}},
		{"cursor mid-line completes the word before it", "bo later", 2, 0, []string{"bob"}},
		{"a word ending in a space completes nothing", "bob ", 4, 4, nil},
		{"no match", "zz", 2, 0, nil},
		{"a slash later in the line is not a command", "see /j", 6, 4, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, got := candidates([]rune(tt.line), tt.pos, rooms, users)
			if start != tt.start || !slices.Equal(got, tt.want) {
				t.Errorf("candidates(%q, %d) = %d, %q; want %d, %q", tt.line, tt.pos, start, got, tt.start, tt.want)
			}
		})
	}
}

func TestTabCompletesInTheInput(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, event(protocol.Event{Kind: protocol.Who, Room: "#general", Names: []string{"alice", "bill", "bob"}}))

	m = feed(t, m, typed("/j"), pressed(tea.KeyTab))
	if got := m.input.Value(); got != "/join " {
		t.Fatalf("after /j Tab the input is %q, want %q", got, "/join ")
	}
	if m.focus != paneInput {
		t.Fatal("Tab with text in the input moved focus")
	}
	m = feed(t, m, typed("go"), pressed(tea.KeyTab))
	if got := m.input.Value(); got != "/join #golang " {
		t.Fatalf("room completion gave %q", got)
	}

	m.input.Reset()
	m = feed(t, m, typed("b"), pressed(tea.KeyTab))
	if got := m.input.Value(); got != "bill" {
		t.Fatalf("first candidate = %q, want bill", got)
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if got := m.input.Value(); got != "bob" {
		t.Fatalf("second Tab = %q, want bob", got)
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if got := m.input.Value(); got != "bill" {
		t.Fatalf("third Tab = %q, want to wrap to bill", got)
	}
	m = feed(t, m, typed("!"))
	if got := m.input.Value(); got != "bill!" {
		t.Fatalf("typing after a cycle gave %q", got)
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if got := m.input.Value(); got != "bill!" {
		t.Fatalf("Tab with no candidates changed the input to %q", got)
	}
	if m.focus != paneInput {
		t.Error("Tab with no candidates moved focus")
	}

	m.input.Reset()
	m = feed(t, m, pressed(tea.KeyTab))
	if m.focus != paneMessages {
		t.Errorf("Tab on an empty input left focus at %v, want paneMessages", m.focus)
	}
}
