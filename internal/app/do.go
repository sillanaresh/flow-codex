package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/spawner"
	"fmt"
	"net/url"
	"os"
)

// openConcurrentDB opens flow.db with a generous busy_timeout so that two
// concurrent `flow do` processes (or two goroutines in the tests) will
// serialize at the SQLite file level rather than failing fast with
// SQLITE_BUSY. The pragma is applied at connection-open time via the DSN
// so every conn in the pool inherits it. Schema creation still runs via
// OpenDB to keep DDL in one place.
func openConcurrentDB(path string) (*sql.DB, error) {
	// Ensure schema exists via the shared OpenDB path.
	pre, err := flowdb.OpenDB(path)
	if err != nil {
		return nil, err
	}
	pre.Close()

	q := url.Values{}
	// 30s is enough to cover realistic bootstraps; tests finish in ms.
	q.Set("_pragma", "busy_timeout(30000)")
	// BEGIN IMMEDIATE acquires a RESERVED lock up-front, so two concurrent
	// `flow do` transactions serialize at tx.Begin() (waiting on the busy
	// timeout) instead of racing to the first write and failing.
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}
	return db, nil
}

// cmdDo flips a task to in-progress, starts a Codex session if needed,
// and spawns a terminal tab to run or resume it.
func cmdDo(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: do requires a task ref")
		return 2
	}
	query := args[0]
	fs := flagSet("do")
	fresh := fs.Bool("fresh", false, "discard existing session and re-bootstrap")
	dangerSkip := fs.Bool("dangerously-skip-permissions", false, "pass Codex's dangerous bypass flag through to codex")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	// Step 2: atomic status flip inside a transaction. Captures preSessionID
	// and other fields for later steps. Per spec §6 this commit is the
	// durability boundary — even if bootstrap or iTerm spawn fails below,
	// the task is already in 'in-progress'.
	tx, err := db.Begin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: begin tx: %v\n", err)
		return 1
	}
	// If we don't commit by the end, rollback.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Re-read inside the tx so we see the freshest status.
	var curStatus string
	if err := tx.QueryRow(`SELECT status FROM tasks WHERE slug = ?`, task.Slug).Scan(&curStatus); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-read task: %v\n", err)
		return 1
	}
	if curStatus == "done" {
		fmt.Fprintf(os.Stderr,
			"error: task %q is done; edit its status back to backlog or in-progress to reopen it\n",
			task.Slug)
		return 1
	}

	// Decide bootstrap vs resume based on the row we re-read inside the tx.
	// Codex generates the session id at startup. The Codex SessionStart hook
	// receives that id and transcript path, then registers them back here.
	var curSessionID sql.NullString
	if err := tx.QueryRow(`SELECT session_id FROM tasks WHERE slug=?`, task.Slug).Scan(&curSessionID); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-read session_id: %v\n", err)
		return 1
	}
	needsBootstrap := !curSessionID.Valid || *fresh

	now := flowdb.NowISO()
	if needsBootstrap {
		if _, err := tx.Exec(
			`UPDATE tasks SET status='in-progress',
			 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
			 updated_at=?
			 WHERE slug=? AND status IN ('backlog','in-progress')`,
			now, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: flip status: %v\n", err)
			return 1
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE tasks SET status='in-progress',
			 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
			 updated_at=?
			 WHERE slug=? AND status IN ('backlog','in-progress')`,
			now, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: flip status: %v\n", err)
			return 1
		}
	}
	// Re-select to capture the canonical view.
	row := tx.QueryRow(`SELECT `+flowdb.TaskCols+` FROM tasks WHERE slug = ?`, task.Slug)
	fresh2, err := flowdb.ScanTask(row)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: re-select task: %v\n", err)
		return 1
	}
	task = fresh2
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "error: commit: %v\n", err)
		return 1
	}
	committed = true

	if *fresh && curSessionID.Valid {
		fmt.Printf("--fresh: discarding old session %s\n", curSessionID.String)
	}

	// Look up project (may be nil).
	var project *flowdb.Project
	if task.ProjectSlug.Valid {
		p, err := flowdb.GetProject(db, task.ProjectSlug.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: get project: %v\n", err)
			return 1
		}
		project = p
	}

	cwd := task.WorkDir
	if cwd == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no work_dir\n", task.Slug)
		return 1
	}

	// Spawn the terminal tab.
	var command string
	if needsBootstrap {
		playbookSlug := ""
		isFirstRun := false
		if task.PlaybookSlug.Valid {
			playbookSlug = task.PlaybookSlug.String
			// First run = this is the only non-archived run-task for the
			// playbook. The current run row was just inserted by
			// cmdRunPlaybook, so a count of 1 means no prior runs exist.
			var runCount int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM tasks WHERE playbook_slug = ? AND kind = 'playbook_run' AND archived_at IS NULL`,
				playbookSlug,
			).Scan(&runCount); err != nil {
				fmt.Fprintf(os.Stderr, "warning: count playbook runs: %v\n", err)
			}
			isFirstRun = runCount <= 1
		}
		prompt := buildBootstrapPromptForKindV2(task.Slug, task.Kind, playbookSlug, isFirstRun)
		if *dangerSkip {
			command = "codex --dangerously-bypass-approvals-and-sandbox " + spawner.ShellQuote(prompt)
		} else {
			command = "codex " + spawner.ShellQuote(prompt)
		}
	} else {
		if *dangerSkip {
			command = "codex resume --dangerously-bypass-approvals-and-sandbox " + curSessionID.String
		} else {
			command = "codex resume " + curSessionID.String
		}
	}
	envVars := map[string]string{"FLOW_TASK": task.Slug}
	if project != nil {
		envVars["FLOW_PROJECT"] = project.Slug
	}
	if err := spawner.SpawnTab(buildTabTitle(project, task), cwd, command, envVars); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Post-spawn bookkeeping, outside the main tx.
	now2 := flowdb.NowISO()
	if !needsBootstrap {
		if _, err := db.Exec(
			`UPDATE tasks SET session_last_resumed = ? WHERE slug = ?`,
			now2, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: record resume: %v\n", err)
			return 1
		}
	}
	if _, err := db.Exec(
		`UPDATE workdirs SET last_used_at = ? WHERE path = ?`,
		now2, task.WorkDir,
	); err != nil {
		fmt.Fprintf(os.Stderr, "warning: bump workdir last_used_at: %v\n", err)
	}

	if needsBootstrap {
		fmt.Printf("Spawned %s (Codex will register its session id on startup)\n", task.Slug)
	} else {
		fmt.Printf("Resumed %s (session %s)\n", task.Slug, curSessionID.String)
	}
	return 0
}

// buildBootstrapPromptForKind dispatches to the right prompt variant
// based on task kind. For kind='playbook_run' the playbook variant is
// used; otherwise the regular task variant. Empty kind (legacy rows
// that somehow didn't migrate) falls through to the regular variant.
//
// The bootstrap prompt gets shell-quoted as a single positional argument to
// `codex`. Codex's SessionStart hook registers the generated session id.
// The session loads context in order: task brief + task updates, then
// (if any) project brief + project updates, then AGENTS.md guidance.
// Kept for callers (and tests) that don't track first-run state. New
// callers should use buildBootstrapPromptForKindV2 to opt into the
// first-run variant when relevant.
func buildBootstrapPromptForKind(slug, kind, playbookSlug string) string {
	return buildBootstrapPromptForKindV2(slug, kind, playbookSlug, false)
}

// buildBootstrapPromptForKindV2 is the kind-aware dispatcher with first-
// run awareness for playbook runs. When isFirstRun=true on a playbook
// run, a richer "capture-aggressive" prompt is emitted that nudges the
// session to harvest scripts, edge cases, and decision rules back into
// the live playbook brief / sidecar files.
func buildBootstrapPromptForKindV2(slug, kind, playbookSlug string, isFirstRun bool) string {
	if kind == "playbook_run" {
		return buildPlaybookRunBootstrapPrompt(slug, playbookSlug, isFirstRun)
	}
	return buildTaskBootstrapPrompt(slug)
}

// buildTaskBootstrapPrompt is the prompt for regular tasks.
func buildTaskBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the `flow` skill. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
			"2. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files listed under other: are sidecar references — load on demand when relevant, not eagerly.\n"+
			"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief AND every file under updates:. Files under other: are on-demand references.\n"+
			"4. Read AGENTS.md guidance already loaded by Codex, plus any nested AGENTS.md files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"5. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
		slug,
	)
}

// buildPlaybookRunBootstrapPrompt is the prompt for playbook-run tasks.
// Adds an explicit `flow show playbook <slug>` context-load step and
// frames the run's brief as an authoritative snapshot — the session
// must execute against that snapshot, not re-read the live playbook
// brief (which may drift between runs).
func buildPlaybookRunBootstrapPrompt(runSlug, playbookSlug string, isFirstRun bool) string {
	base := fmt.Sprintf(
		"You are running playbook `%s` as run `%s`. Do ALL of the following in order before executing anything:\n"+
			"1. Invoke the `flow` skill. This loads the operating manual that governs how this session works.\n"+
			"2. Run: flow show playbook %s. This shows the playbook's definition and recent runs — context only, not your instructions. Note any files listed under other: — they're sidecar references you can Read on demand if relevant; do not eagerly load them.\n"+
			"3. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files under other: are references for THIS run; load on demand when relevant. The brief is your authoritative instructions for this run — it was snapshotted from the playbook at the moment this run started. Execute against this, not the live playbook brief.\n"+
			"4. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief and every file under updates:. Files under other: are on-demand references.\n"+
			"5. Read AGENTS.md guidance already loaded by Codex, plus any nested AGENTS.md files under subdirectories you will modify.\n"+
			"6. Only then begin executing your brief.\n"+
			"\n"+
			"While executing: if the user adjusts the playbook's procedure during this run (e.g. 'let's always do X', 'change the approach for...', 'this step should also...'), pause and ask via AskUserQuestion whether to persist the change to the playbook's live brief.md so future runs benefit. Options: 'Persist to playbook' (Edit playbooks/%s/brief.md), 'Just this run' (no change to live playbook), 'Both — persist + log a note in playbooks/%s/updates/'. The run's own brief.md is a frozen snapshot — never edit it to change future behavior; that's what the live playbook brief is for. See flow skill §4.13 for the full pattern.",
		playbookSlug, runSlug, playbookSlug, playbookSlug, playbookSlug,
	)

	if !isFirstRun {
		return base
	}

	firstRunAddendum := fmt.Sprintf(
		"\n"+
			"\n"+
			"⚡ THIS IS THE FIRST RUN OF THIS PLAYBOOK ⚡\n"+
			"\n"+
			"The brief was written aspirationally; this run is where the actual procedure crystallizes. Be MORE proactive than usual about capturing back to the live playbook. Specifically:\n"+
			"\n"+
			"- When you write a script, command, or settle on a concrete decision rule that wasn't in the brief: don't wait for the user to ask. Pause and AskUserQuestion whether to capture it. Three capture targets:\n"+
			"    • 'Add to playbook brief' — append/edit the relevant section of playbooks/%s/brief.md so future runs see it inline\n"+
			"    • 'Save as sidecar file' — write to playbooks/%s/<topic>.md (e.g. decision-tree.md, sample-script.md, edge-cases.md). These get surfaced under `other:` in flow show playbook for future runs to load on demand\n"+
			"    • 'Just this run' — apply locally, don't change the playbook (rare; usually means it's run-specific)\n"+
			"- When you discover an edge case or signal worth watching: AskUserQuestion whether to add it to the 'Signals to watch for' section of the live brief.\n"+
			"- Before flow done at the end of the run, AskUserQuestion: 'Capture anything from this run back to the playbook before closing?' Options: 'Yes — walk me through what to capture' / 'No, close out as-is'. The 'walk me through' path: list candidate captures (scripts produced, decisions made, edge cases hit, commands you ended up using) and offer per-item via AskUserQuestion.\n"+
			"\n"+
			"After this run, the playbook should be substantially more concrete than the aspirational brief it started with. That's the point. Treat capture-back as a primary deliverable of the first run, not an afterthought.",
		playbookSlug, playbookSlug,
	)

	return base + firstRunAddendum
}

// buildBootstrapPrompt is a backwards-compat shim for old callers that
// pass only a slug. Now points at the regular-task variant. Tests still
// call this to verify the regular variant.
func buildBootstrapPrompt(slug string) string {
	return buildTaskBootstrapPrompt(slug)
}

// buildTabTitle returns a short iTerm tab title. Project-scoped tasks get
// "<project-slug>/<task-slug>"; floating tasks get just "<task-slug>".
// Titles longer than 30 runes are truncated with a trailing ellipsis.
func buildTabTitle(project *flowdb.Project, task *flowdb.Task) string {
	raw := task.Slug
	if project != nil {
		raw = project.Slug + "/" + task.Slug
	}
	const maxLen = 30
	runes := []rune(raw)
	if len(runes) > maxLen {
		return string(runes[:maxLen-1]) + "…"
	}
	return raw
}

// findTask resolves a user-supplied ref to exactly one non-archived task.
// Exact alias match first, then exact slug match.
func findTask(db *sql.DB, query string) (*flowdb.Task, int) {
	t, err := ResolveTask(db, query, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, 1
	}
	return t, 0
}
