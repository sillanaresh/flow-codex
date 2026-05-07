package app

import (
	"bytes"
	"encoding/json"
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookSessionStartNoFlowTaskEmitsAmbientHint pins the contract for
// ad-hoc sessions (e.g. bare `codex` with no FLOW_TASK): the hook must
// emit a value-prop framing that names flow, instructs Skill-tool
// invocation, and explicitly disclaims any "substantive" gate. The
// skill — not the hook — owns the decision of whether to offer a task,
// save a KB entry, or stay quiet.
func TestHookSessionStartNoFlowTaskEmitsAmbientHint(t *testing.T) {
	t.Setenv("FLOW_TASK", "")
	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse hook output: %v\nraw: %s", err, out)
	}
	if parsed.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", parsed.HookSpecificOutput.HookEventName)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	for _, want := range []string{
		"already tracks",
		"`flow` skill",
		"knowledge base",
		"AskUserQuestion",
		"existing flow task",
		"create a new one",
		"~/.flow/kb/",
		"don't recognize",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("ambient hint missing %q; got:\n%s", want, ctx)
		}
	}
	// The hint must NOT mention "substantive" — naming the past gate
	// just primes Codex to think about gating again. Affirmative
	// framing only: load the skill, confirm task binding, proceed.
	if strings.Contains(ctx, "substantive") {
		t.Errorf("ambient hint must not mention 'substantive'; got:\n%s", ctx)
	}
	// Must NOT include task-specific instructions (no register-session,
	// no slug-bound reload).
	if strings.Contains(ctx, "flow register-session") {
		t.Errorf("ambient hint should not instruct register-session (no FLOW_TASK bound):\n%s", ctx)
	}
}

// TestHookSessionStartRequiresSkillInvocation pins the invariant that
// the injected additionalContext explicitly instructs the session to
// invoke the flow skill as its first action, and
// mentions the task slug so the agent has something anchor-visible.
func TestHookSessionStartRequiresSkillInvocation(t *testing.T) {
	t.Setenv("FLOW_TASK", "some-slug")
	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})

	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse hook output: %v\nraw: %s", err, out)
	}
	if parsed.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", parsed.HookSpecificOutput.HookEventName)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "`flow` skill") {
		t.Errorf("additionalContext must name the `flow` skill, got:\n%s", ctx)
	}
	// Self-registration is gone — the UUID is pre-allocated by `flow do`.
	// Make sure we don't regress by re-introducing it here.
	if strings.Contains(ctx, "register-session") {
		t.Errorf("additionalContext should not mention register-session (pre-allocated by flow do):\n%s", ctx)
	}
	if !strings.Contains(ctx, "some-slug") {
		t.Errorf("additionalContext should mention the task slug, got:\n%s", ctx)
	}
}

// TestHookUserPromptSubmitAdHocEmitsSkillNudge pins the contract for
// ad-hoc sessions (FLOW_TASK unset): every prompt must produce a
// hookSpecificOutput payload that nudges Codex to invoke the flow
// skill and apply §4.14 — without keyword gating, since users won't
// say "create a task" themselves.
func TestHookUserPromptSubmitAdHocEmitsSkillNudge(t *testing.T) {
	t.Setenv("FLOW_TASK", "")
	out := captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse hook output: %v\nraw: %s", err, out)
	}
	if parsed.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Errorf("hookEventName = %q, want UserPromptSubmit",
			parsed.HookSpecificOutput.HookEventName)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	for _, want := range []string{
		"already tracks",
		"`flow` skill",
		"knowledge base",
		"AskUserQuestion",
		"existing flow task",
		"create a new one",
		"~/.flow/kb/",
		"don't recognize",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("UserPromptSubmit ambient hint missing %q; got:\n%s", want, ctx)
		}
	}
	// Must NOT mention "substantive" — see SessionStart test for the
	// rationale (don't prime Codex on the rejected gate).
	if strings.Contains(ctx, "substantive") {
		t.Errorf("UserPromptSubmit hint must not mention 'substantive'; got:\n%s", ctx)
	}
}

// TestHookUserPromptSubmitBoundIsNoOp pins the bound-session contract:
// when FLOW_TASK is set, the hook exits 0 with no output. The
// SessionStart hook already loaded full task context; repeating it on
// every prompt would be noisy and expensive.
func TestHookUserPromptSubmitBoundIsNoOp(t *testing.T) {
	t.Setenv("FLOW_TASK", "some-slug")
	out := captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty stdout when FLOW_TASK is set, got:\n%s", out)
	}
}

// TestBuildBootstrapPromptInvokesSkill pins the same invariant for the
// fresh-spawn prompt used by `flow do` (the hook only covers resume).
func TestBuildBootstrapPromptInvokesSkill(t *testing.T) {
	prompt := buildBootstrapPrompt("task-x")
	if !strings.Contains(prompt, "flow skill") && !strings.Contains(prompt, "`flow` skill") {
		t.Errorf("bootstrap prompt must name the flow skill:\n%s", prompt)
	}
	if strings.Contains(prompt, "register-session") {
		t.Errorf("bootstrap prompt should not mention register-session (pre-allocated by flow do):\n%s", prompt)
	}
	if !strings.Contains(prompt, "task-x") {
		t.Errorf("bootstrap prompt must mention the task slug")
	}
}

func TestHookSessionStartRegistersCodexSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)
	t.Setenv("FLOW_TASK", "bound-task")

	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "Bound Task", "--slug", "bound-task"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}

	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	_, _ = w.Write([]byte(`{"session_id":"codex-session-1","transcript_path":"` + filepath.Join(tmp, "session.jsonl") + `","cwd":"` + tmp + `","source":"startup"}`))
	_ = w.Close()
	t.Cleanup(func() { os.Stdin = oldStdin })

	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !bytes.Contains([]byte(out), []byte("SessionStart")) {
		t.Fatalf("expected hook output, got %s", out)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "bound-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != "codex-session-1" {
		t.Fatalf("session_id = %+v, want codex-session-1", task.SessionID)
	}
	if !task.TranscriptPath.Valid || !strings.Contains(task.TranscriptPath.String, "session.jsonl") {
		t.Fatalf("transcript_path = %+v, want session.jsonl", task.TranscriptPath)
	}
}

func TestHookSessionStartResumeUpdatesLastResumed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)
	t.Setenv("FLOW_TASK", "resume-task")

	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "Resume Task", "--slug", "resume-task"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}

	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"old-session", flowdb.NowISO(), "resume-task",
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	_, _ = w.Write([]byte(`{"session_id":"new-session","transcript_path":"` + filepath.Join(tmp, "resume.jsonl") + `","source":"resume"}`))
	_ = w.Close()
	t.Cleanup(func() { os.Stdin = oldStdin })

	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "SessionStart") {
		t.Fatalf("expected hook output, got %s", out)
	}

	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "resume-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != "new-session" {
		t.Fatalf("session_id = %+v, want new-session", task.SessionID)
	}
	if !task.SessionLastResumed.Valid {
		t.Fatalf("session_last_resumed should be set on resume registration")
	}
	if !task.TranscriptPath.Valid || !strings.Contains(task.TranscriptPath.String, "resume.jsonl") {
		t.Fatalf("transcript_path = %+v, want resume.jsonl", task.TranscriptPath)
	}
}
