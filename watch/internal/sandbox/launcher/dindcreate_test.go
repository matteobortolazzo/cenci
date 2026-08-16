package launcher

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/errcode"
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

	got := e.createFailureError(true, ociCreateStderr, runErr)
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

	got := e.createFailureError(true, stderr, runErr)
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

	got := e.createFailureError(false, ociCreateStderr, runErr)
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
	got := e.createFailureError(false, "", errors.New("exit status 1"))
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
	got := e.createFailureError(true, c.String(), errors.New("exit status 125"))
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
