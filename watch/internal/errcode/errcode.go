// Package errcode is the registry of Cenci's stable, structured error
// identifiers.
//
// Codes follow the convention:
//
//	CENCI-<AREA>-<SUBAREA>-<NNN>
//
// where AREA is the broad component (e.g. SANDBOX, DAEMON, CREDENTIALS),
// SUBAREA narrows it further (e.g. START, SOCKET, CODEX), and NNN is a
// zero-padded three-digit sequence number scoped per AREA-SUBAREA pair (i.e.
// numbering restarts at 001 for each distinct AREA-SUBAREA combination, not
// globally). Examples: CENCI-SANDBOX-START-001, CENCI-DAEMON-SOCKET-002,
// CENCI-CREDENTIALS-CODEX-003.
//
// Every registered code maps to an Entry describing a short human-readable
// message, common causes, and diagnostic-command hints, retrievable via
// Lookup. This registry is the single source of truth for error codes
// referenced elsewhere in the codebase — consumers should always use the
// exported Code constants rather than bare string literals.
//
// See docs/error-codes.md for the full index of registered codes.
package errcode

import (
	"fmt"
	"regexp"
)

// Code is a stable, structured error identifier in the form
// CENCI-<AREA>-<SUBAREA>-<NNN>.
type Code string

// String returns the code's string representation.
func (c Code) String() string {
	return string(c)
}

// Entry is the diagnostic content a Code resolves to via Lookup.
type Entry struct {
	// Message is a short, human-readable description of the failure.
	Message string
	// Causes lists common root causes for this failure.
	Causes []string
	// Hints lists diagnostic commands or steps to investigate further.
	Hints []string
}

// Sandbox startup-failure codes (CENCI-SANDBOX-START-*), attached by
// watch/internal/sandbox/launcher/launch.go's startupFailureDetail /
// waitUntilReady.
const (
	// SandboxStartAgentCLIMissing is attached when sandbox/entrypoint.sh
	// detects the agent CLI path is missing or not executable and writes
	// /home/dev/.cenci-agent-startup-error before the readiness marker is
	// ever touched.
	SandboxStartAgentCLIMissing Code = "CENCI-SANDBOX-START-001"

	// SandboxStartGenericEntrypoint is attached for any other
	// entrypoint-startup failure: the boot log, the generic
	// .cenci-startup-failed EXIT-trap marker, the container's runtime logs,
	// or the fully generic fallback when none of those yielded diagnostics.
	SandboxStartGenericEntrypoint Code = "CENCI-SANDBOX-START-002"
)

// registry is the data-driven source of truth for every registered Code's
// diagnostic content. init() validates its contents at load time.
var registry = map[Code]Entry{
	SandboxStartAgentCLIMissing: {
		Message: "The sandboxed agent CLI path is missing or not executable.",
		Causes: []string{
			"The agent-CLI host-global volume was never bootstrapped or was removed.",
			"The mounted agent CLI binary is not executable inside the container.",
		},
		Hints: []string{
			"cenci sandbox update-agent",
			"docker/podman inspect the agent-CLI volume mount",
		},
	},
	SandboxStartGenericEntrypoint: {
		Message: "The sandbox entrypoint failed during container startup.",
		Causes: []string{
			"An entrypoint step (credential seeding, plugin install, config migration) failed.",
			"The container exited before its readiness marker was written.",
		},
		Hints: []string{
			"docker/podman logs <container> --tail 50",
			"cenci sandbox build to rebuild the image and retry",
		},
	},
}

// allCodes lists every exported Code constant declared above. init() uses it
// to cross-check that each declared constant has a corresponding registry
// entry, catching a constant that was declared but never wired into the
// registry map.
var allCodes = []Code{
	SandboxStartAgentCLIMissing,
	SandboxStartGenericEntrypoint,
}

// init validates every registered code against the documented format and
// panics on malformed or duplicate codes, failing fast at package load
// rather than letting a malformed entry silently reach a consumer. It also
// cross-checks that every declared Code constant in allCodes has a
// corresponding registry entry, so a constant added without wiring it into
// registry fails loudly at init time instead of Lookup silently returning
// ok=false forever.
func init() {
	format := regexp.MustCompile(`^CENCI-[A-Z]+-[A-Z]+-[0-9]{3}$`)
	seen := make(map[Code]bool, len(registry))
	for code := range registry {
		if !format.MatchString(string(code)) {
			panic("errcode: malformed code " + string(code) + "; must match " + format.String())
		}
		if seen[code] {
			panic("errcode: duplicate code " + string(code))
		}
		seen[code] = true
	}

	for _, c := range allCodes {
		if _, ok := registry[c]; !ok {
			panic(fmt.Sprintf("errcode: constant %s has no registry entry", c))
		}
	}
}

// Lookup returns the Entry registered for code, and whether it was found.
func Lookup(c Code) (Entry, bool) {
	entry, ok := registry[c]
	return entry, ok
}
