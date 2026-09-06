package launcher

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// -- ticket #1087: forward planning.attended as CENCI_ATTENDED -------------
//
// #1086's planning.attended flag lives in the host's
// ~/.config/cenci/config.json. The sandbox mounts /home/dev as a per-repo
// named volume, so the container's own copy of that path is a container-local
// file no host process ever writes -- without this forward, the switch whose
// entire purpose is "I expect to be asked" silently does nothing for every
// sandboxed session. The fix forwards the RESOLVED flag as an env var at exec
// time (never a bind mount of the host fleet config, whose temp-file-then-
// rename write path would pin a stale inode into the container for the
// container's whole lifetime).
//
// CENCI_ATTENDED is always emitted explicitly as "1" or "0" -- absent must
// stay meaningful as its own third state for #1088's resolution order, which
// reads an absent variable as "no launcher resolved this; go ask the host
// config yourself".

// writeFleetConfig points the fleet-config resolver (XDG_CONFIG_HOME, read by
// run.DefaultConfigPath) at a temp dir and writes body to
// <tmp>/cenci/config.json. body == "" writes no file at all, exercising the
// missing-config path.
func writeFleetConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if body == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "cenci"), 0o700); err != nil {
		t.Fatalf("mkdir cenci config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cenci", "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write fleet config: %v", err)
	}
}

// clearAttendedEnv removes CENCI_ATTENDED from the process env for the test,
// restoring the original afterwards. t.Setenv cannot unset, so it is used
// only to register the restore; the Unsetenv immediately after is what
// produces the absent third state a host (non-dispatch) run really has.
func clearAttendedEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CENCI_ATTENDED", "")
	if err := os.Unsetenv("CENCI_ATTENDED"); err != nil {
		t.Fatalf("unset CENCI_ATTENDED: %v", err)
	}
}

// attendedTokens returns every "-e CENCI_ATTENDED=<v>" value assembleExecEnv
// emitted, in order. A slice (not a single value) so a duplicate forward is a
// visible failure rather than a silently shadowed one -- with two -e tokens
// for the same name, the container runtime keeps the last, so a test reading
// only the first would pass while the container saw the other value.
func attendedTokens(args []string) []string {
	var values []string
	for i := 0; i < len(args); i++ {
		if args[i] != "-e" || i+1 >= len(args) {
			continue
		}
		if v, ok := strings.CutPrefix(args[i+1], "CENCI_ATTENDED="); ok {
			values = append(values, v)
		}
	}
	return values
}

// requireAttended asserts assembleExecEnv emitted exactly one CENCI_ATTENDED
// token, carrying want.
func requireAttended(t *testing.T, agent, want string) {
	t.Helper()
	args := assembleExecEnv(agent)
	got := attendedTokens(args)
	if len(got) != 1 {
		t.Fatalf("assembleExecEnv(%q) emitted %d CENCI_ATTENDED tokens, want exactly 1; got: %v", agent, len(got), args)
	}
	if got[0] != want {
		t.Errorf("assembleExecEnv(%q) forwarded CENCI_ATTENDED=%s, want CENCI_ATTENDED=%s", agent, got[0], want)
	}
}

// TestAssembleExecEnv_AttendedTrue covers the headline AC: with
// planning.attended true on the host, an exec session carries
// CENCI_ATTENDED=1.
func TestAssembleExecEnv_AttendedTrue(t *testing.T) {
	clearAttendedEnv(t)
	writeFleetConfig(t, `{"planning": {"attended": true}}`)

	for _, agent := range []string{"claude", "codex", "opencode"} {
		requireAttended(t, agent, "1")
	}
}

// TestAssembleExecEnv_AttendedFalseOrAbsent_ExplicitZero covers the AC that
// the false and absent host states both produce an EXPLICIT
// CENCI_ATTENDED=0, never an unset variable: absent is #1088's third state
// (a host run with no launcher), so a launcher that resolved the flag and
// found it off must say so rather than leave the variable out.
func TestAssembleExecEnv_AttendedFalseOrAbsent_ExplicitZero(t *testing.T) {
	cases := map[string]string{
		"explicit false":        `{"planning": {"attended": false}}`,
		"planning block absent": `{"dispatch": {"planRefined": true}}`,
		"empty config":          `{}`,
		"no config file":        "",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			clearAttendedEnv(t)
			writeFleetConfig(t, body)
			requireAttended(t, "claude", "0")
		})
	}
}

// TestAssembleExecEnv_MalformedFleetConfig_ResolvesZero covers the AC that a
// broken fleet config must never make the sandbox unusable: it resolves to
// CENCI_ATTENDED=0 and the launch proceeds. Both a malformed whole file and a
// non-bool attended value (QueryPlanningAttended's two distinct error shapes,
// including the explicit-null case its doc comment calls out) are covered.
func TestAssembleExecEnv_MalformedFleetConfig_ResolvesZero(t *testing.T) {
	cases := map[string]string{
		"malformed json":    `{"planning": `,
		"non-bool attended": `{"planning": {"attended": "yes"}}`,
		"null attended":     `{"planning": {"attended": null}}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			clearAttendedEnv(t)
			writeFleetConfig(t, body)
			requireAttended(t, "claude", "0")
		})
	}
}

// TestAssembleExecEnv_DispatchPinnedEnvWins covers the defense-in-depth AC:
// a session `cenci dispatch` launches carries CENCI_ATTENDED=0 even when the
// host flag is true. dispatch pins the variable in the spawned tmux window's
// own environment (run.Opts.Unattended, watch/internal/run/run.go), so the
// launcher must honor an explicitly-set value over the host config -- a
// dispatched session that routed into an interactive AskUserQuestion inside a
// detached tmux window would wait forever with the ticket stuck on Working.
func TestAssembleExecEnv_DispatchPinnedEnvWins(t *testing.T) {
	writeFleetConfig(t, `{"planning": {"attended": true}}`)
	t.Setenv("CENCI_ATTENDED", "0")

	requireAttended(t, "claude", "0")
}

// TestAssembleExecEnv_ExplicitEnvOneWins pins the other direction of the same
// precedence rule: an explicit CENCI_ATTENDED=1 in the launcher's own
// environment is honored even when the host config is off or unreadable, so
// the value a caller pinned is never silently reinterpreted.
func TestAssembleExecEnv_ExplicitEnvOneWins(t *testing.T) {
	writeFleetConfig(t, `{"planning": {"attended": false}}`)
	t.Setenv("CENCI_ATTENDED", "1")

	requireAttended(t, "claude", "1")
}

// TestAssembleExecEnv_UnrecognizedEnvValue_FoldsToZero pins that any non-empty
// pinned value other than the exact "1" resolves to the restrictive "0" rather
// than being forwarded verbatim: the container must only ever see one of the
// two documented values, so #1088's consumer never has to parse a third.
func TestAssembleExecEnv_UnrecognizedEnvValue_FoldsToZero(t *testing.T) {
	writeFleetConfig(t, `{"planning": {"attended": true}}`)

	for _, v := range []string{"true", "yes", "01", "2"} {
		t.Setenv("CENCI_ATTENDED", v)
		requireAttended(t, "claude", "0")
	}
}

// TestAssembleExecEnv_EmptyEnvValueIsNotAPin pins that an EMPTY
// CENCI_ATTENDED reads as unset rather than as a pinned posture, so the host
// config still decides. This is the "set and non-empty" gate the provider-key
// forwards in this same file already use, and it is what lets a caller scrub
// its child environment with a bare `CENCI_ATTENDED=` without silently
// asserting "nobody is watching" — the exact shape the black-box open-path
// fixture uses (openTestEnv, watch/sandbox_open_test.go).
func TestAssembleExecEnv_EmptyEnvValueIsNotAPin(t *testing.T) {
	writeFleetConfig(t, `{"planning": {"attended": true}}`)
	t.Setenv("CENCI_ATTENDED", "")

	requireAttended(t, "claude", "1")
}

// TestAssembleExecEnv_AttendedNotSecretAndValueInline pins that
// CENCI_ATTENDED is forwarded as an inline "-e NAME=value" token (it carries
// no secret, unlike the #759 provider keys' bare "-e NAME" form) and is not
// classified as a secret env name.
func TestAssembleExecEnv_AttendedNotSecretAndValueInline(t *testing.T) {
	clearAttendedEnv(t)
	writeFleetConfig(t, `{"planning": {"attended": true}}`)

	args := assembleExecEnv("claude")
	if slices.Contains(args, "CENCI_ATTENDED") {
		t.Errorf("assembleExecEnv emitted a bare -e CENCI_ATTENDED token; want the inline NAME=value form (the flag is not a secret); got: %v", args)
	}
	if isSecretEnvName("CENCI_ATTENDED") {
		t.Error("isSecretEnvName(\"CENCI_ATTENDED\") = true, want false — the attended flag carries no secret and must render unredacted in audit/dry-run output")
	}
}

// TestForwardedEnvVars_ReportsAttended covers the audit AC: the sandbox
// posture must reflect the new passthrough. CENCI_ATTENDED materially changes
// how a session behaves (asks in chat vs posts the question to the ticket and
// stops), so an operator reading `cenci audit` has to see it — reported with
// Secret:false, alongside the secret-classified provider keys.
func TestForwardedEnvVars_ReportsAttended(t *testing.T) {
	clearAttendedEnv(t)
	writeFleetConfig(t, `{"planning": {"attended": true}}`)

	var found bool
	for _, e := range forwardedEnvVars("claude") {
		if e.Name != "CENCI_ATTENDED" {
			continue
		}
		found = true
		if e.Secret {
			t.Error("forwardedEnvVars reported CENCI_ATTENDED as secret, want Secret:false")
		}
	}
	if !found {
		t.Errorf("forwardedEnvVars(\"claude\") does not report CENCI_ATTENDED; got: %+v", forwardedEnvVars("claude"))
	}
}
