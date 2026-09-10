package tui

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/internal/server"
	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	tea "github.com/charmbracelet/bubbletea"
)

const patience = 10 * time.Second

func listen(ctx context.Context, t *testing.T) string {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := server.New(server.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(patience):
			t.Error("server did not stop")
		}
	})
	return ln.Addr().String()
}

func dial(ctx context.Context, t *testing.T, addr, name string) *client.Client {
	t.Helper()
	c, err := client.Dial(ctx, addr, client.Options{Name: name, DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial as %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close %s: %v", name, err)
		}
	})
	return c
}

func awaitEvent(t *testing.T, c *client.Client, match func(protocol.Event) bool, what string) protocol.Event {
	t.Helper()
	deadline := time.After(patience)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				t.Fatalf("event stream closed before %s", what)
			}
			if match(e) {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s within %s", what, patience)
		}
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.After(patience)
	for !cond() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestChatThroughTheUI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := listen(ctx, t)

	watcher := dial(ctx, t, addr, "bob")
	alice := dial(ctx, t, addr, "alice")

	p := tea.NewProgram(
		newModel(alice, addr, "alice", false),
		tea.WithContext(ctx),
		tea.WithoutRenderer(),
		tea.WithInput(nil),
	)
	ran := make(chan error, 1)
	go func() {
		_, err := p.Run()
		ran <- err
	}()

	send := func(line string) {
		t.Helper()
		p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(line)})
		p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	}

	send("hello from the ui")
	got := awaitEvent(t, watcher, func(e protocol.Event) bool {
		return e.Kind == protocol.Msg && e.From == "alice"
	}, "alice's message")
	if got.Text != "hello from the ui" {
		t.Errorf("watcher saw %q, want %q", got.Text, "hello from the ui")
	}

	send("/msg bob a private word")
	dm := awaitEvent(t, watcher, func(e protocol.Event) bool {
		return e.Kind == protocol.PrivMsg && e.From == "alice"
	}, "alice's private message")
	if dm.Text != "a private word" || dm.To != "bob" {
		t.Errorf("private message = %+v, want 'a private word' to bob", dm)
	}

	send("/join #golang")
	awaitEvent(t, watcher, func(e protocol.Event) bool {
		return e.Kind == protocol.Leave && e.From == "alice"
	}, "alice leaving the default room")

	send("/quit")
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("program returned %v", err)
		}
	case <-time.After(patience):
		t.Fatal("/quit did not stop the program")
	}
	if alice.State() != client.Closed {
		t.Errorf("client state after /quit = %s, want closed", alice.State())
	}
}

func TestReconnectBanner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srvCtx, stopServer := context.WithCancel(ctx)
	srv := server.New(server.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	served := make(chan error, 1)
	go func() { served <- srv.Serve(srvCtx, ln) }()

	c, err := client.Dial(ctx, addr, client.Options{
		Name:        "alice",
		DialTimeout: 2 * time.Second,
		Reconnect:   true,
		Backoff:     client.BackoffConfig{Min: 20 * time.Millisecond, Max: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	stopServer()
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}

	m := newModel(c, addr, "alice", false)
	waitFor(t, func() bool { return c.State() == client.Reconnecting }, "the client to start reconnecting")
	poll, ok := m.poll()().(linkMsg)
	if !ok {
		t.Fatal("poll did not return a linkMsg")
	}
	next, _ := tea.Model(m).Update(poll)
	m = next.(*Model)
	if got := plain(m.View()); !strings.Contains(got, "reconnecting to "+addr) {
		t.Errorf("status bar does not show the reconnect banner:\n%s", got)
	}
	if got := texts(m, m.current); len(got) == 0 || !strings.Contains(got[len(got)-1], "reconnecting") {
		t.Errorf("transcript = %q, want a reconnecting notice", got)
	}
	if cmd := m.guard(protocol.Command{Name: "say", Text: "into the void"}); cmd != nil {
		t.Error("a send while reconnecting was not rejected")
	}
	got := texts(m, m.current)
	if last := got[len(got)-1]; !strings.HasPrefix(last, "error:not connected") {
		t.Errorf("last transcript line = %q, want an inline not-connected error", last)
	}
}
