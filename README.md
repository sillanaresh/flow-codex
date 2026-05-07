# flow-codex

![Status](https://img.shields.io/badge/status-alpha-orange) ![License](https://img.shields.io/badge/license-MIT-blue.svg)

`flow-codex` is a local task manager and working-memory layer for Codex.
It keeps your projects, tasks, notes, playbooks, and durable knowledge in
`~/.flow/`, then starts or resumes a dedicated Codex session for each task.

This fork is based on the original `Facets-cloud/flow` project, but the
session backend has been changed from Claude Code to Codex.

## What It Does

- Tracks projects and tasks as local markdown briefs plus a SQLite index.
- Opens one Codex terminal session per task with `flow do <task>`.
- Resumes the same Codex session later with `codex resume <session-id>`.
- Registers Codex session IDs and transcript paths through Codex hooks.
- Keeps dated progress notes under each task and project.
- Maintains a local knowledge base for durable facts about you, your org,
  products, processes, and business context.
- Runs repeatable playbooks for recurring work such as weekly reviews,
  triage, planning, or support rotations.
- Runs a headless `codex exec` close-out sweep on `flow done` so useful
  learnings from the task transcript can be written back to the knowledge
  base and project updates.

Everything is local. There is no server, cloud account, or telemetry.

## Install From Source

```bash
make build
make install
flow init
```

`flow init` creates `~/.flow/`, initializes the SQLite database and
knowledge-base files, installs the `flow` skill at
`~/.codex/skills/flow/SKILL.md`, installs Codex hooks at
`~/.codex/hooks.json`, and enables Codex hooks in `~/.codex/config.toml`.

## Daily Use

Start Codex normally, then say:

> let's get to work

The installed skill can list your active work, help you add tasks, save
progress notes, and open a task in a dedicated Codex tab.

Common commands:

```bash
flow list tasks
flow add task "Fix onboarding bug" --slug onboarding-bug --work-dir /path/to/repo
flow do onboarding-bug
flow transcript onboarding-bug
flow done onboarding-bug
```

## How Sessions Work

On the first `flow do <task>`, flow starts:

```bash
codex "<bootstrap prompt>"
```

Codex generates its own session ID. The Codex SessionStart hook receives
that ID and transcript path, then stores them on the task row. Later
`flow do <task>` calls run:

```bash
codex resume <session-id>
```

The bootstrap prompt tells Codex to load the `flow` skill, read the task
brief and updates, review `AGENTS.md` guidance, and work inside the
task's `work_dir`.

If you pass:

```bash
flow do <task> --dangerously-skip-permissions
```

flow forwards Codex's dangerous bypass flag:

```bash
--dangerously-bypass-approvals-and-sandbox
```

Use that only when you trust the workspace and want fewer approval prompts.

## Data Layout

```text
~/.flow/
  flow.db
  kb/
    user.md
    org.md
    products.md
    processes.md
    business.md
  projects/<slug>/
    brief.md
    updates/*.md
  tasks/<slug>/
    brief.md
    updates/*.md
  playbooks/<slug>/
    brief.md
    updates/*.md
```

The markdown files are the human-readable source of truth. The SQLite
database is the fast index used by the CLI.

## macOS Terminal Permissions

flow can open Codex in iTerm2 or stock macOS Terminal.app. iTerm2 uses a
native tab API. Terminal.app requires macOS Accessibility permission
because AppleScript has to send Cmd-T through System Events.

If `flow do` reports an Accessibility error, grant permission to
**Terminal**, not Codex and not the `flow` binary:

```bash
open "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
```

Then enable Terminal in the Accessibility list and retry the same
`flow do` command.

## Development

```bash
go test ./...
go build -o flow .
```

Tests use temp `$FLOW_ROOT` and `$HOME` values so they do not touch your
real `~/.flow/` or `~/.codex/` directories. External
commands such as terminal spawning and `codex exec` are mocked in tests.

## Upstream

Original project:

```text
https://github.com/Facets-cloud/flow
```

This fork keeps upstream history and credits while changing the primary
agent backend to Codex.

## License

[MIT](LICENSE)
