package tui

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/charmbracelet/lipgloss"
)

const (
	clock    = "15:04"
	dayStamp = "Monday, 2 January 2006"
)

var escapes = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func plainText(s string) string {
	return escapes.ReplaceAllString(s, "")
}

func (m *Model) View() string {
	if m.view.tooSmall {
		return "hearth: terminal too small (need " + strconv.Itoa(minCols) + "x" + strconv.Itoa(minRows) + ")"
	}
	right := lipgloss.JoinVertical(lipgloss.Left, m.body.View(), m.rule(), m.entry())
	body := right
	if m.view.sidebar > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebar(), m.spine(), right)
	}
	if m.help {
		body = m.overlay()
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, m.statusBar())
}

func (m *Model) transcript() []string {
	var out []string
	var day time.Time
	if r, ok := m.rooms[m.current]; ok {
		for _, e := range r.log.Snapshot() {
			if !e.Time.IsZero() && !sameDay(e.Time, day) {
				day = e.Time
				out = append(out, m.daybreak(day))
			}
			out = append(out, m.line(e)...)
		}
	}
	if len(out) == 0 {
		out = m.primer()
	}
	for len(out) < m.view.log {
		out = append([]string{""}, out...)
	}
	return out
}

var primerTips = [][2]string{
	{"Enter", "send what you typed to the room"},
	{"/join #room", "open another room; Ctrl+N and Ctrl+P step through them"},
	{"/msg name text", "say something privately, in its own @name tab"},
	{"/who", "list who is here; /nick name renames you"},
	{"Tab", "complete a command, a name or a room"},
	{"F1", "every key and command"},
}

func (m *Model) primer() []string {
	w := max(8, m.view.messages)
	head := "You are " + m.me + " on " + m.addr + ". Nothing here yet."
	if m.current != "" {
		head = "Nothing in " + m.current + " yet — type a line to start it off."
	}
	out := make([]string, 0, 3+len(primerTips))
	out = append(out, "", "  "+m.style.day.Render(fit(head, w-2)), "")
	keyw := min(primerKeyCols, max(1, (w-2)/2))
	for _, tip := range primerTips {
		out = append(out, "  "+
			m.style.searchKey.Render(fit(tip[0], keyw))+
			m.style.system.Render(fit(tip[1], max(1, w-2-keyw))))
	}
	return out
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func (m *Model) daybreak(at time.Time) string {
	label := " " + dayLabel(at) + " "
	w := max(8, m.view.messages)
	dashes := max(2, w-lipgloss.Width(label))
	left := dashes / 2
	return m.style.divider.Render(strings.Repeat("─", left)) +
		m.style.day.Render(label) +
		m.style.divider.Render(strings.Repeat("─", dashes-left))
}

func dayLabel(at time.Time) string {
	switch daysApart(at, time.Now()) {
	case 0:
		return "Today"
	case 1:
		return "Yesterday"
	default:
		return at.Format(dayStamp)
	}
}

func daysApart(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	first := time.Date(ay, am, ad, 0, 0, 0, 0, time.Local)
	second := time.Date(by, bm, bd, 0, 0, 0, 0, time.Local)
	return int(second.Sub(first).Hours() / 24)
}

func (m *Model) line(e protocol.Event) []string {
	who, style := m.speaker(e)
	text := e.Text
	if e.Kind == protocol.Join {
		text = e.From + " joined " + e.Room
	}
	if e.Kind == protocol.Leave {
		text = e.From + " left " + e.Room
	}
	if e.Kind == protocol.Nick {
		text = e.From + " is now known as " + e.To
	}
	if e.Kind == protocol.Who {
		text = "online in " + e.Room + " (" + strconv.Itoa(len(e.Names)) + "): " + strings.Join(e.Names, ", ")
	}
	if e.Kind == protocol.Rooms {
		text = "rooms (" + strconv.Itoa(len(e.Names)) + "): " + strings.Join(e.Names, ", ")
	}
	if e.Kind == protocol.Away {
		text = e.From + " is back"
		if e.Text != "" {
			text = e.From + " is away: " + e.Text
		}
	}
	width := max(8, m.view.messages)
	head := m.style.clock.Render("["+e.Time.Format(clock)+"] ") + style.Render(fit(who, nameCols)) + " "
	body := wrap(text, max(1, width-gutterCols))
	lines := make([]string, 0, len(body))
	for i, l := range body {
		if i == 0 {
			lines = append(lines, head+m.tone(e).Render(l))
			continue
		}
		lines = append(lines, strings.Repeat(" ", gutterCols)+m.tone(e).Render(l))
	}
	return lines
}

func (m *Model) speaker(e protocol.Event) (string, lipgloss.Style) {
	switch e.Kind {
	case protocol.Msg, protocol.History:
		if e.From == m.me {
			return m.label(e.From), m.style.own
		}
		return m.label(e.From), m.style.name(e.From)
	case protocol.PrivMsg:
		if e.From == m.me {
			return "→" + e.To, m.style.private
		}
		return e.From + "→", m.style.private
	case protocol.Error:
		return pad("!"), m.style.failure
	default:
		return pad("*"), m.style.system
	}
}

func (m *Model) tone(e protocol.Event) lipgloss.Style {
	switch e.Kind {
	case protocol.Msg, protocol.History:
		if e.From == m.me {
			return m.style.own
		}
		if namesMe(e.Text, m.me) {
			return m.style.mention
		}
		return m.style.text
	case protocol.PrivMsg:
		return m.style.private
	case protocol.Error:
		return m.style.failure
	default:
		return m.style.system
	}
}

func (m *Model) label(who string) string {
	if who == m.me {
		return "you"
	}
	return who
}

func pad(mark string) string {
	return strings.Repeat(" ", nameCols-1) + mark
}

func (m *Model) sidebar() string {
	w := m.view.sidebar
	title := m.style.sidebarTitle
	if m.focus == paneRooms {
		title = m.style.focused.Bold(true)
	}
	lines := []string{title.Render(fit("Rooms", w))}
	items := m.items()
	if len(items) == 0 {
		lines = append(lines, m.style.system.Render(fit(" /join #room", w)))
	}
	for i, it := range items {
		if it.user {
			break
		}
		r := m.rooms[it.name]
		style := m.style.room
		if isDM(it.name) {
			style = m.style.private
		}
		if it.name == m.current {
			style = m.style.roomCurrent
		}
		if m.focus == paneRooms && i == m.choice {
			style = style.Underline(true)
		}
		lines = append(lines, style.Render(roomLabel(it.name, r.members, r.unread, r.mentions, it.name == m.current, w)))
	}
	heading := "Users"
	if m.room != "" {
		heading = "Users in " + m.room
	}
	lines = append(lines, fit("", w), m.style.sidebarTitle.Render(fit(heading, w)))
	if r, ok := m.rooms[m.room]; ok {
		at := len(m.order)
		for _, u := range r.users {
			style := m.style.user
			label := " " + m.label(u)
			if reason, away := m.away[u]; away {
				style = m.style.awayUser
				label += " (" + reason + ")"
			}
			if u == m.me {
				style = m.style.self
			} else {
				if m.focus == paneRooms && at == m.choice {
					style = style.Underline(true)
				}
				at++
			}
			lines = append(lines, style.Render(fit(label, w)))
		}
	}
	for len(lines) < m.view.body {
		lines = append(lines, fit("", w))
	}
	return strings.Join(lines[:m.view.body], "\n")
}

func (m *Model) spine() string {
	lines := make([]string, m.view.body)
	for i := range lines {
		if i == m.view.log {
			lines[i] = m.style.divider.Render("┴")
			continue
		}
		lines[i] = m.style.divider.Render("│")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) rule() string {
	w := max(1, m.view.messages)
	line := m.style.divider
	if m.focus == paneMessages {
		line = m.style.focused
	}
	if !m.pending || w < 20 {
		return line.Render(strings.Repeat("─", w))
	}
	pill := " ↓ new messages "
	left := w - lipgloss.Width(pill) - 2
	return line.Render(strings.Repeat("─", left)) +
		m.style.pill.Render(pill) + line.Render("──")
}

func (m *Model) entry() string {
	if !m.find.on {
		return fit(m.input.View(), m.view.messages)
	}
	bar := m.style.searchKey.Render("find ") + m.find.query + m.style.prompt.Render("▌") +
		m.style.system.Render("  "+m.find.summary())
	return fit(bar, m.view.messages)
}

func (m *Model) statusBar() string {
	parts := []string{m.connection() + " as " + m.me}
	if isDM(m.current) {
		parts = append(parts, m.current+" · in "+m.room)
	} else if m.current != "" {
		parts = append(parts, m.current)
	}
	if unread, mentions := m.unseen(); unread > 0 {
		s := strconv.Itoa(unread) + " unread"
		if mentions > 0 {
			s += " (" + strconv.Itoa(mentions) + " @)"
		}
		parts = append(parts, s)
	}
	switch m.focus {
	case paneMessages:
		parts = append(parts, m.focus.String()+" · Tab to type")
	case paneRooms:
		parts = append(parts, m.focus.String()+" · Enter opens · Tab to type")
	}
	if m.lastErr != "" {
		parts = append(parts, "! "+m.lastErr)
	}
	hints := []string{"F1: help", "^F: find", "Tab: panes"}
	style := m.style.status
	if m.link != client.Connected || m.lastErr != "" {
		style = m.style.statusAlert
	}
	return style.Render(fit(statusLine(parts, hints, m.view.width), m.view.width))
}

func statusLine(parts, hints []string, width int) string {
	for n := len(hints); n >= 0; n-- {
		bar := " " + strings.Join(append(parts[:len(parts):len(parts)], hints[:n]...), " · ")
		if lipgloss.Width(bar) <= width || n == 0 {
			return bar
		}
	}
	return " " + strings.Join(parts, " · ")
}

func (p pane) String() string {
	switch p {
	case paneMessages:
		return "messages"
	case paneRooms:
		return "rooms"
	default:
		return "input"
	}
}

func (m *Model) connection() string {
	switch m.link {
	case client.Reconnecting:
		if m.attempt > 0 {
			return "reconnecting to " + m.addr + "… (attempt " + strconv.FormatUint(m.attempt, 10) + ")"
		}
		return "reconnecting to " + m.addr + "…"
	case client.Closed:
		return "closed:" + m.addr
	default:
		return "connected to " + m.addr
	}
}

func overlayEdge(s *styles) lipgloss.TerminalColor {
	if len(s.palette) == 0 {
		return lipgloss.NoColor{}
	}
	return accent
}

var helpKeys = [][2]string{
	{"Enter", "send the line you typed"},
	{"Tab", "complete a command, name or room"},
	{"Tab on empty line", "move between typing, messages, rooms"},
	{"Ctrl+N / Ctrl+P", "next / previous room"},
	{"Up / Down", "history, scroll, or pick in the list"},
	{"Enter on a user", "open a private @name tab"},
	{"PgUp / PgDn", "scroll the transcript"},
	{"Ctrl+F", "search this transcript"},
	{"Ctrl+L", "clear this transcript"},
	{"Esc", "back to typing, or close this"},
	{"F1", "show or hide this help"},
	{"Ctrl+C", "quit hearth"},
}

var helpFooter = []string{
	"A bare line goes to the room; in a @name tab it goes to that person.",
	"Tag someone with @name, or the whole room with @all or @here.",
}

func (m *Model) helpKeyColumn(key, desc int) []string {
	rows := make([]string, 0, 2+len(helpKeys))
	rows = append(rows, m.style.overlayKey.Render(fit("Keys", key+desc)), "")
	for _, k := range helpKeys {
		rows = append(rows, m.style.overlayKey.Render(fit(k[0], key))+
			m.style.overlay.Render(fit(k[1], desc)))
	}
	return rows
}

func (m *Model) helpCommandColumn() []string {
	rows := make([]string, 0, 3+len(protocol.Commands))
	rows = append(rows, m.style.overlayKey.Render(fit("Commands", helpCmdCols)), "")
	for _, spec := range protocol.Commands {
		rows = append(rows, m.style.overlay.Render(fit(spec.Usage, helpCmdCols)))
	}
	return append(rows, m.style.overlay.Render(fit("/close", helpCmdCols)))
}

func (m *Model) overlay() string {
	budget := max(4, m.view.width-4)
	key := min(helpKeyCols, budget/2)
	desc := min(helpDescCols, budget-key)
	width := key + desc
	rows := m.helpKeyColumn(key, desc)
	if budget >= helpKeyCols+helpDescCols+2+helpCmdCols {
		width = helpKeyCols + helpDescCols + 2 + helpCmdCols
		blank := fit("", helpKeyCols+helpDescCols)
		rows = m.helpKeyColumn(helpKeyCols, helpDescCols)
		for i, c := range m.helpCommandColumn() {
			if i < len(rows) {
				rows[i] += "  " + c
				continue
			}
			rows = append(rows, blank+"  "+c)
		}
	}
	rows = append(rows, "")
	for _, line := range helpFooter {
		rows = append(rows, m.style.overlay.Render(fit(line, width)))
	}
	if n := max(1, m.view.body-2); len(rows) > n {
		rows = rows[:n]
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(overlayEdge(&m.style)).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
	return lipgloss.Place(m.view.width, m.view.body, lipgloss.Center, lipgloss.Center, box)
}
