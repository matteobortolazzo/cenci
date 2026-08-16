package launcher

import (
	"fmt"
	"runtime"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// RepoDindConfig reads <repoRoot>/.cenci/config.json's "sandbox.dind" key
// and returns the resolved boolean plus an error. Only an absent config
// file, an absent "sandbox"/"dind" key, and an explicit `"dind":false`
// resolve to a safe (false, nil) off state; every other failure class hard-
// fails (#632). See repoSandboxBoolConfig (repoconfig.go), which owns that
// classification for every `sandbox.<key>` boolean.
func RepoDindConfig(repoRoot string) (bool, error) {
	return repoSandboxBoolConfig(repoRoot, "dind")
}

// ResolveDind resolves whether dind mode is on for this launch, in
// precedence order: --dind and --no-dind together is a usage error;
// --no-dind always wins, never errors on scope, and never reads repo config
// (an escape hatch that must always work even with corrupt config on disk);
// --dind turns dind on; otherwise the repo config (RepoDindConfig) decides.
// A RepoDindConfig read/parse error is propagated unchanged — a plain,
// non-usage hard error (exit 1) — because corrupt or wrong-typed stored
// config must never silently resolve to dind-off (#632). Requesting dind-on
// (via --dind or config) outside repo scope is a usage error — dind is only
// ever available in repo scope (#585).
//
// This resolves the REQUEST only (flags and stored config); it deliberately
// knows nothing about whether the host can actually run dind. Launch-path
// callers must go through resolveDindForHost, which layers the
// host-platform gate (#962) on top of this decision — calling ResolveDind
// directly from a launch/report path would silently skip that gate.
func ResolveDind(opts Options, scope Scope) (bool, error) {
	if opts.Dind && opts.NoDind {
		return false, usageErrorf("--dind and --no-dind cannot be combined")
	}
	if opts.NoDind {
		return false, nil
	}

	on := opts.Dind
	if !on && scope.WorkspaceScope == "repo" {
		var err error
		on, err = RepoDindConfig(scope.RepoRoot)
		if err != nil {
			return false, err
		}
	}
	if on && scope.WorkspaceScope != "repo" {
		return false, usageErrorf("--dind requires repo scope (a git repository); it is not supported in legacy/default scope")
	}
	return on, nil
}

// dindHostOS is the host operating system the dind platform gate
// (dindHostSupportsSysbox) evaluates, defaulting to this build's
// runtime.GOOS. It is a package var solely so tests can pin either host
// class on any runner — no launcher code path ever reassigns it.
var dindHostOS = runtime.GOOS

// dindHostSupportsSysbox reports whether this host could ever have
// sysbox-runc registered with its Docker daemon (#962). Sysbox is a
// Linux-only OCI runtime installed on the machine that runs dockerd; on
// macOS dockerd lives inside Docker Desktop's LinuxKit VM, which cannot be
// modified, so sysbox-runc can never appear in `docker info`'s runtimes
// there and no host-side install fixes it.
//
// This is deliberately a static platform fact, not a probe: SysboxRegistered
// answers "is sysbox registered right now", this answers "could it ever be",
// and only the second distinguishes a Linux host with a fixable setup gap
// (still a hard error, with actionable install pointers) from a macOS host
// where those pointers are unfollowable advice and the hard error simply
// makes the sandbox unlaunchable.
func dindHostSupportsSysbox() bool { return dindHostOS != "darwin" }

// dindHostLabel names dindHostOS the way a user would recognize it, for the
// degrade warning and the audit note.
func dindHostLabel() string {
	if dindHostOS == "darwin" {
		return "macOS"
	}
	return dindHostOS
}

// resolveDindForHost applies the host-platform gate to ResolveDind's
// flag/config decision and reports whether a requested dind mode was
// degraded away (#962). It returns (on, degraded, err):
//
//   - Every ResolveDind error class — the --dind/--no-dind usage error, the
//     non-repo-scope usage error, and the malformed/wrong-typed config hard
//     error (#632) — propagates unchanged on every platform, with
//     degraded=false: the gate only ever downgrades an otherwise-successful
//     dind-ON decision, it never swallows input validation or corrupt
//     stored config.
//   - degraded=true means dind was genuinely requested (via --dind or repo
//     config) but this host can never run it, so it was turned off and the
//     caller owes the user a warning. A launch that never asked for dind —
//     including one that opted out with --no-dind — is never "degraded";
//     nothing was dropped, so nothing is warned about.
//
// Launch/DryRun (resolveLaunchContext) and Audit all resolve dind through
// this one function, so a real launch, its preview, and its posture report
// can never disagree about whether dind is on for this host.
func resolveDindForHost(opts Options, scope Scope) (bool, bool, error) {
	on, err := ResolveDind(opts, scope)
	if err != nil || !on {
		return false, false, err
	}
	if !dindHostSupportsSysbox() {
		return false, true, nil
	}
	return true, false, nil
}

// dindPreflight rejects a dind launch when the resolved outer runtime isn't
// Docker (sysbox-runc is only ever registered with Docker, never Podman), and
// when sysbox-runc isn't registered with it — returning a hard error with
// install pointers for the two supported hosts (#585).
func (e *Engine) dindPreflight() error {
	if e.Runtime != "docker" {
		return fmt.Errorf("dind mode requires Docker as the outer container runtime (got %q); Podman does not support the sysbox-runc OCI runtime dind relies on", e.Runtime)
	}
	registered, err := sandbox.SysboxRegistered(e.Runtime)
	if err != nil {
		return fmt.Errorf("checking sysbox-runc registration with %s: %w", e.Runtime, err)
	}
	if !registered {
		return fmt.Errorf("dind mode requires the sysbox-runc OCI runtime to be registered with Docker, but it isn't.\n" +
			"Install it: on Arch Linux, the AUR package is sysbox-ce (e.g. 'yay -S sysbox-ce'); on Ubuntu, download the nestybox sysbox-ce .deb from https://github.com/nestybox/sysbox/releases and install it with 'sudo apt install ./sysbox-ce_*.deb'.\n" +
			"Docs: https://github.com/nestybox/sysbox")
	}
	return nil
}
