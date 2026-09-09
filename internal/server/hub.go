package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/TamerlanK/hearth/internal/protocol"
)

const historyLimit = 50

var errNameTaken = errors.New("name already taken")

type hub struct {
	clients  map[*client]struct{}
	history  map[string][]protocol.Event
	register chan registration
	leaving  chan *client
	requests chan request
}

type registration struct {
	c     *client
	reply chan error
}

type request struct {
	c     *client
	cmd   protocol.Command
	reply chan []protocol.Event
}

func newHub() *hub {
	return &hub{
		clients:  make(map[*client]struct{}),
		history:  make(map[string][]protocol.Event),
		register: make(chan registration),
		leaving:  make(chan *client),
		requests: make(chan request),
	}
}

func (h *hub) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				c.trySend(systemEvent("server shutting down"))
				c.closeSend()
				delete(h.clients, c)
			}
			return
		case r := <-h.register:
			if h.named(r.c.name) != nil {
				r.reply <- errNameTaken
				continue
			}
			h.clients[r.c] = struct{}{}
			r.reply <- nil
			h.broadcast(protocol.Event{Kind: protocol.Join, Room: r.c.room, From: r.c.name, Time: time.Now()})
			r.c.trySendAll(h.replay(r.c.room))
		case c := <-h.leaving:
			delete(h.clients, c)
			c.closeSend()
			h.broadcast(protocol.Event{Kind: protocol.Leave, Room: c.room, From: c.name, Time: time.Now()})
			h.forgetEmpty(c.room)
		case r := <-h.requests:
			r.reply <- h.handle(r.c, r.cmd)
		}
	}
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
		room := h.roomArg(c, cmd.Args)
		return []protocol.Event{{Kind: protocol.Who, Room: room, Names: h.names(room), Time: time.Now()}}
	case "rooms":
		return []protocol.Event{{Kind: protocol.Rooms, Names: h.rooms(), Time: time.Now()}}
	case "history":
		room := h.roomArg(c, cmd.Args)
		if events := h.replay(room); len(events) > 0 {
			return events
		}
		return []protocol.Event{systemEvent("no history for " + room)}
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
	e := protocol.Event{Kind: protocol.Msg, Room: c.room, From: c.name, Text: text, Time: time.Now()}
	h.record(e)
	h.broadcast(e)
	return nil
}

func (h *hub) private(c *client, cmd protocol.Command) []protocol.Event {
	if len(cmd.Args) != 1 || cmd.Text == "" {
		return []protocol.Event{errorEvent("usage: /msg <name> <text>")}
	}
	to := h.named(cmd.Args[0])
	if to == nil {
		return []protocol.Event{errorEvent("no such user " + cmd.Args[0])}
	}
	e := protocol.Event{Kind: protocol.PrivMsg, From: c.name, To: to.name, Text: printable(cmd.Text), Time: time.Now()}
	if to == c {
		return []protocol.Event{e}
	}
	to.trySend(e)
	return []protocol.Event{e}
}

func (h *hub) joinRoom(c *client, args []string) []protocol.Event {
	if len(args) != 1 {
		return []protocol.Event{errorEvent("usage: /join <room>")}
	}
	room := roomName(args[0])
	if err := validateRoom(room); err != nil {
		return []protocol.Event{errorEvent(err.Error())}
	}
	if room == c.room {
		return []protocol.Event{errorEvent("already in " + room)}
	}
	old := c.room
	h.broadcast(protocol.Event{Kind: protocol.Leave, Room: old, From: c.name, Time: time.Now()})
	c.room = room
	h.forgetEmpty(old)
	h.broadcast(protocol.Event{Kind: protocol.Join, Room: room, From: c.name, Time: time.Now()})
	return h.replay(room)
}

func (h *hub) rename(c *client, args []string) []protocol.Event {
	if len(args) != 1 {
		return []protocol.Event{errorEvent("usage: /nick <name>")}
	}
	name := args[0]
	if err := validateName(name); err != nil {
		return []protocol.Event{errorEvent(err.Error())}
	}
	if name == c.name {
		return nil
	}
	if h.named(name) != nil {
		return []protocol.Event{errorEvent("name taken")}
	}
	old := c.name
	c.name = name
	h.broadcast(protocol.Event{Kind: protocol.Nick, Room: c.room, From: old, To: name, Time: time.Now()})
	return nil
}

func (h *hub) broadcast(e protocol.Event) {
	for c := range h.clients {
		if e.Room == "" || c.room == e.Room {
			c.trySend(e)
		}
	}
}

func (h *hub) record(e protocol.Event) {
	h.history[e.Room] = append(h.history[e.Room], e)
	if past := h.history[e.Room]; len(past) > historyLimit {
		h.history[e.Room] = past[len(past)-historyLimit:]
	}
}

func (h *hub) replay(room string) []protocol.Event {
	past := h.history[room]
	events := make([]protocol.Event, 0, len(past))
	for _, e := range past {
		e.Kind = protocol.History
		events = append(events, e)
	}
	return events
}

func (h *hub) forgetEmpty(room string) {
	if len(h.names(room)) == 0 {
		delete(h.history, room)
	}
}

func (h *hub) named(name string) *client {
	for c := range h.clients {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (h *hub) names(room string) []string {
	var names []string
	for c := range h.clients {
		if c.room == room {
			names = append(names, c.name)
		}
	}
	sort.Strings(names)
	return names
}

func (h *hub) rooms() []string {
	seen := make(map[string]struct{}, len(h.clients))
	for c := range h.clients {
		seen[c.room] = struct{}{}
	}
	rooms := make([]string, 0, len(seen))
	for room := range seen {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)
	return rooms
}

func (h *hub) roomArg(c *client, args []string) string {
	if len(args) == 1 {
		return roomName(args[0])
	}
	return c.room
}

func (h *hub) join(ctx context.Context, c *client) error {
	r := registration{c: c, reply: make(chan error, 1)}
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
	switch n := utf8.RuneCountInString(room) - 1; {
	case n == 0:
		return errors.New("room name is empty")
	case n > maxRoomLen:
		return fmt.Errorf("room name is longer than %d characters", maxRoomLen)
	}
	for _, r := range room {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return errors.New("room name must be printable with no spaces")
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
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}
