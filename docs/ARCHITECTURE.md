# Architecture

## Overview

- The server runs an accept loop that hands each accepted `net.Conn` to a connection handler.
- Exactly one hub goroutine owns all shared state (the room registry, every client's name and room, and per-room history); nothing else touches it.
- Each client gets two goroutines: a reader that parses lines from the socket and a writer that drains an outbound channel.
- All communication between the hub and clients is by channel, so there are no locks on chat state.
- Outbound channels are buffered (32 events). Fan-out is non-blocking: a client that cannot keep up has messages dropped and counted, and the 5s write deadline eventually disconnects one that is truly stuck.
- `internal/protocol` owns the wire format: `Event`, `Command`, the `Encoder`/`Decoder` interfaces and the two codecs. The server passes `protocol.Event` values around and never formats a string for the wire; rendering happens inside the codec a client owns.
- Each connection negotiates its encoding once (text or JSON lines) and keeps it. `internal/server/session.go` runs the handshake: the first line is inspected for `HELLO`, then every line is a name attempt until the hub accepts one, and the accepted name is returned to the caller.
- Shutdown is context-driven: cancelling the server context stops the accept loop, then the hub, then the connections.
- The terminal client will be a thin TUI over `pkg/client`, the public library that handles dialing, negotiation and delivery.

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

The hub holds `rooms map[string]*room`, and a `room` is a name, a `members
map[*client]struct{}` and a `*ring.Ring[protocol.Event]` of past messages. Each
client carries a `*room` pointer, so every client is in exactly one room and
`broadcast` walks that room's members directly instead of filtering the whole
client set. There is no separate index of clients: the room registry is the
complete membership list, which is why `named` (uniqueness for `nick` and
registration) walks rooms and members.

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
