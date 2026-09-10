package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	sidebarCols  = 24
	sidebarFloor = 60
	minCols      = 24
	minRows      = 6
	clockCols    = 8
	nameCols     = 12
	gutterCols   = clockCols + nameCols + 1
)

type layout struct {
	width    int
	height   int
	sidebar  int
	body     int
	messages int
	log      int
	tooSmall bool
}

func computeLayout(width, height int) layout {
	l := layout{width: width, height: height}
	if width < minCols || height < minRows {
		l.tooSmall = true
		return l
	}
	if width >= sidebarFloor {
		l.sidebar = sidebarCols
	}
	l.body = height - 1
	l.messages = width - l.sidebar
	if l.sidebar > 0 {
		l.messages--
	}
	l.log = max(1, l.body-2)
	return l
}

func wrap(s string, width int) []string {
	if width < 1 {
		return []string{s}
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		lines = append(lines, wrapParagraph(para, width)...)
	}
	return lines
}

func wrapParagraph(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(line)+1+lipgloss.Width(w) <= width {
			line += " " + w
			continue
		}
		lines, line = appendSplit(lines, line, width), w
	}
	return appendSplit(lines, line, width)
}

func appendSplit(lines []string, line string, width int) []string {
	for lipgloss.Width(line) > width {
		var head string
		head, line = splitAt(line, width)
		lines = append(lines, head)
	}
	return append(lines, line)
}

func splitAt(s string, width int) (string, string) {
	w := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if i > 0 && w+rw > width {
			return s[:i], s[i:]
		}
		w += rw
	}
	return s, ""
}

func fit(s string, width int) string {
	if width < 1 {
		return ""
	}
	if w := lipgloss.Width(s); w <= width {
		return s + strings.Repeat(" ", width-w)
	}
	head, _ := splitAt(s, width-1)
	if lipgloss.Width(head) >= width {
		head = ""
	}
	return head + "…" + strings.Repeat(" ", max(0, width-lipgloss.Width(head)-1))
}

func badge(unread int) string {
	switch {
	case unread <= 0:
		return ""
	case unread > 99:
		return "•99+"
	default:
		return "•" + strconv.Itoa(unread)
	}
}

func roomLabel(name string, members, unread int, current bool, width int) string {
	mark := " "
	if current {
		mark = ">"
	}
	label := mark + name
	if members > 0 {
		label += " (" + strconv.Itoa(members) + ")"
	}
	if b := badge(unread); b != "" {
		label += " " + b
	}
	return fit(label, width)
}
