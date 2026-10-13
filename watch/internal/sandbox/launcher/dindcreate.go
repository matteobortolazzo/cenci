package launcher

import (
	"fmt"
	"io"
	"regexp"
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

// createFailureError translates a failed container create into the error
// Launch returns. Every failure keeps the original "<runtime> run: %w"
// wording except the two classes that wording actively misleads on: a create
// the runtime rejected because of a memory or pids limit flag (limitsOn: at
// least one limit flag was on the argv; LIMITS-002, checked first because
// runc's cgroup errors arrive wrapped in the same "OCI runtime create failed"
// text the dind check keys on), and a dind launch rejected by the OCI runtime.
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
