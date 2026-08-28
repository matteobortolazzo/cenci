package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
)

// TestSeverityForCode_MapsRegisteredCodesToTheirTier pins the severity
// taxonomy from the ticket: container exited/dead/not-found, START-001, and
// START-002 are fatal; the readiness-timeout START-003, daemon-unreachable,
// and event-socket-missing are degraded. Version-mismatch findings (which
// attach no registered code at all) are covered separately below.
func TestSeverityForCode_MapsRegisteredCodesToTheirTier(t *testing.T) {
	cases := []struct {
		name string
		code errcode.Code
		want Severity
	}{
		{"agent CLI missing", errcode.SandboxStartAgentCLIMissing, SeverityFatal},
		{"generic entrypoint failure", errcode.SandboxStartGenericEntrypoint, SeverityFatal},
		{"session/container not found", errcode.SandboxSessionNotFound, SeverityFatal},
		{"readiness timeout", errcode.SandboxStartReadinessTimeout, SeverityDegraded},
		{"daemon unreachable", errcode.DaemonConnUnreachable, SeverityDegraded},
		{"event socket missing", errcode.DaemonSocketMissing, SeverityDegraded},
		{"dind startup failure", errcode.SandboxDindStartupFailure, SeverityDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := severityForCode(tc.code); got != tc.want {
				t.Errorf("severityForCode(%s) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestSeverityForCode_UnregisteredOrEmptyCodeDefaultsWarning covers the
// unknown-plugin/image-version findings, which attach no registered code at
// all (Code is the zero value) — severityForCode must not panic and must
// classify them as the lowest tier (warning), never fatal or degraded.
func TestSeverityForCode_UnregisteredOrEmptyCodeDefaultsWarning(t *testing.T) {
	cases := []errcode.Code{
		errcode.Code(""),
		errcode.Code("CENCI-DOES-NOTEXIST-999"),
	}
	for _, code := range cases {
		if got := severityForCode(code); got != SeverityWarning {
			t.Errorf("severityForCode(%q) = %q, want %q", code, got, SeverityWarning)
		}
	}
}

// TestSeverityForCode_AllRegisteredCodesAreExplicitlyMapped is a regression
// guard for severityForCode's exhaustiveness: every code registered in
// errcode's registry (errcode.AllCodes()) must land in one of
// severityForCode's explicit fatal/degraded switch cases, not silently fall
// through to the default warning branch — that branch is reserved for
// codeless findings (e.g. an unknown image/plugin version), not for a
// registered-but-forgotten Code. warningByDesign lists any registered code
// that is intentionally warning-tier; it is empty today because every
// registered code (as of #571/#572) maps to fatal or degraded. A future code
// added to the registry without a matching severityForCode case will fail
// this test (silently under-classified as warning) instead of drifting
// undetected.
func TestSeverityForCode_AllRegisteredCodesAreExplicitlyMapped(t *testing.T) {
	// CENCI-SANDBOX-DIND-002 (#962) is intentionally warning-tier: it
	// records a host capability the sandbox never had (macOS cannot
	// register sysbox-runc), not a failure of this session. The session
	// itself launched and works — only nested Docker is absent — and no
	// recovery action on this host can change that, which is exactly what
	// separates it from DIND-001's degraded tier (a dockerd that should
	// have started and didn't).
	warningByDesign := map[errcode.Code]bool{
		errcode.SandboxDindPlatformUnsupported: true,
	}

	codes := errcode.AllCodes()
	if len(codes) == 0 {
		t.Fatal("errcode.AllCodes() returned no codes; test cannot exercise the exhaustiveness guard")
	}

	for _, code := range codes {
		got := severityForCode(code)
		if got == SeverityWarning && !warningByDesign[code] {
			t.Errorf("severityForCode(%s) = %q, the default/unmapped tier; add an explicit fatal/degraded case to severityForCode's switch, or add %s to warningByDesign if it is intentionally warning-tier", code, got, code)
		}
	}
}

// TestRenderFinding_ReusesRegisteredHintsVerbatim asserts renderFinding pulls
// its recovery-command lines straight from errcode.Lookup(code).Hints rather
// than re-deriving or paraphrasing them, so the diagnose output and the
// errcode registry never drift apart.
func TestRenderFinding_ReusesRegisteredHintsVerbatim(t *testing.T) {
	entry, ok := errcode.Lookup(errcode.SandboxStartAgentCLIMissing)
	if !ok {
		t.Fatalf("errcode.Lookup(%s) = _, false; want a registered entry", errcode.SandboxStartAgentCLIMissing)
	}
	if len(entry.Hints) == 0 {
		t.Fatalf("errcode.Lookup(%s).Hints is empty; test needs at least one hint to assert verbatim reuse", errcode.SandboxStartAgentCLIMissing)
	}

	f := finding{
		Message:  "the agent CLI path is missing or not executable",
		Code:     errcode.SandboxStartAgentCLIMissing,
		Severity: SeverityFatal,
	}
	rendered := renderFinding(f)

	for _, hint := range entry.Hints {
		if !strings.Contains(rendered, hint) {
			t.Errorf("renderFinding output missing recovery hint %q verbatim; got:\n%s", hint, rendered)
		}
	}
	if !strings.Contains(rendered, f.Message) {
		t.Errorf("renderFinding output missing the finding message %q; got:\n%s", f.Message, rendered)
	}
	if !strings.Contains(rendered, string(errcode.SandboxStartAgentCLIMissing)) {
		t.Errorf("renderFinding output missing the error code %q; got:\n%s", errcode.SandboxStartAgentCLIMissing, rendered)
	}
}

// TestRenderFinding_NoCodeOmitsHintsButKeepsMessage covers a finding with no
// registered code (e.g. an "unknown" version-reporting warning): renderFinding
// must still surface the message and severity without fabricating hints or
// an error code that was never attached.
func TestRenderFinding_NoCodeOmitsHintsButKeepsMessage(t *testing.T) {
	f := finding{
		Message:  "plugin manifest version could not be determined",
		Severity: SeverityWarning,
	}
	rendered := renderFinding(f)

	if !strings.Contains(rendered, f.Message) {
		t.Errorf("renderFinding output missing the finding message %q; got:\n%s", f.Message, rendered)
	}
	if strings.Contains(rendered, "CENCI-") {
		t.Errorf("renderFinding output unexpectedly contains an error code for a finding with no Code; got:\n%s", rendered)
	}
}

// TestVersionOrUnknown_FallsBackWhenReadFails pins the shared "unknown"
// fallback every best-effort version read (image base-version,
// pluginManifestVersion) funnels through: a failed or empty read must never
// surface as a blank string, always as the literal "unknown".
func TestVersionOrUnknown_FallsBackWhenReadFails(t *testing.T) {
	cases := []struct {
		name    string
		content string
		ok      bool
		want    string
	}{
		{"successful read", "1.2.3", true, "1.2.3"},
		{"read failed", "", false, "unknown"},
		{"read reported ok but content is empty", "", true, "unknown"},
		{"content has surrounding whitespace preserved by caller", "  1.2.3  ", true, "  1.2.3  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionOrUnknown(tc.content, tc.ok); got != tc.want {
				t.Errorf("versionOrUnknown(%q, %v) = %q, want %q", tc.content, tc.ok, got, tc.want)
			}
		})
	}
}

// TestDiagnose_RuntimeUnreachable_ReportsDaemonUnreachableNotSessionNotFound
// pins the containerStartupState error-type split: when the runtime binary
// itself cannot be run (an error type other than *exec.ExitError — e.g. the
// binary is missing), that is a runtime/daemon-reachability problem, not
// evidence the container is missing, so it must be reported as
// DaemonConnUnreachable/degraded, never as the misleading
// SandboxSessionNotFound/fatal ("relaunch the session") finding.
func TestDiagnose_RuntimeUnreachable_ReportsDaemonUnreachableNotSessionNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-572",
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	scope := Scope{
		ContainerName: "claude-cenci-test",
		VolumeName:    "claude-cenci-home-test",
		Image:         "cenci-sandbox:test",
	}

	if err := e.Diagnose(scope); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, string(errcode.DaemonConnUnreachable)) {
		t.Errorf("expected %s (runtime unreachable) in the report, got:\n%s", errcode.DaemonConnUnreachable, out)
	}
	if !strings.Contains(out, string(SeverityDegraded)) {
		t.Errorf("expected degraded severity for the runtime-unreachable finding, got:\n%s", out)
	}
	if strings.Contains(out, string(errcode.SandboxSessionNotFound)) {
		t.Errorf("did not expect %s (session not found) when the runtime itself is unreachable, got:\n%s", errcode.SandboxSessionNotFound, out)
	}
}

// TestDiagnose_RecentLogsAndMountsReadFailure_ReportUnknownAndWarn pins Fix
// 2: when the best-effort logs/mounts reads fail (here, because the runtime
// binary cannot be run at all), the report still prints the "Recent logs"
// and "Mounts" section headers with "unknown" bodies — never silently
// dropping the section — and appends a SeverityWarning finding for each so
// the failure is visible in the findings summary too.
func TestDiagnose_RecentLogsAndMountsReadFailure_ReportUnknownAndWarn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-572",
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	scope := Scope{
		ContainerName: "claude-cenci-test",
		VolumeName:    "claude-cenci-home-test",
		Image:         "cenci-sandbox:test",
	}

	if err := e.Diagnose(scope); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Recent logs (tail):\nunknown") {
		t.Errorf("expected the Recent logs section header with an \"unknown\" body, got:\n%s", out)
	}
	if !strings.Contains(out, "Mounts:\nunknown") {
		t.Errorf("expected the Mounts section header with an \"unknown\" body, got:\n%s", out)
	}
	if !strings.Contains(out, "recent container logs could not be read") {
		t.Errorf("expected a warning finding for the failed logs read, got:\n%s", out)
	}
	if !strings.Contains(out, "container mounts could not be read") {
		t.Errorf("expected a warning finding for the failed mounts read, got:\n%s", out)
	}
}

// TestDiagnose_IncludesSecretsCaveat pins Fix 6: the report always prints a
// one-line caveat warning that recent logs and mount paths may contain
// sensitive data, so an operator doesn't paste raw diagnose output somewhere
// public without reviewing it first.
func TestDiagnose_IncludesSecretsCaveat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-572",
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	scope := Scope{
		ContainerName: "claude-cenci-test",
		VolumeName:    "claude-cenci-home-test",
		Image:         "cenci-sandbox:test",
	}

	if err := e.Diagnose(scope); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "may contain sensitive data") {
		t.Errorf("expected a secrets caveat in the report, got:\n%s", out)
	}
}

// TestMarketplaceManifestPath_OpencodeReturnsEmptyNotClaudePath pins Fix 5:
// opencode has no marketplace.json (it's provisioned via a git clone, not a
// marketplace checkout), so it must not fall back to the claude path.
func TestMarketplaceManifestPath_OpencodeReturnsEmptyNotClaudePath(t *testing.T) {
	cases := []struct {
		agent string
		want  string
	}{
		{"claude", "/home/dev/.claude/plugins/marketplaces/cenci/.claude-plugin/marketplace.json"},
		{"codex", "/home/dev/.codex/plugins/marketplaces/cenci/.claude-plugin/marketplace.json"},
		{"opencode", ""},
	}
	for _, tc := range cases {
		if got := marketplaceManifestPath(tc.agent); got != tc.want {
			t.Errorf("marketplaceManifestPath(%q) = %q, want %q", tc.agent, got, tc.want)
		}
	}
}

// TestPluginManifestVersion_OpencodeReportsCleanFailureWithoutReadingClaudePath
// asserts pluginManifestVersion short-circuits for opencode instead of
// attempting a read with an empty path (or worse, silently reading claude's
// manifest). Runtime is set to a binary that does not exist so the test
// would fail loudly (a non-"" Runtime invocation attempt surfaces as an
// unrelated error) if pluginManifestVersion regressed to actually invoking
// the runtime for opencode.
func TestPluginManifestVersion_OpencodeReportsCleanFailureWithoutReadingClaudePath(t *testing.T) {
	e := &Engine{Runtime: "cenci-test-runtime-does-not-exist-572"}
	scope := Scope{
		ContainerName: "opencode-cenci-test",
		VolumeName:    "opencode-cenci-home-test",
		Image:         "cenci-sandbox:test",
	}

	version, ok := e.pluginManifestVersion(scope, "opencode")
	if ok || version != "" {
		t.Errorf("pluginManifestVersion(opencode) = %q, %v; want \"\", false", version, ok)
	}
}

// -- Nested Docker (#630): always-on 3-state "Nested Docker:" section -------
//
// Per the ticket's Q3, Diagnose must always print a "Nested Docker:" line
// (never partial silence, per the package's #572 failure-visibility
// convention): "not a dind session" when scope.DindVolumeName was never
// created (a `volume inspect` miss), "no failure recorded" when the session
// is dind but the dockerd-startup-error marker is absent, or the marker's
// diagnostic content plus a Degraded CENCI-SANDBOX-DIND-001 finding when the
// marker is present. These three tests use writeFakeRuntime (a real,
// scriptable fake docker on PATH), not the "runtime does not exist" pattern
// the tests above use, since exercising all three states needs a working
// `volume inspect` + home-volume marker read.

// dindDiagnoseScope returns a Scope with a realistic DindVolumeName set, so
// Diagnose's `volume inspect scope.DindVolumeName` probe has something to
// inspect (the fake only cares about the exit code FAKE_VOLUME_INSPECT_EXIT
// scripts, not the name itself).
func dindDiagnoseScope() Scope {
	return Scope{
		ContainerName:  "claude-cenci-test",
		VolumeName:     "claude-cenci-home-test",
		DindVolumeName: "claude-cenci-dind-test",
		Image:          "cenci-sandbox:test",
	}
}

func TestDiagnose_NestedDocker_NotADindSession(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	e := &Engine{Runtime: "docker", Stdout: &stdout, Stderr: &stderr}
	// FAKE_VOLUME_INSPECT_EXIT=1: scope.DindVolumeName was never created —
	// this session was never launched with --dind.
	t.Setenv("FAKE_VOLUME_INSPECT_EXIT", "1")

	if err := e.Diagnose(dindDiagnoseScope()); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Nested Docker:") {
		t.Errorf("expected an always-on \"Nested Docker:\" line (Q3), got:\n%s", out)
	}
	if !strings.Contains(out, "not a dind session") {
		t.Errorf("expected the not-a-dind-session state, got:\n%s", out)
	}
	if strings.Contains(out, string(errcode.SandboxDindStartupFailure)) {
		t.Errorf("did not expect %s when the session isn't dind at all, got:\n%s", errcode.SandboxDindStartupFailure, out)
	}
}

func TestDiagnose_NestedDocker_NoFailureRecorded(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	e := &Engine{Runtime: "docker", Stdout: &stdout, Stderr: &stderr}
	// FAKE_VOLUME_INSPECT_EXIT unset (default 0): the dind volume exists, so
	// this is a genuine dind session. FAKE_DOCKERD_MARKER unset: no
	// persistent dockerd-startup-error marker was ever written.

	if err := e.Diagnose(dindDiagnoseScope()); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Nested Docker:") {
		t.Errorf("expected an always-on \"Nested Docker:\" line (Q3), got:\n%s", out)
	}
	if !strings.Contains(out, "no failure recorded") {
		t.Errorf("expected the no-failure-recorded state for a healthy dind session, got:\n%s", out)
	}
	if strings.Contains(out, string(errcode.SandboxDindStartupFailure)) {
		t.Errorf("did not expect %s when no dockerd marker was ever written, got:\n%s", errcode.SandboxDindStartupFailure, out)
	}
}

func TestDiagnose_NestedDocker_MarkerPresent_AttachesDegradedFinding(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	e := &Engine{Runtime: "docker", Stdout: &stdout, Stderr: &stderr}
	const marker = "2026-07-24T09:00:00Z dockerd exited with status 1: failed to start daemon: mkdir /var/lib/docker/overlay2: read-only file system"
	t.Setenv("FAKE_DOCKERD_MARKER", marker)

	if err := e.Diagnose(dindDiagnoseScope()); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Nested Docker:") {
		t.Errorf("expected an always-on \"Nested Docker:\" line (Q3), got:\n%s", out)
	}
	if !strings.Contains(out, marker) {
		t.Errorf("expected the dockerd-startup-error marker's diagnostic surfaced verbatim, got:\n%s", out)
	}
	if !strings.Contains(out, string(errcode.SandboxDindStartupFailure)) {
		t.Errorf("expected %s attached for the recorded dockerd startup failure, got:\n%s", errcode.SandboxDindStartupFailure, out)
	}
	if !strings.Contains(out, string(SeverityDegraded)) {
		t.Errorf("expected degraded severity for the dind startup-failure finding, got:\n%s", out)
	}
}
