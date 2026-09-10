# Contributing

Thanks for looking. Hearth is small on purpose, so the bar for a change is
"does this make the thing clearer or more correct", not "does this add a
feature".

## Before you start

- Read [CLAUDE.md](CLAUDE.md). It is the style guide, including the rules that
  surprise people: no comments in Go code, standard library first, a closed
  list of allowed dependencies, every blocking call takes a `context.Context`.
- Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) if you are touching
  `internal/server` or `pkg/client`, and [docs/PROTOCOL.md](docs/PROTOCOL.md)
  if you are touching the wire format.
- Open an issue first for anything that changes the protocol, adds a
  dependency, or adds a flag.

## Making a change

```sh
git clone https://github.com/TamerlanK/hearth && cd hearth
make check          # fmt, vet, lint, test -race; must pass before and after
```

- One logical change per commit, with a
  [conventional commit](https://www.conventionalcommits.org/) message:
  `feat:`, `fix:`, `docs:`, `test:`, `chore:`, `refactor:`.
- A bug fix comes with a regression test that fails before the fix.
- Tests are table-driven `testing` only, always run with `-race`. Integration
  tests bind `127.0.0.1:0`; no fixed ports, no `time.Sleep` for
  synchronisation.
- If behaviour or the wire format changes, the docs change in the same commit.
  A non-obvious choice gets a numbered entry in [DECISIONS.md](DECISIONS.md)
  saying what was decided, why, and what would make us revisit it.
- No `TODO` without an issue reference: `// TODO(#12): ...`.

## Pull requests

CI runs lint, the race test matrix, a fuzz smoke, cross-compilation,
`govulncheck` and a Docker build. A PR is ready when CI is green, `make
check` passes locally, and the description says what changed and why in a
paragraph a reviewer can read in a minute.

## Reporting a security issue

See [SECURITY.md](SECURITY.md).
