package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/TamerlanK/hearth/internal/ring"
	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	logDepth     = 500
	historyDepth = 100
	sendTimeout  = 5 * time.Second
	pollInterval = time.Second
	refreshEvery = 10
)

type pane int

const (
	paneInput pane = iota
	paneMessages
	paneRooms
)

type eventMsg protocol.Event

type streamClosedMsg struct{}

type linkMsg struct {
	state   client.State
	attempt uint64
}

type sentMsg struct {
	err error
}

type quitMsg struct{}

type roomState struct {
	name    string
	members int
	unread  int
	users   []string
	log     *ring.Ring[protocol.Event]
}

type Model struct {
	conn    *client.Client
	events  <-chan protocol.Event
	addr    string
	me      string
	current string
	order   []string
	rooms   map[string]*roomState
	link    client.State
	attempt uint64
	focus   pane
	choice  int
	help    bool
	follow  bool
	pending bool
	past    []string
	recall  int
	ticks   int
	asked   map[protocol.Kind]bool
	view    layout
	style   styles
	body    viewport.Model
	input   textinput.Model
}

func newModel(c *client.Client, addr, name string, color bool) *Model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "type a message or /command"
	in.CharLimit = protocol.MaxLineBytes / 2
	in.Focus()
	s := newStyles(color)
	in.PromptStyle = s.prompt
	in.PlaceholderStyle = s.system
	m := &Model{
		conn:   c,
		addr:   addr,
		me:     name,
		rooms:  map[string]*roomState{},
		asked:  map[protocol.Kind]bool{},
		link:   client.Connected,
		follow: true,
		style:  s,
		body:   viewport.New(80, 20),
		input:  in,
	}
	if c != nil {
		m.events = c.Events()
	}
	m.resize(80, 24)
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.await(), m.poll(), m.refresh())
}

func (m *Model) await() tea.Cmd {
	ch := m.events
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return eventMsg(e)
	}
}

func (m *Model) poll() tea.Cmd {
	c := m.conn
	if c == nil {
		return nil
	}
	return tea.Tick(pollInterval, func(time.Time) tea.Msg {
		return linkMsg{state: c.State(), attempt: c.Stats().Attempt}
	})
}

func (m *Model) dispatch(cmd protocol.Command) tea.Cmd {
	c := m.conn
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		return sentMsg{err: c.Send(ctx, cmd)}
	}
}

func (m *Model) refresh() tea.Cmd {
	return tea.Batch(
		m.dispatch(protocol.Command{Name: "rooms"}),
		m.dispatch(protocol.Command{Name: "who"}),
	)
}

func (m *Model) shutdown() tea.Cmd {
	c := m.conn
	return tea.Sequence(func() tea.Msg {
		if c != nil {
			if err := c.Close(); err != nil {
				return sentMsg{err: err}
			}
		}
		return quitMsg{}
	}, tea.Quit)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	case eventMsg:
		return m, tea.Batch(m.apply(protocol.Event(msg)), m.await())
	case streamClosedMsg:
		m.link = client.Closed
		m.note("connection closed")
		return m, nil
	case linkMsg:
		if msg.state != m.link {
			m.announce(msg.state)
		}
		m.link, m.attempt = msg.state, msg.attempt
		m.ticks++
		next := m.poll()
		if m.link == client.Connected && m.ticks%refreshEvery == 0 {
			next = tea.Batch(next, m.refresh())
		}
		return m, next
	case sentMsg:
		if msg.err != nil {
			m.fail(msg.err.Error())
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) resize(width, height int) {
	m.view = computeLayout(width, height)
	m.body.Width = max(1, m.view.messages)
	m.body.Height = m.view.log
	m.input.Width = max(4, m.view.messages-4)
	m.redraw()
}

func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		stop := m.shutdown()
		return m, stop
	case "esc":
		if m.help {
			m.help = false
			return m, nil
		}
	case "f1":
		m.help = !m.help
		return m, nil
	case "?":
		if m.focus != paneInput {
			m.help = !m.help
			return m, nil
		}
	case "tab":
		m.cycle(1)
		return m, nil
	case "shift+tab":
		m.cycle(-1)
		return m, nil
	case "ctrl+n":
		next := m.step(1)
		return m, next
	case "ctrl+p":
		prev := m.step(-1)
		return m, prev
	case "ctrl+l":
		m.wipe()
		return m, nil
	case "pgup":
		m.body.PageUp()
		m.settle()
		return m, nil
	case "pgdown":
		m.body.PageDown()
		m.settle()
		return m, nil
	case "up":
		m.scroll(-1)
		return m, nil
	case "down":
		m.scroll(1)
		return m, nil
	case "enter":
		if m.focus == paneRooms {
			picked := m.pick()
			return m, picked
		}
		sent := m.submit()
		return m, sent
	}
	if m.focus != paneInput {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) cycle(by int) {
	m.focus = pane((int(m.focus) + by + 3) % 3)
	if m.focus == paneInput {
		m.input.Focus()
		return
	}
	m.input.Blur()
	if m.focus == paneRooms {
		m.choice = max(0, slices.Index(m.order, m.current))
	}
}

func (m *Model) scroll(by int) {
	switch m.focus {
	case paneRooms:
		m.choice = min(max(0, len(m.order)-1), max(0, m.choice+by))
	case paneMessages:
		if by < 0 {
			m.body.ScrollUp(-by)
		} else {
			m.body.ScrollDown(by)
		}
		m.settle()
	default:
		m.browse(-by)
	}
}

func (m *Model) settle() {
	m.follow = m.body.AtBottom()
	if m.follow {
		m.pending = false
	}
}

func (m *Model) browse(by int) {
	if len(m.past) == 0 {
		return
	}
	m.recall = min(len(m.past), max(0, m.recall+by))
	if m.recall == 0 {
		m.input.Reset()
		return
	}
	m.input.SetValue(m.past[len(m.past)-m.recall])
	m.input.CursorEnd()
}

func (m *Model) pick() tea.Cmd {
	if m.choice < 0 || m.choice >= len(m.order) {
		return nil
	}
	return m.join(m.order[m.choice])
}

func (m *Model) step(by int) tea.Cmd {
	if len(m.order) < 2 {
		return nil
	}
	at := slices.Index(m.order, m.current)
	return m.join(m.order[(at+by+len(m.order))%len(m.order)])
}

func (m *Model) join(room string) tea.Cmd {
	if room == "" || room == m.current {
		return nil
	}
	return m.guard(protocol.Command{Name: "join", Args: []string{room}})
}

func (m *Model) wipe() {
	if r, ok := m.rooms[m.current]; ok {
		r.log = ring.New[protocol.Event](logDepth)
	}
	m.follow, m.pending = true, false
	m.redraw()
}

func (m *Model) submit() tea.Cmd {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return nil
	}
	m.input.Reset()
	m.remember(line)
	cmd, err := protocol.TextCodec{}.Decode([]byte(line))
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	switch cmd.Name {
	case "quit":
		return m.shutdown()
	case "help":
		m.help = true
		return nil
	}
	return m.guard(cmd)
}

func (m *Model) guard(cmd protocol.Command) tea.Cmd {
	if m.link != client.Connected {
		m.fail("not connected (" + m.link.String() + "), message not sent")
		return nil
	}
	switch cmd.Name {
	case "who":
		m.asked[protocol.Who] = true
	case "rooms":
		m.asked[protocol.Rooms] = true
	}
	return m.dispatch(cmd)
}

func (m *Model) remember(line string) {
	m.recall = 0
	if n := len(m.past); n > 0 && m.past[n-1] == line {
		return
	}
	m.past = append(m.past, line)
	if len(m.past) > historyDepth {
		m.past = m.past[len(m.past)-historyDepth:]
	}
}

func (m *Model) announce(next client.State) {
	switch next {
	case client.Reconnecting:
		m.note("connection lost, reconnecting")
	case client.Closed:
		m.note("connection closed")
	}
}

func (m *Model) note(text string) {
	m.record(protocol.Event{Kind: protocol.System, Text: text, Time: time.Now()})
}

func (m *Model) fail(text string) {
	m.record(protocol.Event{Kind: protocol.Error, Text: text, Time: time.Now()})
}

func (m *Model) apply(e protocol.Event) tea.Cmd {
	var cmd tea.Cmd
	switch e.Kind {
	case protocol.Who:
		m.touch(e.Room).users = e.Names
		m.touch(e.Room).members = len(e.Names)
		if !m.answer(e.Kind) {
			m.redraw()
			return nil
		}
	case protocol.Rooms:
		m.list(e.Names)
		if !m.answer(e.Kind) {
			m.redraw()
			return nil
		}
	case protocol.Join:
		if e.From == m.me {
			m.current = e.Room
			m.touch(e.Room).unread = 0
			m.follow, m.pending = true, false
			cmd = m.refresh()
		}
		m.enter(e.Room, e.From)
	case protocol.Leave:
		m.exit(e.Room, e.From)
	case protocol.Nick:
		if e.From == m.me {
			m.me = e.To
		}
		m.rename(e.Room, e.From, e.To)
	case protocol.System:
		if e.Text == "reconnected" {
			cmd = m.refresh()
		}
	case protocol.Pong:
		e.Text = "pong"
	}
	m.record(e)
	return cmd
}

func (m *Model) answer(kind protocol.Kind) bool {
	if !m.asked[kind] {
		return false
	}
	delete(m.asked, kind)
	return true
}

func (m *Model) list(entries []string) {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name, count, ok := strings.Cut(entry, " (")
		r := m.touch(name)
		seen[name] = true
		if !ok {
			continue
		}
		n := 0
		for _, ch := range strings.TrimSuffix(count, ")") {
			if ch < '0' || ch > '9' {
				n = -1
				break
			}
			n = n*10 + int(ch-'0')
		}
		if n >= 0 {
			r.members = n
		}
	}
	m.order = slices.DeleteFunc(m.order, func(name string) bool {
		return !seen[name] && name != m.current
	})
	for name := range m.rooms {
		if !seen[name] && name != m.current {
			delete(m.rooms, name)
		}
	}
	m.choice = min(m.choice, max(0, len(m.order)-1))
}

func (m *Model) touch(name string) *roomState {
	if name == "" {
		name = m.current
	}
	if r, ok := m.rooms[name]; ok {
		return r
	}
	r := &roomState{name: name, log: ring.New[protocol.Event](logDepth)}
	m.rooms[name] = r
	m.order = append(m.order, name)
	slices.Sort(m.order)
	return r
}

func (m *Model) enter(room, who string) {
	r := m.touch(room)
	if !slices.Contains(r.users, who) {
		r.users = append(r.users, who)
		slices.Sort(r.users)
	}
	r.members = max(r.members, len(r.users))
}

func (m *Model) exit(room, who string) {
	r := m.touch(room)
	r.users = slices.DeleteFunc(r.users, func(n string) bool { return n == who })
	r.members = max(0, r.members-1)
}

func (m *Model) rename(room, from, to string) {
	r := m.touch(room)
	if i := slices.Index(r.users, from); i >= 0 {
		r.users[i] = to
		slices.Sort(r.users)
	}
}

func (m *Model) record(e protocol.Event) {
	room := e.Room
	if e.Kind == protocol.PrivMsg || e.Kind == protocol.Who || room == "" {
		room = m.current
	}
	r := m.touch(room)
	r.log.Push(e)
	if room != m.current {
		if e.Kind == protocol.Msg || e.Kind == protocol.PrivMsg {
			r.unread++
		}
		return
	}
	m.redraw()
	if !m.follow {
		m.pending = true
	}
}

func (m *Model) redraw() {
	m.body.SetContent(strings.Join(m.transcript(), "\n"))
	if m.follow {
		m.body.GotoBottom()
		m.pending = false
	}
}
