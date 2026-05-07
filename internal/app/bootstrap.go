package app

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// newUUID returns a new UUID v4 in the standard 8-4-4-4-12 hex format.
// Kept for tests and older migration helpers. Codex sessions generate their
// own IDs and register them through the SessionStart hook.
//
// Overridable in tests so concurrency tests can inject deterministic IDs.
var newUUID = func() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand read: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // v4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// EncodeCwdForClaude encodes an absolute cwd path for Claude Code's
// ~/.claude/projects/<dir> directory naming. Used by `flow transcript`
// only as a backward-compatible fallback for old Claude-backed tasks.
//
// Rule (derived empirically by scanning ~/.claude/projects/* against the
// original cwd recorded inside each dir's *.jsonl files — CC's source is
// not public): the characters `/`, `.`, and `_` are each replaced by `-`.
// Other characters pass through unchanged. Samples:
//
//	/Users/alice/code/myapp                      → -Users-alice-code-myapp
//	/Users/alice/.flow/tasks/foo/workspace       → -Users-alice--flow-tasks-foo-workspace
//	/Users/alice/.cache/work/_default            → -Users-alice--cache-work--default
//	/Users/alice/monorepo/.../1_input_instance   → ...-1-input-instance
//
// If CC introduces a new substitution in a future version, add the char
// here and update TestEncodeCwdForClaude with a sample confirming it.
func EncodeCwdForClaude(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-", "_", "-")
	return r.Replace(cwd)
}
