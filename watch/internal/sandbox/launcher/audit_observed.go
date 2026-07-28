package launcher

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// This file implements ticket #627's observed-mode derivation for `cenci
// audit`/`cenci security explain`: when the scoped container is actually
// running, Audit (audit.go) derives the reported Posture from a single
// combined read-only `inspect` probe instead of the hypothetical
// buildPlannedPosture derivation. It mirrors launch.go's
// inspectReusePosture/parseReusePosture/deriveDindPosture (ticket #628) —
// one combined inspect call, a fail-closed parser, and reuse of this
// package's own classify* helpers — so this module never re-derives
// mount/credential/dind classification independently.

// observedMount is a single mount's source/destination/read-only state, as
// read back by inspectObservedPosture.
type observedMount struct {
	Source      string
	Destination string
	ReadOnly    bool
}

// observedInspect is the combined observed-inspect probe result
// inspectObservedPosture returns: the container's image reference, network
// mode, configured OCI runtime, the cenci-sand.dind label, whether
// CENCI_SANDBOX_DIND=1 is present in the container's create-time env, and
// every mount's source/destination/read-only state.
type observedInspect struct {
	Image       string
	NetworkMode string
	Runtime     string
	DindLabel   string
	DindEnv     bool
	Mounts      []observedMount
}

// ErrMalformedObservedInspect is the sentinel parseObservedInspect returns
// (wrapped with context via %w) for any unparsable inspect response —
// detectable via errors.Is per watch/docs/error-handling.md #412. Its whole
// purpose is to let deriveObservedPosture/Audit distinguish "inspect
// genuinely failed" from "the observed posture legitimately parsed to some
// value" so a malformed response can never be silently read as the
// permissive zero-value observedInspect{} (bridge/no-mounts/dind-off —
// indistinguishable from a real default-safe running container) per
// watch/docs/error-handling.md #628, watch/docs/go-gotchas.md #598.
var ErrMalformedObservedInspect = errors.New("malformed observed inspect output")

// inspectObservedPosture issues the single combined read-only inspect probe
// deriveObservedPosture needs: image reference, network mode, OCI runtime,
// the cenci-sand.dind label, the CENCI_SANDBOX_DIND=1 env flag, and every
// mount's source/destination/read-only state. The `.HostConfig.NetworkMode`
// format-string token is the distinctive marker faketest_test.go's fake
// runtime keys its FAKE_OBSERVED_POSTURE response on — it must never appear
// in inspectReusePosture's (launch.go) or containerHasSharedAgentMount's
// format strings, which key off different tokens (`cenci-sand.dind` alone,
// `.RW` alone).
func (e *Engine) inspectObservedPosture(name string) (observedInspect, error) {
	// The --format argument is deliberately kept on a single Go source line
	// (using the template's own {{"\n"}} action to emit real newlines into
	// STDOUT) rather than a literal multi-line raw string: an embedded
	// newline BYTE inside a single argv element gets recorded as several
	// physical lines by the fake-runtime call-log harness
	// (faketest_test.go/audit_security_faketest_test.go), which would make
	// TestAudit_ObservedMode_NoMutation_CallLogOnlyPsAndInspect misread one
	// inspect invocation as multiple non-ps/inspect "calls".
	out, err := exec.Command(e.Runtime, "inspect", "--format",
		`{{.Config.Image}}|{{.HostConfig.NetworkMode}}|{{.HostConfig.Runtime}}|{{index .Config.Labels "cenci-sand.dind"}}|{{range .Config.Env}}{{if eq . "CENCI_SANDBOX_DIND=1"}}1{{end}}{{end}}{{"\n"}}{{range .Mounts}}{{.Source}}::{{.Destination}}::{{.RW}}{{"\n"}}{{end}}`, name).Output()
	if err != nil {
		return observedInspect{}, fmt.Errorf("%s inspect %s observed posture: %w", e.Runtime, name, err)
	}
	obs, err := parseObservedInspect(string(out))
	if err != nil {
		return observedInspect{}, fmt.Errorf("%s inspect %s observed posture: %w", e.Runtime, name, err)
	}
	return obs, nil
}

// parseObservedInspect parses inspectObservedPosture's stdout: line 1 is
// "<image>|<networkMode>|<runtime>|<dindLabel>|<dindenv 0-or-1>"; remaining
// lines are "<source>::<destination>::<rw true-or-false>" per mount.
//
// It fails closed (watch/docs/go-gotchas.md #598, mirroring
// parseReusePosture #628) rather than silently defaulting to the permissive
// zero-value observedInspect{} on unrecognized shape: an
// empty/truncated/garbled inspect response would otherwise read as
// bridge/no-mounts/dind-off — a confident default-safe posture
// deriveObservedPosture must never report for a container whose actual
// state could not be determined. A header that
// doesn't split into exactly 5 "|"-delimited fields, a mount line that
// doesn't split into exactly 3 "::"-delimited components, or a mount RW
// field that isn't exactly "true" or "false" are all rejected the same way
// — a dropped or misread line could be exactly the host-socket bind or
// host-network mode the boundary-weakening checks depend on.
//
// Real `docker inspect --format` appends its own trailing newline on top of
// the template's own per-mount {{"\n"}} action, so the captured stdout
// always ends with one blank line after the last mount (or right after the
// header when there are no mounts) — same artifact as parseReusePosture's,
// trimmed the same way (ticket #684) rather than rejected.
func parseObservedInspect(out string) (observedInspect, error) {
	lines := splitLines(out)
	if len(lines) == 0 {
		return observedInspect{}, fmt.Errorf("empty observed inspect output: %w", ErrMalformedObservedInspect)
	}
	fields := strings.Split(lines[0], "|")
	if len(fields) != 5 {
		return observedInspect{}, fmt.Errorf("malformed observed inspect header %q: expected 5 %q-delimited fields, got %d: %w", lines[0], "|", len(fields), ErrMalformedObservedInspect)
	}
	p := observedInspect{
		Image:       fields[0],
		NetworkMode: fields[1],
		Runtime:     fields[2],
		DindLabel:   fields[3],
		DindEnv:     fields[4] == "1",
	}
	mountLines := lines[1:]
	for len(mountLines) > 0 && mountLines[len(mountLines)-1] == "" {
		mountLines = mountLines[:len(mountLines)-1]
	}
	for _, line := range mountLines {
		parts := strings.SplitN(line, "::", 3)
		if len(parts) != 3 {
			return observedInspect{}, fmt.Errorf("malformed observed inspect mount line %q: expected \"source::destination::rw\": %w", line, ErrMalformedObservedInspect)
		}
		var readOnly bool
		switch parts[2] {
		case "true":
			readOnly = false
		case "false":
			readOnly = true
		default:
			return observedInspect{}, fmt.Errorf("malformed observed inspect mount line %q: unrecognized rw field %q (want \"true\" or \"false\"): %w", line, parts[2], ErrMalformedObservedInspect)
		}
		p.Mounts = append(p.Mounts, observedMount{Source: parts[0], Destination: parts[1], ReadOnly: readOnly})
	}
	return p, nil
}

// observedReuseMounts adapts observedInspect's mounts to launch.go's
// reuseMount shape, so deriveObservedPosture can reuse deriveDindPosture and
// mountExposesHostSocket verbatim instead of re-deriving their logic.
func observedReuseMounts(mounts []observedMount) []reuseMount {
	out := make([]reuseMount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, reuseMount{Source: m.Source, Destination: m.Destination})
	}
	return out
}

// hostSocketBoundaryWeakening is the BoundaryWeakening entry
// deriveObservedPosture reports when an observed mount exposes a host
// Docker/Podman socket into the running container — a weakening only
// observed mode can ever detect (a planned launch never stages one; see
// mountExposesHostSocket's own doc comment), distinct from the
// --host-network weakening.
func hostSocketBoundaryWeakening() BoundaryWeakening {
	return BoundaryWeakening{
		Option: "host-socket-mount",
		Active: true,
		Effect: "a host Docker/Podman socket is mounted into the running container, granting it control over the host's own container runtime",
	}
}

// deriveObservedDind maps obs's cenci-sand.dind label / legacy signals /
// mounts (via deriveDindPosture, launch.go #628) into this package's own
// DindPosture. dindUnknown (an unrecognized label value) is deliberately
// mapped to Source: DindSourceUnknown with a non-reassuring note — it must
// never render as a confident "disabled" (watch/docs/go-gotchas.md #598).
func deriveObservedDind(obs observedInspect) DindPosture {
	rp := reusePosture{
		DindLabel: obs.DindLabel,
		Runtime:   obs.Runtime,
		DindEnv:   obs.DindEnv,
		Mounts:    observedReuseMounts(obs.Mounts),
	}
	switch deriveDindPosture(rp) {
	case dindOn:
		storageVolume := ""
		for _, m := range obs.Mounts {
			if m.Destination == "/var/lib/docker" {
				storageVolume = m.Source
			}
		}
		return DindPosture{
			Enabled:       true,
			Source:        DindSourceObserved,
			Runtime:       obs.Runtime,
			StorageVolume: storageVolume,
			Note:          "nested Docker runs under the sysbox-runc OCI runtime, isolated from the host Docker daemon and never touching the host's own container runtime.",
		}
	case dindUnknown:
		return DindPosture{
			Enabled: false,
			Source:  DindSourceUnknown,
			Note:    "the running container's cenci-sand.dind label is an unrecognized value; its nested-Docker posture could not be conclusively determined and must not be read as disabled.",
		}
	default: // dindOff
		return DindPosture{Enabled: false, Source: DindSourceObserved}
	}
}

// deriveObservedPosture derives Posture from a running container's actual
// inspected state (basis:"running"), reusing mountKindForDestination,
// namedVolumes, credentialSources, deriveDindPosture, and
// mountExposesHostSocket rather than re-deriving any of their
// classification logic (audit.go:14-23). forwardedEnv/reseedCreds have no
// inspect source — they are per-exec-only values — so they are copied
// verbatim from planned (Q4: the running-basis "next-exec" qualifier is
// carried in WriteText/WriteExplanation's rendering, not a second JSON
// field). Env (create-time env names) is likewise carried over from planned:
// the combined inspect probe deliberately does not read back .Config.Env
// (mirroring inspectReusePosture's "one combined inspect call" minimalism),
// so it is not an observed fact of the running container either.
func (e *Engine) deriveObservedPosture(scope Scope, agent, home string, opts Options, planned Posture) (Posture, error) {
	obs, err := e.inspectObservedPosture(scope.ContainerName)
	if err != nil {
		return Posture{}, err
	}

	mounts := make([]MountPosture, 0, len(obs.Mounts))
	for _, m := range obs.Mounts {
		mounts = append(mounts, MountPosture{
			Source:      m.Source,
			Destination: m.Destination,
			ReadOnly:    m.ReadOnly,
			Kind:        mountKindForDestination(m.Destination),
		})
	}

	stagedKinds := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		stagedKinds[m.Kind] = true
	}

	workspace := planned.Workspace
	for _, m := range mounts {
		if m.Kind == MountKindWorkspace {
			workspace = WorkspacePosture{HostPath: m.Source, ContainerPath: m.Destination, ReadOnly: m.ReadOnly}
		}
	}

	netWeakened := obs.NetworkMode == NetworkModeHost
	network := NetworkPosture{Mode: obs.NetworkMode, Weakened: netWeakened}

	weakenings := make([]BoundaryWeakening, 0, 2)
	if netWeakened {
		weakenings = append(weakenings, BoundaryWeakening{
			Option: "--host-network",
			Active: true,
			Effect: "the container joins the host network namespace instead of an isolated bridge network",
		})
	}
	if mountExposesHostSocket(observedReuseMounts(obs.Mounts)) {
		weakenings = append(weakenings, hostSocketBoundaryWeakening())
	}

	imageType := ImageTypeMonolith
	if scope.UsingRepoImage {
		imageType = ImageTypeRepo
	}

	return Posture{
		Basis:    PostureBasisRunning,
		Agent:    agent,
		Scope:    scope.WorkspaceScope,
		RepoRoot: scope.RepoRoot,

		Image:     ImagePosture{Reference: obs.Image, Type: imageType},
		Workspace: workspace,
		Network:   network,
		Dind:      deriveObservedDind(obs),

		Mounts:             mounts,
		Volumes:            namedVolumes(mounts),
		Env:                planned.Env,
		ForwardedEnv:       planned.ForwardedEnv,
		CredentialSources:  credentialSources(home, agent, stagedKinds),
		BoundaryWeakenings: weakenings,

		ReseedCreds: opts.ReseedCreds,
	}, nil
}
