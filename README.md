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

Programs should speak the JSON encoding instead: send `HELLO hearth/1 json` as
the first line and every line in both directions becomes one JSON object. See
[docs/PROTOCOL.md](docs/PROTOCOL.md), which has a working Python client.

## Features

_TBD._

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Protocol

See [docs/PROTOCOL.md](docs/PROTOCOL.md).

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
