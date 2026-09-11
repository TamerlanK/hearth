# Architecture

Hearth is one Go module, one binary, and about 4 000 lines of code outside
tests. This document is the map: what the pieces are, which goroutine owns
what, why the shape is what it is, and where it stops.

## Components

| Package | Role | Depends on |
|---------|------|------------|
| `cmd/hearth` | `main`: calls `cli.Execute`, nothing else | `internal/cli` |
| `internal/cli` | cobra commands `serve`, `connect`, `version`; flags, env binding, signal handling, the 5 s shutdown grace | `internal/server`, `internal/tui`, `pkg/client` |
| `internal/server` | accept loop, admission caps, handshake, the hub, per-connection read/write loops, Prometheus collectors and the metrics/pprof mux | `internal/ratelimit`, `internal/ring`, `pkg/protocol` |
| `internal/ratelimit` | token bucket, one per connection, not goroutine-safe by design | — |
| `internal/ring` | generic fixed-capacity ring buffer used for room history | — |
| `internal/tui` | bubbletea terminal UI over `pkg/client` | `pkg/client`, `pkg/protocol` |
| `pkg/client` | public Go client: dial, negotiate JSON, request correlation, bounded event channel, reconnect | `pkg/protocol` |
| `pkg/protocol` | wire format: `Event`, `Command`, the command table, text and JSON codecs | — |
| `cmd/hearth-load` | load generator used for `docs/BENCHMARKS.md`; a separate `main`, never in the release binary | `pkg/client`, `pkg/protocol` |

Nothing outside `internal/` and `pkg/` imports `internal/`; `pkg/client` does
not import the server or the UI. The dependency arrows all point at
`pkg/protocol`, which is where a wire change starts.

## Overview

- The server runs an accept loop that hands each accepted `net.Conn` to a connection handler.
- Exactly one hub goroutine owns all shared state (the room registry, every client's name and room, and per-room history); nothing else touches it.
- Each client gets two goroutines: a reader that parses lines from the socket and a writer that drains an outbound channel.
- All communication between the hub and clients is by channel, so there are no locks on chat state.
- Outbound channels are buffered (32 events). Fan-out is non-blocking: a client that cannot keep up has messages dropped and counted, and the 5s write deadline eventually disconnects one that is truly stuck.
- `pkg/protocol` owns the wire format: `Event`, `Command`, the `Encoder`/`Decoder` interfaces and the two codecs. The server passes `protocol.Event` values around and never formats a string for the wire; rendering happens inside the codec a client owns.
- Each connection negotiates its encoding once (text or JSON lines) and keeps it. `internal/server/session.go` runs the handshake: the first line is inspected for `HELLO`, then every line is a name attempt until the hub accepts one, and the accepted name is returned to the caller.
- Shutdown is context-driven: cancelling the server context stops the accept loop, then the hub, then the connections.
- The terminal client is a thin TUI over `pkg/client`, the public library that handles dialing, negotiation and delivery. `internal/tui` follows the Elm architecture: one `Model` holds every piece of state, `Update` is the only place that mutates it, `View` is a pure function of it, and anything that can block is a `tea.Cmd`. Server events reach the model through a command that blocks on one receive from `Client.Events()` and is re-issued after each event, so no goroutine ever touches the model. `internal/tui/doc.go` has the detail.

## Goroutine model

```
Serve(ctx, ln)
 ├─ hub.run(ctx)                      1 goroutine for the whole server
 ├─ listener closer                   waits on ctx, closes ln
 └─ per accepted connection
      ├─ handleConn / readLoop        owns the socket's read side, the scanner,
      │                               the rate limiter, and the handshake
      ├─ writeLoop                    owns the socket's write side, seq, and Close
      └─ interrupt watcher            waits on ctx or the overload signal and
                                      sets an immediate read deadline
```

Three goroutines per connection plus two for the server, so 5000 clients is
about 15 000 goroutines (`go_goroutines` in the load runs). Each has one exit
path:

- **hub**: `ctx.Done()`.
- **readLoop**: EOF, a read error, `/quit`, the idle deadline, the overload
  signal, or the interrupt watcher's deadline on shutdown.
- **writeLoop**: `send` closed by the hub (on leave or shutdown) or a write
  error.
- **interrupt watcher**: `connDone` closed when `handleConn` returns.

Every hub interaction from the client side is a `select` against the hub's
channel and `ctx.Done()`, so a client can never block on a hub that has
already exited. The hub itself never blocks on a client: fan-out is `trySend`
with a `default:` case.

## Ownership rules

Any piece of shared state has exactly one owner goroutine, or a mutex whose
job is written down here.

| State | Owner | How others reach it |
|-------|-------|---------------------|
| `hub.rooms`, `room.members`, `room.history`, `client.name`, `client.room` | hub goroutine | messages on `register`, `leaving`, `requests`, each carrying a reply channel |
| `client.send` (the outbox) | hub closes it; hub *and* the connection's own read goroutine send on it | `client.mu` serialises `trySend` against `closeSend`; also guards `closed`, `dropped`, `inARow`, `overloaded` |
| `client.enc`, `client.dec`, `client.seq` | connection goroutine during the handshake, then `writeLoop` | the `go` statement is the happens-before edge; nothing else touches them |
| `client.bucket`, `client.limited` | the connection's read goroutine only | nothing else needs them, so the bucket has no lock |
| `client.conn` `Close` | `writeLoop` | readers are interrupted with `SetReadDeadline(now)`, never by closing |
| `Server.active`, `Server.perIP` | `Server.mu` | admission is one locked check-then-increment |
| Prometheus collectors | the library's own atomics | — |
| `pkg/client`: socket reads, reconnect, `events` channel | the client's reader goroutine | `Client.mu` guards `conn`, `name`, `room`, `waiter`; `reqMu` serialises requests |

The rule that produces most of the design is the first row: **the hub owns
the chat state and nobody else can see it.** Everything below follows from
keeping that true.

## Life of a connection

```
accept loop            handleConn goroutine            hub goroutine          writeLoop goroutine
-----------            --------------------            -------------          -------------------
Accept() ──────────▶  active++ ; over MaxClients?
                       └─ yes: write "! server full", close, return
                       write name prompt (direct)
                       readLine ─▶ HELLO? ─▶ switch codec, reply "protocol json"
                              ─▶ decode ─▶ validateName
                       join(c, name) ─────────────▶  name taken? reply err
                       (repeat until nil)  ◀────────  else set c.name/c.room,
                                                     add to the default room,
                                                     reply nil
                       (hub emits Join itself) ────▶  fan-out via trySend
                       (handshake returns the name;
                        the conn goroutine keeps it
                        for logging only)
                       start writeLoop ──────────────────────────────────────▶ range over send
                       readLoop:
                         line ─▶ dec.Decode ─▶ Command
                              ─▶ /quit, /help answered locally
                              ─▶ hub.do(cmd) ──▶ hub applies it, fans out Events,
                                                 returns Events for this client
                                                 ──▶ send chan ──▶ enc.Encode ──▶ conn.Write
                       readLoop returns (EOF, /quit, idle, error)
                       leave(c) ──────────────────▶  drop from its room; close(c.send);
                                                     emit Leave; GC the room if empty
                       wait writerDone                                          ◀── send drained, range ends,
                                                                                    conn.Close()
                       active--
```

Every hub interaction from the client side is a `select` against the hub's channel and `ctx.Done()`, so a client can never block on a hub that has already exited.

## Shutdown ordering

1. `Serve`'s context is cancelled (signal, or listener failure).
2. The listener is closed; `Accept` fails and the loop exits.
3. The hub sees `ctx.Done()`, queues `* server shutting down` to every client with `trySend`, closes each client's `send` channel and returns.
4. Each `writeLoop` drains what is left in `send` (including the notice), ends, and closes its connection.
5. Each `handleConn` had its pending read interrupted with an immediate read deadline, so `readLoop` (or the handshake) returns; `leave` is skipped because the context is done; it waits for `writeLoop` and returns.
6. `Serve` waits on the `WaitGroup` covering the hub, the listener closer and every `handleConn`, then returns `nil`.

The per-connection unwind is always the same sequence, whether triggered by the client or by shutdown:

**read loop ends → unregister → hub closes `send` → write loop drains and ends → conn closed.**

The read is interrupted with `SetReadDeadline(now)` rather than `conn.Close()` so that the writer, not the reader, is the only thing that closes a connection. That keeps `Close` single-owner and lets the shutdown notice reach the client before the socket goes away.

## Rooms and history

The hub holds `rooms map[string]*room` and `byName map[string]*client`. A
`room` is a name, a `members map[*client]struct{}` and a
`*ring.Ring[protocol.Event]` of past messages. Each client carries a `*room`
pointer, so every client is in exactly one room and `broadcast` walks that
room's members directly instead of filtering the whole client set. `byName` is
the directory behind name uniqueness at registration and `nick` and behind
`msg` delivery; it is written in the same hub steps that change membership
(`add`, `remove`, `rename`), so the two views never disagree and a lookup is
one map read however many people are online.

- A room is created on the first `join` that names it and deleted when its last
  member leaves, taking its history with it. The default room is exempt: it is
  created at startup and never deleted, so a client always has somewhere to
  land.
- `Config.MaxRooms` caps how many rooms exist at once. It is checked only when a
  `join` would create one; joining an existing room is never refused.
- History is `internal/ring.Ring[T]`, a fixed-capacity buffer that overwrites
  oldest-first. `Config.HistorySize` sets the capacity (50 by default).
  `Snapshot` returns oldest → newest, which is exactly replay order, and it
  returns a copy so the hub can relabel each event's `Kind` to `history`
  without touching what is stored.
- Only `say` messages are recorded. Joins, leaves, renames and private messages
  are not, so a replay reads as a conversation rather than a log.

Fan-out is O(members of one room) per message and the registry is O(rooms). That
is the right shape until the client count stops fitting in one goroutine's
budget.

## Who owns a client's name

`client.name` and `client.room` are written and read **only by the hub
goroutine**. Nothing else ever touches them, which makes `/nick` race-free by
construction rather than by careful ordering.

The alternative — letting the connection goroutine keep its own copy of the name
and pass it in with each command — needs the copy invalidated the moment `/nick`
succeeds, and the hub is the only one who knows that. So the name moved the
other way:

- `handshake` parses a candidate name and hands it to `hub.join(ctx, c, name)`.
  The hub decides uniqueness and assigns `c.name` in the same step, so two
  clients racing for one name cannot both win.
- `handshake` **returns** the accepted name. The connection goroutine keeps that
  string for its `slog` logger and never reads `c.name` again — the logger's
  name is a snapshot from join time and may go stale after a `/nick`, which is
  fine for a log field and is the only place it is used.
- Every event the hub emits already carries `From`, so a client goroutine never
  needs to know its own current name in order to render or route anything.

The same argument covers `client.room`: `newClient` leaves it nil, the hub sets
it during registration, and only the hub follows the pointer afterwards.

## Failure modes and how they're handled

| Failure | What the server does | Where |
|---------|----------------------|-------|
| **Slow client** (stops reading, keeps the socket open) | Its outbox (32 events) fills; further events are dropped for it alone and counted. After `MaxDropsInARow` consecutive drops the client is marked overloaded, its blocked read is interrupted, and it is disconnected; the write loop makes one best-effort attempt to deliver `! too many dropped messages, disconnecting` before the socket closes. Nobody else waits: fan-out is `trySend` with a `default:` case. | `client.trySend`, `client.writeLoop` |
| **Slow client that also stops draining TCP** | The 5s write deadline fails the write, the write loop returns, and the connection closes. | `client.writeEvent` |
| **Over-long line** (no newline, megabytes of data) | `bufio.Scanner` is capped at 4096 bytes, so memory cannot grow: the scan fails with `ErrLineTooLong`, the client gets one `! line too long, disconnecting`, and the connection closes. The cap is enforced before any decoding. | `client.readLine` |
| **Over-long message** (a legal line whose text is huge) | Rejected after decoding at 1024 runes with `! message is longer than 1024 characters`; the connection stays open. This is separate from the byte cap because a 4096-byte line can hold far fewer runes than a client expects. | `Server.readLoop` |
| **Flood** (`yes \| nc`) | A token bucket per connection, checked **before** decoding, so malformed floods cost the same as valid ones. Over-limit lines are dropped and counted in `hearth_rate_limited_total`; the client is told once per flood, not once per line, and the warning re-arms only when the bucket has refilled to full. Other clients see traffic at `--rate`, nothing more. The read loop still pays to read and discard each line, so a flood costs CPU on one core — bounded by the kernel's read throughput, not by anything the flooder can make the server allocate. | `internal/ratelimit`, `Server.readLoop` |
| **One host opening many connections** | `--max-clients-per-ip` (10 by default) is checked in the same locked step as `--max-clients`; the connection gets `! too many connections from your address` and closes before the greeting. | `Server.admit` |
| **Client that connects and says nothing** | A 10s absolute deadline covers the whole handshake. Once named, the deadline is cleared and `IdleTimeout` takes over per read. | `Server.handshake` |
| **Panic in a connection goroutine** | Recovered at three points — handshake, read loop, write loop — each logging the value and stack at `ERROR` with the connection's `client_id`. The read loop's recover returns normally, so the usual unwind still runs: the hub unregisters the client, the room's `leave` is broadcast, the outbox closes and the socket closes. Only that connection dies; the accept loop, the hub and every other client are untouched. | `recoverPanic`, `Server.handshake` |
| **Panic anywhere else in the connection** | An outer recover in `handleConn` runs after the deferred `release` and gauge decrement, so admission slots and the per-IP count are never leaked by a panic. | `Server.handleConn` |
| **Graceful shutdown** | See *Shutdown ordering* above: the notice is queued to every client, outboxes close, write loops drain and close their sockets, and `Serve` returns only once every goroutine has ended. | `hub.run`, `Server.Serve` |

The pattern is that every limit except name and room uniqueness is enforced by the connection's own goroutine, before the hub is involved. The hub stays a plain serial loop over channels with no timeouts, no deadlines and no rate state, which is what keeps it easy to reason about.

## Why there is no mutex on chat state

The client set is touched by exactly one goroutine, the hub. Joining, leaving, broadcasting and `/who` are all messages to that goroutine; anything that needs an answer sends its own reply channel and waits. That gives three properties for free:

- **No data races by construction.** There is nothing to forget to lock because only one goroutine can see the map.
- **Atomic decisions.** Name uniqueness is decided and the registration applied in the same hub step, so two clients racing for the same name cannot both win. A check-then-register split across two messages would be a race.
- **No lock ordering.** The hub never blocks on a client (fan-out is `trySend` with a `default:` case), and clients never hold anything while waiting on the hub, so there is nothing to deadlock.

Two pieces of state live outside the hub and are documented where they sit:

- `Server.mu` guards `Server.active` (the open-connection count) and `Server.perIP` (connections per remote address). Admission is one locked step — check both caps, then increment — so the accept path never round-trips to the hub, and two simultaneous connections from one host cannot both pass a cap with one slot left.
- `client.mu` guards `closed`, `dropped`, `inARow` and `overloaded`, and serialises `trySend` against `closeSend`. The client's own read goroutine also writes to `send` (command replies), so the hub cannot be the sole writer; the tiny mutex is what makes closing the channel safe. The consecutive-drop counter lives under the same lock because it is updated on exactly the same path.
- `client.bucket` and `client.limited` (the rate limiter and its warn-once flag) are touched only by that connection's read goroutine, which is the only thing that reads lines. `ratelimit.Bucket` is deliberately not goroutine-safe: giving it a lock would be a lock nobody needs.

`client.name` and `client.room` need no lock because only the hub goroutine ever touches them — see *Who owns a client's name* above.

`client.enc`, `client.dec` and `client.seq` are single-writer by construction: the connection goroutine sets the codec during negotiation and writes the handshake events itself, then starts `writeLoop`, which is the only goroutine to touch `seq` afterwards. The `go` statement provides the happens-before edge. That is what makes `seq` monotonic per connection with no counter lock.

## The command line

`internal/cli` holds the cobra commands and nothing else: `serve` builds a
`server.Config` from flags, opens the listener, and runs `server.Serve` under a
signal-cancelled context. On SIGINT or SIGTERM it logs the number of clients
still connected and waits up to 5s for `Serve` to return before giving up with
an error. Every flag also reads `HEARTH_<FLAG>` from the environment when it is
not set on the command line (see `DECISIONS.md` D48).

`connect --plain` is two goroutines over one `client.Client`: one parses stdin
lines with the text codec and hands the commands to `Send`, the other renders
`Events()` with the same codec onto stdout. Whichever ends first (EOF on stdin,
the server closing, or Ctrl+C) makes the command close the client and wait for
the renderer to drain before returning; the stdin reader cannot be interrupted
while blocked on a terminal and is left to exit with the process.

`send`, `who` and `rooms` are the scripting surface. Each dials as a normal
user, does one thing through `pkg/client`, and exits: `send` says the text
(or each stdin line) and waits for the server's echo before reporting success,
`who` and `rooms` print their answer with the querying user removed. All four
client commands share `resolveTarget`, which fills a missing address or name
from `hearth/config.json` under the user config dir (`--config`,
`HEARTH_CONFIG`); only `connect` writes that file (D77, D79).

## The client library

`pkg/client` is the public library the CLI and the TUI use; see
`docs/CLIENT.md` for the user-facing contract. Inside, one reader goroutine
owns the socket: it decodes JSON events, resolves the single pending request
if the event matches (or is an error), and pushes every event onto a 256-slot
channel, dropping the oldest when the consumer is behind. Writes go straight
to the socket under no lock beyond the connection pointer's mutex, since each
`EncodeCommand` is one `Write` of a complete line. Cancellation of a caller's
context is turned into a socket deadline with `context.AfterFunc`, so no call
blocks past its context. The same goroutine that reads also reconnects, so
there is never more than one live socket.

## Design decisions

The long-form log is `DECISIONS.md`; this is the table for the five that
shape everything else, plus one that the benchmarks turned into a decision.

| Decision | Alternatives considered | Why this one | Trade-off |
|----------|-------------------------|--------------|-----------|
| **One hub goroutine** owns rooms, names and history | Shard by room, each shard its own goroutine; or a global `sync.RWMutex` around the maps | Name uniqueness and room membership need one atomic step; one goroutine gives that with no lock ordering to get wrong. The fan-out benchmark puts the hub at 35 ns per recipient, so it delivers a message to 1000 outboxes in 35 µs and never exceeded 4% of CPU in the load runs. | Every command serialises through one goroutine. Sharding would help only after the write path is fixed, and would need a cross-shard name directory. |
| **Channels, not a mutex**, between connections and the hub | Mutex-guarded maps that connection goroutines touch directly | Ownership is structural: the map is reachable from one goroutine, so there is nothing to forget to lock and `-race` has nothing to find. Reply channels make request/response explicit. | Two small mutexes still exist (`client.mu`, `Server.mu`) for state the hub cannot own; each is documented above. Channel hops cost more than an uncontended lock, which is irrelevant at chat rates. |
| **Line-delimited text or JSON**, one JSON object per line | Length-prefixed binary framing (protobuf, msgpack) | `telnet` and `nc` are clients; a human can read a capture; every language has a JSON parser and a line reader. The encode cost is 0.5 µs per event, an order of magnitude under the syscall that follows it. | 11% of server CPU under load is JSON marshalling because each recipient encodes the same event. A binary frame would be smaller and faster but unreadable at the prompt. |
| **Drop on full outbox**, never block the hub | Block until the slow client drains; or unbounded queues; or per-client goroutine that blocks in fan-out | One slow reader must not stall a room. A 32-slot buffer absorbs bursts; beyond it that client alone loses events, is counted, and is disconnected after 100 consecutive drops. Delivery is documented as best-effort. | Chat is lossy under overload. `seq` detects reordering, not loss, so a client cannot ask for a resend. |
| **Negotiation by the first line** (`HELLO hearth/1 json`) | A version handshake with capability lists; sniffing the first byte; separate ports | One comparison, zero state machine, and a server that does not understand it treats it as a name, which is the correct fallback. | The greeting goes out before the encoding is known, so a JSON client reads exactly one text line first. A second negotiable option would want a real handshake. |
| **One `write(2)` per event per recipient** (current) | Batch with `bufio.Writer` flushed when the outbox is empty; encode once per broadcast and share the bytes | Simplest correct thing; at rooms of tens of people it is invisible. | The profile at 500 000 deliveries/s puts 55% of CPU in the syscall and 11% in encoding. This is the documented next change; see `docs/BENCHMARKS.md`. |

## Known limitations

- **No authentication, no TLS, no authorisation.** Names are first come, first
  served; everything is plaintext; any client can join any room. See
  `SECURITY.md`.
- **Nothing is durable.** History lives in memory, dies with its room, and
  does not survive a restart.
- **Delivery is best-effort.** A client that falls behind loses events and
  cannot request them again; there are no request ids in the protocol, so a
  stray `error` event during a `pkg/client` request is attributed to that
  request.
- **One process, one hub.** No clustering; a room cannot span servers, and the
  single hub serialises every command. Measured ceiling on one laptop is
  around 930 000 deliveries/s before drops start, limited by the write path,
  not the hub.
- **Joins are broadcast to the whole room.** Connecting N clients into one
  room costs N²/2 events, and name uniqueness is a linear walk over every
  member. Fine at the default `--max-clients 100`; visible at 5000.
- **A client is in exactly one room.** Following two rooms needs two
  connections.
- **The room list in the TUI is polled, not pushed.** The UI re-asks
  `rooms` and `who` every 10 s and after each of its own joins; a move by
  someone else shows up on the next poll, not instantly.
- **Per-address caps key on the address string**, so a NAT looks like one
  client and an IPv6 host with many addresses looks like many.
- **Message text is capped at 1024 runes and lines at 4096 bytes**; there is
  no multi-line message.

## What I'd do next

In the order the evidence supports, not the order that is most fun:

1. **Batch writes and encode once.** `bufio.Writer` in `writeLoop` flushed
   when `send` is empty, and `[]byte` fan-out from the hub with `seq` patched
   per connection or moved out of the body. The profile says this is two
   thirds of server CPU under load. Expected result: the 5000-client run at
   well under half the CPU, and single-room fan-out past a million
   deliveries/s.
2. **TLS and a shared-secret token.** `crypto/tls` on the listener behind
   `--tls-cert`/`--tls-key`, and a `HELLO hearth/1 json token=...` extension
   that stays within the first-line negotiation. Both fit the "edge enforces
   limits, hub stays plain" rule.
3. **Coalesce joins in large rooms**, so a connect storm emits O(N) events
   instead of N²/2. Names are already indexed (D72); the join notices are
   what is left of the storm cost.
4. **Request ids** in `hearth/2`, so `pkg/client` can correlate replies
   properly and run more than one request at a time.
5. **Persist history** to an append-only file per room, replayed on start,
   so a restart is not amnesia. Still in-process; a database is a different
   project.
6. **Push room-list changes** so the sidebar is live, and let a client sit in
   more than one room.

Tests, docs and a `DECISIONS.md` entry come with each, per `CONTRIBUTING.md`.
