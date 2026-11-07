package main_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/modelswitch"
)

// `cenci daemon model-switches` (#1179), end to end: `cenci notify` writes the
// hook-side store with no daemon at all, and a fresh `cenci daemon
// model-switches` process reads it back the way Phase 9 does.

const (
	msModelA = "claude-sonnet-5-5"
	msModelB = "claude-opus-5-5"

	msUnknownLine = "Model switch status unknown\n"
)

// msState is an isolated, owner-only state home.
func msState(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	return state
}

func msStore(state string) string { return filepath.Join(state, "cenci", "model-switch") }

// msCLI runs `cenci daemon model-switches args...` with XDG_STATE_HOME set to
// state and CLAUDE_CODE_SESSION_ID set to sessionEnv; an empty sessionEnv means
// the variable is explicitly cleared, never inherited.
func msCLI(t *testing.T, state, sessionEnv string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"daemon", "model-switches"}, args...)...)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "CLAUDE_CODE_SESSION_ID="+sessionEnv)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exit = exitErr.ExitCode()
	default:
		t.Fatalf("run model-switches: %v", err)
	}
	return out.String(), errBuf.String(), exit
}

// msNotify delivers one hook payload through the real `cenci notify` against a
// socket nobody listens on, so no daemon is involved.
func msNotify(t *testing.T, state, payload string) {
	t.Helper()
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", filepath.Join(t.TempDir(), "no-daemon.sock"))
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "XDG_CONFIG_HOME="+t.TempDir(), "CLAUDE_CODE_SESSION_ID=", "CENCI_SANDBOX=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, out)
	}
}

func msPayload(session, from, to, source, agentID, agentType string) string {
	p := fmt.Sprintf(`{"hook_event_name":"PostModelSwitch","session_id":%q,"from_model":%q,"to_model":%q,"source":%q`, session, from, to, source)
	if agentID != "" {
		p += fmt.Sprintf(`,"agent_id":%q,"agent_type":%q`, agentID, agentType)
	}
	return p + "}"
}

func requireUnknown(t *testing.T, state, stdout, stderr string, exit int) {
	t.Helper()
	if exit != 1 {
		t.Errorf("exit = %d, want 1 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	if stdout != msUnknownLine {
		t.Errorf("stdout = %q, want exactly %q", stdout, msUnknownLine)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected a reason on stderr")
	}
	if strings.Contains(stderr, state) || strings.Contains(stdout, state) {
		t.Errorf("output leaks the state path: stdout %q stderr %q", stdout, stderr)
	}
}

// TestModelSwitchesPersistWithoutADaemon covers the persistence AC: notify
// writes the record with no daemon running, and a fresh process reads it back
// as the exact PR lines, with the session id taken from CLAUDE_CODE_SESSION_ID.
func TestModelSwitchesPersistWithoutADaemon(t *testing.T) {
	state := msState(t)
	msNotify(t, state, msPayload("persist-1", msModelA, msModelB, "auto", "", ""))
	msNotify(t, state, msPayload("persist-1", msModelB, msModelA, "sdk", "sub1", "cenci:reviewer"))

	stdout, stderr, exit := msCLI(t, state, "persist-1")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit = %d, stderr = %q, stdout = %q", exit, stderr, stdout)
	}
	want := "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session) — fallback\n" +
		"Model switch: claude-opus-5-5 → claude-sonnet-5-5 (subagent cenci:reviewer)\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
}

// TestModelSwitchesSurviveTheTTL covers the paneless-session TTL half of the
// persistence AC: a record aged 3h (past the 2h session TTL) is still reported.
func TestModelSwitchesSurviveTheTTL(t *testing.T) {
	state := msState(t)
	msNotify(t, state, msPayload("ttl-1", msModelA, msModelB, "auto", "", ""))
	age := time.Now().Add(-3 * time.Hour)
	record := filepath.Join(msStore(state), "ttl-1")
	old := strconv.FormatInt(age.Unix(), 10)
	if err := os.WriteFile(record, []byte(old+" auto "+msModelA+" "+msModelB+" main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Age the file after the last write: a write resets the mtime.
	if err := os.Chtimes(record, age, age); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(record); err != nil || time.Since(info.ModTime()) < 2*time.Hour {
		t.Fatalf("record not aged past the TTL (stat err = %v)", err)
	}

	stdout, stderr, exit := msCLI(t, state, "ttl-1")
	want := "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session) — fallback\n"
	if exit != 0 || stdout != want {
		t.Errorf("exit = %d, stdout = %q (stderr %q), want the 3h-old switch reported", exit, stdout, stderr)
	}
}

func TestModelSwitchesSessionFlagOverridesTheEnvironment(t *testing.T) {
	state := msState(t)
	msNotify(t, state, msPayload("from-flag", msModelA, msModelB, "sdk", "", ""))
	msNotify(t, state, msPayload("from-env", msModelB, msModelA, "sdk", "", ""))

	stdout, _, exit := msCLI(t, state, "from-env", "--session", "from-flag")
	if exit != 0 || stdout != "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session)\n" {
		t.Errorf("--session did not win over the environment: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = msCLI(t, state, "from-env")
	if exit != 0 || stdout != "Model switch: claude-opus-5-5 → claude-sonnet-5-5 (main session)\n" {
		t.Errorf("the environment session id was not used: exit %d, stdout %q", exit, stdout)
	}
}

// TestModelSwitchesOverflowPrintsMoreLine covers the cap at the CLI: 8 lines in
// order, then `Model switch: +<n> more`.
func TestModelSwitchesOverflowPrintsMoreLine(t *testing.T) {
	state := msState(t)
	for i := 0; i < modelswitch.MaxSwitches+3; i++ {
		msNotify(t, state, msPayload("cap-1", fmt.Sprintf("model-%d", i), fmt.Sprintf("model-%d", i+1), "auto", "", ""))
	}
	stdout, _, exit := msCLI(t, state, "cap-1")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if exit != 0 || len(lines) != modelswitch.MaxSwitches+1 {
		t.Fatalf("exit %d, %d lines, want %d:\n%s", exit, len(lines), modelswitch.MaxSwitches+1, stdout)
	}
	if lines[0] != "Model switch: model-0 → model-1 (main session) — fallback" {
		t.Errorf("first line = %q", lines[0])
	}
	if lines[len(lines)-1] != "Model switch: +3 more" {
		t.Errorf("last line = %q, want the overflow line", lines[len(lines)-1])
	}
}

// TestModelSwitchesNeutralLabelUnlessAuto covers the fallback-label edge case
// at the CLI.
func TestModelSwitchesNeutralLabelUnlessAuto(t *testing.T) {
	state := msState(t)
	msNotify(t, state, msPayload("label-1", msModelA, msModelB, "command", "", ""))
	stdout, _, exit := msCLI(t, state, "label-1")
	if exit != 0 || stdout != "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session)\n" {
		t.Errorf("exit %d, stdout %q, want the neutral label", exit, stdout)
	}
}

// TestModelSwitchesNeverPrintsRejectedStrings covers the validation edge case
// through notify and the CLI: the raw invalid value never reaches the output.
func TestModelSwitchesNeverPrintsRejectedStrings(t *testing.T) {
	state := msState(t)
	msNotify(t, state, `{"hook_event_name":"PostModelSwitch","session_id":"bad-1","from_model":"opus\u001b[31m\nevil","to_model":"claude-opus-5-5","source":"auto"}`)

	stdout, _, exit := msCLI(t, state, "bad-1")
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	if strings.Contains(stdout, "evil") || strings.Contains(stdout, "\x1b") {
		t.Errorf("a rejected string reached the output: %q", stdout)
	}
	if want := "Model switch: (invalid model id) → claude-opus-5-5 (main session) — fallback\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestModelSwitchesValidSessionWithNoRecordIsEmptySuccess pins that "no record"
// is not "unknown": exit 0 with no output at all, and the read creates nothing.
func TestModelSwitchesValidSessionWithNoRecordIsEmptySuccess(t *testing.T) {
	t.Run("no store at all", func(t *testing.T) {
		state := msState(t)
		stdout, stderr, exit := msCLI(t, state, "never-switched")
		if exit != 0 || stdout != "" || stderr != "" {
			t.Errorf("exit %d, stdout %q, stderr %q, want empty success", exit, stdout, stderr)
		}
		if _, err := os.Lstat(filepath.Join(state, "cenci")); !os.IsNotExist(err) {
			t.Errorf("the reader created state (err = %v)", err)
		}
	})
	t.Run("store with other sessions only", func(t *testing.T) {
		state := msState(t)
		msNotify(t, state, msPayload("someone-else", msModelA, msModelB, "auto", "", ""))
		stdout, stderr, exit := msCLI(t, state, "never-switched")
		if exit != 0 || stdout != "" || stderr != "" {
			t.Errorf("exit %d, stdout %q, stderr %q, want empty success", exit, stdout, stderr)
		}
	})
}

// TestModelSwitchesUnknownCases covers every unknown input: the exact stdout
// line, exit 1 and a path-free reason on stderr.
func TestModelSwitchesUnknownCases(t *testing.T) {
	seeded := func(t *testing.T) string {
		state := msState(t)
		msNotify(t, state, msPayload("sess-1", msModelA, msModelB, "auto", "", ""))
		return state
	}
	cases := []struct {
		name string
		run  func(t *testing.T) (state, stdout, stderr string, exit int)
	}{
		{"environment variable unset", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			o, e, x := msCLI(t, state, "")
			return state, o, e, x
		}},
		{"environment session id invalid", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			o, e, x := msCLI(t, state, "../sess-1")
			return state, o, e, x
		}},
		{"explicit empty --session does not fall back to the environment", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			o, e, x := msCLI(t, state, "sess-1", "--session", "")
			return state, o, e, x
		}},
		{"invalid --session", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			o, e, x := msCLI(t, state, "", "--session", "a/b")
			return state, o, e, x
		}},
		{"untrusted 0777 store directory", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			if err := os.Chmod(msStore(state), 0o777); err != nil {
				t.Fatal(err)
			}
			o, e, x := msCLI(t, state, "sess-1")
			return state, o, e, x
		}},
		{"corrupt record", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			if err := os.WriteFile(filepath.Join(msStore(state), "sess-1"), []byte("not a record\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			o, e, x := msCLI(t, state, "sess-1")
			return state, o, e, x
		}},
		{"older format record", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			if err := os.WriteFile(filepath.Join(msStore(state), "sess-1"), []byte("1700000000 claude-sonnet-5-5 claude-opus-5-5 main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			o, e, x := msCLI(t, state, "sess-1")
			return state, o, e, x
		}},
		{"incomplete marker", func(t *testing.T) (string, string, string, int) {
			state := seeded(t)
			if err := os.WriteFile(filepath.Join(msStore(state), "sess-1.incomplete"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			o, e, x := msCLI(t, state, "sess-1")
			return state, o, e, x
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, stdout, stderr, exit := tc.run(t)
			requireUnknown(t, state, stdout, stderr, exit)
		})
	}
}

// TestModelSwitchesFailedStdoutWriteExitsOne pins that a record that exists but
// cannot be written out exits 1 rather than 0: the Phase 9 caller maps exit 1 to
// `Model switch status unknown`, and a truncated or empty exit-0 output would be
// read as the no-record case. Stdout is /dev/full, where every write fails with
// ENOSPC; the harness has no portable equivalent, so the test is skipped, with
// that reason, only where /dev/full does not exist.
func TestModelSwitchesFailedStdoutWriteExitsOne(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no /dev/full on this platform (%v): the failed-stdout exit mapping is covered on Linux only", err)
	}
	t.Cleanup(func() { _ = full.Close() })

	state := msState(t)
	msNotify(t, state, msPayload("full-1", msModelA, msModelB, "auto", "", ""))

	cmd := exec.Command(binaryPath, "daemon", "model-switches")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "CLAUDE_CODE_SESSION_ID=full-1")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = full, &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("err = %v, want exit 1 after a failed stdout write (stderr %q)", err, stderr.String())
	}
	if strings.TrimSpace(stderr.String()) == "" {
		t.Error("expected a reason on stderr")
	}
	if strings.Contains(stderr.String(), state) {
		t.Errorf("stderr leaks the state path: %q", stderr.String())
	}
}

func TestModelSwitchesUsageErrorsExitTwo(t *testing.T) {
	state := msState(t)
	for name, args := range map[string][]string{
		"positional argument": {"sess-1"},
		"unknown flag":        {"--bogus"},
		"flag without value":  {"--session"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, exit := msCLI(t, state, "sess-1", args...)
			if exit != 2 {
				t.Errorf("exit = %d, want 2 (stdout %q, stderr %q)", exit, stdout, stderr)
			}
			if stdout == msUnknownLine {
				t.Error("a usage error must not print the unknown line")
			}
		})
	}
}
