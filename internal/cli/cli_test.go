package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/internal/server"
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
				"--history", "3", "--rate", "1.5", "--burst", "4", "--default-room", "lobby", "--max-rooms", "0", "--max-drops", "9"},
			want: func(c server.Config) server.Config {
				c.MaxClients, c.MaxClientsPerIP, c.IdleTimeout, c.HistorySize = 7, 2, 90*time.Second, 3
				c.MessagesPerSecond, c.Burst, c.DefaultRoom, c.MaxRooms, c.MaxDropsInARow = 1.5, 4, "lobby", 0, 9
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
