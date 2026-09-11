package tui

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var at = time.Date(2026, 9, 10, 15, 4, 0, 0, time.UTC)

func fresh(t *testing.T, width, height int) *Model {
	t.Helper()
	m := newModel(nil, "chat.example.com:4000", "alice", false)
	m.resize(width, height)
	return m
}

func feed(t *testing.T, m *Model, msgs ...tea.Msg) *Model {
	t.Helper()
	var next tea.Model = m
	for _, msg := range msgs {
		next, _ = next.Update(msg)
	}
	return next.(*Model)
}

func event(e protocol.Event) tea.Msg {
	if e.Time.IsZero() {
		e.Time = at
	}
	return eventMsg(e)
}

func typed(s string) tea.Msg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func pressed(k tea.KeyType) tea.Msg {
	return tea.KeyMsg{Type: k}
}

func texts(m *Model, room string) []string {
	r, ok := m.rooms[room]
	if !ok {
		return nil
	}
	var out []string
	for _, e := range r.log.Snapshot() {
		out = append(out, string(e.Kind)+":"+e.Text)
	}
	return out
}

func joined(t *testing.T, width, height int) *Model {
	t.Helper()
	return feed(t, fresh(t, width, height),
		event(protocol.Event{Kind: protocol.Rooms, Names: []string{"#general (2)", "#golang (1)"}}),
		event(protocol.Event{Kind: protocol.Join, Room: "#general", From: "alice"}),
		event(protocol.Event{Kind: protocol.Who, Room: "#general", Names: []string{"alice", "bob"}}),
	)
}

func TestRoomSwitchAndUnread(t *testing.T) {
	m := joined(t, 100, 30)
	if m.current != "#general" {
		t.Fatalf("current room = %q, want #general", m.current)
	}

	m = feed(t, m, event(protocol.Event{Kind: protocol.Join, Room: "#golang", From: "alice"}))
	if m.current != "#golang" {
		t.Fatalf("after joining, current room = %q, want #golang", m.current)
	}

	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "one"}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "two"}),
		event(protocol.Event{Kind: protocol.Leave, Room: "#general", From: "bob"}),
	)
	if got := m.rooms["#general"].unread; got != 2 {
		t.Errorf("#general unread = %d, want 2 (a leave is not unread)", got)
	}
	if got := m.rooms["#golang"].unread; got != 0 {
		t.Errorf("the viewed room accrued %d unread, want 0", got)
	}
	if got := strings.Join(texts(m, "#golang"), "|"); !strings.Contains(got, "join:") {
		t.Errorf("#golang transcript = %q, want the join event", got)
	}
	if got := len(texts(m, "#general")); got != 4 {
		t.Errorf("#general kept %d events, want 4", got)
	}

	m = feed(t, m, event(protocol.Event{Kind: protocol.Join, Room: "#general", From: "alice"}))
	if got := m.rooms["#general"].unread; got != 0 {
		t.Errorf("unread after viewing = %d, want 0", got)
	}
	if !strings.Contains(m.View(), "one") {
		t.Error("the transcript does not show the room we switched back to")
	}
}

func TestUnreadBadgeReachesTheSidebar(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Join, Room: "#golang", From: "alice"}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "one"}),
	)
	if got := m.View(); !strings.Contains(got, "#general (2) •1") {
		t.Errorf("sidebar has no unread badge:\n%s", got)
	}
}

func TestScrollLock(t *testing.T) {
	m := joined(t, 100, 30)
	for i := range 60 {
		m = feed(t, m, event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: strings.Repeat("x", i%9+1)}))
	}
	if !m.follow || m.pending {
		t.Fatalf("follow = %v, pending = %v, want true and false at the bottom", m.follow, m.pending)
	}

	m = feed(t, m, pressed(tea.KeyPgUp))
	if m.follow {
		t.Fatal("scrolling up did not release the scroll lock")
	}
	at := m.body.YOffset

	m = feed(t, m, event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "while you were away"}))
	if !m.pending {
		t.Error("a message arriving while scrolled up did not raise the pill")
	}
	if m.body.YOffset != at {
		t.Errorf("the viewport moved from %d to %d while scrolled up", at, m.body.YOffset)
	}
	if !strings.Contains(m.View(), "↓ new messages") {
		t.Error("the new-messages pill is not rendered")
	}

	m = feed(t, m, pressed(tea.KeyPgDown), pressed(tea.KeyPgDown), pressed(tea.KeyPgDown))
	if !m.follow || m.pending {
		t.Errorf("follow = %v, pending = %v, want true and false back at the bottom", m.follow, m.pending)
	}
	if strings.Contains(m.View(), "↓ new messages") {
		t.Error("the pill survived scrolling back to the bottom")
	}
}

func TestSendingWhileDisconnected(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, linkMsg{state: client.Reconnecting, attempt: 3})
	if !strings.Contains(m.View(), "reconnecting to chat.example.com:4000… (attempt 3)") {
		t.Errorf("status bar does not report the reconnect:\n%s", m.View())
	}

	next, _ := tea.Model(m).Update(typed("hello"))
	next, cmd := next.Update(pressed(tea.KeyEnter))
	m = next.(*Model)
	if cmd != nil {
		t.Error("Enter while disconnected still produced a command")
	}
	got := texts(m, "#general")
	last := got[len(got)-1]
	if !strings.HasPrefix(last, "error:not connected") {
		t.Errorf("last transcript line = %q, want an inline not-connected error", last)
	}
	if m.input.Value() != "" {
		t.Errorf("input still holds %q", m.input.Value())
	}
}

func TestCommandHistory(t *testing.T) {
	m := joined(t, 100, 30)
	m.link = client.Connected
	for _, line := range []string{"first", "second"} {
		m = feed(t, m, typed(line), pressed(tea.KeyEnter))
	}
	if want := []string{"first", "second"}; !equal(m.past, want) {
		t.Fatalf("history = %q, want %q", m.past, want)
	}
	m = feed(t, m, pressed(tea.KeyUp))
	if got := m.input.Value(); got != "second" {
		t.Errorf("one Up = %q, want %q", got, "second")
	}
	m = feed(t, m, pressed(tea.KeyUp))
	if got := m.input.Value(); got != "first" {
		t.Errorf("two Ups = %q, want %q", got, "first")
	}
	m = feed(t, m, pressed(tea.KeyUp))
	if got := m.input.Value(); got != "first" {
		t.Errorf("past the oldest entry = %q, want it to stay at %q", got, "first")
	}
	m = feed(t, m, pressed(tea.KeyDown), pressed(tea.KeyDown))
	if got := m.input.Value(); got != "" {
		t.Errorf("back past the newest entry = %q, want an empty input", got)
	}
}

func TestUnknownCommandNeverReachesTheServer(t *testing.T) {
	m := joined(t, 100, 30)
	next, _ := tea.Model(m).Update(typed("/dance"))
	next, cmd := next.Update(pressed(tea.KeyEnter))
	if cmd != nil {
		t.Error("an unknown command produced a command instead of an inline error")
	}
	got := texts(next.(*Model), "#general")
	if last := got[len(got)-1]; !strings.HasPrefix(last, "error:") {
		t.Errorf("last transcript line = %q, want an inline error", last)
	}
}

func TestNickRenamesSelf(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, event(protocol.Event{Kind: protocol.Nick, Room: "#general", From: "alice", To: "alicia"}))
	if m.me != "alicia" {
		t.Errorf("me = %q, want alicia", m.me)
	}
	if got := m.rooms["#general"].users; !equal(got, []string{"alicia", "bob"}) {
		t.Errorf("users = %q, want [alicia bob]", got)
	}
	m = feed(t, m, event(protocol.Event{Kind: protocol.Nick, Room: "#general", From: "bob", To: "bobby"}))
	if m.me != "alicia" {
		t.Errorf("someone else's rename changed me to %q", m.me)
	}
}

func TestPanesAndOverlay(t *testing.T) {
	m := joined(t, 100, 30)
	if m.focus != paneInput {
		t.Fatalf("focus starts at %v, want paneInput", m.focus)
	}
	m = feed(t, m, typed("?"))
	if m.help {
		t.Fatal("? opened the overlay while typing instead of entering a character")
	}
	if m.input.Value() != "?" {
		t.Fatalf("input = %q, want ?", m.input.Value())
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if m.focus != paneMessages || m.input.Focused() {
		t.Fatalf("Tab left focus at %v (input focused: %v)", m.focus, m.input.Focused())
	}
	m = feed(t, m, typed("?"))
	if !m.help {
		t.Fatal("? outside the input did not open the overlay")
	}
	if !strings.Contains(m.View(), "Keys") {
		t.Error("the overlay is not rendered")
	}
	m = feed(t, m, pressed(tea.KeyEsc))
	if m.help {
		t.Error("Esc did not close the overlay")
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if m.focus != paneRooms {
		t.Fatalf("focus = %v, want paneRooms", m.focus)
	}
	if want := 0; m.choice != want {
		t.Errorf("selection = %d, want %d (the current room)", m.choice, want)
	}
	m = feed(t, m, pressed(tea.KeyShiftTab), pressed(tea.KeyShiftTab))
	if m.focus != paneInput || !m.input.Focused() {
		t.Errorf("cycling back left focus at %v (input focused: %v)", m.focus, m.input.Focused())
	}
	m = feed(t, m, pressed(tea.KeyShiftTab))
	if m.focus != paneRooms {
		t.Errorf("Shift+Tab from the input = %v, want it to wrap to paneRooms", m.focus)
	}
}

func TestClearWipesTheCurrentRoomOnly(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "kept elsewhere"}),
		event(protocol.Event{Kind: protocol.Join, Room: "#golang", From: "alice"}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#golang", From: "carol", Text: "wiped"}),
		tea.KeyMsg{Type: tea.KeyCtrlL},
	)
	if got := texts(m, "#golang"); len(got) != 0 {
		t.Errorf("#golang kept %q after Ctrl+L", got)
	}
	if got := texts(m, "#general"); len(got) == 0 {
		t.Error("Ctrl+L wiped a room that was not being viewed")
	}
}

func TestResizeRelaysOut(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, tea.WindowSizeMsg{Width: 64, Height: 18})
	if m.view.messages != 64-sidebarCols-1 || m.body.Width != m.view.messages {
		t.Errorf("messages pane = %d, viewport = %d, want %d", m.view.messages, m.body.Width, 64-sidebarCols-1)
	}
	if got := lines(m.View()); len(got) != 18 {
		t.Errorf("frame is %d rows, want 18", len(got))
	}
	m = feed(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	if m.view.sidebar != 0 {
		t.Errorf("sidebar = %d at 40 columns, want it hidden", m.view.sidebar)
	}
	for _, l := range lines(m.View()) {
		if w := lipgloss.Width(l); w > 40 {
			t.Fatalf("row %q is %d wide, want at most 40", l, w)
		}
	}
	m = feed(t, m, tea.WindowSizeMsg{Width: 10, Height: 3})
	if got := m.View(); !strings.Contains(got, "too small") {
		t.Errorf("tiny terminal rendered %q", got)
	}
}

func TestGoldenInitialRender(t *testing.T) {
	m := feed(t, fresh(t, 100, 30),
		event(protocol.Event{Kind: protocol.Rooms, Names: []string{"#general (3)", "#golang (2)"}}),
		event(protocol.Event{Kind: protocol.Join, Room: "#golang", From: "alice"}),
		event(protocol.Event{Kind: protocol.Who, Room: "#golang", Names: []string{"alice", "carol"}}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#golang", From: "carol", Text: "hello everyone"}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#golang", From: "alice", Text: "hey"}),
		event(protocol.Event{Kind: protocol.PrivMsg, From: "carol", To: "alice", Text: "a word in private, and enough of them to need wrapping across the pane"}),
		event(protocol.Event{Kind: protocol.Error, Text: "unknown command dance"}),
	)
	got := plain(m.View())
	golden := filepath.Join("testdata", "initial.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", golden, err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s (run go test ./internal/tui -update to create it): %v", golden, err)
	}
	if got != string(want) {
		t.Errorf("render does not match %s\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func plain(s string) string {
	return ansi.ReplaceAllString(s, "")
}

func lines(s string) []string {
	return strings.Split(plain(s), "\n")
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWhoAndRoomsRepliesShowOnlyWhenAsked(t *testing.T) {
	m := joined(t, 100, 30)
	before := len(texts(m, "#general"))
	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Who, Room: "#general", Names: []string{"alice", "bob", "carol"}}),
		event(protocol.Event{Kind: protocol.Rooms, Names: []string{"#general (3)"}}),
	)
	if got := len(texts(m, "#general")); got != before {
		t.Fatalf("background refresh replies were shown: %q", texts(m, "#general"))
	}
	if got := m.rooms["#general"].users; !equal(got, []string{"alice", "bob", "carol"}) {
		t.Errorf("sidebar users = %q, want the refreshed list", got)
	}

	m = feed(t, m, typed("/who"), pressed(tea.KeyEnter),
		event(protocol.Event{Kind: protocol.Who, Room: "#general", Names: []string{"alice", "bob", "carol"}}),
		typed("/rooms"), pressed(tea.KeyEnter),
		event(protocol.Event{Kind: protocol.Rooms, Names: []string{"#general (3)"}}),
	)
	got := texts(m, "#general")
	if len(got) != before+2 || got[before] != "who:" || got[before+1] != "rooms:" {
		t.Fatalf("asked-for replies not shown once each: %q", got[before:])
	}
	view := plain(m.View())
	for _, want := range []string{"online in #general (3): alice, bob, carol", "rooms (1): #general (3)"} {
		if !strings.Contains(view, want) {
			t.Errorf("transcript lacks %q:\n%s", want, view)
		}
	}

	m = feed(t, m, event(protocol.Event{Kind: protocol.Who, Room: "#general", Names: []string{"alice"}}))
	if got := len(texts(m, "#general")); got != before+2 {
		t.Error("a later background reply was shown after the asked-for one")
	}
}

func TestSidebarRefreshesPeriodically(t *testing.T) {
	m := joined(t, 100, 30)
	for range 2 * refreshEvery {
		m = feed(t, m, linkMsg{state: client.Connected})
	}
	if m.ticks != 2*refreshEvery {
		t.Errorf("ticks = %d, want %d", m.ticks, 2*refreshEvery)
	}
	if len(m.asked) != 0 {
		t.Errorf("a background refresh marked %v as asked for", m.asked)
	}
	before := len(texts(m, "#general"))
	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Rooms, Names: []string{"#general (2)", "#ops (1)"}}),
		event(protocol.Event{Kind: protocol.Who, Room: "#general", Names: []string{"alice", "bob"}}),
	)
	if got := len(texts(m, "#general")); got != before {
		t.Errorf("background replies were shown: %q", texts(m, "#general")[before:])
	}
	if !slices.Contains(m.order, "#ops") {
		t.Errorf("sidebar order = %q, want the refreshed room list", m.order)
	}
}
