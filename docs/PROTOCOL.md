# Protocol — `hearth/1`

Hearth is a line-oriented TCP protocol with two interchangeable encodings over
identical semantics:

- **text** — what a human sees in `telnet` or `nc`. Default.
- **json** — one JSON object per line, for programmatic clients. Opt in during
  negotiation.

Both directions of both encodings use the same framing, the same commands and
the same events. A client picks an encoding once, at connect time, and keeps it
for the life of the connection.

## Framing

- A message is a sequence of bytes terminated by `\n` (U+000A).
- A trailing `\r` before the `\n` is accepted and ignored, so CRLF clients
  (telnet) work unchanged. The server never sends `\r`.
- Lines are UTF-8. A line containing invalid UTF-8 is rejected as malformed.
- **A line may be at most 4096 bytes**, excluding the terminator. A client that
  sends a longer line gets one error line and is disconnected; the server never
  sends a line longer than that.
- The server never emits a bare `\n` inside a field: text renderings and JSON
  strings are checked so one event is always exactly one line.

## Negotiation

```
client                                   server
  |  <-- connect                            |
  |  <---------------- one text line: the name prompt
  |  ---- "HELLO hearth/1 json[ token=…]" > |     (optional)
  |  <---------------- {"kind":"system","text":"protocol json", ...}
  |  ---- {"cmd":"nick","args":["alice"]} > |
  |  <---------------- {"kind":"join", ...} |
```

1. On connect the server sends **exactly one text line**, the name prompt:

   ```
   * Welcome to hearth. Enter a name (1-20 characters, no spaces):
   ```

   It is a `system` event, hence the `*` prefix.

   It is sent in text encoding because the encoding is not yet known. A JSON
   client should discard server lines until it receives the `protocol json`
   system event; the prompt is guaranteed to be the only one.

2. The server inspects the **first line the client sends**.

   - If it is `HELLO hearth/1 json`, optionally followed by ` token=<token>`,
     the connection switches to the JSON encoding for both directions and the
     server replies with

     ```json
     {"kind":"system","text":"protocol json","time":"2026-09-09T15:04:05Z","seq":2}
     ```

     The client then supplies its name with a `nick` command.

   - **Anything else** is treated as the name reply in text encoding (or, on a
     server that requires a token, as the token). This is what telnet does, and
     it is why existing clients keep working.

3. Authentication, **only when the operator set a token**. A server with no
   token skips this step entirely and ignores any `token=` it is sent.

   - A JSON client puts the token in the negotiation line:
     `HELLO hearth/1 json token=s3cret`.
   - A text client is prompted for it *before* the name prompt:

     ```
     * This server requires a token. Enter it:
     ```

     and answers with the token as a bare line.

   A wrong or missing token gets one `error` event, `bad token`, and the
   connection is closed. There is no retry: reconnect to try again. The
   comparison is constant-time. The token protects nothing on its own over
   plaintext TCP — a server that requires one should also be behind TLS.

4. Naming: the name must be 1–20 printable characters with no whitespace, and
   unique across the server. A rejected name gets an `error` event and another
   attempt; the connection stays in the naming state until a name is accepted.
   Accepted commands while naming are `say` (in text: a bare line) and `nick`.
   **The whole handshake — negotiation and naming together — must finish within
   10 seconds**, however many name attempts it takes, or the connection is
   closed without a message. The idle timeout applies only afterwards.

5. Once named, the client is in the default room `#general` and every command
   below is available.

There is no other handshake, no version list and no capability exchange. A
server that does not understand `HELLO hearth/1 json` will treat it as a name,
which is the intended fallback.

**TLS** is transport only and changes nothing above: a server started with a
certificate speaks the same protocol inside a TLS session, on the same port.
A client either dials TLS or it does not; there is no in-band upgrade.

If the server is at capacity it sends one line and closes, before the prompt:

```
! server full, try again later
```

## Commands (client → server)

A command has a name, zero or more whitespace-free `args`, and at most one
free-text `text` field. The two encodings differ only in how those are spelled.

**Text**: a line starting with `/` is a command; the first word is the name
(case-insensitive), the rest is split as documented per command. **Any other
line is a `say`.**

**JSON**: an object with `cmd` (required), `args` (optional array of strings)
and `text` (optional string). Unknown fields are ignored.

| Command | Args | Text | Text example | JSON example |
|---------|------|------|--------------|--------------|
| `say` | — | the message | `hello everyone` or `/say hello everyone` | `{"cmd":"say","text":"hello everyone"}` |
| `msg` | `<name>` | the message | `/msg bob are you there` | `{"cmd":"msg","args":["bob"],"text":"are you there"}` |
| `join` | `<room>` | — | `/join golang` | `{"cmd":"join","args":["golang"]}` |
| `nick` | `<name>` | — | `/nick robert` | `{"cmd":"nick","args":["robert"]}` |
| `who` | `[room]` | — | `/who` or `/who golang` | `{"cmd":"who","args":["golang"]}` |
| `rooms` | — | — | `/rooms` | `{"cmd":"rooms"}` |
| `history` | `[room]` | — | `/history` | `{"cmd":"history"}` |
| `away` | — | the reason | `/away lunch` or `/away` to return | `{"cmd":"away","text":"lunch"}` |
| `ping` | — | — | `/ping` | `{"cmd":"ping"}` |
| `quit` | — | — | `/quit` | `{"cmd":"quit"}` |
| `help` | — | — | `/help` | `{"cmd":"help"}` |

Parsing rules that a client implementer needs:

- Only `say` and `msg` carry free text. For `msg`, the **first** whitespace-run
  after the command name separates the recipient from the text; the text keeps
  its internal spacing. For every other command the remainder is split on
  whitespace into `args` and any surplus args are a usage error.
- Leading and trailing whitespace is trimmed from the line and from each field.
- An empty line is a `say` with empty text, which the server ignores. Send
  nothing rather than relying on this.
- Room names are normalised to a leading `#`: `golang` and `#golang` are the
  same room. A room name is 1–24 characters of `a-z`, `0-9` and `-`; anything
  else is rejected. Rooms are created on first `join` and destroyed when the
  last member leaves, except the default room, which always exists.
- `quit` and `help` are answered by the connection itself and never reach the
  chat state; everything else is applied in the order received.
- Control characters are stripped from message text before it is broadcast, so
  no client can inject terminal escapes into another client's terminal. **Tab
  (U+0009) is the one exception** and is passed through; it cannot break the
  one-event-one-line rule the way `\n` and `\r` would.
- **A message is at most 1024 runes**, counted after decoding. This is separate
  from the 4096-byte line cap: a line within the byte limit can still be
  rejected for length, and the connection stays open when it is.
- The server may rate limit a client. Lines over the limit are discarded
  without being applied, and the client is told once per flood rather than once
  per line, so an `error` event is not a reliable per-line acknowledgement.

## Events (server → client)

Every event is one `Event`. In JSON:

```go
{
  "kind":  string,    // required, see the table below
  "room":  string,    // omitted when empty
  "from":  string,    // omitted when empty
  "to":    string,    // omitted when empty
  "text":  string,    // omitted when empty
  "names": [string],  // omitted when empty
  "time":  string,    // always present, RFC 3339 with nanoseconds
  "seq":   number     // server-assigned, omitted when 0
}
```

`seq` is assigned by the server, starts at 1 and **increases by exactly one per
event on that connection**. It is per connection, not global: two clients see
different numbers for the same broadcast. A gap never appears; if the server has
to drop events for a client that is not reading (see *Flow control*), the
dropped events are never numbered. Use `seq` to detect your own reordering bugs,
not to detect loss.

`time` is when the server created the event, except for `history`, where it is
when the original message was sent. It is RFC 3339 with nanosecond precision in
the server's local zone, so the offset is whatever the server runs in
(`2026-09-09T15:04:05.123456789+04:00`); parse the offset, do not assume `Z`.
The examples below use `Z` for brevity.

| Kind | Fields used | Meaning | Text rendering |
|------|-------------|---------|----------------|
| `msg` | `room` `from` `text` | Room message | `[15:04] alice: hello everyone` |
| `privmsg` | `from` `to` `text` | Private message; sent to both parties | `[15:04] alice -> bob: are you there` |
| `away` | `room` `from` `text` | Someone's presence changed; empty `text` means they are back | `* bob is away: lunch` |
| `system` | `text` | Notice from the server | `* protocol json` |
| `join` | `room` `from` | Someone entered a room, including yourself | `* bob joined #general` |
| `leave` | `room` `from` | Someone left a room, including on disconnect | `* bob left #general` |
| `nick` | `room` `from` `to` | Rename; `from` is the old name | `* bob is now known as robert` |
| `who` | `room` `names` | Reply to `who`, sorted | `* online in #general (2): alice, bob` |
| `rooms` | `names` | Reply to `rooms`, sorted; each name carries its member count | `* rooms (2): #general (2), #golang (1)` |
| `history` | `room` `from` `text` | A replayed past message | `[15:04] alice: earlier message` |
| `error` | `text` | Something the client asked for failed | `! name taken, try another:` |
| `pong` | — | Reply to `ping` | `* pong` |

JSON examples, one per kind:

```json
{"kind":"msg","room":"#general","from":"alice","text":"hello everyone","time":"2026-09-09T15:04:05.123456789Z","seq":7}
{"kind":"privmsg","from":"alice","to":"bob","text":"are you there","time":"2026-09-09T15:04:05Z","seq":8}
{"kind":"system","text":"protocol json","time":"2026-09-09T15:04:05Z","seq":2}
{"kind":"join","room":"#general","from":"bob","time":"2026-09-09T15:04:05Z","seq":3}
{"kind":"leave","room":"#general","from":"bob","time":"2026-09-09T15:04:05Z","seq":9}
{"kind":"nick","room":"#general","from":"bob","to":"robert","time":"2026-09-09T15:04:05Z","seq":10}
{"kind":"who","room":"#general","names":["alice","bob"],"time":"2026-09-09T15:04:05Z","seq":11}
{"kind":"rooms","names":["#general (2)","#golang (1)"],"time":"2026-09-09T15:04:05Z","seq":12}
{"kind":"history","room":"#general","from":"alice","text":"earlier message","time":"2026-09-09T14:58:01Z","seq":4}
{"kind":"error","text":"name taken, try another:","time":"2026-09-09T15:04:05Z","seq":5}
{"kind":"pong","time":"2026-09-09T15:04:05Z","seq":13}
```

Each entry in a `rooms` reply is `"<room> (<members>)"` — the count is part of
the string rather than a parallel array, so a client that only displays the list
needs no extra field. The default room appears even when it is empty.

History is **replayed in order as one `history` event per past message**, not
nested inside a single envelope event. That keeps every kind one flat line and
lets a client render a replayed message with exactly the code that renders a
live one; the cost is that a replay is *n* lines, bounded by the server's
history size.

In the text encoding `history` renders exactly like `msg`; a text user sees the
replay as ordinary chat lines with their original timestamps. Only JSON clients
can tell replay from live traffic.

### Rooms and history

- Everyone starts in `#general`. `join` moves you: the server broadcasts a
  `leave` for the old room, then a `join` for the new one, then replays the new
  room's history to you alone.
- `msg`, `join`, `leave` and `nick` events go only to clients in that `room`.
  An event with no `room` (`privmsg`, `system`, `who`, `rooms`, `pong`,
  `error`) goes only to the client it concerns.
- The server keeps the **last N messages per room in memory** (50 by default,
  set by the operator), replayed on join and on `history`. After the replay
  the server may send `system` events the operator configured (a message of
  the day); a client must not assume the replay is the last thing before live
  traffic. Only `say` messages
  are recorded; joins, leaves, renames and private messages are not. It is not
  durable: when the last member leaves a room the room and its history are
  discarded, and nothing survives a restart. `history` for a room with nothing
  stored answers with a `system` event.
- The number of rooms that can exist at once may be capped by the operator. A
  `join` that would create a room beyond the cap is refused with an `error`;
  joining a room that already exists is always allowed.

## Error handling

Errors arrive as `error` events (`! ...` in text) and, with three exceptions,
**do not close the connection**. The client stays in whatever state it was in
and may try again.

| Situation | `error` text | Connection |
|-----------|--------------|------------|
| Unparseable line (bad JSON, invalid UTF-8) | `malformed line` | stays open |
| Command name not in the table above | `unknown command dance (try /help)` | stays open |
| Wrong argument count | `usage: /msg <name> <text>` (from the command table, so it always matches `/help`) | stays open |
| Name rejected while naming | `name is empty, try again:`, `name is longer than 20 characters, try again:`, `name must be printable with no spaces, try again:`, `expected a name, try again:` (a JSON command other than `nick` or `say` while naming) | stays open |
| Name already in use | `name taken, try another:` (naming) / `name taken` (`nick`) | stays open |
| Unknown recipient for `msg` | `no such user bob` | stays open |
| Wrong or missing token | `bad token` | **closed** (during the handshake) |
| Bad room name | `room name must be 1-24 characters of a-z, 0-9 or -` | stays open |
| Already in the requested room | `already in #general` | stays open |
| `join` would exceed the room cap | `too many rooms, limit is 64` | stays open |
| Message longer than 1024 runes | `message is longer than 1024 characters` | stays open |
| Sending faster than the server's rate limit | `rate limited` (once per flood, not per line) | stays open |
| Line longer than 4096 bytes | `line too long, disconnecting` | **closed** |
| Server at capacity | `server full, try again later` | **closed** (before the prompt) |
| Too many connections from your address | `too many connections from your address` | **closed** (before the prompt) |
| Handshake not completed within 10s | *(none)* | **closed** |
| Too many events dropped in a row | `too many dropped messages, disconnecting` (best effort) | **closed** |
| Idle longer than the server's timeout | `disconnected: idle for 5m0s` (a `system` event) | **closed** |

The server also sends `* server shutting down` (a `system` event) to everyone
and closes on graceful shutdown. In every closing case the queued event is
flushed before the socket goes away, so the last line you read explains why.

### Flow control

Each connection has an outbound queue. Room traffic — `msg`, `join`, `leave`,
`nick`, `away`, and a `privmsg` from someone else — is admitted only while
fewer than 32 events are queued; beyond that it is **dropped** for that client
only, so a client that stops reading does not stall anyone else, and once the
5-second write deadline expires it is disconnected. Chat delivery is therefore
best-effort, not guaranteed. Read continuously; do not use the socket as a
queue.

Events the client asked for are never dropped: the history replay and the
message of the day on join, the answers to `who`, `rooms`, `history` and
`ping`, the echo of its own `msg`, and every `error`. They are queued whatever
the backlog, in order with everything around them, and while that backlog is
at the limit the server stops reading the client's input until it has drained.
A `history` reply of 50 messages arrives complete.

A client that accumulates too many dropped events in a row (100 by default) is
disconnected rather than left in a permanently degraded state. The server tries
to send `! too many dropped messages, disconnecting` first, but by definition
that client is not reading, so the notice often does not arrive — treat an
unexplained close after a burst as this case.

Inbound, each connection has a token bucket (5 lines per second, burst 10, by
default). It is checked **before** the line is decoded, so malformed and valid
lines cost the same. Over-limit lines are discarded, and the server emits one
`error` event per flood rather than one per line.

## Compatibility

This document specifies **`hearth/1`**, identified by that string in the
`HELLO` line. Within `hearth/1` the server may:

- add new `Kind` values, new commands, and new fields to `Event`.

A client MUST therefore ignore unknown JSON fields and MUST NOT fail on an
unknown `kind` — skip the event and carry on. In the text encoding an unknown
event still arrives as one line, prefixed `*`, `!`, or `[`.

Within `hearth/1` the server will not: remove or rename a documented `Kind`,
command, or field; change the framing; change the meaning of an existing field;
or make the text renderings above less readable. Anything that would break
those gets a new version string and a new `HELLO` line, and the server will
keep accepting `hearth/1` for at least one release after that.

Note that the text encoding is a **rendering**, not a parsing target. It is
stable enough to read and to script against loosely, but a program should
negotiate JSON.

## Writing a client

The minimum JSON client:

```python
import json, socket

s = socket.create_connection(("localhost", 4000))
f = s.makefile("rwb")

f.readline()                                   # the one text prompt
f.write(b"HELLO hearth/1 json\n"); f.flush()

def send(cmd, args=None, text=None):
    line = {"cmd": cmd}
    if args: line["args"] = args
    if text: line["text"] = text
    f.write((json.dumps(line) + "\n").encode()); f.flush()

for raw in f:                                  # every line from here is JSON
    e = json.loads(raw)
    if e["kind"] == "system" and e.get("text") == "protocol json":
        send("nick", ["pybot"])
    elif e["kind"] == "join" and e.get("from") == "pybot":
        send("say", text="hello from python")
    elif e["kind"] == "msg":
        print(f"{e['from']}: {e['text']}")
    elif e["kind"] == "error":
        print("error:", e["text"])
```

Checklist for a robust client: read lines continuously; ignore unknown `kind`
values and unknown fields; keep lines under 4096 bytes; expect `error` events
without assuming disconnection; treat delivery as best-effort.

## Version history

The version string is `hearth/1` and has not changed. Within it the protocol
has only grown, which the compatibility rules above allow:

| Date | Change | Compatibility |
|------|--------|---------------|
| 2026-09-09 | First version: text framing, the name prompt, `say`, `msg`, `who`, `quit`, `help`; events `msg`, `privmsg`, `system`, `join`, `leave`, `who`, `error`. | — |
| 2026-09-09 | JSON encoding behind `HELLO hearth/1 json`; per-connection `seq`; `ping`/`pong`; typed errors `malformed line`, `unknown command`. | additive |
| 2026-09-09 | Rooms (`join`, `rooms`), history replay (`history`), renaming (`nick`), room-scoped events, the `too many rooms` and `already in` errors. | additive |
| 2026-09-09 | Limits on the wire: 4096-byte lines, 1024-rune messages, rate limiting with the once-per-flood `rate limited` error, per-address cap, the 10 s handshake deadline, disconnect after consecutive drops. | additive |
| 2026-09-10 | No wire change. `pkg/client` and the terminal UI shipped as JSON clients of this version. | — |

A breaking change would become `hearth/2` with a new `HELLO` line, and the
server would keep accepting `hearth/1` for at least one release.
