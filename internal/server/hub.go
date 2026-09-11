package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/TamerlanK/hearth/internal/ring"
	"github.com/TamerlanK/hearth/pkg/protocol"
)

var errNameTaken = errors.New("name already taken")

type room struct {
	name    string
	members map[*client]struct{}
	history *ring.Ring[protocol.Event]
}

type hub struct {
	defaultRoom string
	historySize int
	maxRooms    int
	motd        []protocol.Event

	rooms    map[string]*room
	byName   map[string]*client
	register chan registration
	leaving  chan *client
	requests chan request
}

type registration struct {
	c     *client
	name  string
	reply chan error
}

type request struct {
	c     *client
	cmd   protocol.Command
	reply chan []protocol.Event
}

func newHub(cfg Config) *hub {
	h := &hub{
		defaultRoom: cfg.DefaultRoom,
		historySize: cfg.HistorySize,
		maxRooms:    cfg.MaxRooms,
		motd:        motdEvents(cfg.MOTD),
		rooms:       make(map[string]*room),
		byName:      make(map[string]*client),
		register:    make(chan registration),
		leaving:     make(chan *client),
		requests:    make(chan request),
	}
	h.create(cfg.DefaultRoom)
	return h
}

func (h *hub) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for _, r := range h.rooms {
				for c := range r.members {
					c.trySend(systemEvent("server shutting down"))
					c.closeSend()
				}
			}
			return
		case r := <-h.register:
			r.reply <- h.add(r.c, r.name)
		case c := <-h.leaving:
			h.remove(c)
		case r := <-h.requests:
			r.reply <- h.handle(r.c, r.cmd)
		}
	}
}

func (h *hub) create(name string) *room {
	r := &room{
		name:    name,
		members: make(map[*client]struct{}),
		history: ring.New[protocol.Event](h.historySize),
	}
	h.rooms[name] = r
	roomsCurrent.Set(float64(len(h.rooms)))
	return r
}

func (h *hub) discardIfEmpty(r *room) {
	if len(r.members) == 0 && r.name != h.defaultRoom {
		delete(h.rooms, r.name)
		roomsCurrent.Set(float64(len(h.rooms)))
	}
}

func (h *hub) add(c *client, name string) error {
	if _, taken := h.byName[name]; taken {
		return errNameTaken
	}
	r := h.rooms[h.defaultRoom]
	c.name = name
	c.room = r
	h.byName[name] = c
	r.members[c] = struct{}{}
	h.broadcast(r, protocol.Event{Kind: protocol.Join, Room: r.name, From: name, Time: time.Now()})
	c.trySendAll(replay(r))
	c.trySendAll(h.motd)
	return nil
}

func (h *hub) remove(c *client) {
	r := c.room
	delete(r.members, c)
	delete(h.byName, c.name)
	c.closeSend()
	h.broadcast(r, protocol.Event{Kind: protocol.Leave, Room: r.name, From: c.name, Time: time.Now()})
	h.discardIfEmpty(r)
}

func (h *hub) handle(c *client, cmd protocol.Command) []protocol.Event {
	switch cmd.Name {
	case "say":
		return h.say(c, cmd.Text)
	case "msg":
		return h.private(c, cmd)
	case "join":
		return h.joinRoom(c, cmd.Args)
	case "nick":
		return h.rename(c, cmd.Args)
	case "who":
		name := h.roomArg(c, cmd.Args)
		events := []protocol.Event{{Kind: protocol.Who, Room: name, Names: h.names(name), Time: time.Now()}}
		return append(events, h.awayIn(name)...)
	case "rooms":
		return []protocol.Event{{Kind: protocol.Rooms, Names: h.roomList(), Time: time.Now()}}
	case "history":
		name := h.roomArg(c, cmd.Args)
		if r, ok := h.rooms[name]; ok {
			if events := replay(r); len(events) > 0 {
				return events
			}
		}
		return []protocol.Event{systemEvent("no history for " + name)}
	case "away":
		return h.away(c, printable(cmd.Text))
	case "ping":
		return []protocol.Event{{Kind: protocol.Pong, Time: time.Now()}}
	default:
		return []protocol.Event{errorEvent("unknown command " + cmd.Name + " (try /help)")}
	}
}

func (h *hub) say(c *client, text string) []protocol.Event {
	text = printable(text)
	if text == "" {
		return nil
	}
	h.away(c, "")
	e := protocol.Event{Kind: protocol.Msg, Room: c.room.name, From: c.name, Text: text, Time: time.Now()}
	c.room.history.Push(e)
	start := time.Now()
	h.broadcast(c.room, e)
	fanoutSeconds.Observe(time.Since(start).Seconds())
	return nil
}

func (h *hub) private(c *client, cmd protocol.Command) []protocol.Event {
	if len(cmd.Args) != 1 || cmd.Text == "" {
		return []protocol.Event{errorEvent(protocol.Usage("msg"))}
	}
	to := h.byName[cmd.Args[0]]
	if to == nil {
		return []protocol.Event{errorEvent("no such user " + cmd.Args[0])}
	}
	e := protocol.Event{Kind: protocol.PrivMsg, From: c.name, To: to.name, Text: printable(cmd.Text), Time: time.Now()}
	messagesTotal.WithLabelValues(string(e.Kind)).Inc()
	if to != c {
		to.trySend(e)
	}
	return []protocol.Event{e}
}

func (h *hub) joinRoom(c *client, args []string) []protocol.Event {
	if len(args) != 1 {
		return []protocol.Event{errorEvent(protocol.Usage("join"))}
	}
	name := roomName(args[0])
	if err := validateRoom(name); err != nil {
		return []protocol.Event{errorEvent(err.Error())}
	}
	if name == c.room.name {
		return []protocol.Event{errorEvent("already in " + name)}
	}
	next, ok := h.rooms[name]
	if !ok {
		if h.maxRooms > 0 && len(h.rooms) >= h.maxRooms {
			return []protocol.Event{errorEvent(fmt.Sprintf("too many rooms, limit is %d", h.maxRooms))}
		}
		next = h.create(name)
	}
	old := c.room
	h.broadcast(old, protocol.Event{Kind: protocol.Leave, Room: old.name, From: c.name, Time: time.Now()})
	delete(old.members, c)
	h.discardIfEmpty(old)

	c.room = next
	next.members[c] = struct{}{}
	h.broadcast(next, protocol.Event{Kind: protocol.Join, Room: next.name, From: c.name, Time: time.Now()})
	if c.away != "" {
		h.broadcast(next, protocol.Event{Kind: protocol.Away, Room: next.name, From: c.name, Text: c.away, Time: time.Now()})
	}
	return replay(next)
}

func (h *hub) rename(c *client, args []string) []protocol.Event {
	if len(args) != 1 {
		return []protocol.Event{errorEvent(protocol.Usage("nick"))}
	}
	name := args[0]
	if err := validateName(name); err != nil {
		return []protocol.Event{errorEvent(err.Error())}
	}
	if name == c.name {
		return nil
	}
	if _, taken := h.byName[name]; taken {
		return []protocol.Event{errorEvent("name taken")}
	}
	old := c.name
	c.name = name
	delete(h.byName, old)
	h.byName[name] = c
	h.broadcast(c.room, protocol.Event{Kind: protocol.Nick, Room: c.room.name, From: old, To: name, Time: time.Now()})
	return nil
}

func (h *hub) away(c *client, reason string) []protocol.Event {
	if c.away == reason {
		return nil
	}
	c.away = reason
	h.broadcast(c.room, protocol.Event{Kind: protocol.Away, Room: c.room.name, From: c.name, Text: reason, Time: time.Now()})
	return nil
}

func (h *hub) awayIn(room string) []protocol.Event {
	r, ok := h.rooms[room]
	if !ok {
		return nil
	}
	var events []protocol.Event
	for c := range r.members {
		if c.away != "" {
			events = append(events, protocol.Event{Kind: protocol.Away, Room: room, From: c.name, Text: c.away, Time: time.Now()})
		}
	}
	slices.SortFunc(events, func(a, b protocol.Event) int { return strings.Compare(a.From, b.From) })
	return events
}

func (h *hub) broadcast(r *room, e protocol.Event) {
	messagesTotal.WithLabelValues(string(e.Kind)).Inc()
	for c := range r.members {
		c.trySend(e)
	}
}

func replay(r *room) []protocol.Event {
	past := r.history.Snapshot()
	for i := range past {
		past[i].Kind = protocol.History
	}
	return past
}

func (h *hub) names(room string) []string {
	r, ok := h.rooms[room]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(r.members))
	for c := range r.members {
		names = append(names, c.name)
	}
	slices.Sort(names)
	return names
}

func (h *hub) roomList() []string {
	rooms := make([]string, 0, len(h.rooms))
	for name, r := range h.rooms {
		rooms = append(rooms, fmt.Sprintf("%s (%d)", name, len(r.members)))
	}
	slices.Sort(rooms)
	return rooms
}

func (h *hub) roomArg(c *client, args []string) string {
	if len(args) == 1 {
		return roomName(args[0])
	}
	return c.room.name
}

func (h *hub) join(ctx context.Context, c *client, name string) error {
	r := registration{c: c, name: name, reply: make(chan error, 1)}
	select {
	case h.register <- r:
		return <-r.reply
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hub) leave(ctx context.Context, c *client) {
	select {
	case h.leaving <- c:
	case <-ctx.Done():
	}
}

func (h *hub) do(ctx context.Context, c *client, cmd protocol.Command) []protocol.Event {
	r := request{c: c, cmd: cmd, reply: make(chan []protocol.Event, 1)}
	select {
	case h.requests <- r:
		return <-r.reply
	case <-ctx.Done():
		return nil
	}
}

func motdLines(motd string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(motd, "\r\n", "\n"), "\n") {
		if line = printable(strings.TrimRight(line, " ")); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func motdEvents(motd string) []protocol.Event {
	lines := motdLines(motd)
	events := make([]protocol.Event, 0, len(lines))
	for _, line := range lines {
		events = append(events, protocol.Event{Kind: protocol.System, Text: line})
	}
	return events
}

func systemEvent(text string) protocol.Event {
	return protocol.Event{Kind: protocol.System, Text: text, Time: time.Now()}
}

func errorEvent(text string) protocol.Event {
	return protocol.Event{Kind: protocol.Error, Text: text, Time: time.Now()}
}

func roomName(s string) string {
	return "#" + strings.TrimPrefix(s, "#")
}

func validateRoom(room string) error {
	name := strings.TrimPrefix(room, "#")
	badRoom := fmt.Errorf("room name must be 1-%d characters of a-z, 0-9 or -", maxRoomLen)
	if len(name) == 0 || len(name) > maxRoomLen {
		return badRoom
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return badRoom
		}
	}
	return nil
}

func validateName(name string) error {
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return errors.New("name is empty")
	case n > maxNameLen:
		return fmt.Errorf("name is longer than %d characters", maxNameLen)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return errors.New("name must be printable with no spaces")
		}
	}
	return nil
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}
