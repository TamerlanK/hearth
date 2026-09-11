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

var at = today(15, 4)

func today(hour, minute int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d, hour, minute, 0, 0, time.Local)
}

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
	m = feed(t, m, pressed(tea.KeyBackspace), pressed(tea.KeyTab))
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

func TestDMConversationsGetTheirOwnTab(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, event(protocol.Event{Kind: protocol.PrivMsg, From: "bob", To: "alice", Text: "psst"}))
	if m.current != "#general" {
		t.Fatalf("an incoming DM moved the view to %q", m.current)
	}
	if got := texts(m, "@bob"); !equal(got, []string{"privmsg:psst"}) {
		t.Fatalf("@bob transcript = %q, want the DM", got)
	}
	if got := texts(m, "#general"); slices.Contains(got, "privmsg:psst") {
		t.Error("the DM also landed in the room transcript")
	}
	if m.rooms["@bob"].unread != 1 || !strings.Contains(plain(m.View()), "@bob @1") {
		t.Errorf("no unread badge on the DM tab:\n%s", plain(m.View()))
	}

	m = feed(t, m, pressed(tea.KeyTab), pressed(tea.KeyTab))
	if want := []item{{"#general", false}, {"#golang", false}, {"@bob", false}, {"bob", true}}; !slices.Equal(m.items(), want) {
		t.Fatalf("items = %v, want %v", m.items(), want)
	}
	m = feed(t, m, pressed(tea.KeyDown), pressed(tea.KeyDown), pressed(tea.KeyEnter))
	if m.current != "@bob" || m.room != "#general" {
		t.Fatalf("after Enter on @bob: current = %q, room = %q", m.current, m.room)
	}
	if m.rooms["@bob"].unread != 0 {
		t.Error("opening the tab did not clear its unread count")
	}
	if got := m.outgoing(protocol.Command{Name: "say", Text: "hi"}); got.Name != "msg" || got.Args[0] != "bob" || got.Text != "hi" {
		t.Errorf("a bare line in a DM tab became %+v, want /msg bob hi", got)
	}
	if got := m.outgoing(protocol.Command{Name: "join", Args: []string{"#ops"}}); got.Name != "join" {
		t.Errorf("a command in a DM tab was rewritten to %+v", got)
	}
	if got := plain(m.View()); !strings.Contains(got, "@bob · in #general") {
		t.Errorf("status bar does not say where the DM is viewed from:\n%s", got)
	}

	m = feed(t, m, pressed(tea.KeyShiftTab), pressed(tea.KeyShiftTab), typed("/close"), pressed(tea.KeyEnter))
	if m.current != "#general" || slices.Contains(m.order, "@bob") {
		t.Errorf("after /close: current = %q, order = %q", m.current, m.order)
	}
	m = feed(t, m, typed("/close"), pressed(tea.KeyEnter))
	if got := texts(m, "#general"); !strings.HasPrefix(got[len(got)-1], "error:only a @name") {
		t.Errorf("/close on a room gave %q", got[len(got)-1])
	}
}

func TestSendingADMOpensItsTabAndSurvivesRefresh(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, event(protocol.Event{Kind: protocol.PrivMsg, From: "alice", To: "carol", Text: "hey"}))
	if m.current != "@carol" {
		t.Fatalf("sending a DM left the view at %q", m.current)
	}
	m = feed(t, m, event(protocol.Event{Kind: protocol.Rooms, Names: []string{"#general (2)"}}))
	if !slices.Contains(m.order, "@carol") {
		t.Error("a rooms refresh dropped the DM tab")
	}
	if slices.Contains(m.order, "#golang") {
		t.Error("a rooms refresh kept a room the server no longer has")
	}
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.current != "@carol" {
		t.Errorf("Ctrl+N with one room and one DM moved to %q", m.current)
	}
}

func TestPickingAUserOpensADM(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, pressed(tea.KeyTab), pressed(tea.KeyTab), pressed(tea.KeyDown), pressed(tea.KeyDown), pressed(tea.KeyEnter))
	if m.current != "@bob" {
		t.Fatalf("Enter on bob opened %q", m.current)
	}
	if got := texts(m, "@bob"); len(got) != 0 {
		t.Errorf("a fresh DM tab has %q in it", got)
	}
}

func TestMentionsAreCountedAndRingTheBell(t *testing.T) {
	m := joined(t, 100, 30)
	m.bell = true
	m = feed(t, m, event(protocol.Event{Kind: protocol.Join, Room: "#golang", From: "alice"}))

	rang := func(e protocol.Event) bool {
		t.Helper()
		next, cmd := tea.Model(m).Update(event(e))
		m = next.(*Model)
		return cmd != nil
	}
	tests := []struct {
		name string
		e    protocol.Event
		want bool
	}{
		{"plain text", protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "hello all"}, false},
		{"name mid-sentence", protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "hey Alice, ping"}, true},
		{"at-mention with punctuation", protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "@alice!"}, true},
		{"name as a prefix of a longer word", protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "alice2 is here"}, false},
		{"own message naming myself", protocol.Event{Kind: protocol.Msg, Room: "#golang", From: "alice", Text: "alice here"}, false},
		{"private message", protocol.Event{Kind: protocol.PrivMsg, From: "bob", To: "alice", Text: "psst"}, true},
		{"system notice", protocol.Event{Kind: protocol.System, Text: "alice"}, false},
	}
	for _, tt := range tests {
		if got := rang(tt.e); got != tt.want {
			t.Errorf("%s: bell = %v, want %v", tt.name, got, tt.want)
		}
	}
	if r := m.rooms["#general"]; r.unread != 4 || r.mentions != 2 {
		t.Errorf("#general unread = %d, mentions = %d; want 4 and 2", r.unread, r.mentions)
	}
	if r := m.rooms["@bob"]; r.unread != 1 || r.mentions != 1 {
		t.Errorf("@bob unread = %d, mentions = %d; want 1 and 1", r.unread, r.mentions)
	}
	view := plain(m.View())
	for _, want := range []string{"#general (2) @4", "@bob @1"} {
		if !strings.Contains(view, want) {
			t.Errorf("sidebar lacks %q:\n%s", want, view)
		}
	}

	m.bell = false
	if rang(protocol.Event{Kind: protocol.PrivMsg, From: "bob", To: "alice", Text: "again"}) {
		t.Error("the bell rang with --bell=false")
	}
	m = feed(t, m, event(protocol.Event{Kind: protocol.Join, Room: "#general", From: "alice"}))
	if r := m.rooms["#general"]; r.unread != 0 || r.mentions != 0 {
		t.Errorf("viewing the room left unread = %d, mentions = %d", r.unread, r.mentions)
	}
}

func TestStatusBarShowsUnreadFocusAndTheLastError(t *testing.T) {
	m := joined(t, 100, 30)
	bar := func() string {
		rows := lines(m.View())
		return rows[len(rows)-1]
	}
	if got := bar(); !strings.Contains(got, "focus: input") || strings.Contains(got, "unread") {
		t.Fatalf("initial status bar = %q", got)
	}
	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Msg, Room: "#golang", From: "carol", Text: "one"}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#golang", From: "carol", Text: "alice, two"}),
		event(protocol.Event{Kind: protocol.PrivMsg, From: "bob", To: "alice", Text: "three"}),
	)
	if got := bar(); !strings.Contains(got, "3 unread (2 @)") {
		t.Errorf("status bar = %q, want the unread and mention totals", got)
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if got := bar(); !strings.Contains(got, "focus: messages") {
		t.Errorf("status bar = %q, want focus: messages", got)
	}
	m = feed(t, m, pressed(tea.KeyTab))
	if got := bar(); !strings.Contains(got, "focus: rooms") {
		t.Errorf("status bar = %q, want focus: rooms", got)
	}
	m = feed(t, m, pressed(tea.KeyTab), typed("/dance"), pressed(tea.KeyEnter))
	if got := bar(); !strings.Contains(got, "! ") || !strings.Contains(got, "dance") {
		t.Errorf("status bar = %q, want the last error", got)
	}
	m = feed(t, m, typed("hello"), pressed(tea.KeyEnter))
	if got := bar(); strings.Contains(got, "! ") {
		t.Errorf("status bar = %q, want the error cleared by the next line", got)
	}
	m = feed(t, m, event(protocol.Event{Kind: protocol.Error, Text: "name taken"}))
	if got := bar(); !strings.Contains(got, "! name taken") {
		t.Errorf("status bar = %q, want a server error surfaced", got)
	}
	m = feed(t, m, event(protocol.Event{Kind: protocol.Join, Room: "#golang", From: "alice"}))
	if got := bar(); !strings.Contains(got, "1 unread (1 @)") {
		t.Errorf("status bar = %q, want only the DM left unread after viewing #golang", got)
	}
}

func TestDaySeparators(t *testing.T) {
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	old := now.AddDate(0, 0, -9)
	m := feed(t, fresh(t, 100, 30),
		event(protocol.Event{Kind: protocol.Join, Room: "#general", From: "alice", Time: old}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "ancient", Time: old}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "older still", Time: old.Add(time.Minute)}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "recent", Time: yesterday}),
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "fresh", Time: now}),
	)
	view := plain(m.View())
	for _, want := range []string{old.Format(dayStamp), "Yesterday", "Today"} {
		if !strings.Contains(view, want) {
			t.Errorf("transcript lacks the %q separator:\n%s", want, view)
		}
	}
	if got := strings.Count(view, "Today"); got != 1 {
		t.Errorf("Today appears %d times, want once", got)
	}
	if got := strings.Count(view, old.Format(dayStamp)); got != 1 {
		t.Errorf("three events on the same day produced %d separators", got)
	}
	if got := strings.Count(view, "Yesterday"); got != 1 {
		t.Errorf("Yesterday appears %d times, want once", got)
	}
}

func TestDayLabel(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"today", now, "Today"},
		{"earlier today", now.Truncate(24 * time.Hour), "Today"},
		{"yesterday", now.AddDate(0, 0, -1), "Yesterday"},
		{"last week", now.AddDate(0, 0, -7), now.AddDate(0, 0, -7).Format(dayStamp)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dayLabel(tt.at); got != tt.want {
				t.Errorf("dayLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAwayDimsTheSidebar(t *testing.T) {
	m := joined(t, 100, 30)
	m = feed(t, m, event(protocol.Event{Kind: protocol.Away, Room: "#general", From: "bob", Text: "lunch"}))
	if got := m.away["bob"]; got != "lunch" {
		t.Fatalf("away[bob] = %q, want lunch", got)
	}
	view := plain(m.View())
	if !strings.Contains(view, "bob (lunch)") {
		t.Errorf("sidebar does not mark bob away:\n%s", view)
	}
	if !strings.Contains(view, "bob is away: lunch") {
		t.Errorf("transcript does not narrate the away:\n%s", view)
	}
	if got := m.rooms["#general"].unread; got != 0 {
		t.Errorf("an away event counted %d unread", got)
	}

	m = feed(t, m, event(protocol.Event{Kind: protocol.Nick, Room: "#general", From: "bob", To: "bobby"}))
	if _, still := m.away["bob"]; still {
		t.Error("the away state stayed under the old name")
	}
	if got := m.away["bobby"]; got != "lunch" {
		t.Errorf("away[bobby] = %q, want the state to follow the rename", got)
	}

	m = feed(t, m, event(protocol.Event{Kind: protocol.Away, Room: "#general", From: "bobby"}))
	if _, still := m.away["bobby"]; still {
		t.Error("an empty away event did not clear the state")
	}
	if got := plain(m.View()); !strings.Contains(got, "bobby is back") {
		t.Errorf("transcript does not narrate the return:\n%s", got)
	}

	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Away, Room: "#general", From: "bobby", Text: "afk"}),
	)
	if got := m.away["bobby"]; got != "afk" {
		t.Fatalf("away[bobby] = %q, want afk", got)
	}
	m = feed(t, m, event(protocol.Event{Kind: protocol.Leave, Room: "#general", From: "bobby"}))
	if _, still := m.away["bobby"]; still {
		t.Error("leaving did not drop the away state")
	}
}

func TestLogFileGetsEveryLine(t *testing.T) {
	var journal strings.Builder
	m := joined(t, 100, 30)
	m.journal = &journal
	m = feed(t, m,
		event(protocol.Event{Kind: protocol.Msg, Room: "#general", From: "bob", Text: "written down"}),
		event(protocol.Event{Kind: protocol.PrivMsg, From: "bob", To: "alice", Text: "and this"}),
	)
	if m.journal == nil {
		t.Fatal("a write error disabled the journal")
	}
	got := journal.String()
	for _, want := range []string{"bob: written down", "bob -> alice: and this"} {
		if !strings.Contains(got, want) {
			t.Errorf("log file lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Errorf("log file has %d lines, want 2", n)
	}
}
