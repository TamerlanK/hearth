# CLAUDE.md

Instructions for coding agents working in this repository.

The house style lives in [docs/STYLE.md](docs/STYLE.md) — one document for
humans and agents alike. Read it and follow it for every change.

Also read, before touching the areas they cover:

- [CONTRIBUTING.md](CONTRIBUTING.md) — what a change has to clear to land.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — before `internal/server` or
  `pkg/client`.
- [docs/PROTOCOL.md](docs/PROTOCOL.md) — before the wire format.
- [DECISIONS.md](DECISIONS.md) — why the non-obvious choices are what they are.
  Append an entry when you make another one.

`make check` must pass before you call a task done.
