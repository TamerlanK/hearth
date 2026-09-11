package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/internal/server"
	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
	"github.com/spf13/cobra"
)

const wait = 5 * time.Second

func serveRoot(t *testing.T) (*cobra.Command, *serveOpts) {
	t.Helper()
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "serve" {
			root.RemoveCommand(c)
		}
	}
	serve, o := newServeCmd()
	serve.RunE = func(*cobra.Command, []string) error { return nil }
	root.AddCommand(serve)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root, o
}

func TestServeFlagsAndEnv(t *testing.T) {
	base := server.Config{
		MaxClients: 100, MaxClientsPerIP: 10, IdleTimeout: 5 * time.Minute, HistorySize: 50,
		DefaultRoom: "general", MaxRooms: 64, MessagesPerSecond: 5, Burst: 10, MaxDropsInARow: 100,
	}
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    func(server.Config) server.Config
		addr    string
		wantErr string
	}{
		{
			name: "defaults",
			want: func(c server.Config) server.Config { return c },
			addr: ":4000",
		},
		{
			name: "flags",
			args: []string{"--addr", ":5000", "--max-clients", "7", "--max-per-ip", "2", "--idle-timeout", "90s",
				"--history", "3", "--rate", "1.5", "--burst", "4", "--default-room", "lobby", "--max-rooms", "0", "--max-drops", "9",
				"--motd", "welcome\nrules"},
			want: func(c server.Config) server.Config {
				c.MaxClients, c.MaxClientsPerIP, c.IdleTimeout, c.HistorySize = 7, 2, 90*time.Second, 3
				c.MessagesPerSecond, c.Burst, c.DefaultRoom, c.MaxRooms, c.MaxDropsInARow = 1.5, 4, "lobby", 0, 9
				c.MOTD = "welcome\nrules"
				return c
			},
			addr: ":5000",
		},
		{
			name: "env",
			env:  map[string]string{"HEARTH_ADDR": ":6000", "HEARTH_MAX_CLIENTS": "3", "HEARTH_IDLE_TIMEOUT": "1m"},
			want: func(c server.Config) server.Config {
				c.MaxClients, c.IdleTimeout = 3, time.Minute
				return c
			},
			addr: ":6000",
		},
		{
			name: "flags override env",
			args: []string{"--addr", ":7000", "--max-clients", "8"},
			env:  map[string]string{"HEARTH_ADDR": ":6000", "HEARTH_MAX_CLIENTS": "3", "HEARTH_BURST": "2"},
			want: func(c server.Config) server.Config {
				c.MaxClients, c.Burst = 8, 2
				return c
			},
			addr: ":7000",
		},
		{
			name:    "bad env value",
			env:     map[string]string{"HEARTH_RATE": "fast"},
			wantErr: `HEARTH_RATE="fast"`,
		},
		{
			name:    "bad flag prints usage",
			args:    []string{"--max-clients", "many"},
			wantErr: "Usage:\n  hearth serve [flags]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			root, o := serveRoot(t)
			root.SetArgs(append([]string{"serve"}, tt.args...))
			err := root.ExecuteContext(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if want := tt.want(base); !reflect.DeepEqual(o.cfg, want) {
				t.Errorf("config = %+v\nwant     %+v", o.cfg, want)
			}
			if o.addr != tt.addr {
				t.Errorf("addr = %q, want %q", o.addr, tt.addr)
			}
		})
	}
}

func TestVersionJSON(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"version", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	for _, key := range []string{"version", "commit", "date", "go", "os", "arch"} {
		if got[key] == "" {
			t.Errorf("missing %q in %v", key, got)
		}
	}
}

func TestConnectPlain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := server.New(server.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, ln) }()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	root := newRootCmd()
	root.SetIn(stdinR)
	root.SetOut(stdoutW)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"connect", ln.Addr().String(), "--plain", "--name", "alice", "--timeout", "2s"})
	cmdDone := make(chan error, 1)
	go func() {
		cmdDone <- root.ExecuteContext(ctx)
		if err := stdoutW.Close(); err != nil {
			t.Errorf("close stdout: %v", err)
		}
	}()

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdoutR)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	expect := func(substr string) {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("output ended before %q", substr)
				}
				if strings.Contains(line, substr) {
					return
				}
			case <-time.After(wait):
				t.Fatalf("no line containing %q within %s", substr, wait)
			}
		}
	}

	expect("alice joined #general")
	if _, err := io.WriteString(stdinW, "hello there\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	expect("alice: hello there")

	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-cmdDone:
		if err != nil {
			t.Fatalf("connect returned %v", err)
		}
	case <-time.After(wait):
		t.Fatal("connect did not exit after stdin closed")
	}
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(wait):
		t.Fatal("server did not stop")
	}
}

func TestFromBuildInfo(t *testing.T) {
	vcs := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-09-10T12:00:00Z"},
		},
	}
	tests := []struct {
		name                  string
		version, commit, date string
		bi                    *debug.BuildInfo
		want                  [3]string
	}{
		{"no build info", "dev", "none", "unknown", nil, [3]string{"dev", "none", "unknown"}},
		{"ldflags win", "v1.2.3", "abc1234", "2026-01-01", vcs, [3]string{"v1.2.3", "abc1234", "2026-01-01"}},
		{"go install fills the gaps", "dev", "none", "unknown", vcs, [3]string{"v0.1.0", "0123456", "2026-09-10T12:00:00Z"}},
		{"devel module version is ignored", "dev", "none", "unknown", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, [3]string{"dev", "none", "unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, c, d := fromBuildInfo(tt.version, tt.commit, tt.date, tt.bi)
			if got := [3]string{v, c, d}; got != tt.want {
				t.Errorf("fromBuildInfo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name, format, level string
		wantErr             string
		wantLine            string
	}{
		{name: "text info", format: "text", level: "info", wantLine: "INFO  hello"},
		{name: "json debug", format: "JSON", level: "debug", wantLine: `"msg":"hello"`},
		{name: "bad level", format: "text", level: "loud", wantErr: `parse --log-level "loud"`},
		{name: "bad format", format: "xml", level: "info", wantErr: `unknown --log-format "xml"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			log, err := newLogger(&out, tt.format, tt.level, false)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newLogger: %v", err)
			}
			log.Info("hello")
			if !strings.Contains(out.String(), tt.wantLine) {
				t.Errorf("output %q does not contain %q", out.String(), tt.wantLine)
			}
		})
	}
}

func TestServeListenErrors(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	busy := ln.Addr().String()
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"chat port busy", []string{"--addr", busy}, "listen on " + busy},
		{"metrics port busy", []string{"--addr", "127.0.0.1:0", "--metrics-addr", busy}, "listen on " + busy + " for metrics"},
		{"bad log level", []string{"--addr", "127.0.0.1:0", "--log-level", "loud"}, "parse --log-level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append([]string{"serve"}, tt.args...))
			err := root.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestServeRunsAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logR, logW := io.Pipe()
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(logW)
	root.SetArgs([]string{"serve", "--addr", "127.0.0.1:0", "--metrics-addr", "127.0.0.1:0", "--log-format", "json"})
	cmdDone := make(chan error, 1)
	go func() {
		cmdDone <- root.ExecuteContext(ctx)
		if err := logW.Close(); err != nil {
			t.Errorf("close log pipe: %v", err)
		}
	}()

	logs := make(chan map[string]any)
	go func() {
		defer close(logs)
		sc := bufio.NewScanner(logR)
		for sc.Scan() {
			var rec map[string]any
			if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
				t.Errorf("log line is not JSON: %v\n%s", err, sc.Text())
				continue
			}
			logs <- rec
		}
	}()
	expectLog := func(msg string) map[string]any {
		t.Helper()
		for {
			select {
			case rec, ok := <-logs:
				if !ok {
					t.Fatalf("logs ended before %q", msg)
				}
				if rec["msg"] == msg {
					return rec
				}
			case <-time.After(wait):
				t.Fatalf("no %q log within %s", msg, wait)
			}
		}
	}

	addr, _ := expectLog("hearth listening")["addr"].(string)
	metricsAddr, _ := expectLog("metrics listening")["metrics_addr"].(string)
	if addr == "" || metricsAddr == "" {
		t.Fatalf("startup logs missing addresses: addr=%q metrics_addr=%q", addr, metricsAddr)
	}

	conn, err := (&net.Dialer{Timeout: wait}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("dial chat: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	greeting, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(greeting, "Enter a name") {
		t.Fatalf("greeting = %q, %v", greeting, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+metricsAddr+"/healthz", nil)
	if err != nil {
		t.Fatalf("build healthz request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("healthz = %d %q, %v", resp.StatusCode, body, err)
	}

	cancel()
	expectLog("hearth stopped")
	select {
	case err := <-cmdDone:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(wait):
		t.Fatal("serve did not exit after cancel")
	}
}

func startTestServer(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := server.New(server.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(wait):
			t.Error("server did not stop")
		}
	})
	return ln.Addr().String()
}

func runCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var out bytes.Buffer
	root := newRootCmd()
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

func TestConnectRemembersTheServerAndName(t *testing.T) {
	addr := startTestServer(t)
	cfg := filepath.Join(t.TempDir(), "nested", "config.json")

	_, err := runCLI(t, "", "--config", cfg, "connect", "--plain")
	if !errors.Is(err, errNoServer) {
		t.Fatalf("connect with nothing remembered: %v, want errNoServer", err)
	}

	out, err := runCLI(t, "", "--config", cfg, "connect", addr, "--plain", "--name", "alice")
	if err != nil || !strings.Contains(out, "alice joined #general") {
		t.Fatalf("first connect: err = %v, output = %q", err, out)
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var saved remembered
	if err := json.Unmarshal(b, &saved); err != nil || saved != (remembered{Addr: addr, Name: "alice"}) {
		t.Fatalf("config = %s (%v), want addr %s and name alice", b, err, addr)
	}

	out, err = runCLI(t, "", "--config", cfg, "connect", "--plain")
	if err != nil || !strings.Contains(out, "alice joined #general") {
		t.Fatalf("connect from memory: err = %v, output = %q", err, out)
	}

	out, err = runCLI(t, "", "--config", cfg, "connect", "--plain", "--name", "bob")
	if err != nil || !strings.Contains(out, "bob joined #general") {
		t.Fatalf("connect with an explicit name: err = %v, output = %q", err, out)
	}
	b, err = os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(b), `"name": "bob"`) {
		t.Errorf("config after the second connect = %s (%v), want bob remembered", b, err)
	}

	t.Setenv("HEARTH_CONFIG", cfg)
	out, err = runCLI(t, "", "connect", "--plain")
	if err != nil || !strings.Contains(out, "bob joined #general") {
		t.Fatalf("connect via HEARTH_CONFIG: err = %v, output = %q", err, out)
	}
}

func TestResolveTargetFallsBackToTheOSUser(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"--config", cfg, "version"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	addr, name, err := resolveTarget(root, []string{"h:1"}, "")
	if err != nil || addr != "h:1" || name != osUserName() {
		t.Errorf("resolveTarget = %q, %q, %v; want h:1 and the OS user", addr, name, err)
	}
	if _, got, _ := resolveTarget(root, []string{"h:1"}, "carol"); got != "carol" {
		t.Errorf("an explicit name resolved to %q", got)
	}
}

func TestSendWhoAndRooms(t *testing.T) {
	addr := startTestServer(t)
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	bob, err := client.Dial(ctx, addr, client.Options{Name: "bob", DialTimeout: wait})
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()
	next := func(kind protocol.Kind) protocol.Event {
		t.Helper()
		for {
			select {
			case e, ok := <-bob.Events():
				if !ok {
					t.Fatal("bob's event stream closed")
				}
				if e.Kind == kind {
					return e
				}
			case <-time.After(wait):
				t.Fatalf("bob got no %s event", kind)
			}
		}
	}

	out, err := runCLI(t, "", "--config", cfg, "send", addr, "--name", "ci", "deploy", "finished")
	if err != nil || out != "" {
		t.Fatalf("send: err = %v, output = %q", err, out)
	}
	if e := next(protocol.Msg); e.From != "ci" || e.Text != "deploy finished" {
		t.Errorf("bob saw %+v, want 'deploy finished' from ci", e)
	}

	if _, err := runCLI(t, "one\n\ntwo\n", "--config", cfg, "send", addr, "--name", "ci", "--to", "bob"); err != nil {
		t.Fatalf("send from stdin: %v", err)
	}
	for _, want := range []string{"one", "two"} {
		if e := next(protocol.PrivMsg); e.Text != want || e.To != "bob" {
			t.Errorf("bob's DM = %+v, want %q", e, want)
		}
	}

	_, err = runCLI(t, "", "--config", cfg, "send", addr, "--name", "ci", "--to", "nobody", "hello?")
	var serr *client.ServerError
	if !errors.As(err, &serr) || !strings.Contains(err.Error(), "no such user") {
		t.Errorf("send to a missing user: %v, want the server's error", err)
	}

	_, err = runCLI(t, "", "--config", cfg, "send", addr, "--name", "bob", "impostor")
	if !errors.Is(err, client.ErrNameTaken) || !strings.Contains(err.Error(), "--name") {
		t.Errorf("send with a taken name: %v, want ErrNameTaken and a hint", err)
	}

	out, err = runCLI(t, "", "--config", cfg, "who", addr, "--name", "peek")
	if err != nil || out != "bob\n" {
		t.Errorf("who: err = %v, output = %q, want just bob", err, out)
	}
	out, err = runCLI(t, "", "--config", cfg, "who", addr, "--name", "peek", "--json")
	var who struct {
		Room  string
		Names []string
	}
	if err != nil || json.Unmarshal([]byte(out), &who) != nil || who.Room != "#general" || !reflect.DeepEqual(who.Names, []string{"bob"}) {
		t.Errorf("who --json: err = %v, output = %q", err, out)
	}

	out, err = runCLI(t, "", "--config", cfg, "rooms", addr, "--name", "peek")
	if err != nil || out != "#general 1\n" {
		t.Errorf("rooms: err = %v, output = %q, want '#general 1' (bob only)", err, out)
	}
	out, err = runCLI(t, "", "--config", cfg, "rooms", addr, "--name", "peek", "--json")
	var rooms []client.RoomInfo
	if err != nil || json.Unmarshal([]byte(out), &rooms) != nil || len(rooms) != 1 || rooms[0].Members != 1 {
		t.Errorf("rooms --json: err = %v, output = %q", err, out)
	}

	if err := os.WriteFile(cfg, []byte(`{"addr":"`+addr+`","name":"ci"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "", "--config", cfg, "send", "from", "memory"); err != nil {
		t.Fatalf("send with a remembered server: %v", err)
	}
	if e := next(protocol.Msg); e.From != "ci" || e.Text != "from memory" {
		t.Errorf("bob saw %+v, want 'from memory' from ci", e)
	}
}

func TestLooksLikeAddr(t *testing.T) {
	for s, want := range map[string]bool{"localhost:4000": true, ":4000": true, "[::1]:1": true, "hello": false, "a b": false, "host:": true} {
		if got := looksLikeAddr(s); got != want {
			t.Errorf("looksLikeAddr(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPrettyHandler(t *testing.T) {
	at := time.Date(2026, 9, 11, 16, 49, 54, 0, time.UTC)
	tests := []struct {
		name  string
		color bool
		log   func(*slog.Logger)
		want  string
	}{
		{
			name: "message only",
			log:  func(l *slog.Logger) { l.Info("hearth stopped", "event", "shutdown") },
			want: "16:49:54 INFO  hearth stopped\n",
		},
		{
			name: "attrs are aligned and event is dropped",
			log: func(l *slog.Logger) {
				l.With("client_id", "6d39d0fb").Info("client joined", "event", "join", "name", "alice", "room", "#general")
			},
			want: "16:49:54 INFO  client joined              client_id=6d39d0fb name=alice room=#general\n",
		},
		{
			name: "groups keep their prefix and values quote only when needed",
			log: func(l *slog.Logger) {
				l.Warn("a warning", slog.Group("config", "idle_timeout", 5*time.Minute, "motd", "be kind"), "empty", "", "n", 3)
			},
			want: "16:49:54 WARN  a warning                  config.idle_timeout=5m0s config.motd=\"be kind\" empty=\"\" n=3\n",
		},
		{
			name: "long messages push the attrs along",
			log:  func(l *slog.Logger) { l.Error("a message longer than the column it is padded to", "err", "boom") },
			want: "16:49:54 ERROR a message longer than the column it is padded to err=boom\n",
		},
		{
			name: "debug below the level is dropped",
			log:  func(l *slog.Logger) { l.Debug("noise") },
			want: "",
		},
		{
			name:  "colour wraps the time, level and keys",
			color: true,
			log:   func(l *slog.Logger) { l.Info("hi", "k", "v") },
			want:  "\x1b[2m16:49:54\x1b[0m \x1b[32mINFO \x1b[0m hi                         \x1b[2mk=\x1b[0mv\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			h := newPrettyHandler(&out, slog.LevelInfo, tt.color)
			tt.log(slog.New(clockHandler{Handler: h, at: at}))
			if got := out.String(); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

type clockHandler struct {
	slog.Handler
	at time.Time
}

func (c clockHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Time = c.at
	return c.Handler.Handle(ctx, r)
}

func (c clockHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return clockHandler{Handler: c.Handler.WithAttrs(attrs), at: c.at}
}

func (c clockHandler) WithGroup(name string) slog.Handler {
	return clockHandler{Handler: c.Handler.WithGroup(name), at: c.at}
}
