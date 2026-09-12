package tui

import (
	"slices"
	"strings"

	"github.com/TamerlanK/hearth/pkg/protocol"
)

const closeCommand = "close"

var everyone = []string{"@all", "@here"}

type completion struct {
	head  string
	tail  string
	cands []string
	i     int
}

func (c completion) active() bool {
	return len(c.cands) > 1
}

func candidates(line []rune, pos int, rooms, users []string) (int, []string) {
	pos = min(max(0, pos), len(line))
	start := pos
	for start > 0 && line[start-1] != ' ' {
		start--
	}
	word := string(line[start:pos])
	if word == "" {
		return start, nil
	}
	var pool []string
	switch first, _, _ := strings.Cut(strings.TrimLeft(string(line), " "), " "); {
	case start == 0 && strings.HasPrefix(word, "/"):
		for _, spec := range protocol.Commands {
			pool = append(pool, "/"+spec.Name)
		}
		pool = append(pool, "/"+closeCommand)
	case first == "/join" || first == "/who" || first == "/history":
		pool = append(pool, rooms...)
		word = "#" + strings.TrimPrefix(word, "#")
	case !strings.HasPrefix(word, "@"):
		pool = users
	default:
		pool = append(pool, everyone...)
		for _, u := range users {
			pool = append(pool, "@"+u)
		}
	}
	var out []string
	for _, cand := range pool {
		if len(cand) >= len(word) && strings.EqualFold(cand[:len(word)], word) {
			out = append(out, cand)
		}
	}
	slices.Sort(out)
	return start, out
}

func (m *Model) complete() {
	if m.comp.active() {
		m.comp.i = (m.comp.i + 1) % len(m.comp.cands)
		m.place(m.comp.cands[m.comp.i], "")
		return
	}
	line, pos := []rune(m.input.Value()), m.input.Position()
	pos = min(max(0, pos), len(line))
	start, cands := candidates(line, pos, m.roomNames(), m.userNames())
	if len(cands) == 0 {
		return
	}
	m.comp = completion{head: string(line[:start]), tail: string(line[pos:]), cands: cands}
	if len(cands) == 1 {
		m.place(cands[0], " ")
		m.comp = completion{}
		return
	}
	m.place(cands[0], "")
}

func (m *Model) place(word, sep string) {
	m.input.SetValue(m.comp.head + word + sep + m.comp.tail)
	m.input.SetCursor(len([]rune(m.comp.head + word + sep)))
}

func (m *Model) roomNames() []string {
	var rooms []string
	for _, name := range m.order {
		if !isDM(name) {
			rooms = append(rooms, name)
		}
	}
	return rooms
}

func (m *Model) userNames() []string {
	var users []string
	if r, ok := m.rooms[m.room]; ok {
		users = append(users, r.users...)
	}
	for _, name := range m.order {
		if isDM(name) {
			users = append(users, name[1:])
		}
	}
	slices.Sort(users)
	return slices.DeleteFunc(slices.Compact(users), func(u string) bool { return u == m.me })
}
