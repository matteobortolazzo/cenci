package launcher

import (
	"bytes"
	"strings"
	"testing"
)

// TestValidateAgent pins the launcher's single agent gate: claude, codex, and
// opencode (#490) are the only accepted values; anything else is a usage
// error.
func TestValidateAgent(t *testing.T) {
	cases := []struct {
		agent   string
		wantErr bool
	}{
		{"claude", false},
		{"codex", false},
		{"opencode", false},
		{"gemini", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			err := ValidateAgent(tc.agent)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateAgent(%q) = nil, want a usage error", tc.agent)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateAgent(%q) = %v, want nil", tc.agent, err)
			}
		})
	}
}

// TestDefaultModel pins the per-agent model default applied when neither a
// shortcut nor an explicit --model chose one. OpenCode has no cenci-side
// model default: config-driven permissions with no --model equivalent to
// force, so DefaultModel("opencode") must return "" and --model is only
// forwarded when the user explicitly passes one (#490).
func TestDefaultModel(t *testing.T) {
	cases := []struct {
		agent string
		want  string
	}{
		{"claude", "sonnet"},
		{"codex", "gpt-5.6-terra"},
		{"opencode", ""},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			if got := DefaultModel(tc.agent); got != tc.want {
				t.Errorf("DefaultModel(%q) = %q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}

// Creation-time env must never carry TMUX_PANE: `docker run -e TMUX_PANE=...`
// lands in /proc/1/environ of the long-lived shared container and goes stale
// as soon as the creating pane closes, at which point `cenci sandbox
// reap-orphans` classified PID 1 as an orphan, killed it, and tore down the
// container with every attached agent session (#356). Pane identity is
// injected per exec session instead (Launch's execEnvArgs).
func TestAssembleRunArgs_NoCreationTimeTmuxPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "%20")
	home := t.TempDir()
	e := &Engine{Runtime: "docker", Stderr: &bytes.Buffer{}}
	scope := Scope{
		ContainerName:     "claude-cenci-repo",
		VolumeName:        "claude-cenci-repo-home",
		Hostname:          "cenci-repo",
		Image:             "claude-cenci-repo:latest",
		WorkspaceBindHost: t.TempDir(),
		Workdir:           "/workspace",
		WorkspaceScope:    "repo",
	}

	args, err := e.assembleRunArgs("claude", "/usr/local/bin/cenci", "/run/user/1000/cenci", true, scope, Options{Agent: "claude"}, home, false)
	if err != nil {
		t.Fatalf("assembleRunArgs: %v", err)
	}

	for _, a := range args {
		if strings.Contains(a, "TMUX_PANE") {
			t.Errorf("container-creation args must not carry TMUX_PANE (goes stale on the shared container, #356); got %q in:\n%s", a, strings.Join(args, " "))
		}
	}
}

// -- ticket #628: reuse-posture derivation/socket-matcher unit tests --------
//
// NOTE (red phase): deriveDindPosture, mountExposesHostSocket, reusePosture,
// reuseMount, reuseDindPosture, and the dindOn/dindOff/dindUnknown constants
// do not exist yet -- they land in launch.go in the next, non-red phase
// (ticket #628's Implementation Order step 2). Every reference to them below
// is therefore a compile error until that lands; that is the intended
// red-phase state (mirroring dryrun_test.go's historical #589 red-phase
// note), not a bug to fix by stubbing the implementation in this file.
//
// The tri-state type is named reuseDindPosture (not dindPosture) because
// audit.go already defines a function named dindPosture -- a distinct name
// avoids colliding with that existing identifier while keeping the plan's
// dindOn/dindOff/dindUnknown value names exactly as specified.

// TestDeriveDindPosture pins the tri-state derivation rules: an explicit
// cenci-sand.dind label is authoritative over every other signal (even a
// conflicting one), a legacy unlabeled container derives its posture from any
// one of the three independent create-time DinD signals, and an unrecognized
// label value is ambiguous -- never collapsed into the safest-looking case
// (watch AGENTS.md #598).
func TestDeriveDindPosture(t *testing.T) {
	cases := []struct {
		name string
		p    reusePosture
		want reuseDindPosture
	}{
		{
			name: "label on is authoritative even with no other dind signals",
			p:    reusePosture{DindLabel: "on"},
			want: dindOn,
		},
		{
			name: "label off is authoritative even with a conflicting sysbox-runc runtime signal",
			p:    reusePosture{DindLabel: "off", Runtime: "sysbox-runc"},
			want: dindOff,
		},
		{
			name: "legacy unlabeled container derives on from the sysbox-runc runtime signal alone",
			p:    reusePosture{Runtime: "sysbox-runc"},
			want: dindOn,
		},
		{
			name: "legacy unlabeled container derives on from the CENCI_SANDBOX_DIND env signal alone",
			p:    reusePosture{DindEnv: true},
			want: dindOn,
		},
		{
			name: "legacy unlabeled container derives on from a /var/lib/docker storage mount alone",
			p:    reusePosture{Mounts: []reuseMount{{Source: "claude-cenci-dind-repo", Destination: "/var/lib/docker"}}},
			want: dindOn,
		},
		{
			name: "legacy unlabeled container with none of the three signals derives off",
			p:    reusePosture{Runtime: "runc", Mounts: []reuseMount{{Source: "workspace-vol", Destination: "/workspace"}}},
			want: dindOff,
		},
		{
			name: "unrecognized label value is ambiguous, not the safest-looking case (#598)",
			p:    reusePosture{DindLabel: "maybe"},
			want: dindUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveDindPosture(tc.p); got != tc.want {
				t.Errorf("deriveDindPosture(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// TestMountExposesHostSocket pins the host-socket matcher: it flags a mount
// whose source OR destination basename is docker.sock/podman.sock, and it
// must not over-match the cenci socket-dir mount (a directory, not a
// *.sock file) or ordinary workspace/home mounts (plan Risks: over-broad
// socket match).
func TestMountExposesHostSocket(t *testing.T) {
	cases := []struct {
		name   string
		mounts []reuseMount
		want   bool
	}{
		{
			name:   "docker.sock source basename is flagged",
			mounts: []reuseMount{{Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"}},
			want:   true,
		},
		{
			name:   "podman.sock destination basename is flagged",
			mounts: []reuseMount{{Source: "/run/user/1000/podman/podman.sock", Destination: "/run/podman/podman.sock"}},
			want:   true,
		},
		{
			name:   "the cenci socket directory mount is not flagged",
			mounts: []reuseMount{{Source: "/run/user/1000/cenci", Destination: cenciSocketMountDest}},
			want:   false,
		},
		{
			name:   "the workspace bind mount is not flagged",
			mounts: []reuseMount{{Source: "/home/user/repo", Destination: "/workspace"}},
			want:   false,
		},
		{
			name:   "the home volume mount is not flagged",
			mounts: []reuseMount{{Source: "claude-cenci-home-default", Destination: "/home/dev"}},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mountExposesHostSocket(tc.mounts); got != tc.want {
				t.Errorf("mountExposesHostSocket(%+v) = %v, want %v", tc.mounts, got, tc.want)
			}
		})
	}
}

// TestParseReusePosture_FailsClosedOnMalformedOrEmptyOutput is a direct unit
// test of parseReusePosture's boundary shapes -- justified despite this
// project's integration-first default because the exact "genuinely empty
// string" case this Critical finding calls out cannot be produced through
// the fake-runtime shell scripts' `${FAKE_REUSE_POSTURE:-default}`
// substitution (an empty env var value falls back to the default under
// that substitution form, same as unset), so an integration test cannot
// reach it. It pins the finding's core requirement: a truncated/garbled
// `inspect` response that still exits 0 must never silently collapse to
// the zero-value reusePosture{} (which derives to the fully permissive
// "no host socket, dindOff" outcome) -- it must return an error instead,
// exactly like containerStartupState's existing "unexpected container
// state" shape check (watch AGENTS.md #572, #598).
func TestParseReusePosture_FailsClosedOnMalformedOrEmptyOutput(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		wantErrSub string
	}{
		{
			name:       "genuinely empty output (exit 0, zero bytes) is rejected, not defaulted to the zero-value posture",
			out:        "",
			wantErrSub: "empty",
		},
		{
			name:       "a header line missing the expected pipe-delimited fields is rejected",
			out:        "garbage-no-pipes\n",
			wantErrSub: "malformed reuse posture header",
		},
		{
			name:       "a header line with only one pipe (missing the dind-env field) is rejected",
			out:        "off|runc\n",
			wantErrSub: "malformed reuse posture header",
		},
		{
			name:       "a mount line that fails to split on \"::\" is rejected rather than silently dropped",
			out:        "off|runc|0\nno-delimiter-here\n",
			wantErrSub: "malformed reuse posture mount line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseReusePosture(tc.out)
			if err == nil {
				t.Fatalf("parseReusePosture(%q) = nil error, want a fail-closed rejection", tc.out)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("parseReusePosture(%q) error = %q, want it to contain %q", tc.out, err.Error(), tc.wantErrSub)
			}
		})
	}
}

// TestParseReusePosture_ValidShapeStillParses is the happy-path companion to
// the fail-closed test above: a well-formed inspect response must still
// parse into the expected reusePosture fields, so the new validation doesn't
// reject legitimate output.
func TestParseReusePosture_ValidShapeStillParses(t *testing.T) {
	p, err := parseReusePosture("on|sysbox-runc|1\nworkspace-vol::/workspace\ndind-vol::/var/lib/docker\n")
	if err != nil {
		t.Fatalf("parseReusePosture: %v", err)
	}
	if p.DindLabel != "on" || p.Runtime != "sysbox-runc" || !p.DindEnv {
		t.Errorf("parseReusePosture header fields = %+v, want DindLabel=on Runtime=sysbox-runc DindEnv=true", p)
	}
	want := []reuseMount{
		{Source: "workspace-vol", Destination: "/workspace"},
		{Source: "dind-vol", Destination: "/var/lib/docker"},
	}
	if len(p.Mounts) != len(want) || p.Mounts[0] != want[0] || p.Mounts[1] != want[1] {
		t.Errorf("parseReusePosture mounts = %+v, want %+v", p.Mounts, want)
	}
}

// dindShutdownSentinelPath is the shutdown-sentinel file the dind-only
// keepalive TERM/INT trap must write (#630's Assumptions: hardcoded under
// /home/dev, mirroring the marker's own hardcoded root-phase path — never
// $HOME, since this runs as PID 1 inside the container where dev's home is
// always /home/dev).
const dindShutdownSentinelPath = "/home/dev/.cenci-dockerd-shutdown"

// buildRunArgvTrailingCommand builds a minimal argv via buildRunArgv and
// returns its trailing PID-1 command string (the argument right after
// "-c"), failing the test if the argv doesn't end in the expected
// "<image> -c <command>" shape.
func buildRunArgvTrailingCommand(t *testing.T, dindOn bool) string {
	t.Helper()
	home := t.TempDir()
	e := &Engine{Runtime: "docker", Stderr: &bytes.Buffer{}}
	scope := Scope{
		ContainerName:     "claude-cenci-repo",
		VolumeName:        "claude-cenci-repo-home",
		DindVolumeName:    "claude-cenci-dind-repo",
		Hostname:          "cenci-repo",
		Image:             "claude-cenci-repo:latest",
		WorkspaceBindHost: t.TempDir(),
		Workdir:           "/workspace",
		WorkspaceScope:    "repo",
	}

	argv, err := e.buildRunArgv("claude", "/usr/local/bin/cenci", "/run/user/1000/cenci", true, scope, Options{Agent: "claude"}, home, dindOn)
	if err != nil {
		t.Fatalf("buildRunArgv(dindOn=%v): %v", dindOn, err)
	}
	if len(argv) < 3 || argv[len(argv)-2] != "-c" {
		t.Fatalf("buildRunArgv(dindOn=%v) argv does not end in \"-c\" <command>; got: %v", dindOn, argv)
	}
	return argv[len(argv)-1]
}

// TestBuildRunArgv_NonDind_KeepsExecSleepInfinityVerbatim pins the
// non-regression half of #630's keepalive change: a non-dind launch's
// trailing PID-1 command must stay byte-identical to today's
// "touch /tmp/cenci-ready && exec sleep infinity" — `exec` replaces bash
// entirely, which is fine (and cheaper) when there's no dind-only shutdown
// signal to trap.
func TestBuildRunArgv_NonDind_KeepsExecSleepInfinityVerbatim(t *testing.T) {
	got := buildRunArgvTrailingCommand(t, false)
	want := "touch /tmp/cenci-ready && exec sleep infinity"
	if got != want {
		t.Errorf("non-dind trailing command = %q, want %q verbatim", got, want)
	}
}

// TestBuildRunArgv_Dind_KeepaliveTrapsTermAndWritesSentinel pins #630's
// dind-only keepalive: it must still touch the readiness marker, but must
// NOT `exec sleep infinity` (exec would replace bash and make it unable to
// trap/handle TERM), must install a trap on TERM and INT that touches the
// shutdown-sentinel file dind.sh's sentinel-based classification reads
// (dindShutdownSentinelPath), and must still block indefinitely via some
// `sleep infinity` backgrounded and `wait`ed on (the standard trap-then-wait
// container keepalive shape), so a normal container lifetime doesn't exit
// early absent a signal.
func TestBuildRunArgv_Dind_KeepaliveTrapsTermAndWritesSentinel(t *testing.T) {
	got := buildRunArgvTrailingCommand(t, true)

	if !strings.Contains(got, "touch /tmp/cenci-ready") {
		t.Errorf("dind trailing command missing the readiness marker touch, got: %q", got)
	}
	if strings.Contains(got, "exec sleep infinity") {
		t.Errorf("dind trailing command must not `exec sleep infinity` (exec would replace bash, so it could never trap TERM to write the shutdown sentinel), got: %q", got)
	}
	if !strings.Contains(got, "trap") {
		t.Errorf("dind trailing command missing a `trap` installation, got: %q", got)
	}
	if !strings.Contains(got, "TERM") {
		t.Errorf("dind trailing command's trap must cover TERM (docker/podman stop's signal), got: %q", got)
	}
	if !strings.Contains(got, dindShutdownSentinelPath) {
		t.Errorf("dind trailing command's trap must write the shutdown sentinel at %q, got: %q", dindShutdownSentinelPath, got)
	}
	if !strings.Contains(got, "sleep infinity") {
		t.Errorf("dind trailing command must still block indefinitely (sleep infinity) absent a signal, got: %q", got)
	}
	if !strings.Contains(got, "wait") {
		t.Errorf("dind trailing command must `wait` on the backgrounded sleep so the trap fires while the shell blocks, got: %q", got)
	}
}

// TestBuildRunArgv_Dind_DoesNotLeakIntoNonDindLaunch is a sibling-instance
// regression: dind's keepalive changes must be strictly gated on dindOn —
// asserting both trailing commands in the same test guards against a future
// edit accidentally making the dind trap unconditional.
func TestBuildRunArgv_Dind_DoesNotLeakIntoNonDindLaunch(t *testing.T) {
	nonDind := buildRunArgvTrailingCommand(t, false)
	dind := buildRunArgvTrailingCommand(t, true)
	if nonDind == dind {
		t.Fatalf("non-dind and dind trailing commands must differ; both were: %q", nonDind)
	}
	if strings.Contains(nonDind, "trap") || strings.Contains(nonDind, dindShutdownSentinelPath) {
		t.Errorf("non-dind trailing command must not carry the dind-only TERM trap/sentinel, got: %q", nonDind)
	}
}
