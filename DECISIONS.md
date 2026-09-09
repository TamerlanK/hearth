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

## 2026-09-09 — Wire protocol

### D20. One `Event` type for both encodings, codecs own all formatting
`internal/protocol` defines `Event`/`Command` and two codecs behind
`Encoder`/`Decoder`. The hub builds `protocol.Event` values and never formats a
string; each client holds the codec it negotiated, so the same broadcast reaches
a telnet user as `[15:04] alice: hi` and a JSON user as one object. Adding a
third encoding is a new codec and nothing else. Revisit if an encoding needs
semantics the shared `Event` cannot express.

### D21. Negotiation is one magic first line, not a handshake
The first line a client sends is compared against `HELLO hearth/1 json`;
anything else is its name in text mode. That keeps telnet working with no
sniffing heuristics and no version matrix. The cost is that the server must send
its greeting before it knows the encoding, so a JSON client reads exactly one
text line first — documented in `docs/PROTOCOL.md`. Revisit if a second
negotiable option appears, which would want a real capability exchange.

### D22. The name is supplied by `nick`, in both encodings
Text clients answer the prompt with a bare line, which decodes as `say`; JSON
clients send `{"cmd":"nick","args":["alice"]}`. The naming state accepts either,
so there is one naming path instead of one per encoding.

### D23. `seq` is per connection and assigned at write time
`writeEvent` stamps it, so numbers are dense and monotonic in delivery order on
that connection, and events dropped for a slow client never consume one. A
global sequence would leak fan-out order and make gaps normal. Consequence:
`seq` detects reordering, not loss — clients cannot use it to request a resend.

### D24. Unknown commands are rejected by the codec, not the server
The command vocabulary lives in one table in `internal/protocol`, so both
codecs accept exactly the same set. `Decode` returns `ErrUnknownCommand` with
the parsed name still in `Command.Name` so the server can name it in the error
event. Consequence: the telnet error lost its slash — `! unknown command dance
(try /help)` — because the name is now encoding-independent.

### D25. Rooms are a client field; history is 50 messages and dies with the room
No room registry, no persistence: `broadcast` filters the client set by room,
`/rooms` derives the list from it, and a room's history is discarded when its
last member leaves. That is the smallest thing that makes rooms work and keeps
memory bounded without a store. Revisit when history has to outlive a room or a
restart, which means real storage.

### D26. `gocritic` size thresholds raised to 160 bytes
`Event` is 136 bytes, so `hugeParam`/`rangeValCopy` fired on the `Encode(w,
Event)` signature. The interface takes an event by value on purpose — events are
immutable snapshots fanned out to many clients, and a pointer would invite one
client's codec to mutate what another is about to render. The lint threshold
moved instead of the design, and the checks still fire above 160 bytes.
