package launcher

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
)

// ociCreateStderr is the verbatim shape Docker streams when the OCI runtime
// rejects the container spec — the sysbox-ce 0.7.0 / Docker 29 time-namespace
// incompatibility that motivated #1077. Kept as a literal (not a substring of
// the matcher's own constant) so a matcher loosened to the point of matching
// nothing real still fails these tests.
const ociCreateStderr = `docker: Error response from daemon: failed to create task for container: ` +
	`failed to create shim task: OCI runtime create failed: namespace {"time" ""} does not exist`

// TestCreateFailureError_DindOCIFailureNamesSysboxAndEscapeHatch asserts the
// dind create failure is translated into an error that explains cenci's own
// role in it: the raw daemon text never mentions sysbox, dind, or the flag
// cenci added, so a user who hits it has nothing to act on.
func TestCreateFailureError_DindOCIFailureNamesSysboxAndEscapeHatch(t *testing.T) {
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, ociCreateStderr, runErr)
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed dind create; want an error")
	}
	msg := got.Error()

	for _, want := range []string{
		"--runtime=sysbox-runc",
		"--no-dind",
		`"sandbox": {"dind": false}`,
		string(errcode.SandboxDindRuntimeCreateFailed),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("createFailureError message missing %q; got:\n%s", want, msg)
		}
	}

	if !errors.Is(got, runErr) {
		t.Errorf("createFailureError dropped the underlying error from the %%w chain; got:\n%s", msg)
	}
}

// TestCreateFailureError_DindNonOCIFailureKeepsPlainWording asserts the
// sysbox explanation is scoped to the failure class it actually explains. A
// dind launch can fail to create for ordinary reasons (name conflict, bad
// mount); blaming sysbox for those would send the user down the wrong path.
func TestCreateFailureError_DindNonOCIFailureKeepsPlainWording(t *testing.T) {
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")
	stderr := `docker: Error response from daemon: invalid mount config for type "bind": ` +
		`bind source path does not exist: /nope`

	got := e.createFailureError(true, false, stderr, runErr)
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed create; want an error")
	}
	if msg := got.Error(); strings.Contains(msg, "sysbox-runc") {
		t.Errorf("createFailureError blamed sysbox for an unrelated dind create failure; got:\n%s", msg)
	}
	if want := "docker run: exit status 125"; got.Error() != want {
		t.Errorf("createFailureError = %q, want the plain wording %q", got.Error(), want)
	}
}

// TestCreateFailureError_NonDindKeepsPlainWording asserts a launch that never
// asked for dind is never told about sysbox, even when the runtime failure is
// OCI-shaped: cenci did not add --runtime=sysbox-runc to that argv, so sysbox
// cannot be the cause.
func TestCreateFailureError_NonDindKeepsPlainWording(t *testing.T) {
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(false, false, ociCreateStderr, runErr)
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed create; want an error")
	}
	if msg := got.Error(); strings.Contains(msg, "sysbox-runc") {
		t.Errorf("createFailureError mentioned sysbox for a non-dind launch; got:\n%s", msg)
	}
	if want := "docker run: exit status 125"; got.Error() != want {
		t.Errorf("createFailureError = %q, want the plain wording %q", got.Error(), want)
	}
}

// TestCreateFailureError_PodmanRuntimeNamedInPlainWording pins that the plain
// branch still names the resolved runtime rather than hardcoding "docker",
// matching the pre-#1077 "%s run: %w" contract.
func TestCreateFailureError_PodmanRuntimeNamedInPlainWording(t *testing.T) {
	e := &Engine{Runtime: "podman"}
	got := e.createFailureError(false, false, "", errors.New("exit status 1"))
	if want := "podman run: exit status 1"; got.Error() != want {
		t.Errorf("createFailureError = %q, want %q", got.Error(), want)
	}
}

// TestCapturedStderr_MirrorsVerbatimWhileCapturing asserts the classification
// capture never costs the user the runtime's own diagnostics: everything
// written still reaches the mirror byte-for-byte.
func TestCapturedStderr_MirrorsVerbatimWhileCapturing(t *testing.T) {
	var mirror bytes.Buffer
	c := newCapturedStderr(&mirror)

	chunks := []string{"docker: Error response ", "from daemon: OCI runtime create failed\n"}
	for _, chunk := range chunks {
		n, err := c.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("Write(%q) returned error: %v", chunk, err)
		}
		if n != len(chunk) {
			t.Fatalf("Write(%q) = %d, want %d; a short write makes os/exec abort the stream copy", chunk, n, len(chunk))
		}
	}

	want := strings.Join(chunks, "")
	if mirror.String() != want {
		t.Errorf("mirror got %q, want the stream verbatim %q", mirror.String(), want)
	}
	if c.String() != want {
		t.Errorf("captured got %q, want %q", c.String(), want)
	}
}

// TestCapturedStderr_CapsRetainedBytesButKeepsMirroring asserts a runaway
// stderr stream cannot grow the classification buffer without bound, while
// the mirror still receives every byte.
func TestCapturedStderr_CapsRetainedBytesButKeepsMirroring(t *testing.T) {
	var mirror bytes.Buffer
	c := newCapturedStderr(&mirror)

	flood := strings.Repeat("x", stderrCaptureLimit*3)
	if _, err := c.Write([]byte(flood)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if got := len(c.String()); got != stderrCaptureLimit {
		t.Errorf("captured %d bytes, want the cap %d", got, stderrCaptureLimit)
	}
	if got := mirror.Len(); got != len(flood) {
		t.Errorf("mirror got %d bytes, want every byte %d", got, len(flood))
	}
}

// TestCapturedStderr_ClassifiesAcrossChunkBoundaries asserts the retained
// prefix is a single contiguous buffer, so a marker split across two Write
// calls (the normal case for a piped subprocess) still classifies. Guards
// against a per-Write matcher that would silently stop recognizing the
// failure whenever the OS happened to split the stream mid-marker.
func TestCapturedStderr_ClassifiesAcrossChunkBoundaries(t *testing.T) {
	c := newCapturedStderr(io.Discard)
	for _, chunk := range []string{"failed to create shim task: OCI runtime ", "create failed: namespace"} {
		if _, err := c.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}

	e := &Engine{Runtime: "docker"}
	got := e.createFailureError(true, false, c.String(), errors.New("exit status 125"))
	if !strings.Contains(got.Error(), "sysbox-runc") {
		t.Errorf("a marker split across writes was not classified; got:\n%s", got.Error())
	}
}

// TestSeverityForCode_DindRuntimeCreateFailedIsFatal pins the new code's tier
// explicitly. It is fatal, not degraded: unlike DIND-001 (the session runs,
// only nested Docker is missing) and DIND-002 (a host capability that never
// existed), the container was never created at all, so there is no session.
func TestSeverityForCode_DindRuntimeCreateFailedIsFatal(t *testing.T) {
	if got := severityForCode(errcode.SandboxDindRuntimeCreateFailed); got != SeverityFatal {
		t.Errorf("severityForCode(%s) = %q, want %q", errcode.SandboxDindRuntimeCreateFailed, got, SeverityFatal)
	}
}

// TestDindRuntimeCreateFailed_IsRegistered asserts the new code carries the
// diagnostic content every registered code owes its consumers — renderFinding
// prints Hints verbatim, so an entry without them degrades diagnose output.
func TestDindRuntimeCreateFailed_IsRegistered(t *testing.T) {
	entry, ok := errcode.Lookup(errcode.SandboxDindRuntimeCreateFailed)
	if !ok {
		t.Fatalf("errcode.Lookup(%s) = _, false; want a registered entry", errcode.SandboxDindRuntimeCreateFailed)
	}
	if entry.Message == "" {
		t.Error("registered entry has an empty Message")
	}
	if len(entry.Causes) == 0 {
		t.Error("registered entry has no Causes")
	}
	if len(entry.Hints) == 0 {
		t.Error("registered entry has no Hints")
	}
}

// -- ticket #1216: limit-rejection classification (AC6) ----------------------
//
// Real runtime create failures, kept as literals (not substrings of the
// matcher's own markers) so a matcher loosened to match nothing real still
// fails these tests. The runc/moby shapes are the verbatim form Docker
// streams when a memory or pids cgroup controller is not delegated to the
// engine; the crun shape is podman's.
const (
	runcMemoryMaxStderr = `docker: Error response from daemon: failed to create task for container: ` +
		`failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: ` +
		`error during container init: error setting cgroup config for procHooks process: ` +
		`openat2 /sys/fs/cgroup/docker/0d3c1f/memory.max: no such file or directory: unknown`
	runcMemorySwapMaxStderr = `docker: Error response from daemon: failed to create task for container: ` +
		`failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: ` +
		`error during container init: error setting cgroup config for procHooks process: ` +
		`openat2 /sys/fs/cgroup/docker/0d3c1f/memory.swap.max: no such file or directory: unknown`
	runcPidsMaxStderr = `docker: Error response from daemon: failed to create task for container: ` +
		`failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: ` +
		`error during container init: error setting cgroup config for procHooks process: ` +
		`openat2 /sys/fs/cgroup/docker/0d3c1f/pids.max: no such file or directory: unknown`
	runcCgroupV1MemoryStderr = `docker: Error response from daemon: failed to create task for container: ` +
		`failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: ` +
		`error during container init: error setting cgroup config for procHooks process: ` +
		`open /sys/fs/cgroup/memory/docker/0d3c1f/memory.limit_in_bytes: no such file or directory: unknown`
	crunPidsControllerStderr   = "Error: crun: the requested cgroup controller `pids` is not available: OCI runtime error"
	crunMemoryControllerStderr = "Error: crun: the requested cgroup controller `memory` is not available: OCI runtime error"
	mobyMinimumMemoryStderr    = `docker: Error response from daemon: Minimum memory limit allowed is 6MB.`
)

// assertLimitsRejectedError asserts the LIMITS-002 error contract shared by
// every limit-rejection case: the wrapped run error is preserved, the code is
// present, both opt-out keys are named, and the sysbox explanation is absent.
func assertLimitsRejectedError(t *testing.T, got, runErr error) {
	t.Helper()
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed create; want an error")
	}
	msg := got.Error()
	for _, want := range []string{
		string(errcode.SandboxLimitsCreateRejected),
		`"memoryLimit": "none"`,
		`"pidsLimit": 0`,
		"docker run:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("limit-rejection error missing %q; got:\n%s", want, msg)
		}
	}
	if !errors.Is(got, runErr) {
		t.Errorf("limit-rejection error dropped the underlying error from the %%w chain; got:\n%s", msg)
	}
	if strings.Contains(msg, string(errcode.SandboxDindRuntimeCreateFailed)) || strings.Contains(msg, "sysbox-runc") {
		t.Errorf("limit-rejection error must not carry the DinD sysbox explanation; got:\n%s", msg)
	}
}

// TestCreateFailureError_LimitRejection_NamesCauseCodeAndBothOptOuts is AC6:
// a create the runtime rejected because of a sent limit flag maps to
// CENCI-SANDBOX-LIMITS-002 naming the cause and both opt-out keys.
func TestCreateFailureError_LimitRejection_NamesCauseCodeAndBothOptOuts(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		// cause is the verbatim runtime marker the error must quote; empty
		// where the marker is a free-form sentence not worth pinning.
		cause string
	}{
		{"runc cgroup v2 memory.max", runcMemoryMaxStderr, "memory.max"},
		{"runc cgroup v2 memory.swap.max", runcMemorySwapMaxStderr, "memory.swap.max"},
		{"runc cgroup v2 pids.max", runcPidsMaxStderr, "pids.max"},
		{"runc cgroup v1 memory.limit_in_bytes", runcCgroupV1MemoryStderr, "memory.limit_in_bytes"},
		{"crun pids controller not available", crunPidsControllerStderr, ""},
		{"crun memory controller not available", crunMemoryControllerStderr, ""},
		{"moby minimum memory limit", mobyMinimumMemoryStderr, "Minimum memory limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{Runtime: "docker"}
			runErr := errors.New("exit status 125")

			got := e.createFailureError(false, true, tc.stderr, runErr)

			assertLimitsRejectedError(t, got, runErr)
			if tc.cause != "" && !strings.Contains(got.Error(), tc.cause) {
				t.Errorf("limit-rejection error does not name the cause %q; got:\n%s", tc.cause, got.Error())
			}
		})
	}
}

// TestCreateFailureError_LimitRejection_WinsOverDindOCIFailure pins the
// marker precedence: runc's cgroup errors arrive wrapped in the same "OCI
// runtime create failed" text the DinD mapping keys on, so with both dind and
// limits on, the more specific limit marker must win -- blaming sysbox for a
// missing memory controller would send the user to the wrong fix.
func TestCreateFailureError_LimitRejection_WinsOverDindOCIFailure(t *testing.T) {
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, true, runcMemoryMaxStderr, runErr)

	assertLimitsRejectedError(t, got, runErr)
}

// TestCreateFailureError_DindOCIFailureWithoutLimitMarkers_KeepsDind003 is
// the regression for the narrowing: with limits on, a dind OCI failure that
// carries no limit marker (the #1077 time-namespace case) keeps its
// DIND-003 explanation, never LIMITS-002.
func TestCreateFailureError_DindOCIFailureWithoutLimitMarkers_KeepsDind003(t *testing.T) {
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, true, ociCreateStderr, runErr)
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed dind create; want an error")
	}
	msg := got.Error()
	if !strings.Contains(msg, string(errcode.SandboxDindRuntimeCreateFailed)) {
		t.Errorf("a dind OCI failure without limit markers lost its DIND-003 explanation; got:\n%s", msg)
	}
	if strings.Contains(msg, string(errcode.SandboxLimitsCreateRejected)) {
		t.Errorf("a dind OCI failure without limit markers was misclassified as LIMITS-002; got:\n%s", msg)
	}
}

// TestCreateFailureError_LimitsOff_NeverYieldsLimits002 pins that the
// classification is gated on a limit flag actually having been sent: with
// both opt-outs active no limit flag was on the argv, so even a stderr full
// of limit markers cannot be the limits' doing and keeps the original path.
func TestCreateFailureError_LimitsOff_NeverYieldsLimits002(t *testing.T) {
	runErr := errors.New("exit status 125")
	for name, stderr := range map[string]string{
		"runc memory.max":  runcMemoryMaxStderr,
		"runc pids.max":    runcPidsMaxStderr,
		"crun pids":        crunPidsControllerStderr,
		"moby minimum mem": mobyMinimumMemoryStderr,
	} {
		t.Run("non-dind "+name, func(t *testing.T) {
			e := &Engine{Runtime: "docker"}
			got := e.createFailureError(false, false, stderr, runErr)
			if want := "docker run: exit status 125"; got.Error() != want {
				t.Errorf("createFailureError(limitsOn=false) = %q, want the plain wording %q", got.Error(), want)
			}
		})
	}

	t.Run("dind keeps DIND-003 not LIMITS-002", func(t *testing.T) {
		e := &Engine{Runtime: "docker"}
		got := e.createFailureError(true, false, runcMemoryMaxStderr, runErr)
		msg := got.Error()
		if strings.Contains(msg, string(errcode.SandboxLimitsCreateRejected)) {
			t.Errorf("createFailureError(limitsOn=false) yielded LIMITS-002; got:\n%s", msg)
		}
		if !strings.Contains(msg, string(errcode.SandboxDindRuntimeCreateFailed)) {
			t.Errorf("createFailureError(dindOn, limitsOn=false) on an OCI failure lost DIND-003; got:\n%s", msg)
		}
	})
}

// TestCreateFailureError_NonLimitFailureWithLimitsOn_KeepsPlainWording is the
// match-miss regression (watch/AGENTS.md critical rule): the limit markers
// are narrow, so an unrelated create failure -- including ones whose text
// merely mentions memory or a different cgroup controller -- keeps its
// visible plain "<runtime> run: <err>" wording.
func TestCreateFailureError_NonLimitFailureWithLimitsOn_KeepsPlainWording(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
	}{
		{"name conflict", `docker: Error response from daemon: Conflict. The container name "/claude-cenci-repo" is already in use by container "0d3c1f".`},
		{"bad bind mount", `docker: Error response from daemon: invalid mount config for type "bind": bind source path does not exist: /nope`},
		{"fork cannot allocate memory", `docker: Error response from daemon: failed to create task for container: fork/exec /usr/bin/runc: cannot allocate memory`},
		{"different cgroup controller", "Error: crun: the requested cgroup controller `cpuset` is not available: OCI runtime error"},
		{"empty stderr", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{Runtime: "docker"}
			got := e.createFailureError(false, true, tc.stderr, errors.New("exit status 125"))
			if want := "docker run: exit status 125"; got.Error() != want {
				t.Errorf("createFailureError = %q, want the plain wording %q", got.Error(), want)
			}
		})
	}
}

// TestCreateFailureError_LimitMarkerSplitAcrossWrites_Classifies mirrors the
// dind chunk-boundary regression for the limit markers: the retained prefix
// is one contiguous buffer, so a marker split across two Write calls still
// classifies.
func TestCreateFailureError_LimitMarkerSplitAcrossWrites_Classifies(t *testing.T) {
	c := newCapturedStderr(io.Discard)
	for _, chunk := range []string{"openat2 /sys/fs/cgroup/docker/0d3c1f/mem", "ory.max: no such file or directory: unknown"} {
		if _, err := c.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}

	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")
	assertLimitsRejectedError(t, e.createFailureError(false, true, c.String(), runErr), runErr)
}

// -- ticket #1159: stale-kernel classification (CENCI-SANDBOX-DIND-004) -------

// staleKernelRelease is the running-kernel release the stale-kernel fixture
// names. It is a literal in both the stderr and the tests so a matcher that
// quietly reads some other release still fails.
const staleKernelRelease = "7.2.2-1-cachyos"

// staleKernelStderr is the verbatim shape Docker streams on a host whose
// packages were upgraded past the running kernel without a reboot:
// sysbox-runc bind-mounts /lib/modules/$(uname -r) and that directory is gone.
// The two line breaks are part of the real stream (the daemon wraps its
// chained error), and the /lib/modules path straddles one of them. Kept as a
// literal, not built from the matcher's own pattern, so a matcher loosened to
// match nothing real still fails these tests.
const staleKernelStderr = `docker: Error response from daemon: failed to create task for container: failed to create shim task:
OCI runtime create failed: failed to create lib container mount: failed to stat mount source at
/lib/modules/7.2.2-1-cachyos: stat /lib/modules/7.2.2-1-cachyos: no such file or directory`

// withKernelProbeFixture pins both host-probe seams for one test: the running
// kernel release (osrelease, written the way /proc does, with a trailing
// newline) and a modules root holding one directory per entry of modules. It
// returns the modules root and restores the seams with t.Cleanup. Tests that
// call it must not use t.Parallel: the seams are package-level vars.
func withKernelProbeFixture(t *testing.T, osrelease string, modules []string) string {
	t.Helper()
	dir := t.TempDir()

	osReleaseFile := filepath.Join(dir, "osrelease")
	if err := os.WriteFile(osReleaseFile, []byte(osrelease+"\n"), 0o600); err != nil {
		t.Fatalf("write osrelease fixture: %v", err)
	}
	root := filepath.Join(dir, "lib", "modules")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create modules root fixture: %v", err)
	}
	for _, m := range modules {
		if err := os.MkdirAll(filepath.Join(root, m), 0o755); err != nil {
			t.Fatalf("create modules entry %q: %v", m, err)
		}
	}

	prevOSRelease, prevRoot := dindOSReleasePath, dindModulesRoot
	dindOSReleasePath, dindModulesRoot = osReleaseFile, root
	t.Cleanup(func() { dindOSReleasePath, dindModulesRoot = prevOSRelease, prevRoot })
	return root
}

// assertStaleKernelError asserts the DIND-004 contract: the code, the running
// release, the display path, the remediation, the escape hatch and the
// available list are present; the DIND-003 guidance is absent; and the wrapped
// run error survives.
func assertStaleKernelError(t *testing.T, got, runErr error, release, available string) {
	t.Helper()
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed dind create; want an error")
	}
	msg := got.Error()
	for _, want := range []string{
		string(errcode.SandboxDindStaleKernelModules),
		release,
		"/lib/modules/" + release + " does not exist",
		"the kernel was upgraded without a reboot",
		"This is not a sysbox-ce version problem",
		"Reboot into the installed kernel",
		"available: " + available,
		"--no-dind",
		`"sandbox": {"dind": false}`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("stale-kernel error missing %q; got:\n%s", want, msg)
		}
	}
	for _, unwanted := range []string{
		"update sysbox-ce",
		"https://github.com/nestybox/sysbox/releases",
		string(errcode.SandboxDindRuntimeCreateFailed),
		string(errcode.SandboxLimitsCreateRejected),
	} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("stale-kernel error must not contain %q; got:\n%s", unwanted, msg)
		}
	}
	if !strings.HasPrefix(msg, "docker run: exit status 125\n") {
		t.Errorf("stale-kernel error lost the %q first line; got:\n%s", "docker run: exit status 125", msg)
	}
	if !errors.Is(got, runErr) {
		t.Errorf("stale-kernel error dropped the underlying error from the %%w chain; got:\n%s", msg)
	}
}

// assertDind003Unchanged asserts a probe miss falls through to the unchanged
// DIND-003 wording, including its sysbox-ce hint, and never reports DIND-004.
func assertDind003Unchanged(t *testing.T, got, runErr error) {
	t.Helper()
	if got == nil {
		t.Fatal("createFailureError returned nil for a failed dind create; want an error")
	}
	msg := got.Error()
	for _, want := range []string{
		string(errcode.SandboxDindRuntimeCreateFailed),
		"update sysbox-ce",
		"https://github.com/nestybox/sysbox/releases",
		"--no-dind",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("DIND-003 fall-through missing %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, string(errcode.SandboxDindStaleKernelModules)) {
		t.Errorf("a probe miss was misclassified as DIND-004; got:\n%s", msg)
	}
	if !errors.Is(got, runErr) {
		t.Errorf("DIND-003 fall-through dropped the underlying error from the %%w chain; got:\n%s", msg)
	}
}

// TestCreateFailureError_StaleKernel_RemediatesWithoutBlamingSysbox is the
// headline AC: the verbatim stale-kernel stderr, a running kernel equal to the
// named release and no /lib/modules/<release> directory yields DIND-004 with
// the reboot remediation, the sorted installed releases and the escape hatch,
// and nothing that sends the operator to update sysbox-ce.
func TestCreateFailureError_StaleKernel_RemediatesWithoutBlamingSysbox(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos", "7.2.9-1-cachyos"})
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, staleKernelStderr, runErr)

	assertStaleKernelError(t, got, runErr, staleKernelRelease, "7.2.9-1-cachyos, 7.3.1-1-cachyos")
}

// TestCreateFailureError_StaleKernel_PrintsFixedDisplayPathNotTheRoot pins that
// the message names the path the runtime named (/lib/modules/<release>), never
// the test-overridable modules root used for the probes.
func TestCreateFailureError_StaleKernel_PrintsFixedDisplayPathNotTheRoot(t *testing.T) {
	root := withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos"})
	e := &Engine{Runtime: "docker"}

	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, staleKernelStderr, runErr)

	// The DIND-003 fall-through also omits the root, so first prove the
	// DIND-004 branch fired; otherwise the root-absence check below is vacuous.
	assertStaleKernelError(t, got, runErr, staleKernelRelease, "7.3.1-1-cachyos")
	if strings.Contains(got.Error(), root) {
		t.Errorf("stale-kernel error leaked the probe root %q instead of /lib/modules; got:\n%s", root, got.Error())
	}
}

// TestCreateFailureError_OtherSysboxRejection_KeepsDind003WithStaleFixture is
// the "other spec rejections keep DIND-003" AC and the #1077 edge case: the
// time-namespace stderr names no /lib/modules path, so it keeps DIND-003 with
// its update-sysbox-ce hint even when the host probes would say "stale".
func TestCreateFailureError_OtherSysboxRejection_KeepsDind003WithStaleFixture(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos"})
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, ociCreateStderr, runErr)

	assertDind003Unchanged(t, got, runErr)
}

// TestCreateFailureError_StaleKernel_ModulesDirExists_KeepsDind003 is the edge
// case where the stderr names /lib/modules/<release> but that directory exists
// locally: the missing directory is on another side, so the sysbox wording stays.
func TestCreateFailureError_StaleKernel_ModulesDirExists_KeepsDind003(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{staleKernelRelease, "7.3.1-1-cachyos"})
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, staleKernelStderr, runErr)

	assertDind003Unchanged(t, got, runErr)
}

// TestCreateFailureError_StaleKernel_ReleaseDiffersFromRunningKernel_KeepsDind003
// is the remote DOCKER_HOST edge case: the stderr names a release that is not
// the local running kernel, which the local probe cannot judge.
func TestCreateFailureError_StaleKernel_ReleaseDiffersFromRunningKernel_KeepsDind003(t *testing.T) {
	withKernelProbeFixture(t, "7.3.1-1-cachyos", []string{"7.3.1-1-cachyos"})
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, staleKernelStderr, runErr)

	assertDind003Unchanged(t, got, runErr)
}

// TestCreateFailureError_StaleKernel_OSReleaseUnreadable_KeepsDind003 is the
// edge case where /proc/sys/kernel/osrelease cannot be read.
func TestCreateFailureError_StaleKernel_OSReleaseUnreadable_KeepsDind003(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos"})
	dindOSReleasePath = filepath.Join(t.TempDir(), "absent-osrelease")
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, staleKernelStderr, runErr)

	assertDind003Unchanged(t, got, runErr)
}

// TestCreateFailureError_StaleKernel_StatErrorOtherThanNotExist_KeepsDind003 is
// the edge case where the local stat fails with something other than
// not-exist. The modules root is a regular file, so statting <root>/<release>
// fails with ENOTDIR, which errors.Is(err, fs.ErrNotExist) does not match, and
// the result is the same for root and non-root runners (chmod 000 is bypassed
// by root).
func TestCreateFailureError_StaleKernel_StatErrorOtherThanNotExist_KeepsDind003(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, nil)
	notADir := filepath.Join(t.TempDir(), "modules-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write regular-file modules root: %v", err)
	}
	dindModulesRoot = notADir
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, false, staleKernelStderr, runErr)

	assertDind003Unchanged(t, got, runErr)
}

// TestCreateFailureError_StaleKernel_UnlistableOrEmptyRoot_PrintsNoneFound is
// the edge case where the modules root is empty or cannot be listed: the
// remediation still prints, with "available: none found" in place of a list.
func TestCreateFailureError_StaleKernel_UnlistableOrEmptyRoot_PrintsNoneFound(t *testing.T) {
	runErr := errors.New("exit status 125")

	t.Run("empty root", func(t *testing.T) {
		withKernelProbeFixture(t, staleKernelRelease, nil)
		e := &Engine{Runtime: "docker"}

		got := e.createFailureError(true, false, staleKernelStderr, runErr)

		assertStaleKernelError(t, got, runErr, staleKernelRelease, "none found")
	})

	t.Run("nonexistent root", func(t *testing.T) {
		withKernelProbeFixture(t, staleKernelRelease, nil)
		dindModulesRoot = filepath.Join(t.TempDir(), "no-such-modules-root")
		e := &Engine{Runtime: "docker"}

		got := e.createFailureError(true, false, staleKernelStderr, runErr)

		assertStaleKernelError(t, got, runErr, staleKernelRelease, "none found")
	})
}

// TestCreateFailureError_StaleKernel_UnsafeReleaseToken_KeepsDind003 is the
// edge case for a release token with a separator, "..", or characters outside
// [A-Za-z0-9._+~-]. Each case sets the running kernel release to the same
// hostile token, so a missing validation would pass the equality check, stat a
// path that does not exist and fire DIND-004; falling through to DIND-003 shows
// the token was rejected before any path was joined under the modules root.
func TestCreateFailureError_StaleKernel_UnsafeReleaseToken_KeepsDind003(t *testing.T) {
	for _, token := range []string{"../etc", "a/b", "a..b", ".hidden", "x$y"} {
		t.Run(token, func(t *testing.T) {
			withKernelProbeFixture(t, token, []string{"7.3.1-1-cachyos"})
			stderr := strings.ReplaceAll(staleKernelStderr, staleKernelRelease, token)
			e := &Engine{Runtime: "docker"}
			runErr := errors.New("exit status 125")

			got := e.createFailureError(true, false, stderr, runErr)

			assertDind003Unchanged(t, got, runErr)
		})
	}
}

// TestCreateFailureError_StaleKernel_NonDind_KeepsPlainWording is the edge case
// for a launch that never asked for dind: exactly the plain wording, even with
// stale-kernel stderr and probes that would otherwise say "stale".
func TestCreateFailureError_StaleKernel_NonDind_KeepsPlainWording(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos"})
	e := &Engine{Runtime: "docker"}

	got := e.createFailureError(false, false, staleKernelStderr, errors.New("exit status 125"))

	if got == nil {
		t.Fatal("createFailureError returned nil for a failed create; want an error")
	}
	if want := "docker run: exit status 125"; got.Error() != want {
		t.Errorf("createFailureError = %q, want the plain wording %q", got.Error(), want)
	}
}

// TestCreateFailureError_StaleKernel_WithLimitsOn_YieldsDind004NotLimits002 is
// the edge case for limitsOn=true: the stale-kernel stderr carries no limit
// marker, so it must be DIND-004, never LIMITS-002.
func TestCreateFailureError_StaleKernel_WithLimitsOn_YieldsDind004NotLimits002(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos"})
	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")

	got := e.createFailureError(true, true, staleKernelStderr, runErr)

	assertStaleKernelError(t, got, runErr, staleKernelRelease, "7.3.1-1-cachyos")
}

// TestCreateFailureError_StaleKernel_SplitAcrossWrites_Classifies is the edge
// case for stderr arriving in chunks with a split inside the matched span
// "stat /lib/modules/<release>: no such file or directory" (the cut falls
// between "stat /lib/mod" and "ules/", not at the earlier path-only line the
// matcher ignores): the retained prefix is one contiguous buffer, so it still
// classifies.
func TestCreateFailureError_StaleKernel_SplitAcrossWrites_Classifies(t *testing.T) {
	withKernelProbeFixture(t, staleKernelRelease, []string{"7.3.1-1-cachyos"})
	const spanStart = "stat /lib/modules/"
	const splitAt = "stat /lib/mod"
	idx := strings.Index(staleKernelStderr, spanStart)
	if idx < 0 {
		t.Fatalf("fixture does not contain %q", spanStart)
	}
	cut := idx + len(splitAt)

	c := newCapturedStderr(io.Discard)
	for _, chunk := range []string{staleKernelStderr[:cut], staleKernelStderr[cut:]} {
		if _, err := c.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}

	e := &Engine{Runtime: "docker"}
	runErr := errors.New("exit status 125")
	got := e.createFailureError(true, false, c.String(), runErr)

	assertStaleKernelError(t, got, runErr, staleKernelRelease, "7.3.1-1-cachyos")
}

// TestSeverityForCode_DindStaleKernelModulesIsFatal pins DIND-004's tier
// explicitly: like DIND-003 the container was never created, so it is fatal.
func TestSeverityForCode_DindStaleKernelModulesIsFatal(t *testing.T) {
	if got := severityForCode(errcode.SandboxDindStaleKernelModules); got != SeverityFatal {
		t.Errorf("severityForCode(%s) = %q, want %q", errcode.SandboxDindStaleKernelModules, got, SeverityFatal)
	}
}
