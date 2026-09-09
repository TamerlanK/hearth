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

### D25. Rooms are a registry the hub owns; history dies with the room
Superseded the earlier "rooms are just a client field" design. The hub holds
`map[string]*room` and each room holds its own members and history, because
`MaxRooms`, member counts in `/rooms` and per-room history capacity all need a
room to be a thing that exists rather than a value derived from the client set.
A room is created on first `join`, deleted when empty, and the default room is
exempt so there is always somewhere to land. Still no persistence: a room's
history goes when the room does. Revisit when history has to outlive a room or a
restart, which means real storage.

### D26. `gocritic` size thresholds raised to 160 bytes
`Event` is 136 bytes, so `hugeParam`/`rangeValCopy` fired on the `Encode(w,
Event)` signature. The interface takes an event by value on purpose — events are
immutable snapshots fanned out to many clients, and a pointer would invite one
client's codec to mutate what another is about to render. The lint threshold
moved instead of the design, and the checks still fire above 160 bytes.

### D27. The hub owns `client.name`, and `handshake` returns it
`/nick` mutates a name that the hub broadcasts and that other clients look up by
(`/msg`), so the name has one owner: the hub goroutine writes and reads it, and
nothing else touches it. `hub.join` takes the candidate name as an argument
instead of the connection goroutine writing `c.name` first, so uniqueness and
assignment happen in one hub step. `handshake` returns the accepted name purely
so the connection can label its logger; that copy is a join-time snapshot and is
allowed to go stale after a rename. Because every event already carries `From`,
no client goroutine ever needs its own current name. The alternative — a cached
copy per connection — needs invalidating exactly when `/nick` succeeds, which
only the hub knows, so it buys a race and saves nothing.

### D28. History is a generic ring, not a slice that gets resliced
`internal/ring.Ring[T]` replaces `append` + trim. The old version reallocated on
every message once a room was busy and made "the last N" an invariant three
callers had to remember. A ring makes the bound structural, `Snapshot` returns
oldest → newest (replay order) as a copy so the hub can relabel `Kind` safely,
and it is small enough to test on its own. `New` panics below capacity 1 rather
than silently accepting a ring that can hold nothing; `Config.HistorySize` is
normalised to 50 when unset, so the panic is only reachable from a programming
error.

### D29. History replays as N flat events, not one nested envelope
A `history` event is one past message, so a replay is *n* lines. The alternative
— one event carrying an array of messages — would need a second shape for
`Event` and a second rendering path in both codecs, and text clients would have
to unpack it to print anything. Flat replay lets a client render a replayed
message with exactly the code that renders a live one. The cost is bounded by
`HistorySize`, which is also what bounds the memory.

### D30. `/help` and usage errors are generated from the codec's command table
The vocabulary already lived in one table in `internal/protocol` (D24), so the
usage strings moved there too and `HelpText()` joins them. `/help`, `usage:`
errors and the set of names `Decode` accepts now cannot disagree, because adding
a row to `Commands` is the only way to add a command. Previously `/help` was a
hand-written const in `internal/server` that had to be edited in lockstep.

### D31. `/rooms` carries member counts inside the name strings
Each entry of the `rooms` event's `names` array is `"#general (2)"` rather than
adding a parallel `counts` array to `Event`. `rooms` is a display list; a client
that wants structured membership asks `/who`. That keeps `Event` one shape for
every kind, which is what makes the codecs small.

### D32. Room names are `[a-z0-9-]`, names stay printable-Unicode
Rooms are identifiers that appear in commands, so they get the narrow ASCII set:
no case-folding question, no confusables, no normalisation. Display names keep
the looser "printable, no whitespace" rule because they are for humans to read,
not to type as arguments.

### D33. Limits are enforced at the edge; the hub gets none of them
Rate limiting, the message length cap, the per-IP cap, the handshake deadline
and the drop-based disconnect all live in the connection's own goroutine or on
the accept path. The hub keeps no timers, no deadlines and no per-client
counters, so it stays a serial loop over channels. The one thing the edge cannot
decide is uniqueness (names, rooms), which is exactly what still goes to the
hub. Consequence: a limit can only see one connection at a time, which is why
the per-IP cap needs `Server.mu` rather than being free like the others.

### D34. The rate limiter runs before decoding, not after
A flood of malformed lines is as expensive as a flood of valid ones, so the
token is spent on the line, not on the command. It also means the limiter cannot
be bypassed by sending garbage. Consequence: `hearth_rate_limited_total` counts
lines, not messages, and it climbs into the millions under `yes | nc` while
`hearth_messages_total` climbs at the configured rate — the gap between them is
the signal.

### D35. Rate-limit warnings are edge-triggered, re-armed only when the bucket refills
Telling a client "rate limited" once per rejected line turns a flood into an
equal flood of error events, which then jams that client's own outbox and gets
it disconnected for being slow — punishing the wrong failure. The client is told
once when it crosses the limit, and the warning re-arms only when its bucket is
back to full burst, meaning it actually stopped. `Bucket.Full` exists for this
and nothing else.

### D36. `Config`'s zero value has no limits; the flags carry the defaults
`MaxClients: 0` already meant unlimited, so every new cap follows it:
`MaxClientsPerIP`, `MaxRooms`, `MessagesPerSecond`, `Burst` and
`MaxDropsInARow` are all "0 = off". The 5/s, burst 10, 10-per-IP and 100-drop
defaults live on the `serve` flags, where `--max-clients 100` already lived.
Embedding `server.New` therefore opts into each limit deliberately, and tests
enable exactly the limit they exercise instead of fighting a default. The risk
is an embedder shipping an unlimited server by accident; the README says so
explicitly.

### D37. Panics are recovered per connection, at three named points
`recover` sits in the handshake, the read loop and the write loop rather than
once around the whole connection, because the read loop's recover has to return
*normally* so the existing unwind still runs — unregister from the hub,
broadcast `leave`, close the outbox, close the socket. A single outer recover
would skip all of that and leak the client into the hub's room map forever. An
outer recover in `handleConn` still exists as a backstop; it runs after the
deferred admission release, so a panic cannot leak a connection slot.

### D38. `Config.LogValue` is an allowlist, so the banner cannot leak
The startup banner logs the resolved config through `slog.LogValuer` rather than
`%+v`. Only fields named in `LogValue` are ever rendered, so adding a
`TLSKeyPath` or a token to `Config` later does not silently start printing it —
the default for a new field is to be invisible. The server exposes
`Server.Config()` so the banner shows values after normalisation, not the ones
the operator typed.

### D39. Metrics register on the default Prometheus registry
Package-level collectors in `internal/server` via `promauto`, served with
`promhttp.Handler()`. A per-`Server` registry would need plumbing through every
call site to make multiple servers in one process countable separately, and
nothing wants that: the binary runs one server. Consequence: two `server.New`
calls in one process share counters, which is visible in tests and harmless
there.

### D40. `client_golang` pinned to v1.23.2 to stay on Go 1.24
v1.24.1 declares `go 1.25.0`, which would have forced this module's `go`
directive up and broken the "Go 1.24+" promise in `CLAUDE.md`. v1.23.2 declares
`go 1.23.0` and has the same API surface for what is used here. `golang.org/x/`
modules were pinned back for the same reason. Revisit when the project moves to
1.25 deliberately.

### D41. Tab survives control-character stripping
`printable` strips everything `unicode.IsPrint` rejects, which includes tab. A
tab cannot break the one-event-one-line invariant the way `\n` or `\r` can, and
stripping it mangles pasted code, so it is passed through explicitly. Everything
else non-printable — including the escape byte that starts a terminal control
sequence — still goes.
