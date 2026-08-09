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

	// SandboxStartReadinessTimeout classifies waitUntilReady's 60-second
	// readiness-poll timeout (the container never failed outright, it simply
	// never reached /tmp/cenci-ready in time). Registry-only for now:
	// registered (message, causes, hints) and mapped by
	// launcher.severityForCode, but neither waitUntilReady nor `cenci
	// diagnose` currently detects a stuck/never-ready container and attaches
	// it — wiring a detection path in for either is left to a follow-up.
	SandboxStartReadinessTimeout Code = "CENCI-SANDBOX-START-003"
)

// Sandbox session-lookup codes (CENCI-SANDBOX-SESSION-*), attached by
// `cenci diagnose` when it cannot find a container for the requested
// session.
const (
	// SandboxSessionNotFound is attached when no container exists for the
	// requested sandbox session (never launched, launched under a different
	// scope, or already auto-removed by --rm).
	SandboxSessionNotFound Code = "CENCI-SANDBOX-SESSION-001"
)

// Sandbox DinD codes (CENCI-SANDBOX-DIND-*), attached by
// watch/internal/sandbox/launcher/launch.go's warnDockerdStartupFailure and
// diagnose.go's "Nested Docker:" section.
const (
	// SandboxDindStartupFailure is attached when the persistent
	// .cenci-dockerd-startup-error marker is present: the nested dockerd
	// failed to start (or crashed/OOMed later) without an intentional
	// shutdown sentinel superseding it.
	SandboxDindStartupFailure Code = "CENCI-SANDBOX-DIND-001"

	// SandboxDindPlatformUnsupported is attached when dind was requested
	// (--dind or the repo's sandbox.dind config) on a host that can never
	// register sysbox-runc — macOS, where dockerd runs inside Docker
	// Desktop's unmodifiable LinuxKit VM. The launch proceeds without
	// nested Docker rather than failing (#962).
	SandboxDindPlatformUnsupported Code = "CENCI-SANDBOX-DIND-002"
)

// Daemon reachability codes (CENCI-DAEMON-*), attached by `cenci diagnose`'s
// read-only daemon probe.
const (
	// DaemonConnUnreachable is attached when the event socket exists but
	// nothing answers a dial (the daemon process died without cleaning up).
	DaemonConnUnreachable Code = "CENCI-DAEMON-CONN-001"

	// DaemonSocketMissing is attached when the daemon's event socket does not
	// exist at all (the daemon was never started, or its socket directory was
	// cleared).
	DaemonSocketMissing Code = "CENCI-DAEMON-SOCKET-001"
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
	SandboxStartReadinessTimeout: {
		Message: "The sandbox container did not signal readiness within the poll budget.",
		Causes: []string{
			"Container startup is unusually slow (large plugin install, or a busy host).",
			"An earlier startup failure went undetected before the readiness poll budget expired.",
		},
		Hints: []string{
			"cenci diagnose --name <session>",
			"docker/podman logs <container> --tail 50",
		},
	},
	SandboxSessionNotFound: {
		Message: "No container exists for the given sandbox session.",
		Causes: []string{
			"The session was never launched, or was launched with a different --name or repo scope.",
			"The container already stopped and was auto-removed (--rm).",
		},
		Hints: []string{
			"cenci sandbox ls",
			"cenci open <shortcut> to relaunch the session",
		},
	},
	SandboxDindStartupFailure: {
		Message: "The nested Docker daemon (DinD) failed to start or crashed.",
		Causes: []string{
			"The inner dockerd process failed to start (e.g. Sysbox not registered, resource limits).",
			"The inner dockerd crashed or was OOM-killed after starting.",
		},
		Hints: []string{
			"cenci diagnose --name <session>",
			"docker/podman logs <container> --tail 50",
		},
	},
	SandboxDindPlatformUnsupported: {
		Message: "Nested Docker (DinD) is unavailable on this host; the session launched without it.",
		Causes: []string{
			"The host is macOS: sysbox-runc is a Linux-only runtime and Docker Desktop's LinuxKit VM cannot register it.",
			"The repo requests dind via --dind or .cenci/config.json's sandbox.dind key.",
		},
		Hints: []string{
			"cenci open <shortcut> --no-dind to launch without the warning",
			"set \"sandbox\": {\"dind\": false} in .cenci/config.json for a repo that does not need nested Docker",
		},
	},
	DaemonConnUnreachable: {
		Message: "The cenci daemon did not answer on its event socket.",
		Causes: []string{
			"The daemon process crashed or exited without cleaning up its socket file.",
			"Another process is holding the socket without answering connections.",
		},
		Hints: []string{
			"cenci daemon status",
			"cenci daemon restart",
		},
	},
	DaemonSocketMissing: {
		Message: "The cenci daemon's event socket does not exist.",
		Causes: []string{
			"The daemon has never been started on this host.",
			"XDG_RUNTIME_DIR (or its fallback socket directory) was cleared, removing the socket.",
		},
		Hints: []string{
			"cenci daemon start",
			"cenci daemon status",
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
	SandboxStartReadinessTimeout,
	SandboxSessionNotFound,
	SandboxDindStartupFailure,
	SandboxDindPlatformUnsupported,
	DaemonConnUnreachable,
	DaemonSocketMissing,
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

// AllCodes returns every registered Code constant (the same set init()
// cross-checks against registry). It returns a copy so callers cannot
// mutate the package-internal allCodes slice. Exported so consumers outside
// this package — e.g. a regression test asserting every registered code is
// explicitly classified by a downstream taxonomy — can walk the full,
// current set of registered codes without duplicating it by hand and
// silently drifting out of sync as new codes are added.
func AllCodes() []Code {
	return append([]Code(nil), allCodes...)
}
