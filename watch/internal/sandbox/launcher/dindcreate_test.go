package launcher

import (
	"bytes"
	"errors"
	"io"
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
