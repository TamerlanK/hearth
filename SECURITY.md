# Security

## What hearth does not do yet

Hearth is a plaintext TCP chat server. Read this before exposing it to a
network you do not control.

- **No accounts.** `--token` is one shared secret for the whole server: it
  decides who may connect, not who anyone is. A name is still claimed by
  whoever asks for it first and released on disconnect. Anyone with the token
  can take any free name.
- **Encryption is opt-in.** Without `--tls-cert`/`--tls-key` everything,
  including private messages and the token itself, crosses the wire in the
  clear. Either start the server with a certificate or put it behind a
  TLS-terminating proxy; a token over plaintext is readable by anyone on the
  path.
- **No authorisation.** Every connected client can join any room, read that
  room's recent history, and see who is online.
- **The metrics port is unauthenticated** and also serves `/debug/pprof/`.
  Bind `--metrics-addr` to localhost or a private interface.

## What it does do

- Optional TLS on the listener (`--tls-cert`, `--tls-key`, TLS 1.2 or later).
- An optional shared-secret token (`--token`), compared with
  `subtle.ConstantTimeCompare`, required before a name is accepted. A wrong
  token closes the connection with no retry, so guessing costs a full
  reconnect and is bounded by the per-address connection cap.
- Per-client rate limiting, checked before any decoding.
- Per-address connection cap and a global connection cap.
- Hard caps on line length (4096 bytes) and message length (1024 runes).
- Handshake and idle timeouts.
- Control characters stripped from messages, so no client can inject terminal
  escape sequences into another client's terminal.
- A client that stops reading is dropped, not waited for.
- Panics are recovered per connection.
- The startup banner logs configuration through an explicit allowlist, so a
  future secret cannot leak into logs by accident; the token is logged only as
  `token_required=true`. Prefer `HEARTH_TOKEN` over `--token` so it does not
  appear in the process list.
- Dependencies are checked with `govulncheck` in CI, and the container image is
  distroless and runs as a non-root user.

## Reporting a vulnerability

Email the maintainer at the address on the
[GitHub profile](https://github.com/TamerlanK) rather than opening a public
issue. You will get an acknowledgement within a week. There is no bug bounty.
