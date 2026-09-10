# hearth

[![CI](https://github.com/TamerlanK/hearth/actions/workflows/ci.yml/badge.svg)](https://github.com/TamerlanK/hearth/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/TamerlanK/hearth)](https://github.com/TamerlanK/hearth/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/TamerlanK/hearth)](https://goreportcard.com/report/github.com/TamerlanK/hearth)
[![Go Reference](https://pkg.go.dev/badge/github.com/TamerlanK/hearth.svg)](https://pkg.go.dev/github.com/TamerlanK/hearth)
[![License: MIT](https://img.shields.io/github/license/TamerlanK/hearth)](LICENSE)

Hearth is a TCP chat server and terminal client shipped as a single binary. It
speaks a small line-oriented protocol, keeps room state in one place, and gives
you a terminal UI for joining a room without installing anything else.

**Status: in development.** The server, the terminal UI and the Go client
library work.

## Install

With Go:

```sh
go install github.com/TamerlanK/hearth/cmd/hearth@latest
```

From a [release](https://github.com/TamerlanK/hearth/releases/latest): download
the archive for your OS and architecture, verify it against `checksums.txt`,
unpack, and put `hearth` on your PATH.

With Docker (the image only makes sense for the server; the client wants your
terminal):

```sh
docker run --rm -p 4000:4000 -p 9090:9090 ghcr.io/tamerlank/hearth:latest
```

Or from a clone: `make build` puts the binary in `bin/hearth`.

## Quickstart

```sh
hearth serve                                                # listens on :4000
hearth connect localhost:4000 --name alice                  # in another terminal
```

### Try it in 30 seconds

Terminal 1:

```sh
hearth serve --addr :4000
```

Terminal 2 (and 3, with another name):

```sh
hearth connect localhost:4000 --name alice
```

`hearth connect` opens the terminal UI: rooms and members on the left, the
transcript on the right, a prompt at the bottom and a status bar under it.

<!-- screenshot: docs/screenshot.png — two terminals, #golang with an unread
     badge on #general. Not committed yet. -->

```
┌ sidebar ───────────┬ messages ───────────────────────────┐
│ Rooms              │ [15:04] alice        hello everyone │
│  #general (3) •2   │ [15:04] bob          hey            │
│ >#golang  (1)      │ [15:04]            * carol joined   │
│                    │                                     │
│ Users in #golang   ├─────────────────────────────────────┤
│  you               │ > type a message or /command_       │
│  carol             │                                     │
├────────────────────┴─────────────────────────────────────┤
│ connected to host:4000 as alice · #golang · ?: help      │
└──────────────────────────────────────────────────────────┘
```

Type a line to send it to the room. `/who` lists users, `/join <room>` moves
rooms, `/msg <name> <text>` is private, `/quit` leaves, `?` or `F1` opens the
help overlay. Ctrl+C in either terminal exits cleanly; the server waits up to 5s
for clients to drain.

The UI needs at least 80x24 to look as drawn. Below 60 columns the sidebar goes
away and the transcript takes the full width; below 24x6 it says so rather than
drawing a broken frame. Colours follow the terminal's light or dark background
and are dropped entirely when `NO_COLOR` is set.

Unlike `--plain`, the UI reconnects on its own when the server goes away: the
status bar turns into `reconnecting to host:4000… (attempt N)` and sending is
refused with an inline error rather than hanging.

### Keybindings

| Key | What it does |
|-----|--------------|
| `Enter` | Send the line, or join the highlighted room when the rooms pane has focus |
| `Tab` / `Shift+Tab` | Cycle focus: input → messages → rooms |
| `Ctrl+N` / `Ctrl+P` | Join the next / previous room |
| `Up` / `Down` | Command history in the input, scroll in the messages pane, pick a room in the rooms pane |
| `PgUp` / `PgDn` | Scroll the transcript |
| `Ctrl+L` | Clear the current room's transcript |
| `?` | Toggle the help overlay (outside the input) |
| `F1` | Toggle the help overlay |
| `Esc` | Close the help overlay |
| `Ctrl+C` | Close the client and quit |

Scrolling up locks the view; a `↓ new messages` pill appears above the prompt
until you scroll back to the bottom. Rooms you are not looking at carry a `•N`
unread badge.

`--plain` runs a minimal stdin/stdout client instead of the terminal UI, which
makes scripting easy (and is required when stdout is not a terminal):

```sh
echo "deploy finished" | hearth connect localhost:4000 --plain --name ci --room ops
```

Every flag has an environment variable named after it, `HEARTH_` plus the flag
name in upper case with dashes as underscores. Flags win over the environment,
and the environment wins over the default:

```sh
HEARTH_ADDR=:5000 HEARTH_LOG_FORMAT=json hearth serve
```

`hearth version --json` prints the build info as JSON, and `hearth completion
bash|zsh|fish|powershell` prints a shell completion script.

From Go, import `pkg/client` and chat in a dozen lines; see
[docs/CLIENT.md](docs/CLIENT.md).

`telnet localhost 4000` still works: enter a name when prompted, then type.
Programs should speak the JSON encoding instead: send `HELLO hearth/1 json` as
the first line and every line in both directions becomes one JSON object. See
[docs/PROTOCOL.md](docs/PROTOCOL.md), which has a working Python client.

## Features

- **Rooms.** Everyone starts in `#general`. `/join <room>` creates a room on
  first use and moves you into it; a room disappears when its last member
  leaves. Room names are 1–24 characters of `a-z`, `0-9` and `-`.
- **History.** The last 50 messages of a room are replayed to you alone when you
  join it, so you arrive mid-conversation rather than blind. The depth, the
  default room and a cap on the number of rooms are `server.Config` fields.
- **Private messages.** `/msg <name> <text>` reaches the two of you and nobody
  else, whatever rooms you are in.
- **Renaming.** `/nick <name>` takes the same rules as the connect prompt and
  tells your room about it.
- **Two encodings, one semantics.** Type into `telnet`, or send
  `HELLO hearth/1 json` and get one JSON object per line instead.
- **No head-of-line blocking.** A client that stops reading has its own events
  dropped and is disconnected; nobody else waits for it.
- **Hardened by default.** Per-client rate limiting, a per-address connection
  cap, a message length cap, handshake and idle timeouts, and panic recovery
  that closes one connection rather than the process.

`/help` prints the whole command list, generated from the same table the parser
uses:

```
* commands: /say <text>, /msg <name> <text>, /join <room>, /nick <name>, /who [room], /rooms, /history [room], /ping, /quit, /help (a bare line is /say)
```

## Operations

```sh
hearth serve --addr :4000 --metrics-addr :9090 --log-format json --log-level info
```

Or the whole thing with Prometheus already scraping it:

```sh
docker compose up        # chat on :4000, Prometheus UI on http://localhost:9090
```

The compose file builds the server image (12 MB, distroless, non-root) and
starts Prometheus with [prometheus.yml](prometheus.yml) pointed at it; query
any `hearth_*` metric from the table below at `http://localhost:9090`.

On start the server logs one banner with the resolved configuration. The
configuration is rendered through `Config.LogValue`, an explicit allowlist of
fields, so a value that is not named there can never reach the log — that is
where future secrets (TLS key paths, tokens) stay out.

### Flags

Each flag reads `HEARTH_<FLAG>` from the environment when it is not given on
the command line; `hearth serve --help` names the variable next to each flag.

| Flag | Default | What it does |
|------|---------|--------------|
| `--addr` | `:4000` | Chat listener address |
| `--metrics-addr` | *(empty)* | Serve `/metrics` and `/healthz` here; empty turns it off |
| `--log-format` | `text` | `text` or `json` |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `--max-clients` | `100` | Concurrent connections (0 = unlimited) |
| `--max-per-ip` | `10` | Concurrent connections from one address (0 = unlimited) |
| `--idle-timeout` | `5m` | Disconnect clients silent this long (0 = never) |
| `--history` | `50` | Messages kept per room and replayed when joining it |
| `--default-room` | `general` | Room every client starts in |
| `--max-rooms` | `64` | Rooms that may exist at once (0 = unlimited) |
| `--rate` | `5` | Sustained lines per second per client (0 = unlimited) |
| `--burst` | `10` | Lines a client may send back to back before `--rate` applies |
| `--max-drops` | `100` | Consecutive dropped events before a client is disconnected (0 = never) |

Two limits are not flags because they are protocol constants: the 4096-byte line
cap and the 1024-rune message cap (`docs/PROTOCOL.md`), and the 10s deadline for
completing the handshake.

`server.Config`'s zero value applies **no** limits, matching the existing
meaning of `MaxClients: 0`. The defaults above live in the flags, so a program
embedding `server.New` opts into each limit deliberately. The one field that is
normalised rather than taken literally is `Burst`: a rate with a zero burst
would let nobody send anything, so it becomes 1.

### Logging

Every line is `log/slog` with a stable key set: `event` (a machine-readable
slug), `client_id` (a short random per-connection id), `remote_addr`, and, once
a client is named, `name` and `room`. `name` and `room` are join-time snapshots
— see the name-ownership note in the architecture doc — so follow `client_id`,
not `name`, when tracing one connection.

```sh
hearth serve --log-format json --log-level debug 2>&1 | jq 'select(.event=="rate_limited")'
```

### Metrics

`--metrics-addr` serves Prometheus text on `/metrics` and a plain `ok` on
`/healthz`. Alongside the usual `go_*` and `process_*` collectors:

| Metric | Type | Meaning |
|--------|------|---------|
| `hearth_connections_current` | gauge | Connections open now |
| `hearth_connections_total` | counter | Connections admitted since start |
| `hearth_messages_total{kind}` | counter | Events produced by the hub, once each, not per recipient |
| `hearth_dropped_messages_total` | counter | Events dropped because an outbox was full |
| `hearth_rate_limited_total` | counter | Lines rejected by a rate limiter |
| `hearth_rooms_current` | gauge | Rooms that exist now |
| `hearth_message_fanout_seconds` | histogram | Time for the hub to hand one message to every outbox |

`hearth_rate_limited_total` climbing fast is normal under a flood: a client
spraying lines is counted once per rejected line, so a `yes | nc` flood adds
millions per minute while `hearth_messages_total{kind="msg"}` rises at
`--rate`. Watch the gap between the two rather than either alone.

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Protocol

See [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Client library

See [docs/CLIENT.md](docs/CLIENT.md).

## Development

Requires Go 1.24+.

```sh
make check         # fmt, vet, lint, test -race — the gate for every change
make fuzz          # fuzz both protocol decoders, 10s each
make build         # -> bin/hearth
make docker        # build the container image as hearth:<version>
make release-dry   # goreleaser snapshot: all six platforms into dist/, no publishing
```

`make lint` needs `golangci-lint` on your PATH and is skipped with a notice when
it is missing; `make release-dry` needs `goreleaser`:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
go install golang.org/x/tools/cmd/goimports@latest
go install github.com/goreleaser/goreleaser/v2@latest
```

Longer fuzzing runs go through `go test` directly, e.g.:

```sh
go test ./pkg/protocol -run '^$' -fuzz FuzzJSONDecode -fuzztime 10m
```

CI (`.github/workflows/ci.yml`) runs lint, the test matrix (Linux, macOS and
Windows on the current and previous Go), a fuzz smoke, cross-compilation for
all release platforms, `govulncheck`, and a Docker image build — all on the
free runners with no secrets beyond `GITHUB_TOKEN`. Coverage profiles are
uploaded as artifacts and summarised on each run's summary page.

### Cutting a release

Releases are tag-driven; nothing else to configure:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

That runs `.github/workflows/release.yml`: goreleaser builds
linux/darwin/windows × amd64/arm64, attaches archives (with LICENSE and
README), `checksums.txt` and a changelog grouped by conventional-commit type to
a GitHub Release, and the image is pushed to `ghcr.io/tamerlank/hearth` tagged
with the version and `latest`. Run `make release-dry` first to see exactly what
a tag would ship. The Homebrew tap and Scoop manifest are scaffolded but
commented out in [.goreleaser.yaml](.goreleaser.yaml) with instructions, since
both need a token that can push to another repository.

Conventions for this repository live in [CLAUDE.md](CLAUDE.md).
