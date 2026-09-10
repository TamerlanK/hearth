# Go client library

`github.com/TamerlanK/hearth/pkg/client` is the public client. It speaks the
JSON encoding from [PROTOCOL.md](PROTOCOL.md) and depends only on the standard
library and `pkg/protocol`.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
)

func main() {
	ctx := context.Background()
	c, err := client.Dial(ctx, "localhost:4000", client.Options{Name: "bot", Room: "ops", Reconnect: true})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	if err := c.Say(ctx, "hello from Go"); err != nil {
		log.Fatal(err)
	}
	for e := range c.Events() {
		if e.Kind == protocol.Msg {
			fmt.Printf("%s: %s\n", e.From, e.Text)
		}
	}
}
```

`Dial` connects, sends `HELLO hearth/1 json`, registers `Options.Name`, joins
`Options.Room` if it is set, and returns once the server has confirmed the
join. `Options.DialTimeout` (10s by default) covers all of that.

## API at a glance

| Call | What it does | Returns when |
|------|--------------|--------------|
| `Say(ctx, text)` | Sends to the current room | The line has been written |
| `PrivMsg(ctx, to, text)` | Private message | The line has been written |
| `Join(ctx, room)` | Moves to a room, creating it if needed | The server sends your `join` event |
| `Nick(ctx, name)` | Renames you | The server sends your `nick` event |
| `Who(ctx)` | Names in your room | The next `who` event arrives |
| `Rooms(ctx)` | Every room with its member count | The next `rooms` event arrives |
| `Send(ctx, cmd)` | Any `protocol.Command`, unacknowledged | The line has been written |
| `Events()` | Every event the server sends, in order | Closed when the client closes |
| `State()` | `Connecting`, `Connected`, `Reconnecting` or `Closed` | Safe from any goroutine |
| `Stats()` | `Dropped` events, `Reconnects` so far and the in-flight reconnect `Attempt` | Safe from any goroutine |
| `Close()` | Disconnects and closes `Events()` | Every goroutine has exited |

Room names are normalised the way the server does it: `ops` and `#ops` are the
same room, and events always carry the `#` form.

## Events and back-pressure

`Events()` is a buffered channel of 256 events. The reader goroutine never
blocks on it: if the consumer falls behind, the **oldest** buffered event is
dropped to make room and `Stats().Dropped` goes up. Drain the channel in a
goroutine of your own if you need every event; the buffer is there to absorb
bursts, not to be a queue.

Every event is delivered, including the ones that answer a request. `Join`
returns after your `join` event, and that event is also on `Events()`. Error
events from the server are delivered too, as `protocol.Error`.

## Errors

- `ErrClosed`: the client is closed, whether by `Close` or because the
  connection dropped without `Reconnect`.
- `ErrNotConnected`: the client is between connections (`State()` is
  `Reconnecting`). Retry after the `reconnected` system event.
- `ErrNameTaken`: from `Dial` or `Nick`.
- `ErrRateLimited`: the server dropped a request because you exceeded its rate.
- `*ServerError`: any other `error` event that answered a request. It carries
  the server's text and satisfies `errors.Is` for the two sentinels above.

## Request correlation and its limits

The protocol does not tag replies. `Join`, `Nick`, `Who` and `Rooms` work by
installing a matcher, sending the command, and resolving on the first event
that either matches or is an `error`. Only one such request is in flight at a
time; concurrent callers queue.

Consequences worth knowing:

- An `error` event that has nothing to do with your request, arriving while it
  is pending, is reported as the request's failure.
- `Who` and `Rooms` accept the next event of that kind. The server only sends
  those in reply to you, so this is safe in practice.
- The server warns about rate limiting once per burst and silently drops the
  rest. A request dropped inside a burst gets no reply, so always pass a
  context with a deadline.
- `Say` and `PrivMsg` have no acknowledgement in the protocol and return as
  soon as the line is written. Their errors, if any, arrive as `error` events.

## Reconnect

Off by default. With `Options.Reconnect` set:

1. When the connection drops, `State()` becomes `Reconnecting`, any pending
   request fails with the read error, and `Send`/`Say`/... return
   `ErrNotConnected`.
2. The client waits, then dials again. The wait starts at `Backoff.Min` (500ms
   by default), doubles each failure, is capped at `Backoff.Max` (30s), and is
   jittered between half and the full value so a fleet of clients does not
   reconnect in lockstep.
3. Each attempt redoes the full handshake with the current name (the one from
   the last successful `Nick`) and re-joins the last room you were in. Room
   history is replayed by the server as usual and arrives on `Events()`.
4. On success the client emits a `system` event with text `reconnected`,
   `Stats().Reconnects` goes up and `State()` returns to `Connected`.

`Stats().Attempt` is the number of the dial currently being attempted: 1 for the
first retry after a drop, climbing with each failure, and back to 0 once a dial
succeeds. There is no channel for state changes, so poll `State()` and `Stats()`
if you want to show progress — that is what the terminal UI's status bar does.

Attempts continue until they succeed or the client is closed. Cancelling the
context passed to `Dial` does **not** stop a running client; that context only
bounds the initial connection. Use `Close` to stop, from any goroutine.

Without `Reconnect`, a dropped connection closes the client: `Events()` closes,
`State()` is `Closed`, and every method returns `ErrClosed`. The server's
`server shutting down` notice, when there is one, is the last event you see.
