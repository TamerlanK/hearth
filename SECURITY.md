# Security

## What hearth does not do yet

Hearth is a plaintext TCP chat server. Read this before exposing it to a
network you do not control.

- **No authentication.** A name is claimed by whoever asks for it first and is
  released on disconnect. There are no accounts, passwords or tokens.
- **No transport encryption.** Everything, including private messages, crosses
  the wire in the clear. Put it behind a TLS-terminating proxy or an SSH
  tunnel, or keep it on a trusted network.
- **No authorisation.** Every connected client can join any room, read that
  room's recent history, and see who is online.
- **The metrics port is unauthenticated** and also serves `/debug/pprof/`.
  Bind `--metrics-addr` to localhost or a private interface.

## What it does do

- Per-client rate limiting, checked before any decoding.
- Per-address connection cap and a global connection cap.
- Hard caps on line length (4096 bytes) and message length (1024 runes).
- Handshake and idle timeouts.
- Control characters stripped from messages, so no client can inject terminal
  escape sequences into another client's terminal.
- A client that stops reading is dropped, not waited for.
- Panics are recovered per connection.
- The startup banner logs configuration through an explicit allowlist, so a
  future secret cannot leak into logs by accident.
- Dependencies are checked with `govulncheck` in CI, and the container image is
  distroless and runs as a non-root user.

## Reporting a vulnerability

Email the maintainer at the address on the
[GitHub profile](https://github.com/TamerlanK) rather than opening a public
issue. You will get an acknowledgement within a week. There is no bug bounty.
