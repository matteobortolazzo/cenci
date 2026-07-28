package launcher

import (
	"errors"
	"reflect"
	"testing"
)

// -- ticket #627: Audit's observed-mode inspect parser ----------------------
//
// parseObservedInspect parses the combined read-only inspect probe
// deriveObservedPosture issues against a running scoped container: line 1 is
// "<image>|<networkMode>|<runtime>|<dindLabel>|<dindEnvFlag 0-or-1>";
// remaining lines are "<source>::<destination>::<rw true-or-false>" per
// mount — mirroring parseReusePosture's (launch.go, ticket #628) own
// fail-closed contract for the sibling reuse-posture probe.
//
// NOTE (red phase): parseObservedInspect, observedInspect/observedMount, and
// ErrMalformedObservedInspect do not exist yet (land in audit_observed.go,
// a later phase) — every test below fails to COMPILE until then. That is
// the intended red-phase state.

// -- well-formed input ------------------------------------------------------

// TestParseObservedInspect_WellFormed_ParsesAllFields pins the parser's
// happy-path contract: every header field and every mount line's three
// "::"-delimited components map onto the correct observedInspect field.
func TestParseObservedInspect_WellFormed_ParsesAllFields(t *testing.T) {
	out := "cenci-sandbox:latest|host|sysbox-runc|on|1\n" +
		"/host/repo::/workspace::true\n" +
		"claude-cenci-home-repo::/home/dev::true\n" +
		"/var/run/docker.sock::/var/run/docker.sock::true\n"

	got, err := parseObservedInspect(out)
	if err != nil {
		t.Fatalf("parseObservedInspect: %v", err)
	}

	if got.Image != "cenci-sandbox:latest" {
		t.Errorf("Image = %q, want %q", got.Image, "cenci-sandbox:latest")
	}
	if got.NetworkMode != "host" {
		t.Errorf("NetworkMode = %q, want %q", got.NetworkMode, "host")
	}
	if got.Runtime != "sysbox-runc" {
		t.Errorf("Runtime = %q, want %q", got.Runtime, "sysbox-runc")
	}
	if got.DindLabel != "on" {
		t.Errorf("DindLabel = %q, want %q", got.DindLabel, "on")
	}
	if !got.DindEnv {
		t.Errorf("DindEnv = false, want true (header's dind-env field is \"1\")")
	}
	if len(got.Mounts) != 3 {
		t.Fatalf("len(Mounts) = %d, want 3, got %+v", len(got.Mounts), got.Mounts)
	}
	// RW=true in the probe means the mount is writable, so ReadOnly must be
	// the inverse (false) — matches the plan's explicit "ro = !RW" mapping
	// and audit_test.go's TestAudit_ObservedMode_MountsImageDindRuntimeVolumesFromInspect
	// integration assertion for the same "true" RW input. (The original
	// assertion here inverted this — `!got.Mounts[0].ReadOnly` required
	// ReadOnly==true for RW=true — contradicting both its own doc comment
	// and the sibling integration test; fixed as a genuine test-authoring
	// bug, not a design change.)
	if got.Mounts[0].Source != "/host/repo" || got.Mounts[0].Destination != "/workspace" || got.Mounts[0].ReadOnly {
		t.Errorf("Mounts[0] = %+v, want {/host/repo /workspace ReadOnly:false} (RW=true in the probe means writable, so ReadOnly must be the inverse)", got.Mounts[0])
	}
	if got.Mounts[2].Source != "/var/run/docker.sock" || got.Mounts[2].Destination != "/var/run/docker.sock" {
		t.Errorf("Mounts[2] = %+v, want the docker.sock mount preserved verbatim", got.Mounts[2])
	}
}

// TestParseObservedInspect_DindEnvZero_IsFalse covers the header's
// dind-env flag's "0" (unset) case, distinct from an empty field.
func TestParseObservedInspect_DindEnvZero_IsFalse(t *testing.T) {
	out := "cenci-sandbox:latest|bridge|runc||0\n"
	got, err := parseObservedInspect(out)
	if err != nil {
		t.Fatalf("parseObservedInspect: %v", err)
	}
	if got.DindEnv {
		t.Errorf("DindEnv = true for header field \"0\", want false")
	}
}

// TestParseObservedInspect_NoMountLines_EmptyMountsSlice covers a container
// with a header line but no mount lines at all (e.g. a from-scratch image
// with zero declared mounts) — Mounts must be an empty, non-panicking slice.
func TestParseObservedInspect_NoMountLines_EmptyMountsSlice(t *testing.T) {
	out := "cenci-sandbox:latest|bridge|runc||0\n"
	got, err := parseObservedInspect(out)
	if err != nil {
		t.Fatalf("parseObservedInspect: %v", err)
	}
	if len(got.Mounts) != 0 {
		t.Errorf("Mounts = %+v, want empty", got.Mounts)
	}
}

// -- fail-closed sentinel (ticket #627, following #628/#598's pattern; #412
// requires a direct package-boundary test for any errors.Is()-detectable
// sentinel, not just indirect integration coverage) ------------------------

// TestParseObservedInspect_EmptyOutput_FailsClosedWithSentinel covers a
// truncated/empty inspect response: the parser must reject it via the
// detectable ErrMalformedObservedInspect sentinel, never the permissive
// zero-value observedInspect{} (bridge/no-mounts/dind-off) that
// deriveObservedPosture's boundary-weakening and dind derivation would
// otherwise silently read as an unweakened, default-safe posture.
func TestParseObservedInspect_EmptyOutput_FailsClosedWithSentinel(t *testing.T) {
	_, err := parseObservedInspect("")
	if err == nil {
		t.Fatal("parseObservedInspect(\"\") = nil error, want a fail-closed error for empty output")
	}
	if !errors.Is(err, ErrMalformedObservedInspect) {
		t.Errorf("parseObservedInspect(\"\") error = %v, want errors.Is(err, ErrMalformedObservedInspect) to hold", err)
	}
}

// TestParseObservedInspect_MalformedHeader_FailsClosedWithSentinel covers a
// header line that doesn't split into exactly 5 "|"-delimited fields (e.g.
// a truncated/garbled `docker inspect --format` response) — content-specific
// per watch/docs/error-handling.md #446/#628: this must be distinguishable,
// via the same sentinel, from a well-formed empty-mounts response, not
// silently collapsed into the permissive zero value.
func TestParseObservedInspect_MalformedHeader_FailsClosedWithSentinel(t *testing.T) {
	tests := []struct {
		name string
		out  string
	}{
		{"too few fields", "cenci-sandbox:latest|bridge|runc\n"},
		{"garbage single token", "not-the-expected-shape\n"},
		{"too many fields", "a|b|c|d|e|f\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseObservedInspect(tc.out)
			if err == nil {
				t.Fatalf("parseObservedInspect(%q) = %+v, nil error, want a fail-closed error", tc.out, got)
			}
			if !errors.Is(err, ErrMalformedObservedInspect) {
				t.Errorf("parseObservedInspect(%q) error = %v, want errors.Is(err, ErrMalformedObservedInspect) to hold", tc.out, err)
			}
			// reflect.DeepEqual rather than != : observedInspect embeds a
			// []observedMount slice field, which Go does not allow comparing
			// with == / != at all (not even a runtime false — it's a compile
			// error), so structural equality is the only way to assert "the
			// zero value only, never a partially-populated permissive result".
			if !reflect.DeepEqual(got, observedInspect{}) {
				t.Errorf("parseObservedInspect(%q) returned a non-zero observedInspect %+v alongside its error; want the zero value only, never a partially-populated permissive result", tc.out, got)
			}
		})
	}
}

// TestParseObservedInspect_MalformedMountLine_FailsClosedWithSentinel covers
// a mount line that fails to split into exactly 3 "::"-delimited components
// — the dropped/garbled line could be exactly the host-socket bind the
// boundary-weakening check depends on, so it must never be silently skipped
// (mirrors parseReusePosture's own mount-line contract, launch.go #628).
func TestParseObservedInspect_MalformedMountLine_FailsClosedWithSentinel(t *testing.T) {
	out := "cenci-sandbox:latest|bridge|runc||0\n" +
		"/host/repo->/workspace (not double-colon delimited)\n"

	got, err := parseObservedInspect(out)
	if err == nil {
		t.Fatalf("parseObservedInspect(%q) = %+v, nil error, want a fail-closed error", out, got)
	}
	if !errors.Is(err, ErrMalformedObservedInspect) {
		t.Errorf("parseObservedInspect malformed mount line error = %v, want errors.Is(err, ErrMalformedObservedInspect) to hold", err)
	}
}

// TestParseObservedInspect_MalformedMountRWField_FailsClosedWithSentinel
// covers a mount line whose RW component is neither "true" nor "false" (an
// unrecognized/garbled boolean rendering) — must fail closed rather than
// silently defaulting ReadOnly to false (the more permissive reading).
func TestParseObservedInspect_MalformedMountRWField_FailsClosedWithSentinel(t *testing.T) {
	out := "cenci-sandbox:latest|bridge|runc||0\n" +
		"/host/repo::/workspace::maybe\n"

	_, err := parseObservedInspect(out)
	if err == nil {
		t.Fatal("parseObservedInspect with an unrecognized RW field = nil error, want a fail-closed error")
	}
	if !errors.Is(err, ErrMalformedObservedInspect) {
		t.Errorf("parseObservedInspect unrecognized-RW error = %v, want errors.Is(err, ErrMalformedObservedInspect) to hold", err)
	}
}

// TestParseObservedInspect_TrailingBlankLineFromRealDockerInspect_IsTolerated
// pins ticket #684's fix, mirroring parseReusePosture's own regression test:
// real `docker inspect --format` appends its own trailing newline on top of
// the template's own per-mount {{"\n"}} action, so the captured stdout
// always ends with one blank line after the last mount (or right after the
// header when there are no mounts). Before this fix, that blank line was
// rejected as a malformed mount line, breaking `cenci audit`/`security
// explain` against any real, already-running container. A non-trailing
// malformed line must still fail closed exactly as before.
func TestParseObservedInspect_TrailingBlankLineFromRealDockerInspect_IsTolerated(t *testing.T) {
	out := "cenci-sandbox:latest|bridge|runc||0\n" +
		"/host/repo::/workspace::true\n\n"

	got, err := parseObservedInspect(out)
	if err != nil {
		t.Fatalf("parseObservedInspect(%q): %v", out, err)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Source != "/host/repo" || got.Mounts[0].Destination != "/workspace" {
		t.Errorf("parseObservedInspect(%q) Mounts = %+v, want a single /host/repo::/workspace mount", out, got.Mounts)
	}

	noMounts := "cenci-sandbox:latest|bridge|runc||0\n\n"
	got, err = parseObservedInspect(noMounts)
	if err != nil {
		t.Fatalf("parseObservedInspect(%q): %v", noMounts, err)
	}
	if len(got.Mounts) != 0 {
		t.Errorf("parseObservedInspect(%q) Mounts = %+v, want empty", noMounts, got.Mounts)
	}

	stillMalformed := "cenci-sandbox:latest|bridge|runc||0\n" +
		"/host/repo->/workspace (not double-colon delimited)\n\n"
	_, err = parseObservedInspect(stillMalformed)
	if err == nil {
		t.Fatal("parseObservedInspect with a non-trailing malformed mount line = nil error, want a fail-closed rejection even with the trailing blank line present")
	}
	if !errors.Is(err, ErrMalformedObservedInspect) {
		t.Errorf("parseObservedInspect(%q) error = %v, want errors.Is(err, ErrMalformedObservedInspect) to hold", stillMalformed, err)
	}
}
