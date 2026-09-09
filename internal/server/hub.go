package server

import (
	"context"
	"errors"
	"sort"
)

var errNameTaken = errors.New("name already taken")

type hub struct {
	clients    map[*client]struct{}
	register   chan registration
	unregister chan *client
	broadcast  chan string
	requests   chan whoRequest
}

type registration struct {
	c     *client
	reply chan error
}

type whoRequest struct {
	reply chan []string
}

func newHub() *hub {
	return &hub{
		clients:    make(map[*client]struct{}),
		register:   make(chan registration),
		unregister: make(chan *client),
		broadcast:  make(chan string),
		requests:   make(chan whoRequest),
	}
}

func (h *hub) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				c.trySend("* server shutting down")
				c.closeSend()
				delete(h.clients, c)
			}
			return
		case r := <-h.register:
			if h.nameTaken(r.c.name) {
				r.reply <- errNameTaken
				continue
			}
			h.clients[r.c] = struct{}{}
			r.reply <- nil
		case c := <-h.unregister:
			delete(h.clients, c)
			c.closeSend()
		case msg := <-h.broadcast:
			for c := range h.clients {
				c.trySend(msg)
			}
		case r := <-h.requests:
			r.reply <- h.names()
		}
	}
}

func (h *hub) nameTaken(name string) bool {
	for c := range h.clients {
		if c.name == name {
			return true
		}
	}
	return false
}

func (h *hub) names() []string {
	names := make([]string, 0, len(h.clients))
	for c := range h.clients {
		names = append(names, c.name)
	}
	sort.Strings(names)
	return names
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
	case h.unregister <- c:
	case <-ctx.Done():
	}
}

func (h *hub) say(ctx context.Context, msg string) {
	select {
	case h.broadcast <- msg:
	case <-ctx.Done():
	}
}

func (h *hub) who(ctx context.Context) []string {
	r := whoRequest{reply: make(chan []string, 1)}
	select {
	case h.requests <- r:
		return <-r.reply
	case <-ctx.Done():
		return nil
	}
}
