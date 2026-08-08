package launcher

import (
	"bufio"
	"fmt"
	"io"
)

// This file implements `cenci security explain` (ticket #594): a text-only,
// plain-language "why this is/isn't safe" narrative rendering of the same
// Posture `cenci audit` derives (see audit.go). WriteExplanation reuses
// Posture entirely — it adds no new detection logic, only a different
// (prose, not tabular) rendering of the fields Audit already computed.

// WriteExplanation renders p as a plain-language security narrative:
// threat-model framing (paraphrasing SECURITY.md — the container is the
// isolation boundary, since the agent itself runs unattended with no
// per-command approval), one safety-assumption paragraph per posture
// element (workspace/home mounts, the cenci-socket/cenci-bin/gitconfig/
// named-volume mounts in p.Mounts, network mode, credential sources, and a
// separate "Nested Docker (sysbox-isolated)" section that is explicitly NOT
// a boundary weakening), and a closing "Boundary weakenings" block. Like
// WriteText, it never prints a credential/secret VALUE — only what Posture
// itself carries (names, sources, present/absent status) — and it never
// mentions the removed --docker flag.
func (p Posture) WriteExplanation(w io.Writer) error {
	bw := bufio.NewWriter(w)

	_, _ = fmt.Fprintf(bw, "cenci security explain: agent=%s scope=%s basis=%s\n\n", p.Agent, p.Scope, p.Basis)

	// Basis framing (ticket #627, AC #5): state up front whether this
	// narrative describes the scoped container's actual observed running
	// state or a hypothetical next-launch plan — using content-specific,
	// visibly distinct wording for each so the two cases can never collapse
	// into an ambiguous, generically-worded intro.
	if p.Basis == PostureBasisRunning {
		_, _ = fmt.Fprintln(bw, "This report describes the observed running state of the scoped container:")
		_, _ = fmt.Fprintln(bw, "it was inspected directly rather than derived from a hypothetical plan, so")
		_, _ = fmt.Fprintln(bw, "the fields below (aside from the next-exec values explicitly labeled as")
		_, _ = fmt.Fprintln(bw, "such) reflect the container's actual current configuration.")
	} else {
		_, _ = fmt.Fprintln(bw, "This report describes a plan for the next launch: no running scoped")
		_, _ = fmt.Fprintln(bw, "container was found (or its state could not be verified), so nothing below")
		_, _ = fmt.Fprintln(bw, "reflects observed state — it is what the launcher WOULD apply.")
	}
	_, _ = fmt.Fprintln(bw)

	if p.InspectWarning != "" {
		_, _ = fmt.Fprintln(bw, "⚠ Inspect warning:")
		_, _ = fmt.Fprintln(bw, p.InspectWarning)
		_, _ = fmt.Fprintln(bw)
	}

	_, _ = fmt.Fprintln(bw, "Threat model:")
	_, _ = fmt.Fprintln(bw, "The container is the security boundary, not the agent's own permissions.")
	_, _ = fmt.Fprintln(bw, "The agent runs unattended, with no per-command approval prompts, so isolation")
	_, _ = fmt.Fprintln(bw, "relies entirely on the container standing between the agent and the host — see")
	_, _ = fmt.Fprintln(bw, "SECURITY.md for the full threat model this narrative paraphrases.")
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Workspace and home:")
	_, _ = fmt.Fprintf(bw, "Only the current repo (%s) is bind-mounted into the container at %s — not\n", p.Workspace.HostPath, p.Workspace.ContainerPath)
	_, _ = fmt.Fprintln(bw, "your whole host filesystem. The container's home directory is backed by a")
	_, _ = fmt.Fprintln(bw, "named volume, not your host home, so the agent cannot read or write host files")
	_, _ = fmt.Fprintln(bw, "outside the mounted repo and the documented mount points.")
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Network:")
	if p.Network.Weakened {
		// AC #8: --host-network's additional effect — a shared host network
		// namespace, host-localhost exposure, and loss of namespace
		// separation — stated as complete sentences, plus the blast-radius
		// warning (also repeated in the Boundary weakenings block below).
		_, _ = fmt.Fprintf(bw, "Network mode is %q — --host-network is active: the container joins a\n", p.Network.Mode)
		_, _ = fmt.Fprintln(bw, "shared host network namespace instead of its own isolated bridge namespace,")
		_, _ = fmt.Fprintln(bw, "losing network namespace separation from the host for the life of this")
		_, _ = fmt.Fprintln(bw, "session — this removes the container's network isolation from the host. Any")
		_, _ = fmt.Fprintln(bw, "port the container binds to localhost is reachable exactly as if that")
		_, _ = fmt.Fprintln(bw, "process were running directly on your host — see the boundary weakening's")
		_, _ = fmt.Fprintln(bw, "blast radius warning below.")
	} else {
		// AC #6/#7: bridge mode uses a separate network namespace and
		// publishes no inbound ports by default — but that does NOT mean
		// outbound-only/complete host isolation: outbound connections may
		// still reach routable host, LAN, or internet services depending on
		// the runtime and firewall configuration (replaces the old
		// "outbound-only and isolated from the host" overclaim).
		_, _ = fmt.Fprintf(bw, "Network mode is %q — the container runs in its own separate network namespace\n", p.Network.Mode)
		_, _ = fmt.Fprintln(bw, "and publishes no inbound ports by default, so nothing on your")
		_, _ = fmt.Fprintln(bw, "host or network can initiate a connection into the container. This does not")
		_, _ = fmt.Fprintln(bw, "mean the container is outbound-only or completely isolated from the host:")
		_, _ = fmt.Fprintln(bw, "outbound connections initiated from inside the container may still reach")
		_, _ = fmt.Fprintln(bw, "routable host, LAN, or internet services, depending on your container")
		_, _ = fmt.Fprintln(bw, "runtime and firewall configuration.")
	}
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Other mounts:")
	writeOtherMountsExplanation(bw, p.Mounts)
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Credential sources:")
	if len(p.CredentialSources) == 0 {
		_, _ = fmt.Fprintln(bw, "No credential sources apply to this agent.")
	}
	for _, c := range p.CredentialSources {
		writeCredentialSourceExplanation(bw, c, p.Agent)
	}
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Forwarded exec env (names only — values are never shown):")
	if p.Basis == PostureBasisRunning {
		// Q4: forwardedEnv has no inspect source (it's a per-exec-only value),
		// so a running-basis narrative must label it "next-exec" rather than
		// presenting it as an observed fact of the running container. Omitted
		// entirely for a planned-basis report, where every field is already a
		// hypothetical next-launch value.
		_, _ = fmt.Fprintln(bw, "(next-exec — not observed from the running container; these are the values")
		_, _ = fmt.Fprintln(bw, "the NEXT exec into this container would forward)")
	}
	if len(p.ForwardedEnv) == 0 {
		_, _ = fmt.Fprintln(bw, "No provider API keys are forwarded for this agent.")
	}
	for _, e := range p.ForwardedEnv {
		secretMark := ""
		if e.Secret {
			secretMark = " (secret, value never printed)"
		}
		_, _ = fmt.Fprintf(bw, "%s%s\n", e.Name, secretMark)
	}
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Nested Docker (sysbox-isolated):")
	switch {
	case p.Dind.Enabled:
		_, _ = fmt.Fprintln(bw, "Nested Docker is enabled for this launch. This is NOT a boundary weakening:")
		_, _ = fmt.Fprintf(bw, "it runs under the %s OCI runtime, isolated from the host Docker daemon and\n", p.Dind.Runtime)
		_, _ = fmt.Fprintln(bw, "never touching the host's own container runtime — its own dedicated isolation")
		_, _ = fmt.Fprintf(bw, "boundary, backed by the %s storage volume.\n", p.Dind.StorageVolume)
	case p.Dind.Source == DindSourceUnknown:
		// #598/#627: an unrecognized cenci-sand.dind label is indeterminate,
		// not "disabled" — the confident-disabled sentence below must never
		// be printed for this case (see DindSourceUnknown's doc comment).
		_, _ = fmt.Fprintln(bw, "Nested Docker's state could not be determined for this launch — the running")
		_, _ = fmt.Fprintln(bw, "container's cenci-sand.dind label is an unrecognized value, so whether a")
		_, _ = fmt.Fprintln(bw, "sysbox-isolated Docker daemon runs inside the container is indeterminate. Do")
		_, _ = fmt.Fprintln(bw, "not read this as disabled.")
	case p.Dind.Source == DindSourcePlatformUnsupported:
		// #962: dind WAS requested but this host can never register
		// sysbox-runc. The plain "disabled" sentence below would be
		// technically true and practically misleading — it reads as "nobody
		// asked for it", leaving the reader with no explanation for why
		// in-container Docker fails.
		_, _ = fmt.Fprintln(bw, "Nested Docker was requested for this launch but is unavailable on this host —")
		_, _ = fmt.Fprintln(bw, "sysbox-runc is a Linux-only OCI runtime and cannot be registered with Docker")
		_, _ = fmt.Fprintln(bw, "Desktop's VM. The sandbox launches without it, so work needing an in-container")
		_, _ = fmt.Fprintln(bw, "Docker daemon (Testcontainers, docker build/run) will not work in this session.")
	default:
		_, _ = fmt.Fprintln(bw, "Nested Docker is disabled for this launch — no sysbox-isolated Docker daemon")
		_, _ = fmt.Fprintln(bw, "runs inside the container.")
	}
	_, _ = fmt.Fprintln(bw)

	if len(p.BoundaryWeakenings) == 0 {
		switch {
		case p.InspectWarning != "":
			_, _ = fmt.Fprintln(bw, "Boundary weakenings: unknown — the running container's actual posture")
			_, _ = fmt.Fprintln(bw, "could not be verified; see the inspect warning above.")
		case p.Dind.Source == DindSourceUnknown:
			// The dind state alone is indeterminate here (no separate
			// InspectWarning — the inspect call itself succeeded), so this
			// must not co-occur with the reassuring "default-safe baseline"
			// claim either.
			_, _ = fmt.Fprintln(bw, "Boundary weakenings: unknown — the running container's nested-Docker state")
			_, _ = fmt.Fprintln(bw, "could not be determined; see the Nested Docker section above.")
		default:
			_, _ = fmt.Fprintln(bw, "Boundary weakenings: none (default-safe baseline)")
		}
	} else {
		_, _ = fmt.Fprintln(bw, "⚠ Boundary weakenings (opt-in, reduces isolation):")
		for _, bwk := range p.BoundaryWeakenings {
			_, _ = fmt.Fprintf(bw, "⚠ %s: %s\n", bwk.Option, bwk.Effect)
			if bwk.Option == "--host-network" {
				_, _ = fmt.Fprintln(bw, "  Blast radius: any process inside the container can reach services on your")
				_, _ = fmt.Fprintln(bw, "  host's network as if it were running there directly — the loss of network")
				_, _ = fmt.Fprintln(bw, "  isolation lasts for the life of this session.")
			}
		}
	}

	return bw.Flush()
}

// writeCredentialSourceExplanation narrates a single CredentialSource's
// safety assumption for WriteExplanation's "Credential sources" section. It
// branches on c.Staged for any "bind-mounted/copied" claim — never on
// c.Present alone, per the #598 acceptance criterion that a present-but-
// not-staged credential (e.g. Codex auth present on the host during a
// Claude audit, which the mount plan never stages for a non-Codex agent)
// must not be narrated as mounted. c.Probe additionally distinguishes an
// unreadable/error probe state from plain absence, so a stat/read failure
// is never narrated as a reassuring "absent". Probe is a plain string, not a
// compiler-enforced enum, so an unrecognized value falls to its own distinct
// "inconclusive" wording below — never the plain-absent case.
func writeCredentialSourceExplanation(bw *bufio.Writer, c CredentialSource, agent string) {
	switch {
	case c.Staged:
		_, _ = fmt.Fprintf(bw, "%s credentials (%s) are present on the host and staged for this launch.\n", c.Type, c.HostPath)
		_, _ = fmt.Fprintln(bw, "They are bind-mounted read-only into a staging path and copied into the")
		_, _ = fmt.Fprintln(bw, "container's own named volume on first start — never baked into an image")
		_, _ = fmt.Fprintln(bw, "layer — so the host's original credential file is never written to by the")
		_, _ = fmt.Fprintln(bw, "container.")
	case c.Probe == CredentialProbeError:
		_, _ = fmt.Fprintf(bw, "%s credentials (%s) could not be read on the host — an unreadable/error\n", c.Type, c.HostPath)
		_, _ = fmt.Fprintln(bw, "probe result, distinct from a plain absence — so nothing is staged or")
		_, _ = fmt.Fprintln(bw, "mounted into a read-only staging path or named volume for this credential")
		_, _ = fmt.Fprintln(bw, "type.")
	case c.Present:
		_, _ = fmt.Fprintf(bw, "%s credentials (%s) are present on the host but are not staged for this\n", c.Type, c.HostPath)
		_, _ = fmt.Fprintf(bw, "launch (agent=%s) — nothing is mounted into a read-only staging path or\n", agent)
		_, _ = fmt.Fprintln(bw, "the container's named volume for this credential type.")
	case c.Probe == CredentialProbeMissing:
		_, _ = fmt.Fprintf(bw, "%s credentials (%s) are absent on the host — nothing is staged or\n", c.Type, c.HostPath)
		_, _ = fmt.Fprintln(bw, "mounted into a read-only staging path or named volume for this credential")
		_, _ = fmt.Fprintln(bw, "type.")
	default:
		_, _ = fmt.Fprintf(bw, "%s credential probe (%s) returned an unrecognized state (%q) — treat this\n", c.Type, c.HostPath, c.Probe)
		_, _ = fmt.Fprintln(bw, "as inconclusive, not absent: nothing is staged or mounted into a read-only")
		_, _ = fmt.Fprintln(bw, "staging path or named volume for this credential type, but presence on the")
		_, _ = fmt.Fprintln(bw, "host could not be determined.")
	}
}

// writeOtherMountsExplanation narrates every MountPosture.Kind in mounts that
// isn't already covered by WriteExplanation's dedicated "Workspace and home"
// or "Credential sources" sections (MountKindWorkspace and the
// MountKind*Creds kinds) — the cenci-socket, cenci-bin, gitconfig, and
// named-volume mounts (agent-cli, home, dind) `cenci audit`'s WriteText
// lists in its "Mounts"/"Volumes" tables but WriteExplanation previously
// never mentioned. Kinds are grouped by similar safety assumption rather
// than one paragraph per kind, matching the prose style of the rest of
// WriteExplanation. It only reads Posture — no new detection logic.
func writeOtherMountsExplanation(bw *bufio.Writer, mounts []MountPosture) {
	present := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		present[m.Kind] = true
	}

	wroteAny := false

	if present[MountKindCenciSocket] {
		wroteAny = true
		_, _ = fmt.Fprintln(bw, "The cenci event socket is bind-mounted into the container read-only. Unlike")
		_, _ = fmt.Fprintln(bw, "a general host control-plane socket, this is a narrow, purpose-built IPC")
		_, _ = fmt.Fprintln(bw, "channel: the agent can only emit hook events back to the host's cenci")
		_, _ = fmt.Fprintln(bw, "daemon over it — it carries no ability to control the host's container")
		_, _ = fmt.Fprintln(bw, "runtime or any other host process.")
	}

	if present[MountKindCenciBin] || present[MountKindGitconfig] {
		wroteAny = true
		_, _ = fmt.Fprintln(bw, "The cenci CLI binary and your git identity (.gitconfig) are also bind-mounted")
		_, _ = fmt.Fprintln(bw, "read-only, so the agent's own cenci invocations and commits behave")
		_, _ = fmt.Fprintln(bw, "consistently inside the container. Neither mount grants the container write")
		_, _ = fmt.Fprintln(bw, "access back to the host file it was read from.")
	}

	if present[MountKindAgentCLIVolume] || present[MountKindHomeVolume] || present[MountKindDindVolume] {
		wroteAny = true
		_, _ = fmt.Fprintln(bw, "The agent CLI install, the container's home directory, and (when nested")
		_, _ = fmt.Fprintln(bw, "Docker is enabled) its storage are all backed by named container-runtime")
		_, _ = fmt.Fprintln(bw, "volumes rather than host bind mounts — the container owns this storage, and")
		_, _ = fmt.Fprintln(bw, "none of it is a path on your host filesystem the agent could otherwise")
		_, _ = fmt.Fprintln(bw, "reach.")
	}

	for _, m := range mounts {
		if m.Kind == MountKindUnknown {
			wroteAny = true
			_, _ = fmt.Fprintf(bw, "An unclassified mount was detected: %s -> %s. This is a drift signal — a\n", m.Source, m.Destination)
			_, _ = fmt.Fprintln(bw, "new mount destination was added without updating this narrative's safety")
			_, _ = fmt.Fprintln(bw, "assumptions for it.")
		}
	}

	if !wroteAny {
		_, _ = fmt.Fprintln(bw, "No additional mounts beyond the workspace and credential sources above apply")
		_, _ = fmt.Fprintln(bw, "to this launch.")
	}
}
