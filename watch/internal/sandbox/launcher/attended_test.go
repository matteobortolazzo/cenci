package launcher

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
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
	args := assembleExecEnv(agent, builtinSecretEnvSet())
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
// broken fleet config must never make the attended flag unresolvable: it
// resolves to CENCI_ATTENDED=0. Both a malformed whole file and a non-bool
// attended value (QueryPlanningAttended's two distinct error shapes, including
// the explicit-null case its doc comment calls out) are covered. This pins
// assembleExecEnv alone: since #1016 the launch as a whole fails closed on an
// unparsable fleet config, because sandboxEnv.forward is read from the same
// file (see forwardenv_test.go and watch/open_test.go).
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
// fixture uses (openTestEnv, watch/sandbox_clitest_helpers_test.go).
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

	args := assembleExecEnv("claude", builtinSecretEnvSet())
	if slices.Contains(args, "CENCI_ATTENDED") {
		t.Errorf("assembleExecEnv emitted a bare -e CENCI_ATTENDED token; want the inline NAME=value form (the flag is not a secret); got: %v", args)
	}
	if isSecretEnvName(builtinSecretEnvSet(), "CENCI_ATTENDED") {
		t.Error("isSecretEnvName(\"CENCI_ATTENDED\") = true, want false — the attended flag carries no secret and must render unredacted in audit/dry-run output")
	}
}

// -- ticket #1464: forward where CENCI_ATTENDED came from ------------------
//
// resolveAttendedFlag yields "0" for every sandbox launch while the host's
// planning.attended is off, a human `cenci open` included, so the pipeline's
// unattended gate cannot tell a dispatcher-pinned session from a host-config
// one by CENCI_ATTENDED alone. The launcher therefore forwards
// CENCI_ATTENDED_ORIGIN beside it: "host-config" when it resolved the flag
// from the fleet config, "pinned" when CENCI_ATTENDED was set in its own
// environment (the dispatcher's Unattended pin, #1087).

// originTokens returns every "-e CENCI_ATTENDED_ORIGIN=<v>" value
// assembleExecEnv emitted, in order (a slice for the same duplicate-visibility
// reason as attendedTokens).
func originTokens(args []string) []string {
	var values []string
	for i := 0; i < len(args); i++ {
		if args[i] != "-e" || i+1 >= len(args) {
			continue
		}
		if v, ok := strings.CutPrefix(args[i+1], "CENCI_ATTENDED_ORIGIN="); ok {
			values = append(values, v)
		}
	}
	return values
}

// requireOrigin asserts assembleExecEnv emitted exactly one
// CENCI_ATTENDED_ORIGIN token, carrying want.
func requireOrigin(t *testing.T, agent, want string) {
	t.Helper()
	args := assembleExecEnv(agent, builtinSecretEnvSet())
	got := originTokens(args)
	if len(got) != 1 {
		t.Fatalf("assembleExecEnv(%q) emitted %d CENCI_ATTENDED_ORIGIN tokens, want exactly 1; got: %v", agent, len(got), args)
	}
	if got[0] != want {
		t.Errorf("assembleExecEnv(%q) forwarded CENCI_ATTENDED_ORIGIN=%s, want CENCI_ATTENDED_ORIGIN=%s", agent, got[0], want)
	}
}

// clearAttendedOriginEnv removes CENCI_ATTENDED_ORIGIN from the process env
// for the test (see clearAttendedEnv for the Setenv-then-Unsetenv idiom).
func clearAttendedOriginEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CENCI_ATTENDED_ORIGIN", "")
	if err := os.Unsetenv("CENCI_ATTENDED_ORIGIN"); err != nil {
		t.Fatalf("unset CENCI_ATTENDED_ORIGIN: %v", err)
	}
}

// TestAssembleExecEnv_AttendedOrigin_HostConfig: a flag resolved from the host
// fleet config is forwarded with origin host-config, whatever it resolved to.
func TestAssembleExecEnv_AttendedOrigin_HostConfig(t *testing.T) {
	cases := map[string]struct {
		body     string
		attended string
	}{
		"attended false":  {`{"planning": {"attended": false}}`, "0"},
		"attended true":   {`{"planning": {"attended": true}}`, "1"},
		"no config file":  {"", "0"},
		"planning absent": {`{}`, "0"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearAttendedEnv(t)
			clearAttendedOriginEnv(t)
			writeFleetConfig(t, tc.body)
			requireAttended(t, "claude", tc.attended)
			requireOrigin(t, "claude", "host-config")
		})
	}
}

// unreadableFleetConfigs are fleet configs QueryPlanningAttended cannot read
// an attended value from (its error shapes).
var unreadableFleetConfigs = map[string]string{
	"malformed json":    `{"planning": `,
	"non-bool attended": `{"planning": {"attended": "yes"}}`,
	"null attended":     `{"planning": {"attended": null}}`,
}

// TestAssembleExecEnv_AttendedOrigin_ConfigUnreadable: a fleet config the
// launcher cannot read still forwards "0" (a broken config never makes the
// sandbox unusable), but never with origin host-config — that origin exempts
// "0" from the pipeline's unattended gate, so an unreadable config would
// otherwise let a non-dispatcher unattended launch count as a human session.
func TestAssembleExecEnv_AttendedOrigin_ConfigUnreadable(t *testing.T) {
	for name, body := range unreadableFleetConfigs {
		t.Run(name, func(t *testing.T) {
			clearAttendedEnv(t)
			clearAttendedOriginEnv(t)
			writeFleetConfig(t, body)
			requireAttended(t, "claude", "0")
			requireOrigin(t, "claude", "config-unreadable")
			if got := originTokens(assembleExecEnv("claude", builtinSecretEnvSet())); len(got) == 1 && got[0] == pipeline.AttendedOriginHostConfig {
				t.Errorf("an unreadable fleet config forwarded origin %q, the human-session exemption", got[0])
			}
		})
	}
}

// TestWarnAttendedConfigUnreadable_OneLineNamingTheError: an unreadable fleet
// config prints exactly one warning line naming the read error; a readable
// config, or a pinned flag (the config is never consulted), prints nothing.
func TestWarnAttendedConfigUnreadable_OneLineNamingTheError(t *testing.T) {
	for name, body := range unreadableFleetConfigs {
		t.Run(name, func(t *testing.T) {
			clearAttendedEnv(t)
			writeFleetConfig(t, body)
			var buf strings.Builder
			e := &Engine{Stderr: &buf}
			e.warnAttendedConfigUnreadable()

			out := buf.String()
			if strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "Warning: ") {
				t.Fatalf("stderr = %q, want exactly one Warning: line", out)
			}
			if !strings.Contains(out, "planning.attended") || !strings.Contains(out, "parsing") {
				t.Errorf("stderr = %q, want it to name planning.attended and the read error", out)
			}
		})
	}
	silent := map[string]func(t *testing.T){
		"readable config": func(t *testing.T) {
			clearAttendedEnv(t)
			writeFleetConfig(t, `{"planning": {"attended": false}}`)
		},
		"pinned flag with an unreadable config": func(t *testing.T) {
			writeFleetConfig(t, `{"planning": `)
			t.Setenv("CENCI_ATTENDED", "0")
		},
	}
	for name, setup := range silent {
		t.Run(name, func(t *testing.T) {
			setup(t)
			var buf strings.Builder
			e := &Engine{Stderr: &buf}
			e.warnAttendedConfigUnreadable()
			if buf.Len() != 0 {
				t.Errorf("stderr = %q, want no warning", buf.String())
			}
		})
	}
}

// TestResolveLaunchContext_UnreadableAttendedWarnsOnce pins the launch-level
// wiring: a fleet config whose planning.attended cannot be read (but which
// still parses, so loadSecretEnvSet does not fail the launch first) prints
// exactly one warning line through the launch preamble that Launch and DryRun
// share.
func TestResolveLaunchContext_UnreadableAttendedWarnsOnce(t *testing.T) {
	for name, body := range map[string]string{
		"non-bool attended": `{"planning": {"attended": "yes"}}`,
		"null attended":     `{"planning": {"attended": null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			clearAttendedEnv(t)
			writeFleetConfig(t, body)
			repo := newAuditTestRepo(t)
			writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":false}}`)
			t.Setenv("HOME", t.TempDir())
			t.Chdir(repo)
			fakeDir := t.TempDir()
			writeFakeRuntime(t, fakeDir, "podman", filepath.Join(fakeDir, "calls.txt"))
			t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

			var stderr strings.Builder
			eng := &Engine{Stderr: &stderr}
			if _, err := eng.resolveLaunchContext(Options{Agent: "claude"}); err != nil {
				t.Fatalf("resolveLaunchContext with an unreadable planning.attended = %v, want a usable launch context", err)
			}
			if got := strings.Count(stderr.String(), "cannot read planning.attended"); got != 1 {
				t.Errorf("stderr has %d \"cannot read planning.attended\" lines, want exactly 1; got:\n%s", got, stderr.String())
			}
		})
	}
}

// TestAttendedOrigin_ConfigUnreadableIsUnattended cross-checks the launcher's
// new origin against the pipeline gate: CENCI_ATTENDED=0 with origin
// config-unreadable is an unattended session, never a human one.
func TestAttendedOrigin_ConfigUnreadableIsUnattended(t *testing.T) {
	t.Setenv(pipeline.AttendedEnv, attendedOff)
	t.Setenv(pipeline.AttendedOriginEnv, attendedOriginConfigUnreadable)
	if !pipeline.Unattended() {
		t.Errorf("pipeline.Unattended() = false for %s=0 %s=%s, want true", pipeline.AttendedEnv, pipeline.AttendedOriginEnv, attendedOriginConfigUnreadable)
	}
}

// TestAssembleExecEnv_AttendedOrigin_EmptyPinIsHostConfig: an empty
// CENCI_ATTENDED is not a pin (TestAssembleExecEnv_EmptyEnvValueIsNotAPin), so
// the flag came from the host config and the origin says so.
func TestAssembleExecEnv_AttendedOrigin_EmptyPinIsHostConfig(t *testing.T) {
	clearAttendedOriginEnv(t)
	writeFleetConfig(t, `{"planning": {"attended": false}}`)
	t.Setenv("CENCI_ATTENDED", "")

	requireOrigin(t, "claude", "host-config")
}

// TestAssembleExecEnv_AttendedOrigin_Pinned: a CENCI_ATTENDED set in the
// launcher's own environment (the dispatcher's pin) is forwarded with origin
// pinned, never host-config, for the pinned "0", the pinned "1" and an
// unrecognized value that folds to "0".
func TestAssembleExecEnv_AttendedOrigin_Pinned(t *testing.T) {
	for _, pin := range []string{"0", "1", "true", "2"} {
		t.Run("pin "+pin, func(t *testing.T) {
			clearAttendedOriginEnv(t)
			writeFleetConfig(t, `{"planning": {"attended": true}}`)
			t.Setenv("CENCI_ATTENDED", pin)

			requireOrigin(t, "claude", "pinned")
		})
	}
}

// TestAssembleExecEnv_AttendedOrigin_PinnedIgnoresHostConfig: the origin
// follows how the flag was resolved, not what the host config says, so a pin
// stays "pinned" with the host attended flag on or off.
func TestAssembleExecEnv_AttendedOrigin_PinnedIgnoresHostConfig(t *testing.T) {
	for name, body := range map[string]string{
		"host attended":   `{"planning": {"attended": true}}`,
		"host unattended": `{"planning": {"attended": false}}`,
		"no config":       "",
	} {
		t.Run(name, func(t *testing.T) {
			clearAttendedOriginEnv(t)
			writeFleetConfig(t, body)
			t.Setenv("CENCI_ATTENDED", "0")

			requireOrigin(t, "claude", "pinned")
		})
	}
}

// TestAssembleExecEnv_AttendedOrigin_AmbientOriginCannotSpoof: a
// CENCI_ATTENDED_ORIGIN already present in the launcher's own environment is
// never passed through. A dispatcher-pinned launch with an ambient
// host-config marker still forwards pinned (the pin is what makes the session
// unattended), and an unpinned launch forwards host-config whatever the
// ambient marker says.
func TestAssembleExecEnv_AttendedOrigin_AmbientOriginCannotSpoof(t *testing.T) {
	t.Run("pin with ambient host-config", func(t *testing.T) {
		writeFleetConfig(t, `{"planning": {"attended": false}}`)
		t.Setenv("CENCI_ATTENDED", "0")
		t.Setenv("CENCI_ATTENDED_ORIGIN", "host-config")

		requireOrigin(t, "claude", "pinned")
	})
	t.Run("no pin with ambient pinned", func(t *testing.T) {
		clearAttendedEnv(t)
		writeFleetConfig(t, `{"planning": {"attended": false}}`)
		t.Setenv("CENCI_ATTENDED_ORIGIN", "pinned")

		requireOrigin(t, "claude", "host-config")
	})
}

// TestAssembleExecEnv_AttendedOrigin_EveryAgent: the marker rides on every
// agent's exec, like CENCI_ATTENDED itself.
func TestAssembleExecEnv_AttendedOrigin_EveryAgent(t *testing.T) {
	clearAttendedEnv(t)
	clearAttendedOriginEnv(t)
	writeFleetConfig(t, `{"planning": {"attended": false}}`)

	for _, agent := range []string{"claude", "codex", "opencode"} {
		requireOrigin(t, agent, "host-config")
	}
}

// TestAssembleExecEnv_AttendedOriginNotSecretAndValueInline: the marker is an
// inline NAME=value token and is not a secret name, so audit and dry-run
// render it unredacted.
func TestAssembleExecEnv_AttendedOriginNotSecretAndValueInline(t *testing.T) {
	clearAttendedEnv(t)
	clearAttendedOriginEnv(t)
	writeFleetConfig(t, `{"planning": {"attended": true}}`)

	args := assembleExecEnv("claude", builtinSecretEnvSet())
	if slices.Contains(args, "CENCI_ATTENDED_ORIGIN") {
		t.Errorf("assembleExecEnv emitted a bare -e CENCI_ATTENDED_ORIGIN token; want the inline NAME=value form; got: %v", args)
	}
	if isSecretEnvName(builtinSecretEnvSet(), "CENCI_ATTENDED_ORIGIN") {
		t.Error("isSecretEnvName(\"CENCI_ATTENDED_ORIGIN\") = true, want false")
	}
}

// TestAttendedOrigin_ConstantsMirrorPipeline: the launcher mirrors the
// pipeline package's marker names (the gate reads them there); a drift would
// silently disconnect the forward from the gate.
func TestAttendedOrigin_ConstantsMirrorPipeline(t *testing.T) {
	if attendedEnvName != pipeline.AttendedEnv {
		t.Errorf("attendedEnvName = %q, pipeline.AttendedEnv = %q", attendedEnvName, pipeline.AttendedEnv)
	}
	if attendedOriginEnvName != pipeline.AttendedOriginEnv {
		t.Errorf("attendedOriginEnvName = %q, pipeline.AttendedOriginEnv = %q", attendedOriginEnvName, pipeline.AttendedOriginEnv)
	}
	if attendedOriginHostConfig != pipeline.AttendedOriginHostConfig {
		t.Errorf("attendedOriginHostConfig = %q, pipeline.AttendedOriginHostConfig = %q", attendedOriginHostConfig, pipeline.AttendedOriginHostConfig)
	}
	if attendedOriginPinned == pipeline.AttendedOriginHostConfig {
		t.Errorf("attendedOriginPinned = %q must differ from the host-config exemption value", attendedOriginPinned)
	}
}

// TestForwardNameValidation_RejectsAttendedOriginMarker: the marker is
// launcher-owned (the CENCI_ reserved prefix), so a user sandboxEnv.forward
// entry can neither forward nor spoof it.
func TestForwardNameValidation_RejectsAttendedOriginMarker(t *testing.T) {
	err := validateForwardName("CENCI_ATTENDED_ORIGIN")
	if err == nil {
		t.Fatal("validateForwardName(CENCI_ATTENDED_ORIGIN) = nil, want a reserved-for-the-launcher error")
	}
	if !strings.Contains(err.Error(), "reserved for the launcher") {
		t.Errorf("error = %q, want it to say the name is reserved for the launcher", err)
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
	for _, e := range forwardedEnvVars("claude", builtinSecretEnvSet()) {
		if e.Name != "CENCI_ATTENDED" {
			continue
		}
		found = true
		if e.Secret {
			t.Error("forwardedEnvVars reported CENCI_ATTENDED as secret, want Secret:false")
		}
	}
	if !found {
		t.Errorf("forwardedEnvVars(\"claude\") does not report CENCI_ATTENDED; got: %+v", forwardedEnvVars("claude", builtinSecretEnvSet()))
	}
}
