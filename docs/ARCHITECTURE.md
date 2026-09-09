# Architecture

## Overview

- The server runs an accept loop that hands each accepted `net.Conn` to a connection handler.
- Exactly one hub goroutine owns all shared state (the set of clients); nothing else touches it.
- Each client gets two goroutines: a reader that parses lines from the socket and a writer that drains an outbound channel.
- All communication between the hub and clients is by channel, so there are no locks on chat state.
- Outbound channels are buffered (32 lines). Fan-out is non-blocking: a client that cannot keep up has messages dropped and counted, and the 5s write deadline eventually disconnects one that is truly stuck.
- Protocol negotiation (text vs JSON lines) is planned; today only the text protocol exists. `internal/protocol` will own framing and message types.
- Shutdown is context-driven: cancelling the server context stops the accept loop, then the hub, then the connections.
- The terminal client will be a thin TUI over `pkg/client`, the public library that handles dialing, negotiation and delivery.

## Life of a connection

```
accept loop            handleConn goroutine            hub goroutine          writeLoop goroutine
-----------            --------------------            -------------          -------------------
Accept() ──────────▶  active++ ; over MaxClients?
                       └─ yes: write "! server full", close, return
                       write name prompt (direct)
                       readLine ─▶ validateName
                       join ──────────────────────▶  name taken? reply err
                       (repeat until nil)  ◀────────  else add to map, reply nil
                       say("* alice joined") ──────▶  fan-out via trySend
                       start writeLoop ──────────────────────────────────────▶ range over send
                       readLoop:
                         line ─▶ command (/who: request+reply chan, /quit, /help)
                              ─▶ say("[15:04] alice: text") ──▶ fan-out via trySend ──▶ send chan ──▶ conn.Write
                       readLoop returns (EOF, /quit, idle, error)
                       leave(c) ──────────────────▶  delete from map; close(c.send)
                       wait writerDone                                          ◀── send drained, range ends,
                       say("* alice left")                                            conn.Close()
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

## Why there is no mutex on chat state

The client set is touched by exactly one goroutine, the hub. Joining, leaving, broadcasting and `/who` are all messages to that goroutine; anything that needs an answer sends its own reply channel and waits. That gives three properties for free:

- **No data races by construction.** There is nothing to forget to lock because only one goroutine can see the map.
- **Atomic decisions.** Name uniqueness is decided and the registration applied in the same hub step, so two clients racing for the same name cannot both win. A check-then-register split across two messages would be a race.
- **No lock ordering.** The hub never blocks on a client (fan-out is `trySend` with a `default:` case), and clients never hold anything while waiting on the hub, so there is nothing to deadlock.

Two pieces of state live outside the hub and are documented where they sit:

- `Server.active` (open-connection count) is an `atomic.Int32` so the `MaxClients` check on the accept path needs no round trip to the hub.
- `client.mu` guards `closed` and `dropped` and serialises `trySend` against `closeSend`. The client's own read goroutine also writes to `send` (command replies), so the hub cannot be the sole writer; the tiny mutex is what makes closing the channel safe.
