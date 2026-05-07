// Package iterm provides iTerm2 tab spawning via osascript.
package iterm

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Runner is the function used to execute osascript.
// Tests override this to capture arguments without invoking osascript.
var Runner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// Available reports whether macOS can resolve the iTerm application.
// Conductor and other hosts may not set TERM_PROGRAM; falling back to
// iTerm in that case gives a confusing AppleScript syntax error when
// iTerm is not installed.
var Available = func() bool {
	for _, app := range []string{"iTerm2", "iTerm"} {
		cmd := exec.Command("osascript", "-e", fmt.Sprintf(`id of application "%s"`, app))
		if err := cmd.Run(); err == nil {
			return true
		}
	}
	return false
}

// SpawnTab opens a new iTerm2 tab with the given title, cwd, and command.
// envVars are attached as an inline prefix to `command` only — so they
// are present in the spawned process's environment but do NOT persist in
// the tab's shell after the command exits.
//
// The typed line is prefixed with a single space so shells with
// `histignorespace` (zsh) or `HISTCONTROL=ignorespace`/`ignoreboth`
// (bash) skip writing it to the shared history file. Shells without
// that opt-in will still record the line — see README for the one-line
// shell config that turns it on.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	envPrefix := ""
	if len(envVars) > 0 {
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(envVars))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, ShellQuote(envVars[k])))
		}
		envPrefix = strings.Join(parts, " ") + " "
	}
	fullCommand := fmt.Sprintf(" cd %s && %s%s", ShellQuote(cwd), envPrefix, command)
	safeCommand := escapeAppleScriptString(fullCommand)
	safeTitle := escapeAppleScriptString(title)

	script := fmt.Sprintf(`tell application "iTerm2"
  tell current window
    set newTab to (create tab with default profile)
    tell current session of newTab
      set name to "%s"
      write text "%s"
    end tell
  end tell
end tell
`, safeTitle, safeCommand)

	return Runner([]string{"-e", script})
}

// ShellQuote wraps s in single quotes with proper escaping.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
