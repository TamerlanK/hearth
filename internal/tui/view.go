package tui

import (
	"strconv"
	"strings"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/charmbracelet/lipgloss"
)

const clock = "15:04"

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
	r, ok := m.rooms[m.current]
	if !ok {
		return nil
	}
	var out []string
	for _, e := range r.log.Snapshot() {
		out = append(out, m.line(e)...)
	}
	for len(out) < m.view.log {
		out = append([]string{""}, out...)
	}
	return out
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
	lines := []string{m.style.sidebarTitle.Render(fit("Rooms", w))}
	items := m.items()
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
	lines = append(lines, fit("", w), m.style.sidebarTitle.Render(fit("Users in "+m.room, w)))
	if r, ok := m.rooms[m.room]; ok {
		at := len(m.order)
		for _, u := range r.users {
			style := m.style.user
			if u == m.me {
				style = m.style.self
			} else {
				if m.focus == paneRooms && at == m.choice {
					style = style.Underline(true)
				}
				at++
			}
			lines = append(lines, style.Render(fit(" "+m.label(u), w)))
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
	if !m.pending || w < 20 {
		return m.style.divider.Render(strings.Repeat("─", w))
	}
	pill := " ↓ new messages "
	left := w - lipgloss.Width(pill) - 2
	return m.style.divider.Render(strings.Repeat("─", left)) +
		m.style.pill.Render(pill) + m.style.divider.Render("──")
}

func (m *Model) entry() string {
	return fit(m.input.View(), m.view.messages)
}

func (m *Model) statusBar() string {
	parts := []string{m.connection() + " as " + m.me}
	if isDM(m.current) {
		parts = append(parts, m.current+" · in "+m.room)
	} else if m.current != "" {
		parts = append(parts, m.current)
	}
	parts = append(parts, "Tab: panes", "?: help")
	bar := " " + strings.Join(parts, " · ")
	style := m.style.status
	if m.link != client.Connected {
		style = m.style.statusAlert
	}
	return style.Render(fit(bar, m.view.width))
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
	{"Enter", "send the line"},
	{"Tab", "complete a /command, name or room; again to cycle matches"},
	{"Tab / Shift+Tab", "on an empty line: cycle input, messages, rooms"},
	{"Ctrl+N / Ctrl+P", "next / previous room"},
	{"Up / Down", "command history, scroll, or pick a room or user"},
	{"Enter on a user", "open a private conversation (@name tab)"},
	{"PgUp / PgDn", "scroll the transcript"},
	{"Ctrl+L", "clear the current room"},
	{"? / F1", "toggle this help"},
	{"Esc", "close this help"},
	{"Ctrl+C", "quit"},
}

func (m *Model) overlay() string {
	rows := make([]string, 0, len(helpKeys)+len(protocol.Commands)+7)
	rows = append(rows, m.style.overlayKey.Render("Keys"), "")
	for _, k := range helpKeys {
		rows = append(rows, m.style.overlayKey.Render(fit(k[0], 18))+m.style.overlay.Render(k[1]))
	}
	rows = append(rows, "", m.style.overlayKey.Render("Commands"), "")
	for _, spec := range protocol.Commands {
		rows = append(rows, m.style.overlay.Render(spec.Usage))
	}
	rows = append(rows, m.style.overlay.Render("/close"))
	rows = append(rows, "", m.style.overlay.Render("a bare line is /say, or a private message in a @name tab · Esc closes this"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(overlayEdge(&m.style)).
		Padding(0, 2).
		Render(strings.Join(rows, "\n"))
	if lipgloss.Height(box) > m.view.body || lipgloss.Width(box) > m.view.width {
		box = strings.Join(rows[:min(len(rows), max(1, m.view.body-2))], "\n")
	}
	return lipgloss.Place(m.view.width, m.view.body, lipgloss.Center, lipgloss.Center, box)
}
