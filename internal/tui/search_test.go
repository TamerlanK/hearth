package tui

import (
	"strings"
	"testing"

	"github.com/TamerlanK/hearth/pkg/protocol"
	tea "github.com/charmbracelet/bubbletea"
)

func chatty(t *testing.T) *Model {
	t.Helper()
	m := joined(t, 100, 30)
	for _, text := range []string{"deploy started", "the build is green", "deploy finished", "unrelated chatter"} {
		m = feed(t, m, event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: text}))
	}
	return m
}

func typeAll(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	for _, r := range s {
		m = feed(t, m, typed(string(r)))
	}
	return m
}

func TestSearchFindsCyclesAndCloses(t *testing.T) {
	m := chatty(t)
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if !m.find.on || m.input.Focused() {
		t.Fatalf("Ctrl+F did not enter search (on=%v, input focused=%v)", m.find.on, m.input.Focused())
	}
	if got := plain(m.View()); !strings.Contains(got, "find ") || !strings.Contains(got, "type to search") {
		t.Fatalf("search bar is not drawn:\n%s", got)
	}

	m = typeAll(t, m, "deploy")
	if len(m.find.hits) != 2 {
		t.Fatalf("hits = %v, want two lines matching deploy", m.find.hits)
	}
	if got := plain(m.View()); !strings.Contains(got, "find deploy") || !strings.Contains(got, "2 of 2") {
		t.Errorf("search bar = %q, want the query and the counter", lastLines(got, 2))
	}
	if m.follow {
		t.Error("a search did not release the scroll lock")
	}

	first := m.find.at
	m = feed(t, m, pressed(tea.KeyEnter))
	if m.find.at == first {
		t.Error("Enter did not move to the next hit")
	}
	m = feed(t, m, pressed(tea.KeyUp))
	if m.find.at != first {
		t.Errorf("Up did not come back to hit %d", first)
	}

	m = feed(t, m, pressed(tea.KeyBackspace))
	if got := m.find.query; got != "deplo" {
		t.Errorf("after Backspace the query is %q", got)
	}

	m = typeAll(t, m, "zzz")
	if len(m.find.hits) != 0 {
		t.Fatalf("hits = %v, want none", m.find.hits)
	}
	if got := plain(m.View()); !strings.Contains(got, "no match") {
		t.Errorf("search bar does not say no match:\n%s", lastLines(got, 2))
	}

	m = feed(t, m, pressed(tea.KeyEsc))
	if m.find.on || !m.input.Focused() || m.focus != paneInput {
		t.Errorf("Esc left search on=%v, focus=%v", m.find.on, m.focus)
	}
	if m.input.Value() != "" {
		t.Errorf("the search text leaked into the input: %q", m.input.Value())
	}
}

func TestSearchIsCaseInsensitiveAndScrollsToTheHit(t *testing.T) {
	m := chatty(t)
	for i := range 40 {
		m = feed(t, m, event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "carol", Text: "filler " + strings.Repeat("x", i%5+1)}))
	}
	bottom := m.body.YOffset
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	m = typeAll(t, m, "DEPLOY STARTED")
	if len(m.find.hits) != 1 {
		t.Fatalf("hits = %v, want one case-insensitive match", m.find.hits)
	}
	if m.body.YOffset >= bottom {
		t.Errorf("the viewport stayed at %d, want it scrolled up to the hit", m.body.YOffset)
	}
}

func TestSearchKeysDoNotReachTheChat(t *testing.T) {
	m := chatty(t)
	before := len(texts(m, "#general"))
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	m = typeAll(t, m, "hello")
	m = feed(t, m, pressed(tea.KeyEnter))
	if got := len(texts(m, "#general")); got != before {
		t.Errorf("typing in search added %d transcript lines", got-before)
	}
	next, cmd := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = next.(*Model)
	if cmd != nil {
		t.Error("Ctrl+F in search produced a command")
	}
	if m.find.on {
		t.Error("Ctrl+F did not toggle search off")
	}
}

func lastLines(s string, n int) string {
	rows := strings.Split(s, "\n")
	if len(rows) < n {
		return s
	}
	return strings.Join(rows[len(rows)-n:], "\n")
}
