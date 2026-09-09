# Protocol

Hearth is newline-framed over TCP. Lines are at most 4096 bytes; a longer line
disconnects the client. A trailing `\r` is ignored so telnet works.

Two encodings are planned, negotiated at connect time:

- **text** — human-readable, typeable over `nc`/`telnet`. Implemented.
- **json** — one JSON object per line, for programmatic clients. Not yet.

## Text encoding

Server → client lines:

| Prefix | Meaning | Example |
|--------|---------|---------|
| `* `   | notice  | `* alice joined`, `* online (2): alice, bob`, `* server shutting down` |
| `! `   | error   | `! name taken, try another:`, `! unknown command /x (try /help)` |
| `[HH:MM] name: ` | chat message | `[15:04] alice: hello` |
| (none) | prompt  | `Welcome to hearth. Enter a name (1-20 characters, no spaces):` |

Client → server lines:

- A line starting with `/` is a command: `/who`, `/quit`, `/help`.
- Anything else is broadcast to everyone, including the sender. Control
  characters are stripped.

Handshake: the server sends the name prompt; the client answers with a name
(1–20 printable characters, no whitespace, unique). Invalid or taken names get
a `! ...` line and another chance. A server at capacity sends
`! server full, try again later` and closes without prompting.

To be defined: the JSON encoding and how it is negotiated, keepalives.
