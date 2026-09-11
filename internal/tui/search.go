package tui

import (
	"strconv"
	"strings"
)

type search struct {
	on    bool
	query string
	hits  []int
	at    int
}

func (s search) summary() string {
	switch {
	case s.query == "":
		return "type to search, Enter next, Shift+Enter previous, Esc close"
	case len(s.hits) == 0:
		return "no match"
	default:
		return strconv.Itoa(s.at+1) + " of " + strconv.Itoa(len(s.hits))
	}
}

func (m *Model) openSearch() {
	m.find = search{on: true}
	m.input.Blur()
	m.focus = paneMessages
}

func (m *Model) closeSearch() {
	m.find = search{}
	m.focus = paneInput
	m.input.Focus()
	m.redraw()
}

func (m *Model) typeSearch(s string) {
	m.find.query += s
	m.scan()
}

func (m *Model) backspaceSearch() {
	if q := []rune(m.find.query); len(q) > 0 {
		m.find.query = string(q[:len(q)-1])
	}
	m.scan()
}

func (m *Model) scan() {
	m.find.hits = nil
	m.find.at = 0
	if m.find.query != "" {
		for i, line := range m.transcript() {
			if strings.Contains(strings.ToLower(plainText(line)), strings.ToLower(m.find.query)) {
				m.find.hits = append(m.find.hits, i)
			}
		}
	}
	m.find.at = max(0, len(m.find.hits)-1)
	m.redraw()
	m.jump()
}

func (m *Model) stepSearch(by int) {
	if len(m.find.hits) == 0 {
		return
	}
	m.find.at = (m.find.at + by + len(m.find.hits)) % len(m.find.hits)
	m.jump()
}

func (m *Model) jump() {
	if len(m.find.hits) == 0 {
		return
	}
	m.follow, m.pending = false, false
	m.body.SetYOffset(max(0, m.find.hits[m.find.at]-m.view.log/2))
}
