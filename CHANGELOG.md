# Changelog

All notable changes to hearth. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[conventional commits](https://www.conventionalcommits.org/), which is what
the release workflow groups a GitHub Release's notes by.

No tag has been cut yet: everything below is unreleased and will ship as
`v0.1.0`. From that tag on, this file is regenerated per release from the
commits between tags.

## [Unreleased]

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

- `hearth version` falls back to the module version and VCS revision from the
  binary's build info, so `go install ...@latest` builds report something
  better than `dev`.

[Unreleased]: https://github.com/TamerlanK/hearth/commits/main
