package errcode

import (
	"regexp"
	"testing"
)

// codeFormat mirrors the CENCI-<AREA>-<SUBAREA>-<NNN> convention documented
// in docs/error-codes.md and enforced by errcode.go's init-time validation.
var codeFormat = regexp.MustCompile(`^CENCI-[A-Z]+-[A-Z]+-[0-9]{3}$`)

// TestLookup_Hit asserts that a registered code resolves to a populated
// Entry (non-empty Message/Causes/Hints), proving the registry actually
// carries real diagnostic content rather than placeholder data.
func TestLookup_Hit(t *testing.T) {
	entry, ok := Lookup(SandboxStartAgentCLIMissing)
	if !ok {
		t.Fatalf("Lookup(%s) = _, false; want true", SandboxStartAgentCLIMissing)
	}
	if entry.Message == "" {
		t.Errorf("Lookup(%s).Message is empty", SandboxStartAgentCLIMissing)
	}
	if len(entry.Causes) == 0 {
		t.Errorf("Lookup(%s).Causes is empty", SandboxStartAgentCLIMissing)
	}
	if len(entry.Hints) == 0 {
		t.Errorf("Lookup(%s).Hints is empty", SandboxStartAgentCLIMissing)
	}
}

// TestLookup_Miss asserts that an unregistered code returns ok=false rather
// than a zero-value Entry masquerading as a hit.
func TestLookup_Miss(t *testing.T) {
	entry, ok := Lookup(Code("CENCI-DOES-NOTEXIST-999"))
	if ok {
		t.Fatalf("Lookup(unregistered) = %+v, true; want false", entry)
	}
	if entry.Message != "" || len(entry.Causes) != 0 || len(entry.Hints) != 0 {
		t.Errorf("Lookup(unregistered) returned a non-zero Entry on miss: %+v", entry)
	}
}

// TestAllConstants_ResolveToCompleteEntries walks every declared exported
// Code constant and asserts each one is registered with a non-empty
// Message/Causes/Hints via Lookup — catching a constant that was declared
// but never wired into the registry map.
func TestAllConstants_ResolveToCompleteEntries(t *testing.T) {
	constants := []Code{
		SandboxStartAgentCLIMissing,
		SandboxStartGenericEntrypoint,
		SandboxStartReadinessTimeout,
		SandboxSessionNotFound,
		DaemonConnUnreachable,
		DaemonSocketMissing,
	}
	for _, code := range constants {
		entry, ok := Lookup(code)
		if !ok {
			t.Errorf("Lookup(%s) = _, false; want true (declared constant must be registered)", code)
			continue
		}
		if entry.Message == "" {
			t.Errorf("Lookup(%s).Message is empty", code)
		}
		if len(entry.Causes) == 0 {
			t.Errorf("Lookup(%s).Causes is empty", code)
		}
		if len(entry.Hints) == 0 {
			t.Errorf("Lookup(%s).Hints is empty", code)
		}
	}
}

// TestRegisteredCodes_MatchFormat asserts every registered code satisfies
// the CENCI-<AREA>-<SUBAREA>-<NNN> format regex, guarding against future
// codes drifting from the documented convention.
func TestRegisteredCodes_MatchFormat(t *testing.T) {
	constants := []Code{
		SandboxStartAgentCLIMissing,
		SandboxStartGenericEntrypoint,
		SandboxStartReadinessTimeout,
		SandboxSessionNotFound,
		DaemonConnUnreachable,
		DaemonSocketMissing,
	}
	for _, code := range constants {
		if !codeFormat.MatchString(string(code)) {
			t.Errorf("code %q does not match format %s", code, codeFormat.String())
		}
	}
}

// TestAllDeclaredConstants_HaveRegistryEntry walks allCodes — the same list
// init() cross-checks against registry — and asserts Lookup succeeds for
// each. This documents and exercises the invariant init() enforces (a
// declared Code constant with no registry entry panics at load time) in the
// normal, non-panicking case.
func TestAllDeclaredConstants_HaveRegistryEntry(t *testing.T) {
	for _, code := range allCodes {
		if _, ok := Lookup(code); !ok {
			t.Errorf("Lookup(%s) = _, false; want true (allCodes entries must be registered)", code)
		}
	}
}

// TestTicketCodes_ExistAndAreDistinct pins the two codes this ticket
// introduces: both must be registered and must not collapse to the same
// value (which would silently merge the agent-CLI-missing and
// generic-entrypoint-failure classes into one code).
func TestTicketCodes_ExistAndAreDistinct(t *testing.T) {
	if SandboxStartAgentCLIMissing != "CENCI-SANDBOX-START-001" {
		t.Errorf("SandboxStartAgentCLIMissing = %q, want CENCI-SANDBOX-START-001", SandboxStartAgentCLIMissing)
	}
	if SandboxStartGenericEntrypoint != "CENCI-SANDBOX-START-002" {
		t.Errorf("SandboxStartGenericEntrypoint = %q, want CENCI-SANDBOX-START-002", SandboxStartGenericEntrypoint)
	}
	if SandboxStartAgentCLIMissing == SandboxStartGenericEntrypoint {
		t.Fatalf("SandboxStartAgentCLIMissing and SandboxStartGenericEntrypoint must be distinct, both = %q", SandboxStartAgentCLIMissing)
	}
	if _, ok := Lookup(SandboxStartAgentCLIMissing); !ok {
		t.Errorf("SandboxStartAgentCLIMissing not registered")
	}
	if _, ok := Lookup(SandboxStartGenericEntrypoint); !ok {
		t.Errorf("SandboxStartGenericEntrypoint not registered")
	}
}

// TestDiagnoseCodes_ExistAndAreDistinct pins the four codes ticket #572 (the
// `cenci diagnose` work) introduces: DaemonConnUnreachable
// (CENCI-DAEMON-CONN-001, daemon unreachable), DaemonSocketMissing
// (CENCI-DAEMON-SOCKET-001, event socket missing), SandboxSessionNotFound
// (CENCI-SANDBOX-SESSION-001, session/container not found), and the
// registry-only SandboxStartReadinessTimeout (CENCI-SANDBOX-START-003,
// readiness timeout — not yet wired into waitUntilReady, only referenced by
// diagnose). Every code must resolve to a real, non-empty Entry (proving the
// registry carries actual diagnostic content, not placeholder data), satisfy
// the CENCI-<AREA>-<SUBAREA>-<NNN> format, and stay pairwise distinct from
// every other registered code — including the two pre-existing #571 codes —
// so no two failure classes silently collapse onto the same identifier.
func TestDiagnoseCodes_ExistAndAreDistinct(t *testing.T) {
	wantValues := map[Code]string{
		DaemonConnUnreachable:        "CENCI-DAEMON-CONN-001",
		DaemonSocketMissing:          "CENCI-DAEMON-SOCKET-001",
		SandboxSessionNotFound:       "CENCI-SANDBOX-SESSION-001",
		SandboxStartReadinessTimeout: "CENCI-SANDBOX-START-003",
	}
	for code, want := range wantValues {
		if string(code) != want {
			t.Errorf("code = %q, want %q", code, want)
		}
	}

	all := []Code{
		SandboxStartAgentCLIMissing,
		SandboxStartGenericEntrypoint,
		SandboxStartReadinessTimeout,
		SandboxSessionNotFound,
		DaemonConnUnreachable,
		DaemonSocketMissing,
	}
	seen := make(map[Code]bool, len(all))
	for _, code := range all {
		if seen[code] {
			t.Fatalf("code %q is not distinct from another registered code", code)
		}
		seen[code] = true

		if !codeFormat.MatchString(string(code)) {
			t.Errorf("code %q does not match format %s", code, codeFormat.String())
		}

		entry, ok := Lookup(code)
		if !ok {
			t.Errorf("Lookup(%s) = _, false; want true", code)
			continue
		}
		if entry.Message == "" {
			t.Errorf("Lookup(%s).Message is empty", code)
		}
		if len(entry.Causes) == 0 {
			t.Errorf("Lookup(%s).Causes is empty", code)
		}
		if len(entry.Hints) == 0 {
			t.Errorf("Lookup(%s).Hints is empty — diagnose renders recovery commands straight from Hints", code)
		}
	}
}
