# Decisions

Append-only log of choices that are not obvious from the code. Newest last.
Each entry: what was decided, why, and what would make us revisit it.

## 2026-09-09 — Repository skeleton

### D1. Single module, `cmd/` + `internal/` + `pkg/` layout
Server, protocol and TUI live under `internal/` so nothing outside the repo can
depend on them; only `pkg/client` is public API. Revisit if a second binary
appears that needs the server package.

### D2. `go.mod` says `go 1.24` while the toolchain is newer
1.24 is the declared floor in the conventions. The directive is the promise to
users, not the machine that happened to build it. Bump only when a feature from
a newer release is actually used.

### D3. cobra is the only third-party dependency so far
Subcommands, flags, help text and shell completion for one import; hand-rolling
that with `flag` costs more code than it saves. The allowed-dependency list is
closed: cobra, bubbletea, bubbles, lipgloss, prometheus client_golang.

### D4. golangci-lint v2 config; `gosimple` deliberately absent
golangci-lint v2 merged `gosimple` and `stylecheck` into `staticcheck`; listing
it is a config error. Every other requested linter is enabled.

### D5. `make lint` skips with a notice when golangci-lint is missing
`make check` must work on a fresh clone with only Go installed. CI is the place
to make the linter mandatory; add that when the workflow exists.

### D6. Version, commit and date injected with `-ldflags` into `internal/cli`
Keeps `cmd/hearth/main.go` free of logic and lets any command read build info.

## 2026-09-09 — Core chat server

### D7. One hub goroutine owns the client set; no mutex on chat state
Join, leave, broadcast and `/who` are messages to that goroutine; anything that
needs an answer carries its own reply channel. Only one goroutine can see the
map, so there is nothing to lock and nothing to deadlock. See
`docs/ARCHITECTURE.md` for the full argument.

### D8. Name uniqueness is decided inside the register step
The registration message carries a reply channel and the hub checks the name
and adds the client atomically. A separate "is this taken?" query followed by a
register would let two clients both win the same name.

### D9. `MaxClients` enforced with an atomic counter on the accept path
Rejecting before the name prompt needs no hub round trip and avoids counting
races between two simultaneous accepts. Handshaking connections count toward
the cap because they hold a socket. Revisit if per-client slots need to be
reserved for something smarter than a total.

### D10. One small mutex per client, guarding `closed` and `dropped`
The client's own read goroutine also writes to its `send` channel (command
replies, `* bye`), so the hub is not the sole writer and a plain `close` could
race a send. `trySend` and `closeSend` share the mutex; the hub's map stays
lock-free. Alternatives considered: routing self-messages through the hub
(extra channel and round trip per reply) or writing them straight to the
socket (interleaves with the write loop).

### D11. Fan-out never blocks; slow clients get drops, not disconnects
`trySend` is a `select` with `default`. A client whose 32-line buffer is full
loses messages and the drop count is logged on leave. The 5s write deadline
eventually disconnects a client that is truly stuck. Revisit if lossy delivery
turns out to matter more than hub liveness.

### D12. Shutdown interrupts reads with `SetReadDeadline(now)`, not `Close`
The write loop is the single owner of `conn.Close()`. Interrupting the read
with a deadline lets `writeLoop` flush `* server shutting down` before the
socket goes away and keeps `Close` from being called twice. This also unblocks
connections still in the handshake, which the hub does not know about.

### D13. `Serve` returns `nil` on context cancellation
Cancellation is the normal way to stop; only a listener failure is an error.
Mirrors how callers already treat `ctx.Err()` at the top level.

### D14. Names are printable *and* contain no whitespace
The spec said "printable"; `unicode.IsPrint` accepts a space, which would make
`[15:04] al ice: hi` ambiguous. Whitespace is rejected as well.

### D15. Control characters are stripped from chat lines
A client could otherwise inject terminal escape sequences into every other
terminal. Non-printable runes are removed with `unicode.IsPrint`; Unicode text
passes through untouched.

### D16. Slow-client test is a round-trip loop, plus a unit test for drops
Filling loopback kernel buffers to make a writer genuinely block is slow and
flaky. The TCP test does 100 send/receive rounds through a hub that also has a
never-reading client, which fails deterministically if the hub ever blocks. The
drop-counting path is covered directly by a `trySend` unit test.

### D17. `noctx`-friendly networking everywhere
`net.ListenConfig.Listen(ctx, ...)` and `net.Dialer.DialContext` instead of the
bare helpers, in tests too, so the linter stays quiet and every blocking call
takes a context as the conventions require.

## 2026-09-09 — No comments in code

### D18. Go source carries no comments at all
Requested by the project owner. Names, small functions and the docs directory
carry the meaning instead; design rationale lives in `docs/ARCHITECTURE.md`
and here. Consequences: the `revive` `exported` and `package-comments` rules
are off, empty packages keep a bare `package x` file, and the CLAUDE.md
documentation rule was rewritten to match.

### D19. `DECISIONS.md` lives at the repository root
Next to `CLAUDE.md` so it is found on first open rather than buried in `docs/`.
