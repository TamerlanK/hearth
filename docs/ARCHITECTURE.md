# Architecture

## Overview

- The server runs an accept loop that hands each accepted `net.Conn` to a connection handler.
- Exactly one hub goroutine owns all shared state (the set of clients, their names and rooms, and per-room history); nothing else touches it.
- Each client gets two goroutines: a reader that parses lines from the socket and a writer that drains an outbound channel.
- All communication between the hub and clients is by channel, so there are no locks on chat state.
- Outbound channels are buffered (32 events). Fan-out is non-blocking: a client that cannot keep up has messages dropped and counted, and the 5s write deadline eventually disconnects one that is truly stuck.
- `internal/protocol` owns the wire format: `Event`, `Command`, the `Encoder`/`Decoder` interfaces and the two codecs. The server passes `protocol.Event` values around and never formats a string for the wire; rendering happens inside the codec a client owns.
- Each connection negotiates its encoding once (text or JSON lines) and keeps it. `internal/server/session.go` is the negotiating -> naming -> chatting state machine.
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
                       join ──────────────────────▶  name taken? reply err
                       (repeat until nil)  ◀────────  else add to map, reply nil
                       (hub emits Join itself) ────▶  fan-out via trySend
                       start writeLoop ──────────────────────────────────────▶ range over send
                       readLoop:
                         line ─▶ dec.Decode ─▶ Command
                              ─▶ /quit, /help answered locally
                              ─▶ hub.do(cmd) ──▶ hub applies it, fans out Events,
                                                 returns Events for this client
                                                 ──▶ send chan ──▶ enc.Encode ──▶ conn.Write
                       readLoop returns (EOF, /quit, idle, error)
                       leave(c) ──────────────────▶  delete from map; close(c.send);
                                                     emit Leave to the room
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

Rooms are a field on the client, not a second index: `broadcast` walks the client set and delivers to whoever is in the event's room. An event with an empty `Room` is not broadcast at all; it goes straight to the one client it answers. `/rooms` derives the room list from the clients, so a room exists exactly while someone is in it. Per-room history is the last 50 messages, replayed on join, and dropped when the room empties. All of it is O(clients) per message, which is the right shape until the client count stops fitting in one goroutine's budget.

## Why there is no mutex on chat state

The client set is touched by exactly one goroutine, the hub. Joining, leaving, broadcasting and `/who` are all messages to that goroutine; anything that needs an answer sends its own reply channel and waits. That gives three properties for free:

- **No data races by construction.** There is nothing to forget to lock because only one goroutine can see the map.
- **Atomic decisions.** Name uniqueness is decided and the registration applied in the same hub step, so two clients racing for the same name cannot both win. A check-then-register split across two messages would be a race.
- **No lock ordering.** The hub never blocks on a client (fan-out is `trySend` with a `default:` case), and clients never hold anything while waiting on the hub, so there is nothing to deadlock.

Two pieces of state live outside the hub and are documented where they sit:

- `Server.active` (open-connection count) is an `atomic.Int32` so the `MaxClients` check on the accept path needs no round trip to the hub.
- `client.mu` guards `closed` and `dropped` and serialises `trySend` against `closeSend`. The client's own read goroutine also writes to `send` (command replies), so the hub cannot be the sole writer; the tiny mutex is what makes closing the channel safe.

`client.name` and `client.room` need no lock even though two goroutines touch the struct: the connection goroutine writes them during the handshake, before the hub has ever seen the client, and from `join` onward only the hub goroutine reads or writes them (`/nick`, `/join`). The connection goroutine reads `name` once, immediately after `join` returns, for its logger.

`client.enc`, `client.dec` and `client.seq` are single-writer by construction: the connection goroutine sets the codec during negotiation and writes the handshake events itself, then starts `writeLoop`, which is the only goroutine to touch `seq` afterwards. The `go` statement provides the happens-before edge. That is what makes `seq` monotonic per connection with no counter lock.
