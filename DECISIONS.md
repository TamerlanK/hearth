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
`pkg/protocol` defines `Event`/`Command` and two codecs behind
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
The command vocabulary lives in one table in `pkg/protocol`, so both
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
The vocabulary already lived in one table in `pkg/protocol` (D24), so the
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

## 2026-09-10 — Protocol goes public

### D42. The whole protocol package moved to `pkg/protocol`
A public client has to name `Event` in its API, and Go forbids importers
outside the module from touching `internal/`. Splitting the package (types
public, codecs internal) would have put `Event` and the code that renders it in
two places for no gain: the codecs, the command table and `HelpText` are all
things a client legitimately wants. So the directory moved as one and the
server imports it from its new path. The two client-side halves the server
never needed, `EncodeCommand` and `DecodeEvent`, were added there rather than in
the client so the wire format stays in one package.

## 2026-09-10 — Client library

### D43. `Say` and `PrivMsg` are unacknowledged; only four calls wait
The server answers `join`, `nick`, `who` and `rooms` with a distinctive event,
so those calls install a matcher and block for it. `say` has no reply of its
own: the echo is a broadcast that anyone can match, and a rate-limited `say` is
dropped with at most one warning per burst, so waiting for the echo would hang
on exactly the failure it was meant to catch. Those two return once the line is
written and their errors arrive on `Events()`. One request is in flight at a
time, and any `error` event while it is pending fails it: the protocol has no
correlation ids, and this is the honest amount of certainty. Documented in
`docs/CLIENT.md`. Revisit if the protocol grows a request id.

### D44. `ErrNotConnected` while reconnecting, not blocking
A write during reconnect could block until the socket is back. It returns
`ErrNotConnected` instead, because a caller who wants to wait can watch for the
`reconnected` event, while a caller who cannot afford to block has no way to
opt out of a blocking write. One more sentinel, no hidden queue.

### D45. `Events()` drops the oldest event, from the reader goroutine
The reader never blocks on the consumer. When the 256-slot buffer is full it
receives one item itself before sending, and counts it in `Stats().Dropped`.
Only the reader ever sends on the channel, so this receive-then-send cannot
lose the new event to a concurrent producer, and the reader is also the only
closer, so `push` after `close` cannot happen.

### D46. The client's tests import `internal/server`
CLAUDE.md says `pkg/client` must not import `internal/server`; the library
does not. The external test package `client_test` does, because testing a
client against the real server is worth more than a fake that agrees with the
client by construction. Nothing outside the module can copy that import, so
the public API is unaffected.

### D47. No doc comments in `pkg/client`, per the repository rule
The convention in CLAUDE.md forbids comments in Go code, which means `go doc`
shows signatures only. The prose lives in `docs/CLIENT.md` and the two
`Example` functions in `example_test.go`, which pkg.go.dev renders as runnable
examples. Revisit if the package is published on its own and the rule is
relaxed for exported identifiers.

## 2026-09-10 — Command-line interface

### D48. Environment variables are bound by hand, not with viper
`applyEnv` in `internal/cli/cli.go` walks the invoked command's flags in a
`PersistentPreRunE` and sets any flag the user did not pass from
`HEARTH_<FLAG>`. Because it runs after parsing, flags always win, and because it
calls `FlagSet.Set` the value goes through the flag's own parser, so a bad
`HEARTH_RATE` fails the same way a bad `--rate` does. The mapping is stamped
into every flag's usage string at construction. Thirty lines instead of a
dependency the allowed list does not include. Revisit if config files are ever
wanted, which is the point where viper starts paying for itself.

### D49. `hearth connect` without `--plain` runs the plain client for now
The TUI is Phase 7. Refusing to run until then would leave the default
invocation broken for anyone who installs the binary, so the command prints one
notice to stderr and falls through to the line client. Remove the fallback when
the TUI lands; `--plain` stays as the scripting path.
**Superseded by D58:** the TUI landed and the fallback is gone.

### D50. The plain client's stdin reader is not cancelled on Ctrl+C
Reading `os.Stdin` cannot be interrupted portably, so the goroutine draining
stdin is the one exception to the no-goroutine-outlives-its-context rule. On
Ctrl+C the command returns and the process exits, which is the only exit path
that goroutine needs. In tests stdin is a pipe that the test closes, so nothing
leaks there.

### D51. Serve's 5s shutdown deadline is enforced in the CLI, not the server
`server.Serve` keeps its contract of returning only when every goroutine has
ended. The `serve` command waits on it with a 5s timer, logs how many clients
were still connected, and exits 1 with `shutdown timed out` if the timer wins.
Putting the deadline in the CLI keeps the library honest about what it did and
leaves the policy where the operator can see it.

## 2026-09-10 — Terminal UI

### D52. The bubbletea model is a `*Model`, not a value
The Elm architecture reads best with a value model, but `Model` embeds a
`viewport.Model` and a `textinput.Model`, each of which embeds a dozen
`lipgloss.Style` structs; the whole thing weighs about 17KB and golangci-lint's
`gocritic hugeParam` refuses to let it be copied on every message. `*Model`
satisfies `tea.Model` just as well and the discipline is unchanged: `Update` is
still the only method that mutates, `View` is still a pure read, and no
goroutine holds the pointer — commands capture the `*client.Client` and the
event channel, never the model.

### D53. Connection state is polled once a second, not pushed
`pkg/client` has no channel for state changes, only a `reconnected` system
event on the way back up. Rather than add one, `poll` is a `tea.Cmd` on a 1s
`tea.Tick` that reads `State()` and `Stats()` and returns a `linkMsg`. Two
atomic loads a second costs nothing, the status bar is never more than a second
stale, and the client keeps its channel-free public surface. `Stats.Attempt`
was added for the "(attempt N)" part of the banner: the existing `Reconnects`
counts successes, which is not what a banner wants to show.

### D54. Sends are refused locally while the link is down
`Update` checks `m.link` before returning a send command and writes an inline
`error` event into the transcript instead. `Client.Send` would return
`ErrNotConnected` anyway, but only after the command has been queued and the
result has come back as a message; refusing up front means the failure appears
under the line the user just typed, in the same place a server error would.

### D55. Unread badges are written generically even though the server allows one room
A client is in exactly one room, so events for another room only arrive in the
window where a `/join` is in flight. The badge logic still increments
`unread` for any `msg`/`privmsg` whose room is not the one being viewed, which
is correct for that window and stays correct if the server ever fans out to
more than one room. The alternative — special-casing the one room the protocol
allows — would have to be undone the day that changes.

### D56. No golden test through `teatest`
The brief allowed `github.com/charmbracelet/x/exp/teatest` for a golden render
test. It is not on CLAUDE.md's allowed-dependency list, and it buys nothing
here: `View` is a pure function of the model, so
`TestGoldenInitialRender` builds a model at 100x30, calls `View` directly,
strips ANSI and compares to `testdata/initial.golden` (`-update` rewrites it).
`TestChatThroughTheUI` covers what teatest would have added — the real
bubbletea runtime, driven with `WithoutRenderer` and `WithInput(nil)`, sending
real keystrokes to a real server while a second client watches.

### D57. `internal/tui/doc.go` carries a package comment
CLAUDE.md forbids comments in Go code, and the rule says it holds "unless the
user says otherwise in the current session". The brief asked for a `doc.go`
explaining the model/update/view split and the event bridging, so this one file
has prose. Everything else in `internal/tui` is comment-free as usual.

### D58. `hearth connect` refuses a non-terminal stdout
The TUI needs a terminal; piping `hearth connect` used to be a working way to
script against the server. Rather than silently falling back, the command
checks `os.Stdout.Stat()` for `os.ModeCharDevice` and errors with a pointer to
`--plain`, which is the supported scripting path and is what the old default
did. `--plain` also keeps reconnect off: a script wants the pipe to end when
the server goes away, while the UI wants to sit there and retry.

## 2026-09-10 — CI, releases, container image

### D59. No coverage badge
Every zero-maintenance badge option needs an account somewhere: Codecov and
Coveralls are third-party services, and the shields.io "endpoint" trick needs a
JSON file hosted behind a stable URL, which on GitHub means a gist plus a PAT
to update it — GITHUB_TOKEN cannot write gists. The brief said to skip it in
that case, so CI uploads each matrix cell's `coverage.out` as an artifact and
prints the total on the run's summary page instead. Revisit if the project
adopts Codecov.

### D60. Module and build caching comes from setup-go, not actions/cache
`actions/setup-go@v5` caches the module and build caches keyed on `go.sum` by
default. A hand-rolled `actions/cache` block would duplicate that for no gain.

### D61. govulncheck runs via `go run`, not its action
`golang/govulncheck-action` installs its own Go toolchain, which can lag behind
`go.mod`'s requirement and fail the job for reasons unrelated to
vulnerabilities. `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` on the
job's own toolchain is one line and always matches.

### D62. The Dockerfile cross-compiles instead of emulating
The release image is multi-arch (amd64 + arm64). Building the arm64 half under
QEMU means running the Go compiler emulated, which is minutes of wasted CI. The
build stage is pinned to `--platform=$BUILDPLATFORM` and cross-compiles with
`GOOS=$TARGETOS GOARCH=$TARGETARCH`, which Go does natively — QEMU is only
needed to *run* the final layers, not to build the binary. The final image is
`gcr.io/distroless/static:nonroot`, ~12 MB total, uid 65532.

### D63. GHCR publishing uses docker/build-push-action, not goreleaser's dockers
goreleaser can build images, but its classic docker pipe shells out to the
host docker and needs per-arch manifests stitched by hand. The
metadata/login/build-push action trio is the boring, documented path, handles
the multi-arch manifest and the `TamerlanK` → `tamerlank` lowercasing itself,
and keeps the image build identical to the one CI already tests. The cost is
that the image version string is injected by build-args rather than shared with
goreleaser's ldflags — same values, two places, both fed from the tag.

### D64. Prometheus port 9090 on the host belongs to Prometheus
Both hearth's `--metrics-addr` and Prometheus's UI default to 9090. In
docker-compose only Prometheus is published on the host (`9090:9090`); hearth's
metrics stay on the compose network where Prometheus scrapes `hearth:9090`.
`docker compose up` therefore gives the UI at `http://localhost:9090` as the
brief asked, and the chat port 4000 is the only hearth port exposed.

## 2026-09-10 — Benchmarks, demo and final documentation

### D65. `/debug/pprof/` lives on the metrics port
The profile behind `docs/BENCHMARKS.md` had to come from the real server under
real load, not from a benchmark harness. `net/http/pprof` is standard library
and the metrics listener already exists, is optional, and is documented as
operator-only, so the profile handlers register on the same mux. Consequence:
`--metrics-addr` now exposes goroutine dumps and CPU profiles, which
`SECURITY.md` says to keep on a private interface. Revisit if the metrics port
ever becomes something a load balancer scrapes across a network boundary.

### D66. The load tool measures per-client rate, spreads rooms, and paces connects
`cmd/hearth-load` takes `--rate` as messages per second *per client*, because
that is how a chat load actually scales, and `--rooms` because a single room of
N clients turns every message into N deliveries, which at N=5000 is a different
experiment from "5000 users". Connections are paced by `--connect-rate` (200/s)
because every join is broadcast to the whole room and an unpaced storm into a
big room overflows outboxes before the measurement starts; the first version
without pacing disconnected 90% of a thousand clients while connecting.
Latency is measured end to end by embedding `time.Now().UnixNano()` in the
message text and reading it back at every receiver; both processes are on one
clock. Server CPU, RSS and drops come from the metrics endpoint rather than
`/proc` so the tool works against a remote server too. The tool is a separate
`main` under `cmd/` so the release binary never carries it.

### D67. A cancelled `Send` interrupts only the write, and clears its deadline
The load tool found that `Client.write` interrupted a cancelled context with
`SetDeadline(now)`, which also failed the reader goroutine's next read and tore
the connection down. It now checks `ctx.Err()` before touching the socket, uses
`SetWriteDeadline` only, and if the interrupt fired it waits for it to finish
and clears the write deadline before returning. The residual cost is that a
write cancelled mid-line may leave a partial line on the wire, which the server
answers with one `malformed line` error; that is documented in
`docs/CLIENT.md` and is preferable to the alternative of never interrupting a
write at all, which would violate the rule that every blocking call honours
its context.

### D68. The demo is one vhs tape driving tmux, with vhs pinned to v0.10.0
`charmbracelet/vhs` records one terminal, so the two side-by-side clients come
from a tmux split inside that terminal, which also means the tape is a single
file that `make demo` can replay. vhs v0.12.0 cancels its own context before
invoking ffmpeg and silently writes no output (`evaluator.go`: `teardown()`
cancels `ctx`, then `Render(ctx)` runs `exec.CommandContext` on it), so the
documented install is `go install github.com/charmbracelet/vhs@v0.10.0`.
The GIF is committed because the README is the first thing a reader sees and
must not depend on a build step.

### D69. `CHANGELOG.md` starts with a single Unreleased section
No tag exists yet, so there is nothing to generate between tags. The file
groups everything to date under Unreleased with Keep a Changelog headings, and
says that from `v0.1.0` on it is regenerated per release from the conventional
commits between tags, which is the same grouping goreleaser already applies to
the GitHub Release notes.

### D70. `hearth version` reads `debug.ReadBuildInfo` when ldflags are absent
`go install github.com/TamerlanK/hearth/cmd/hearth@latest` cannot pass
`-ldflags`, so those builds reported `dev (commit none, built unknown)`. The
version command now falls back to the module version and the `vcs.revision`
and `vcs.time` settings from the binary's build info, and only when the ldflag
value is still the default, so goreleaser builds are unchanged.

### D71. The style guide is `docs/STYLE.md`; `CLAUDE.md` only points at it
The conventions were written in `CLAUDE.md`, and `CONTRIBUTING.md` sent human
contributors there to read them. That asks a person to take their house style
out of a file addressed to a coding agent, and it couples a document the
project owns to one vendor's filename. The rules now live in `docs/STYLE.md`,
addressed to whoever is making the change; `CLAUDE.md` stays as a short
pointer to it plus the reading order for an agent, which is all an agent
instruction file should be. A second agent's convention file can be added the
same way without the style guide moving again. Revisit if the two audiences
ever need genuinely different rules, which would be a sign the rules are
wrong.

### D72. The hub keeps a name directory
`named` walked every member of every room, so registration, `/nick` and
`/msg` all cost O(clients online) and a connect storm of N clients was
O(N²) in name checks on top of the O(N²) join notices. The hub now holds
`byName map[string]*client`, written in `add`, `remove` and `rename`, the
three steps that change membership, and read everywhere a name is resolved.
It is the same goroutine that owns `rooms`, so no new synchronisation. A
private message went from 23.8 µs to 157 ns with 5000 people online and the
5000-client storm from 378 ms to 282 ms (`docs/BENCHMARKS.md`, *Name
lookup*). The regression test is `TestNameIndexFollowsRenameAndLeave`, which
exercises the two ways the directory could go stale. Revisit only if a second
owner of names appears, which the architecture rules out.

### D73. `pkg/` carries doc comments; the no-comments rule covers `internal/` and `cmd/`
The style guide banned comments everywhere, which left `go doc ./pkg/client`
a bare symbol list under a README badge pointing at pkg.go.dev. For code
nobody imports the rule is right: names and structure carry the meaning and
prose lives in `docs/`. For the published packages it hides the one document
a Go user reads first. `pkg/client` and `pkg/protocol` now have a package
comment and a comment on every exported identifier, kept to what the name
cannot say, and `revive`'s `exported` rule enforces it there while a path
exclusion keeps `internal/` and `cmd/` comment-free. The stuttering check is
off because `client.Client` follows `http.Client`, not a naming accident.

## 2026-09-11 — Making the client a product

### D74. Private conversations are client-side tabs, not a protocol change
`hearth/1` has no notion of a conversation: a `privmsg` is one event with a
`from` and a `to`. The TUI derives a tab from that pair (`@` plus the other
party) and keeps the tab's transcript, unread and mention counts locally;
the server still sees exactly the same `msg` commands. The model therefore
holds two names, `room` for where the server has you and `current` for what
is on screen, and the status bar shows both when they differ. Sending a DM
from a room opens the tab, because the user just addressed that person and
the reply will land there; a room refresh never drops a `@` tab because the
server does not know about it. Revisit if `hearth/2` adds request ids or
conversation ids, at which point the tab could be server-authoritative.

### D75. Tab completes when there is text and switches panes when there is not
Tab already cycled panes and is also the universal completion key. Rather
than move one of them to a chord nobody would find, the key does the
expected thing for the state the input is in: with text under the cursor it
completes, with an empty line it cycles. Shift+Tab always cycles, so pane
switching is never more than one key away. Completion is a pure function
over the line, cursor, rooms and users, so the table test covers the cases
without a terminal.

### D76. A mention is a whole-word, case-insensitive match; the bell is on by default
The mention rule trims punctuation from each whitespace-separated word and
compares it case-insensitively with the user's name, so `@alice!` and
`Alice,` match and `alice2` does not. Any private message counts. The bell
is `\a` written to stdout from a `tea.Cmd`, which is a single byte the
renderer can interleave with safely; it is on by default because a chat
client whose mentions are silent is the one people miss, and
`--bell=false` (`HEARTH_BELL`) turns it off.

### D77. The remembered server lives in a JSON file under the user config dir
`os.UserConfigDir()` gives the platform's answer (`~/.config`,
`~/Library/Application Support`, `%AppData%`) and `encoding/json` is in the
standard library, so a config file costs no dependency and no format
decision. It stores only the last address and name, written atomically
(temp file plus rename, mode 0600) after a successful `connect`, and never by
the scripting commands, which should not change a person's defaults. The
path is a persistent `--config` flag so `HEARTH_CONFIG` works through the
same env binding every other flag has. Precedence is flag, env, remembered,
OS user name.

### D78. The message of the day is ordinary `system` events after the replay
No new event kind: each non-empty line of `--motd` becomes one `system`
event queued right after the history replay, so telnet, JSON clients and
the TUI render it with the code they already have, and `docs/PROTOCOL.md`
now says the replay may be followed by system events. Control characters
are stripped like message text, and a `\r\n` operator on Windows gets the
same result as one on Linux.

### D79. `send`, `who` and `rooms` connect as a real user and leave themselves out
The protocol has no anonymous query, so the scripting commands join like
anyone else, under the remembered name or the OS user name. `who` removes
that name from its answer and `rooms` subtracts one from the room it joined,
so the output describes the server as it was before the command ran. `send`
waits for the server to echo the message back before exiting, because
closing a socket with unread data in it sends a reset that can discard what
was just written; the echo is the only acknowledgement `hearth/1` has, and
it makes exit status 0 mean accepted. The first argument is the address only
if it parses as `host:port`, so `hearth send hello` with a remembered server
does the obvious thing.

## 2026-09-11 — Transport security and presence

### D80. TLS wraps the listener in `internal/cli`, not in `internal/server`
`server.Serve` takes a `net.Listener`, so TLS is `tls.NewListener` around the
one the command already opened and the server package never learns about it.
That keeps the accept loop, the handshake and every test working on plain
`net.Pipe` and `127.0.0.1:0` listeners, and it means a future operator can put
the same binary behind a proxy instead without a code path of its own. The
client side is symmetric: `client.Options.TLS` is a `*tls.Config`, nil for
plaintext, so callers get the whole standard configuration surface with no
wrapper type to learn and no dependency added.

### D81. The token rides the negotiation line, and text clients are prompted
There is one moment in `hearth/1` where a client can say something before it
is trusted, and that is the first line. A JSON client appends ` token=…` to
the `HELLO`, which is additive: an old server treats the whole line as a name
and rejects it, and a server with no token ignores the field. Telnet has no
such line, so a token-requiring server asks for the token *before* the name
prompt and takes the answer as a bare line, which keeps the "anything else is
the name" fallback intact one step later. A wrong token gets one `bad token`
error and the socket closes with no retry: guessing then costs a full
reconnect and is already bounded by `--max-per-ip`. The comparison is
`subtle.ConstantTimeCompare`. The refusal is written *after* the encoding
switch, so a JSON client can parse the error it is refused with.

### D82. `/away` is a broadcast plus a `who` rider, not stored presence
Away is per-connection state on the client struct, owned by the hub like the
name and the room. Setting it broadcasts an `away` event to the room so
everyone watching updates immediately, and saying anything clears it, which
is what every chat client has done since IRC. The problem is a client that
arrives later and missed the broadcast: rather than add a presence query, the
`who` reply is followed by one `away` event per away member, so the periodic
refresh the TUI already runs is also the presence sync. A new event kind is
additive and a client that does not know it can ignore it.

### D83. Search is a filter over the rendered transcript
`Ctrl+F` matches against the lines `transcript()` already produces, not the
events behind them, so what you search is exactly what you see: the rendered
join notices, the `you` label, wrapped continuations. Hits are line indices,
which is what the viewport scrolls by, so jumping to one is a `SetYOffset`
and needs no second coordinate system. The cost is that a match spanning a
wrap boundary is not found; the benefit is that the whole feature is a
`strings.Contains` over a slice the view recomputes anyway, with no index to
invalidate when an event arrives mid-search.

### D84. The day separator is relative for two days and absolute after
`Today` and `Yesterday` are what a reader wants for recent traffic and are
unambiguous; anything older gets the full weekday and date, because "3 days
ago" forces arithmetic the reader should not have to do. The separator is
emitted when consecutive events in the ring differ in calendar day, so
out-of-order timestamps (a history replay after live traffic) produce a
second separator rather than being silently merged, which is honest about
what the transcript actually contains.

## 2026-09-11 — Delivery guarantees the docs already promised

### D85. The outbox is one ordered queue with two admission rules
The outbox was a 32-slot channel that everything went through with a
non-blocking send, and `--history` defaults to 50: a client joining a room
with more than 31 messages of history lost the newest ones and the MOTD, on
every join, and `/history` could never answer in full. Making the channel
bigger only moves the cliff; blocking the hub on a slow client is the one
thing the design forbids; a second channel for replies loses the order between
a join notice and the replay that follows it. So the outbox is now a slice
under `client.mu` that the writer pops from. `trySend` (room traffic) refuses
when 32 events are queued and counts the drop exactly as before; `push` (the
replay, the MOTD, replies to the client's own commands, local errors, the
shutdown notice) always appends. Both happen inside the hub step that produced
them, so order is atomic. Memory is bounded from the other side: `readLoop`
stops reading while the client's own backlog is at 32, so a client that does
not read cannot make the server hold more than one reply's worth beyond the
limit, and a `/history` flood is answered at the rate the client drains it.
The hub still never waits on anything. Revisit if replies ever need to be
dropped too, which would mean the reader-side throttle is not enough.

### D86. `--timeout` on `send` bounds each confirmation, not the stream
`hearth send` reading stdin applied `--timeout` (10 s) to the whole command,
so the documented `journalctl -f | grep ERROR | hearth send` pipeline died
after ten seconds with `context deadline exceeded`. A stream has no natural
length, so the only thing a timeout can sensibly bound is the part that can
hang: the dial and handshake, and the wait for the server to echo each
message. `who` and `rooms` bound their one request the same way; `tail` keeps
the old meaning (`--timeout 0` follows until interrupted, anything else stops
after that long) because for a follower the whole run is the natural unit.

### D87. Keep-alive pings live in `pkg/client`, on by default; `tail` reconnects and starts from now
The server's `--idle-timeout` (5 minutes) disconnects a client that sends
nothing, whatever it receives. The TUI survived only because its 10 s room
poll doubles as a keep-alive; `hearth tail`, a pure reader, exited with status
0 after five quiet minutes, which is the worst possible behaviour for a
bridge under a supervisor. The fix belongs in the library, because every
consumer of `pkg/client` that only reads is exposed: `Options.KeepAlive` sends
a `ping` after that long without a write (30 s when zero, negative disables,
following `net.Dialer.KeepAlive`) and consumes exactly one `pong` per ping it
sent, so a `pong` the caller asked for still arrives. Pongs are fungible, so a
count is exact accounting. `tail` additionally reconnects with backoff, prints
only the room it follows plus roomless events (the `#general` leg of the
handshake and other rooms' notices are not "what happens in a room"), and
skips `history` events entirely: a bridge that re-emitted the last 50 messages
on every restart or reconnect would re-fire every alert, and `tail -f` semantics
are "from now". The load generator sets `KeepAlive: -1` so the benchmark
traffic stays what the benchmark says it is.

### D88. `.gitattributes` pins LF for every text file
The tree had none, so a Windows checkout with Git's default
`core.autocrlf=true` rewrote every text file as CRLF: `gofmt -l` flagged all
45 Go files and the TUI golden test compared a CRLF file against LF output,
and `make check` — the gate every document names — failed on a clean clone.
`.editorconfig` already said `end_of_line = lf` but editors are not what
writes a checkout. `* text=auto eol=lf` makes the working tree match the
object store on every platform; `*.gif binary` keeps the demo out of the
heuristic. Fixing the golden test to tolerate `\r\n` would have hidden the
symptom and left `gofmt` broken.

### D89. The Makefile assumes no POSIX shell
`make build` failed on Windows because mingw make with no `sh.exe` on PATH
runs every recipe through `cmd.exe`, which has no `date(1)` and no
`/dev/null`: `2>/dev/null` aborted the git stamps and cmd's interactive
`date` pasted a prompt into `-ldflags`, so the link failed. The fix is to
stop needing a shell. All three stamps now come from `git`, the one tool that
is the same on every platform (`DATE` is the commit time, which is what Go's
own VCS stamping records as `vcs.time` anyway), with `$(or ...)` for the
fallback instead of `||`. `fmt` and `lint` are single commands: the
`golangci-lint` config already carries the `gofmt` and `goimports`
formatters, and `golangci-lint fmt --diff` exits non-zero on a diff, so six
lines of `if [ -n "$out" ]` and a `command -v` guard were doing what one
binary does. `clean` is the one recipe that cannot be shell-agnostic, so it
asks the shell what it is — `$(shell echo 'x')`, which only a POSIX shell
strips — rather than trusting `$(SHELL)`, which still reads `/bin/sh` when
make is about to fall back to cmd, or `$(OS)`, which says `Windows_NT` inside
Git Bash where `rm` is correct. `$(EXE)` gives the Windows binary the `.exe`
Explorer and PowerShell need to run it at all.

### D90. The TUI explains itself on an empty screen
A first run showed a blank pane, an empty "Rooms" heading, a dangling "Users
in " with no room, and a status bar advertising `?: help` — a key the input
pane swallows as a literal `?`, so the one advertised way out did not work.
The fix is not a tutorial mode but the empty state itself: `transcript` falls
back to a primer (who you are, where you are, the six things worth knowing)
whenever the current room has nothing in it, so the space that was blank now
carries the orientation and disappears the moment a message arrives. It is
rendered, never recorded, so it stays out of `--log-file`, out of the ring
buffer and out of every room's state. The status bar now advertises `F1`,
which works from every pane, and names the pane you are in only when it is
not the input — the moment typing does nothing is exactly when a newcomer
needs to be told why. `?` keeps its old meaning so a line may still start
with one. Esc is the way back to typing from any pane, not just out of help.
The help overlay was the other half: at 30 rows it overflowed, and the
truncation fallback dropped the border and let `lipgloss.Place` centre each
ragged line, so the one screen meant to explain the program was the least
legible thing in it. Keys and commands are now two columns sized from the
terminal — both above 78 columns, keys alone below, with the key column
halving on the way down to `minCols` — and every cell is `fit` to a plain
width before it is styled, so nothing slices an ANSI escape and the box fits
inside the frame at every size a test can ask for.

### D91. `@all` is a client-side rule, not a server broadcast
Tagging extends the mention rule of D76 instead of adding a wire message:
every client already decides for itself whether a line names it, so `@all`
and `@here` need no protocol change, no server state and no per-room
subscriber list, and a new client tags people on an old server. The `@` is
required for the room tags — `@all` is a tag, a bare `all` is a word — while
a personal name keeps matching with or without the sigil, so the rule stays
the one sentence D76 describes. `@here` is an alias for `@all` rather than an
away-aware variant: the client does know who is away, but a second tag whose
only difference is who it skips is a distinction to add when someone asks for
it. Completion offers the tags after an `@` so the feature is discoverable
from the keyboard, which is the only place it is documented besides the help
overlay.
