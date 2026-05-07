# flow-codex Repo Conventions

## What This Is

`flow` is a Go CLI that manages local projects/tasks and bootstraps
per-task Codex sessions. It uses SQLite through `modernc.org/sqlite`
(pure Go, no CGO) and stores user data under `~/.flow/`.

## Build And Test

```bash
make build
make test
go test ./...
```

Tests override `$FLOW_ROOT` and `$HOME`, so they should not touch real
`~/.flow/` or `~/.codex/` data. Terminal spawning and
headless Codex execution are mocked through package-level function vars.

## Structure

```text
flow/
  main.go
  internal/
    app/            CLI commands, session orchestration, hooks, skill embed
    flowdb/         SQLite schema, migrations, models, CRUD helpers
    iterm/          iTerm2 AppleScript tab spawning
    terminal/       Terminal.app AppleScript tab spawning
  Makefile
  README.md
  AGENTS.md
```

## Conventions

- Keep SQLite pure Go; do not introduce CGO.
- Use `flag.FlagSet` with `ContinueOnError` via `flagSet()`.
- Exit codes: `0` success, `1` runtime error, `2` usage error.
- Store timestamps as RFC3339 strings.
- Keep command tests next to the source files in `internal/app/`.
- Use real SQLite in tests, not DB mocks.
- The embedded skill at `internal/app/skill/SKILL.md` is the operating
  manual for how Codex sessions use flow. If the skill promises a
  workflow, the code and tests need to support it.
- `hookCommand` and `userPromptSubmitHookCommand` in `internal/app/skill.go`
  are stable install markers for `~/.codex/hooks.json`; changing them can
  orphan existing hook installs.

## Codex Integration

- Fresh task session: `flow do <task>` starts `codex "<bootstrap prompt>"`.
- Resume: `flow do <task>` starts `codex resume <session-id>`.
- Codex generates the session ID; the SessionStart hook registers it back
  into the task row with `transcript_path`.
- `flow done <task>` may run `codex exec` to sweep useful transcript
  learnings into the knowledge base and project updates.
- `flow transcript <task>` reads the stored Codex transcript path first,
  then scans `~/.codex/sessions` as a fallback. Legacy Claude transcript
  lookup remains only for backward compatibility with old data.
