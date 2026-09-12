# Changelog

All notable changes to hearth. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[conventional commits](https://www.conventionalcommits.org/), which is what
the release workflow groups a GitHub Release's notes by.

## [Unreleased]

### Added

- **TUI**: tagging. `@name` tags one person and `@all` or `@here` tags everyone
  in the room, each of whom gets the highlighted line, the `@` badge and the
  bell; `Tab` after an `@` completes a name or a room tag.
- **TUI**: an empty room explains itself — who you are, where you are, and the
  keys and commands worth knowing — instead of showing a blank pane.

### Changed

- **TUI**: the help overlay (`F1`) lays keys and commands out in two columns
  and fits any terminal down to the 24x6 minimum; it used to overflow and lose
  its border on a 30-row screen. The status bar advertises `F1: help`, which
  works everywhere, rather than `?`, which the input pane types as a character,
  and it names the focused pane and the way back to typing only when you are
  not typing. `Esc` returns to the input from any pane.

### Added

- **Security**: `serve --tls-cert`/`--tls-key` serve TLS on the chat listener,
  and `serve --token` requires a shared secret from every client, compared in
  constant time. JSON clients send it as `HELLO hearth/1 json token=…`; text
  clients are prompted for it before the name. `connect`, `send`, `tail`,
  `who` and `rooms` gain `--tls`, `--tls-ca`, `--tls-insecure` and `--token`;
  `pkg/client` gains `Options.TLS` and `Options.Token`.
- **Presence**: `/away [reason]` marks you away and clears when you speak. The
  TUI dims away users in the sidebar and shows the reason; a `who` reply is
  followed by one `away` event per away member.
- **TUI**: `Ctrl+F` searches the current transcript, walks the matches with
  Enter and the arrows, and scrolls the current one into view.
- **TUI**: the transcript is split by day with a Today / Yesterday / full-date
  separator.
- **TUI**: `connect --log-file` appends every line you see to a file.
- **CLI**: `hearth tail` follows a room on stdout, as lines or `--json`.
- **TUI**: private conversations are `@name` tabs with their own transcript
  and badge; a bare line in one is a private message, `/close` removes it,
  and Enter on a user in the sidebar opens one.
- **TUI**: Tab completes `/commands`, room names for `/join`, `/who` and
  `/history`, and user names anywhere; repeated Tab cycles the matches.
- **TUI**: mentions. A message naming you as a whole word, or any private
  message, is highlighted, turns the badge to `@`, and rings the terminal
  bell (`connect --bell=false`).
- **TUI**: the status bar shows the focused pane, unread and mention totals
  across other tabs, and the last error; the focused pane is coloured.
- **CLI**: `hearth send` posts a message (or each stdin line) to a room or
  `--to` a user and exits 0 once the server has echoed it back.
- **CLI**: `hearth who` and `hearth rooms` print who is online or the room
  list, as lines or `--json`, without opening the UI.
- **CLI**: `connect`, `send`, `who` and `rooms` remember the last server and
  name in `hearth/config.json` under the user config dir (`--config`,
  `HEARTH_CONFIG`) and use them when left out.
- **Server**: `serve --motd` sends a message of the day as system events after
  the history replay.
- `pkg/client`: `Name` and `Room` accessors.
- `pkg/client`: `Options.KeepAlive` pings the server when the client has been
  quiet (30 s by default, negative disables) and consumes the pong, so the
  server's idle timeout never ends a quiet session. Every client command has
  `--keepalive` (`HEARTH_KEEPALIVE`).

### Changed

- `serve --log-format text` is a compact human format (short clock, padded
  level, aligned attributes, colour on a terminal unless `NO_COLOR`) instead
  of slog's default `time=… level=… msg="…"` line. `json` is unchanged.

### Fixed

- **Server**: the history replay, the message of the day and every reply to a
  client's own command went through the same 32-event outbox as room traffic
  and were dropped when it was full, so at the default settings a client
  joining a room with more than 31 messages of history lost the newest ones
  and never saw the MOTD, on every join, `/join` and `/history`. The outbox is
  now one ordered queue with two admission rules: room traffic still drops at
  32 queued, replies are never dropped, and the server pauses reading a
  client whose backlog is at the limit.
- **CLI**: `hearth send` reading standard input exited with `context deadline
  exceeded` after `--timeout` (10 s) whatever was left to send, which broke
  the documented `journalctl -f | … | hearth send` pipeline. The timeout now
  bounds the connection and each confirmation, not the stream.
- **CLI**: `hearth tail` exited with status 0 when the server's idle timeout
  (5 minutes by default) closed a quiet connection. It now keeps the
  connection alive, reconnects with backoff, prints only the room it follows
  and what is addressed to it, and no longer replays history, so a restarted
  bridge does not re-emit old messages.
- The repository had no `.gitattributes`, so a Windows checkout with Git's
  default `core.autocrlf=true` failed `make check`: `gofmt` flagged every Go
  file and the TUI golden test compared CRLF against LF. Text files now check
  out as LF everywhere.
- **TUI**: `/who` and `/rooms` typed in the UI showed nothing; the reply is
  now rendered in the transcript. The sidebar re-asks the server every 10 s,
  so member counts no longer go stale until your next join.
- `connect --plain` returned while its output goroutine could still be
  writing; it now drains before returning.

## [0.1.0] - 2026-09-11

First release.

### Added

- **Server** (`hearth serve`): one hub goroutine owning rooms, names and
  history; per-connection reader and writer goroutines; graceful shutdown that
  tells every client and drains before the socket closes.
- **Protocol `hearth/1`**: line-delimited, text or JSON per connection,
  negotiated by a single `HELLO hearth/1 json` first line so telnet keeps
  working. Commands `say`, `msg`, `join`, `nick`, `who`, `rooms`, `history`,
  `ping`, `quit`, `help`; `/help` and usage errors generated from one table.
- **Rooms, history, private messages, renaming**: rooms are created on first
  join and collected when empty; the last N messages per room are replayed on
  join; `/msg` reaches only the two parties.
- **Hardening**: per-client token-bucket rate limiting checked before decoding,
  per-address connection cap, 4096-byte line and 1024-rune message caps,
  handshake and idle timeouts, drop-on-full outboxes with disconnect after N
  consecutive drops, and panic recovery per connection.
- **Observability**: `log/slog` with stable keys, a Prometheus endpoint with
  `hearth_*` collectors, `/healthz`, and `/debug/pprof/` on the metrics port.
- **Client library** `pkg/client`: JSON client with request correlation for
  `Join`/`Nick`/`Who`/`Rooms`, a bounded event channel that drops oldest, and
  optional reconnect with jittered exponential backoff.
- **Terminal UI** (`hearth connect`): bubbletea Elm-style model with rooms and
  users sidebar, per-room transcripts, unread badges, command history, help
  overlay and automatic reconnect; `--plain` for pipes and scripts.
- **CLI**: cobra commands `serve`, `connect`, `version`, `completion`; every
  flag readable from `HEARTH_<FLAG>`.
- **Benchmarks**: `make bench` micro-benchmarks for fan-out, codecs and the
  ring buffer; `cmd/hearth-load` load generator reporting throughput, latency
  percentiles, drops and server CPU/RSS; results in `docs/BENCHMARKS.md`.
- **Packaging**: distroless multi-arch Docker image, compose stack with
  Prometheus, goreleaser archives for six platforms, GHCR publishing on tag.
- **CI**: lint, race tests on Linux/macOS/Windows across two Go versions, fuzz
  smoke, cross-compile, `govulncheck`, Docker build.
- **Docs**: README with a recorded demo, `docs/ARCHITECTURE.md`,
  `docs/PROTOCOL.md`, `docs/CLIENT.md`, `docs/BENCHMARKS.md`, `DECISIONS.md`,
  `CONTRIBUTING.md`, `SECURITY.md`.

### Fixed

- `pkg/client`: cancelling the context passed to `Send` (and `Say`, `PrivMsg`)
  set a deadline on the whole socket and killed the reader, tearing down a
  healthy connection. The interrupt is now write-only and cleared afterwards.

### Changed

- `internal/server`: the hub keeps a name directory, so registration, `/nick`
  and `/msg` resolve a name in one map read instead of walking every member of
  every room. A private message costs the same with 5000 people online as with
  100 (`BenchmarkPrivateMessage`: 23.8 µs → 157 ns); the 5000-client connect
  storm went from 378 ms to 282 ms.
- `pkg/client` and `pkg/protocol` carry doc comments and lint enforces them;
  the no-comments rule now applies to `internal/` and `cmd/` only.
- The style guide moved from `CLAUDE.md` to `docs/STYLE.md`; `CLAUDE.md` is a
  pointer to it.
- `hearth version` falls back to the module version and VCS revision from the
  binary's build info, so `go install ...@latest` builds report something
  better than `dev`.

[Unreleased]: https://github.com/TamerlanK/hearth/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/TamerlanK/hearth/releases/tag/v0.1.0
