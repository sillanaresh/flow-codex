package app

import (
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"os"
	"path/filepath"
	"testing"
)

// TestE2EFullRoundtrip exercises the full command surface in the order a
// user would hit it for a realistic session: init, add project, add task
// under the project, do (bootstrap + spawn), show both, list both, waiting
// set/clear, priority change, update file drop, done, archive, unarchive,
// workdir registry.
//
// Mocks codexRunner and iterm.Runner so nothing actually spawns
// codex or osascript. Uses a temp FLOW_ROOT so the user's real ~/.flow is
// untouched.
func TestE2EFullRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	flowRoot := filepath.Join(tmp, "flow")
	t.Setenv("FLOW_ROOT", flowRoot)
	t.Setenv("HOME", tmp)

	// Fake repo that serves as the project's work_dir.
	repo := filepath.Join(tmp, "code", "budgeting-app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	// Stub osascript for the whole test.
	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	// Stub the headless codex runner so cmdDone doesn't try to invoke
	// the real codex CLI for its post-flip KB sweep.
	oldCodex := codexRunner
	codexRunner = func(task *flowdb.Task, projectSlug, prompt string) error { return nil }
	t.Cleanup(func() { codexRunner = oldCodex })

	// Pin the Codex-generated session id we simulate via the SessionStart hook.
	const fixedSID = "e2e-session-uuid"

	step := func(name string, rc int) {
		t.Helper()
		if rc != 0 {
			t.Fatalf("%s: rc=%d", name, rc)
		}
	}

	// 1. init — creates tree, db, installs skill
	step("init", cmdInit(nil))
	if _, err := os.Stat(filepath.Join(flowRoot, "flow.db")); err != nil {
		t.Fatalf("flow.db not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(flowRoot, "projects")); err != nil {
		t.Fatalf("projects dir not created: %v", err)
	}

	// 2. add project
	step("add project", cmdAdd([]string{"project", "Budgeting App Revamp", "--work-dir", repo}))
	if _, err := os.Stat(filepath.Join(flowRoot, "projects", "budgeting-app-revamp", "brief.md")); err != nil {
		t.Fatalf("project brief.md not created: %v", err)
	}

	// 3. add task under the project
	step("add task", cmdAdd([]string{"task", "Fix Auth Token Expiry",
		"--project", "budgeting-app-revamp"}))
	taskDir := filepath.Join(flowRoot, "tasks", "fix-auth-token-expiry")
	if _, err := os.Stat(filepath.Join(taskDir, "brief.md")); err != nil {
		t.Fatalf("task brief.md not created: %v", err)
	}

	// 4. add a floating task (auto workspace)
	step("add floating task", cmdAdd([]string{"task", "Scratch Investigation"}))
	scratchDir := filepath.Join(flowRoot, "tasks", "scratch-investigation", "workspace")
	if _, err := os.Stat(scratchDir); err != nil {
		t.Fatalf("floating task workspace not created: %v", err)
	}

	// 5. do — spawns a fresh Codex tab. Codex generates the session id,
	// then the SessionStart hook registers it back into flow.
	step("do", cmdDo([]string{"fix-auth-token-expiry"}))
	db, err := flowdb.OpenDB(filepath.Join(flowRoot, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task, err := flowdb.GetTask(db, "fix-auth-token-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}

	// 5b. Write real Codex jsonl content and simulate the SessionStart
	// hook registration so transcript and resume can find it.
	{
		sessionDir := filepath.Join(tmp, ".codex", "sessions", "2026", "05", "07")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		sessionFile := filepath.Join(sessionDir, "rollout-2026-05-07T10-00-00-"+fixedSID+".jsonl")
		content := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}}` + "\n" +
			`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi there!"}]}}` + "\n"
		if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
			t.Fatalf("write session jsonl: %v", err)
		}
		registerCodexSession("fix-auth-token-expiry", codexHookInput{
			SessionID:      fixedSID,
			TranscriptPath: sessionFile,
			CWD:            repo,
			Source:         "startup",
		})
	}
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if !task.SessionID.Valid || task.SessionID.String != fixedSID {
		t.Errorf("session_id after hook registration: got %+v, want %s", task.SessionID, fixedSID)
	}

	// 5c. transcript — should succeed now that session exists.
	step("transcript", cmdTranscript([]string{"fix-auth-token-expiry"}))

	// 6. do again — now session_id is populated, should spawn codex resume.
	step("do resume", cmdDo([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.SessionID.String != fixedSID {
		t.Errorf("session_id should be preserved across resume: got %q", task.SessionID.String)
	}
	if !task.SessionLastResumed.Valid {
		t.Error("session_last_resumed should be set after resume")
	}

	// 7. show task
	step("show task", cmdShow([]string{"task", "fix-auth-token-expiry"}))

	// 8. show project
	step("show project", cmdShow([]string{"project", "budgeting-app-revamp"}))

	// 9. list tasks — should include both
	step("list tasks", cmdList([]string{"tasks"}))

	// 10. list tasks filtered by project
	step("list tasks --project", cmdList([]string{"tasks", "--project", "budgeting-app-revamp"}))

	// 11. list projects
	step("list projects", cmdList([]string{"projects"}))

	// 12. waiting
	step("waiting set", cmdWaiting([]string{"fix-auth-token-expiry", "Alice review"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if !task.WaitingOn.Valid || task.WaitingOn.String != "Alice review" {
		t.Errorf("waiting_on = %v, want Alice review", task.WaitingOn)
	}

	step("waiting clear", cmdWaiting([]string{"fix-auth-token-expiry", "--clear"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.WaitingOn.Valid {
		t.Errorf("waiting_on should be cleared, got %v", task.WaitingOn)
	}

	// 13. priority
	step("priority", cmdPriority([]string{"fix-auth-token-expiry", "high"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.Priority != "high" {
		t.Errorf("priority = %q, want high", task.Priority)
	}

	// 14. drop an update file (skill-written, we simulate with os.WriteFile)
	updatePath := filepath.Join(taskDir, "updates", "2026-04-11-first-milestone.md")
	if err := os.WriteFile(updatePath, []byte("# First milestone\n\nFinished the token refresh endpoint.\n"), 0o644); err != nil {
		t.Fatalf("write update: %v", err)
	}

	// 15. show task again — should list the update file
	// (we can't easily capture stdout here, but we can verify the command returns 0
	// and the file is on disk)
	step("show task with update", cmdShow([]string{"task", "fix-auth-token-expiry"}))

	// 16. done
	step("done", cmdDone([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.Status != "done" {
		t.Errorf("status after done = %q, want done", task.Status)
	}
	// session_id should still be present (flow done is DB-only)
	if task.SessionID.String != "e2e-session-uuid" {
		t.Errorf("session_id cleared by done: %v", task.SessionID)
	}

	// 17. archive
	step("archive", cmdArchive([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if !task.ArchivedAt.Valid {
		t.Errorf("archived_at not set after archive")
	}

	// 18. list tasks (archived should be hidden)
	step("list tasks post-archive", cmdList([]string{"tasks"}))
	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Slug == "fix-auth-token-expiry" && !task.ArchivedAt.Valid {
			t.Errorf("archived task leaked into default list")
		}
	}

	// 19. unarchive
	step("unarchive", cmdUnarchive([]string{"fix-auth-token-expiry"}))
	task, _ = flowdb.GetTask(db, "fix-auth-token-expiry")
	if task.ArchivedAt.Valid {
		t.Errorf("archived_at not cleared after unarchive: %v", task.ArchivedAt)
	}

	// 20. workdir list — the project's work_dir should have been auto-registered
	step("workdir list", cmdWorkdir([]string{"list"}))
	wd, err := flowdb.GetWorkdir(db, repo)
	if err != nil {
		t.Fatalf("repo not auto-registered as workdir: %v", err)
	}
	if wd == nil {
		t.Fatal("GetWorkdir returned nil for auto-registered path")
	}
}
