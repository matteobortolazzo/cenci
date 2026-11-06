package launcher

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
)

// ociRuntimeCreateMarker is the substring the container runtime's daemon puts
// in a create failure it attributes to the OCI runtime rejecting the spec,
// rather than to the daemon's own validation (a bad mount, a name conflict).
// Matching the daemon's wording rather than any specific runtime error keeps
// the classification stable across sysbox versions: the inner reason text
// changes between releases, the daemon's wrapper does not.
const ociRuntimeCreateMarker = "OCI runtime create failed"

// stderrCaptureLimit bounds how much of a failing create's stderr is retained
// for classification. The daemon's create errors are a line or two; the cap
// exists so a runtime that floods stderr cannot grow this buffer without
// bound. Mirroring to the real stderr is never capped — the user still sees
// everything the runtime printed.
const stderrCaptureLimit = 8 << 10

// capturedStderr mirrors a subprocess's stderr to the real stderr while
// retaining a bounded prefix for classification. Launch needs both: the
// runtime's own diagnostics must keep streaming through verbatim (that text
// is often the only real evidence of what failed), and createFailureError
// needs to read it to decide whether the failure is a rejected resource limit
// (#1216) or the dind runtime's (#1077).
type capturedStderr struct {
	mirror   io.Writer
	retained strings.Builder
}

func newCapturedStderr(mirror io.Writer) *capturedStderr {
	return &capturedStderr{mirror: mirror}
}

// Write retains up to stderrCaptureLimit bytes, then forwards p to the mirror
// unchanged. It returns the mirror's own (n, err) so a short write or error
// propagates to os/exec exactly as it would without the capture in between.
func (c *capturedStderr) Write(p []byte) (int, error) {
	if room := stderrCaptureLimit - c.retained.Len(); room > 0 {
		keep := p
		if len(keep) > room {
			keep = keep[:room]
		}
		c.retained.Write(keep)
	}
	return c.mirror.Write(p)
}

// String returns the retained stderr prefix. It is a single contiguous buffer
// across every Write, so a marker split across chunk boundaries — the normal
// case for a piped subprocess — still classifies.
func (c *capturedStderr) String() string { return c.retained.String() }

// limitRejectionMarker matches the narrow set of runtime wordings that
// attribute a failed create to the memory or pids limit flags cenci sent
// (#1216): the cgroup v2 files runc/crun could not write (memory.max,
// memory.swap.max, pids.max), the cgroup v1 memory file, a memory or pids
// cgroup controller reported as not available, and moby's own "Minimum memory
// limit" validation. The set is deliberately narrow (watch/AGENTS.md: never
// broaden a match-miss exclusion into a catch-all): any other create failure
// keeps its plain wording.
var limitRejectionMarker = regexp.MustCompile(
	`memory\.max|memory\.swap\.max|pids\.max|memory\.limit_in_bytes|Minimum memory limit|` +
		`(?i:cgroup controller\W{1,3}(?:memory|pids)\W{1,3}(?:is )?not available)`)

// Host probe seams for the stale-kernel classification (#1159): the file the
// running kernel's release is read from, and the root the installed kernels'
// module directories live under. Production never reassigns them; tests pin
// both (and so must not run in parallel).
var (
	dindOSReleasePath = "/proc/sys/kernel/osrelease"
	dindModulesRoot   = "/lib/modules"
)

// staleKernelStatMarker captures the release token of the daemon's
// "stat /lib/modules/<release>: no such file or directory" wording, the
// missing mount source sysbox-runc reports when the running kernel's modules
// directory has been removed. The capture is deliberately loose (anything up
// to whitespace or a colon); kernelReleaseToken is what makes it safe to join
// under the modules root.
var staleKernelStatMarker = regexp.MustCompile(`stat /lib/modules/([^\s:]+): no such file or directory`)

// kernelReleaseToken is the charset of a kernel release as it appears as a
// single path segment under /lib/modules. It is checked, together with an
// explicit ".." rejection, before the token from container-runtime stderr is
// joined to any path (sandbox/AGENTS.md: validate host paths).
var kernelReleaseToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*$`)

// validKernelRelease reports whether s is a safe single-segment kernel release.
func validKernelRelease(s string) bool {
	return kernelReleaseToken.MatchString(s) && !strings.Contains(s, "..")
}

// staleKernelMissingRelease reports the running kernel's release when stderr
// shows the stale-kernel failure: the stderr names a missing
// /lib/modules/<release> mount source, that release is the one in
// dindOSReleasePath, and a local stat of it returns not-exist. Every other
// outcome (no marker, an unsafe token, an unreadable or different osrelease, a
// directory that exists, a stat error other than not-exist) is ok=false so the
// caller keeps the DIND-003 wording.
func staleKernelMissingRelease(stderr string) (release string, ok bool) {
	m := staleKernelStatMarker.FindStringSubmatch(stderr)
	if m == nil || !validKernelRelease(m[1]) {
		return "", false
	}
	raw, err := os.ReadFile(dindOSReleasePath)
	if err != nil || strings.TrimSpace(string(raw)) != m[1] {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(dindModulesRoot, m[1])); !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	return m[1], true
}

// installedKernelReleases lists the entries of the modules root that look like
// kernel releases, sorted and comma-joined, or "none found" when the root is
// empty or cannot be listed.
func installedKernelReleases() string {
	entries, err := os.ReadDir(dindModulesRoot)
	if err != nil {
		return "none found"
	}
	var names []string
	for _, entry := range entries {
		if validKernelRelease(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "none found"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// createFailureError translates a failed container create into the error
// Launch returns. Every failure keeps the original "<runtime> run: %w"
// wording except the three classes that wording actively misleads on: a create
// the runtime rejected because of a memory or pids limit flag (limitsOn: at
// least one limit flag was on the argv; LIMITS-002, checked first because
// runc's cgroup errors arrive wrapped in the same "OCI runtime create failed"
// text the dind check keys on), a dind launch rejected by the OCI runtime
// because the running kernel's /lib/modules/<release> directory is gone
// (DIND-004, #1159: the host was upgraded without a reboot, and updating
// sysbox-ce cannot help), and any other dind launch rejected by the OCI
// runtime (DIND-003).
//
// The raw daemon text for the dind case names neither sysbox nor dind — e.g.
// `OCI runtime create failed: namespace {"time" ""} does not exist` — and the
// user never typed --runtime=sysbox-runc themselves, cenci appended it
// because dind was on (see assembleRunArgs). Without this mapping the only
// signal they get is an exit status, with nothing connecting it to a cenci
// decision or to the --no-dind escape hatch.
//
// This is deliberately a failure-path mapping rather than a functional
// preflight probe: dindPreflight already checks that sysbox-runc is
// registered, and actually creating a throwaway sysbox container before every
// launch would add real latency to every launch to catch a rare host-level
// breakage (#1077). The limit check follows the same failure-path shape.
func (e *Engine) createFailureError(dindOn, limitsOn bool, stderr string, runErr error) error {
	if marker := limitRejectionMarker.FindString(stderr); limitsOn && marker != "" {
		//nolint:staticcheck // ST1005: multi-sentence user-facing guidance, matching the --agent auth errors in launch.go
		return fmt.Errorf("%s run: %w\n"+
			"The container runtime rejected the create because of a resource limit cenci applied (--memory/--memory-swap/--pids-limit) [%s]; its error mentions %q. "+
			"This usually means the memory or pids cgroup controller is not delegated to the runtime on this host.\n"+
			"To launch without the limit, set \"memoryLimit\": \"none\" and/or \"pidsLimit\": 0 under \"sandbox\" in the repo's .cenci/config.json "+
			"(for example \"sandbox\": {\"memoryLimit\": \"none\", \"pidsLimit\": 0}).",
			e.Runtime, runErr, errcode.SandboxLimitsCreateRejected, marker)
	}
	if !dindOn || !strings.Contains(stderr, ociRuntimeCreateMarker) {
		return fmt.Errorf("%s run: %w", e.Runtime, runErr)
	}
	if release, ok := staleKernelMissingRelease(stderr); ok {
		//nolint:staticcheck // ST1005: multi-sentence user-facing guidance, matching the --agent auth errors in launch.go
		return fmt.Errorf("%s run: %w\n"+
			"Nested Docker (DinD) is on for this launch, so cenci created the container with --runtime=sysbox-runc, "+
			"which bind-mounts the running kernel's modules directory [%s].\n"+
			"The running kernel is %s, but /lib/modules/%s does not exist — the kernel was upgraded without a reboot, "+
			"and sysbox-runc needs that directory. Reboot into the installed kernel (available: %s). "+
			"This is not a sysbox-ce version problem.\n"+
			"To launch without nested Docker meanwhile, pass --no-dind, or set \"sandbox\": {\"dind\": false} in .cenci/config.json.",
			e.Runtime, runErr, errcode.SandboxDindStaleKernelModules, release, release, installedKernelReleases())
	}
	//nolint:staticcheck // ST1005: multi-sentence user-facing guidance, matching the --agent auth errors in launch.go
	return fmt.Errorf("%s run: %w\n"+
		"Nested Docker (DinD) is on for this launch, so cenci created the container with --runtime=sysbox-runc [%s]. "+
		"That runtime is registered with Docker but rejected the container spec, which usually means the installed "+
		"sysbox-ce predates a change in the host's Docker or kernel.\n"+
		"Reproduce it outside cenci with: docker run --rm --runtime=sysbox-runc alpine true\n"+
		"If that fails too, update sysbox-ce (https://github.com/nestybox/sysbox/releases).\n"+
		"To launch without nested Docker meanwhile, pass --no-dind, or set \"sandbox\": {\"dind\": false} in .cenci/config.json.",
		e.Runtime, runErr, errcode.SandboxDindRuntimeCreateFailed)
}
