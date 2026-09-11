# Style guide

The conventions every change to hearth follows. They are the house style, not
suggestions: a pull request that ignores them gets sent back. Read
[CONTRIBUTING.md](../CONTRIBUTING.md) first for how to open one.

Some rules here are unusual on purpose — no comments in Go code, a closed list
of allowed dependencies, a context on every blocking call. Where the reason is
not obvious, [DECISIONS.md](../DECISIONS.md) has the entry that explains it.

## Language and dependencies

- Go 1.24+.
- Prefer the standard library. Add a dependency only when it saves significant
  code, not to save a few lines.
- Allowed dependencies: `spf13/cobra`, `charmbracelet/bubbletea`,
  `charmbracelet/bubbles`, `charmbracelet/lipgloss`,
  `prometheus/client_golang`. Anything else needs an explicit decision from the
  user first.

## Package layout

```
cmd/hearth/        thin entrypoint, no logic
internal/cli/      cobra commands
internal/server/   accept loop, hub, connection handling
internal/ratelimit/ token bucket, one per connection
internal/ring/     generic fixed-capacity ring buffer
internal/tui/      terminal UI
pkg/client/        public Go client library
pkg/protocol/      wire format: framing, message types, negotiation
docs/              STYLE.md, ARCHITECTURE.md, PROTOCOL.md, CLIENT.md, BENCHMARKS.md
DECISIONS.md       running log of non-obvious choices and why
```

- Nothing outside `internal/` or `pkg/` imports `internal/`.
- `pkg/client` must not import `internal/tui` or `internal/server`.

## Documentation

- No comments in `internal/` or `cmd/`: no doc comments, no inline comments.
  Names and structure carry the meaning; anything that needs prose goes in
  `docs/`. The one exception is a package comment in a `doc.go`, for a package
  whose shape needs a paragraph to see (`internal/tui` has one).
- `pkg/` is the published API and follows Go's convention instead: a package
  comment and a doc comment on every exported identifier, because `go doc` and
  pkg.go.dev are how a Go library is read. Say what the name cannot; do not
  restate it. Lint enforces both halves (`revive: exported`).
- A package with no code yet keeps a bare `doc.go` containing only the
  `package` clause so the directory stays a Go package.
- Docs are part of the change: update `docs/` when behaviour or the wire format
  changes, and append to `DECISIONS.md` when a non-obvious choice is made.

## Errors

- Wrap with context: `fmt.Errorf("dial %s: %w", addr, err)`.
- Sentinel errors are package-level vars: `var ErrClosed = errors.New("...")`.
- Compare with `errors.Is` / `errors.As`, never string matching.
- Never ignore an error. No `_ = f()`. If an error genuinely cannot be handled,
  log it with `slog` and say why in a comment.

## Concurrency

- Any shared state has exactly one owner goroutine, **or** is guarded by a mutex
  that is documented in the struct comment naming what it protects.
- Every blocking operation takes a `context.Context` as its first parameter.
- Goroutines have a defined exit path; no goroutine outlives the context that
  started it.

## Tests

- Table-driven, standard library `testing` only. No assertion frameworks.
- Always run with `-race`.
- Every bug fix gets a regression test that fails before the fix.
- Integration tests bind real TCP on `127.0.0.1:0` and use the assigned port.
  No fixed ports, no `time.Sleep` for synchronisation.

## Logging

- `log/slog` only, structured key=value.
- No `fmt.Println` in library code; user-facing CLI output goes to the command's
  configured writer.

## Commits

- Conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `chore:`, `refactor:`.
- Small and focused; one logical change per commit.

## Definition of done

A task is done when:

1. `make check` passes (fmt, vet, lint, test -race).
2. Docs are updated.
3. No `TODO` is left without an issue reference, e.g. `// TODO(#12): ...`.
