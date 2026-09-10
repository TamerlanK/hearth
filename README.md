# hearth

Hearth is a TCP chat server and terminal client shipped as a single binary. It
speaks a small line-oriented protocol, keeps room state in one place, and gives
you a terminal UI for joining a room without installing anything else.

**Status: in development.** The server works; the terminal client is next.

## Quickstart

```sh
make run-server              # listens on :4000
telnet localhost 4000        # in two other terminals; nc also works
```

Enter a name when prompted, then type. `/who` lists users, `/join <room>` moves
rooms, `/msg <name> <text>` is private, `/quit` leaves, `/help` lists commands.
Flags: `hearth serve --addr :4000 --max-clients 100 --idle-timeout 5m`.

From Go, import `pkg/client` and chat in a dozen lines; see
[docs/CLIENT.md](docs/CLIENT.md).

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

On start the server logs one banner with the resolved configuration. The
configuration is rendered through `Config.LogValue`, an explicit allowlist of
fields, so a value that is not named there can never reach the log — that is
where future secrets (TLS key paths, tokens) stay out.

### Flags

| Flag | Default | What it does |
|------|---------|--------------|
| `--addr` | `:4000` | Chat listener address |
| `--metrics-addr` | *(empty)* | Serve `/metrics` and `/healthz` here; empty turns it off |
| `--log-format` | `text` | `text` or `json` |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `--max-clients` | `100` | Concurrent connections (0 = unlimited) |
| `--max-clients-per-ip` | `10` | Concurrent connections from one address (0 = unlimited) |
| `--idle-timeout` | `5m` | Disconnect clients silent this long (0 = never) |
| `--history-size` | `50` | Messages replayed when joining a room |
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
make check   # fmt, vet, lint, test -race
make fuzz    # fuzz both protocol decoders, 10s each
make build   # -> bin/hearth
```

`make lint` needs `golangci-lint` on your PATH and is skipped with a notice when
it is missing:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
go install golang.org/x/tools/cmd/goimports@latest
```

Conventions for this repository live in [CLAUDE.md](CLAUDE.md).
