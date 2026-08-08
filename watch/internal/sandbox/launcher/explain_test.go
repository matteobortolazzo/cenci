package launcher

import (
	"bytes"
	"strings"
	"testing"
)

// -- cenci security explain (ticket #594) ---------------------------------
//
// `cenci security explain` renders the same Posture `cenci audit` derives
// (see audit.go) as a text-only, plain-language "why this is/isn't safe"
// narrative via the new Posture.WriteExplanation method. These are
// package-boundary unit tests against literal Posture fixtures — no live
// Engine/Audit call is needed since WriteExplanation only reads what Posture
// already carries (mirrors audit_test.go's table-driven, content-specific
// assertion style).
//
// NOTE (red phase): WriteExplanation does not exist yet (lands in
// explain.go, a later phase) — every test below fails to COMPILE until then
// ("p.WriteExplanation undefined"). That is the intended red-phase state.
//
// IMPORTANT: --docker was removed from cenci already (see SECURITY.md) — no
// fixture or assertion below may reference it except to assert its absence.

// baselinePosture is the default-safe fixture shared by several tests below:
// bridge network, dind off, no boundary weakenings, one present + one absent
// credential.
func baselinePosture() Posture {
	return Posture{
		Agent:    "claude",
		Scope:    "repo",
		RepoRoot: "/repo",
		Image:    ImagePosture{Reference: "cenci-sandbox:latest", Type: ImageTypeMonolith},
		Workspace: WorkspacePosture{
			HostPath:      "/repo",
			ContainerPath: workspaceContainer,
			ReadOnly:      false,
		},
		Network: NetworkPosture{Mode: NetworkModeBridge, Weakened: false},
		Dind:    DindPosture{Enabled: false, Source: DindSourceOff},
		Mounts: []MountPosture{
			{Source: "/repo", Destination: workspaceContainer, ReadOnly: false, Kind: MountKindWorkspace},
			{Source: "claude-cenci-home-repo", Destination: "/home/dev", ReadOnly: false, Kind: MountKindHomeVolume},
			{Source: "claude-cenci-agent-cli", Destination: "/opt/cenci-agent", ReadOnly: false, Kind: MountKindAgentCLIVolume},
			{Source: "/home/user/.gitconfig", Destination: "/home/dev/.gitconfig", ReadOnly: true, Kind: MountKindGitconfig},
			{Source: "/usr/local/bin/cenci", Destination: "/usr/local/bin/cenci", ReadOnly: true, Kind: MountKindCenciBin},
			{Source: "/run/user/1000/cenci-sockets", Destination: cenciSocketMountDest, ReadOnly: true, Kind: MountKindCenciSocket},
			{Source: "/tmp/host-claude-creds", Destination: "/tmp/host-claude-creds/.credentials.json", ReadOnly: true, Kind: MountKindClaudeCreds},
		},
		CredentialSources: []CredentialSource{
			{Type: CredentialTypeClaude, HostPath: "/home/user/.claude/.credentials.json", Present: true, Probe: CredentialProbePresent},
			{Type: CredentialTypeCodex, HostPath: "/home/user/.codex/auth.json", Present: false, Probe: CredentialProbeMissing},
		},
		BoundaryWeakenings: []BoundaryWeakening{},
	}
}

// -- 1. default-safe framing -------------------------------------------------

// TestWriteExplanation_DefaultSafe_ThreatModelFramingAndNoWeakenings covers
// the default-safe baseline: the narrative must frame the container as the
// security boundary, reference (not verbatim-copy) SECURITY.md's threat
// model, and state the exact "Boundary weakenings: none (default-safe
// baseline)" line WriteText already uses for this case.
func TestWriteExplanation_DefaultSafe_ThreatModelFramingAndNoWeakenings(t *testing.T) {
	p := baselinePosture()
	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "container") || !strings.Contains(out, "boundary") {
		t.Errorf("expected threat-model framing naming the container as the security boundary, got:\n%s", out)
	}
	if !strings.Contains(out, "SECURITY.md") {
		t.Errorf("expected a reference to SECURITY.md's threat model, got:\n%s", out)
	}
	if !strings.Contains(out, "Boundary weakenings: none (default-safe baseline)") {
		t.Errorf("expected the default-safe baseline marker, got:\n%s", out)
	}
}

// -- 2. --host-network boundary weakening ------------------------------------

// TestWriteExplanation_HostNetworkActive_CallsOutBlastRadius covers the only
// boundary-weakening flag today: the narrative must call out --host-network
// specifically (not just a generic "weakened" note) and explain its
// network-isolation-loss blast radius.
func TestWriteExplanation_HostNetworkActive_CallsOutBlastRadius(t *testing.T) {
	p := baselinePosture()
	p.Network = NetworkPosture{Mode: NetworkModeHost, Weakened: true}
	p.BoundaryWeakenings = []BoundaryWeakening{{
		Option: "--host-network",
		Active: true,
		Effect: "the container joins the host network namespace instead of an isolated bridge network",
	}}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "--host-network") {
		t.Errorf("expected the --host-network marker called out explicitly, got:\n%s", out)
	}
	if !strings.Contains(out, "network isolation") {
		t.Errorf("expected the network-isolation-loss blast radius explained, got:\n%s", out)
	}
}

// -- 3. credential present vs absent -----------------------------------------

// TestWriteExplanation_CredentialPresence_ReflectsEachState covers both
// CredentialSource.Present states: the narrative must reflect the actual
// state (not a generic "credentials configured" placeholder) and explain the
// read-only-staging / named-volume assumption behind credential handling.
func TestWriteExplanation_CredentialPresence_ReflectsEachState(t *testing.T) {
	tests := []struct {
		name    string
		present bool
		probe   string
		wantSub string
	}{
		{name: "present", present: true, probe: CredentialProbePresent, wantSub: "read-only"},
		{name: "absent", present: false, probe: CredentialProbeMissing, wantSub: "absent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := baselinePosture()
			p.CredentialSources = []CredentialSource{
				{Type: CredentialTypeClaude, HostPath: "/home/user/.claude/.credentials.json", Present: tc.present, Probe: tc.probe},
			}

			var buf bytes.Buffer
			if err := p.WriteExplanation(&buf); err != nil {
				t.Fatalf("WriteExplanation: %v", err)
			}
			out := buf.String()

			if !strings.Contains(out, tc.wantSub) {
				t.Errorf("case %s: expected the narrative to mention %q, got:\n%s", tc.name, tc.wantSub, out)
			}
			if !strings.Contains(out, "named volume") {
				t.Errorf("case %s: expected the named-volume staging assumption explained, got:\n%s", tc.name, out)
			}
		})
	}
}

// -- 4. dind: separate section, never a boundary weakening -------------------

// TestWriteExplanation_DindEnabled_SeparateSectionNotWeakening covers dind:
// it must get its own "Nested Docker (sysbox-isolated)" narrative section
// (matching WriteText's section header), must NOT appear inside the
// "Boundary weakenings" section, and must never mention --docker (the
// removed DooD flag dind's sysbox-isolated approach replaced).
func TestWriteExplanation_DindEnabled_SeparateSectionNotWeakening(t *testing.T) {
	p := baselinePosture()
	p.Dind = DindPosture{
		Enabled:       true,
		Source:        DindSourceFlag,
		Runtime:       "sysbox-runc",
		StorageVolume: "claude-cenci-dind-repo",
		Note:          "nested Docker runs under the sysbox-runc OCI runtime, isolated from the host Docker daemon and never touching the host's own container runtime.",
	}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Nested Docker (sysbox-isolated)") {
		t.Errorf("expected a separate \"Nested Docker (sysbox-isolated)\" narrative section, got:\n%s", out)
	}
	if strings.Contains(out, "--docker") {
		t.Errorf("dind narrative must never mention --docker (removed flag), got:\n%s", out)
	}

	weakeningsIdx := strings.Index(out, "Boundary weakenings")
	dindIdx := strings.Index(out, "Nested Docker (sysbox-isolated)")
	if weakeningsIdx == -1 || dindIdx == -1 {
		t.Fatalf("expected both a \"Boundary weakenings\" section and a \"Nested Docker\" section, got:\n%s", out)
	}
	weakeningsSection := out[weakeningsIdx:]
	if strings.Contains(weakeningsSection, "sysbox") || strings.Contains(weakeningsSection, "Nested Docker") {
		t.Errorf("dind must not be listed inside the \"Boundary weakenings\" section, got:\n%s", weakeningsSection)
	}
}

// -- 5. regression: --docker must never appear -------------------------------

// TestWriteExplanation_NeverMentionsDocker is a dedicated regression test
// (per watch/docs/error-handling.md's rule on content-specific assertions
// (#446)): asserts the literal string "--docker" never appears in
// WriteExplanation's output,
// across every fixture used by the tests above (default-safe, host-network,
// dind, and both credential-absence variants) — --docker was removed from
// cenci and must never resurface in this narrative.
func TestWriteExplanation_NeverMentionsDocker(t *testing.T) {
	hostNetwork := baselinePosture()
	hostNetwork.Network = NetworkPosture{Mode: NetworkModeHost, Weakened: true}
	hostNetwork.BoundaryWeakenings = []BoundaryWeakening{{
		Option: "--host-network",
		Active: true,
		Effect: "the container joins the host network namespace instead of an isolated bridge network",
	}}

	dind := baselinePosture()
	dind.Dind = DindPosture{
		Enabled:       true,
		Source:        DindSourceFlag,
		Runtime:       "sysbox-runc",
		StorageVolume: "claude-cenci-dind-repo",
	}

	credAbsent := baselinePosture()
	credAbsent.CredentialSources = []CredentialSource{
		{Type: CredentialTypeCodex, HostPath: "/home/user/.codex/auth.json", Present: false, Probe: CredentialProbeMissing},
	}

	fixtures := map[string]Posture{
		"default-safe": baselinePosture(),
		"host-network": hostNetwork,
		"dind":         dind,
		"cred-absent":  credAbsent,
	}
	for name, p := range fixtures {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := p.WriteExplanation(&buf); err != nil {
				t.Fatalf("WriteExplanation: %v", err)
			}
			if strings.Contains(buf.String(), "--docker") {
				t.Errorf("fixture %q: WriteExplanation output must never mention the removed --docker flag, got:\n%s", name, buf.String())
			}
		})
	}
}

// -- 7. mounts: cenci-socket, cenci-bin, gitconfig, named volumes ------------

// TestWriteExplanation_Mounts_ExplainsEachDetectedKind is the reviewer's
// regression test for the gap where WriteExplanation never narrated
// p.Mounts at all: the cenci-socket mount (a literal "socket" per the
// ticket's acceptance criteria), the cenci-bin/gitconfig bind mounts, and
// the agent-cli/home named-volume mounts must each get a safety-assumption
// explanation, without duplicating the dedicated "Workspace and home" or
// "Credential sources" sections for the MountKindWorkspace/MountKind*Creds
// entries baselinePosture also carries in p.Mounts.
func TestWriteExplanation_Mounts_ExplainsEachDetectedKind(t *testing.T) {
	p := baselinePosture()
	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "socket") {
		t.Errorf("expected the cenci-socket mount to be explained (mentioning \"socket\"), got:\n%s", out)
	}
	if strings.Contains(out, "--docker") {
		t.Errorf("cenci-socket explanation must never mention the removed --docker flag, got:\n%s", out)
	}
	if !strings.Contains(out, "gitconfig") {
		t.Errorf("expected the gitconfig mount to be explained, got:\n%s", out)
	}
	if !strings.Contains(out, "named container-runtime") {
		t.Errorf("expected the agent-cli/home named-volume mounts to be explained, got:\n%s", out)
	}
}

// TestWriteExplanation_Mounts_DindVolumeExplained covers MountKindDindVolume
// specifically (only present when nested Docker is enabled): it must be
// covered by the same named-volume narrative as the other volume-backed
// mounts, without being duplicated inside the "Nested Docker
// (sysbox-isolated)" section's own dind-specific narrative.
func TestWriteExplanation_Mounts_DindVolumeExplained(t *testing.T) {
	p := baselinePosture()
	p.Dind = DindPosture{
		Enabled:       true,
		Source:        DindSourceFlag,
		Runtime:       "sysbox-runc",
		StorageVolume: "claude-cenci-dind-repo",
	}
	p.Mounts = append(p.Mounts, MountPosture{
		Source:      "claude-cenci-dind-repo",
		Destination: "/var/lib/docker",
		ReadOnly:    false,
		Kind:        MountKindDindVolume,
	})

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "named container-runtime") {
		t.Errorf("expected the dind-volume mount to be explained alongside the other named-volume mounts, got:\n%s", out)
	}
}

// TestWriteExplanation_Mounts_NoneApply covers the case where p.Mounts is
// empty: the "Other mounts" section must still render (not panic or leave a
// blank gap) with an explicit "none apply" statement, mirroring WriteText's
// "(none)" placeholder for an empty Mounts/Volumes table.
func TestWriteExplanation_Mounts_NoneApply(t *testing.T) {
	p := baselinePosture()
	p.Mounts = nil

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Other mounts:") {
		t.Errorf("expected the \"Other mounts\" section header even when p.Mounts is empty, got:\n%s", out)
	}
	if !strings.Contains(out, "No additional mounts") {
		t.Errorf("expected an explicit \"none apply\" statement for an empty p.Mounts, got:\n%s", out)
	}
}

// TestWriteExplanation_Mounts_UnknownKindSurfaced guards against silently
// dropping a future MountKindUnknown drift sentinel from the narrative — the
// same "no silent no-match discard" principle the project's other match-miss
// exclusions follow.
func TestWriteExplanation_Mounts_UnknownKindSurfaced(t *testing.T) {
	p := baselinePosture()
	p.Mounts = append(p.Mounts, MountPosture{
		Source:      "/host/some/future/path",
		Destination: "/container/future/dest",
		ReadOnly:    true,
		Kind:        MountKindUnknown,
	})

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "unclassified mount") {
		t.Errorf("expected an unclassified-mount drift signal for MountKindUnknown, got:\n%s", out)
	}
	if !strings.Contains(out, "/container/future/dest") {
		t.Errorf("expected the unclassified mount's destination to be named, got:\n%s", out)
	}
}

// -- 8. staged/applicable/probe narrative (ticket #598) ----------------------

// credentialSection extracts the "Credential sources:" narrative block out
// of a full WriteExplanation output, up to (not including) the next
// "Forwarded exec env" section header — narrowing assertions to the
// credential-sources narrative specifically, so a match against a phrase
// used elsewhere in the narrative (e.g. "bind-mounted" in the mounts
// sections) can't produce a false pass/fail.
func credentialSection(t *testing.T, out string) string {
	t.Helper()
	credIdx := strings.Index(out, "Credential sources:")
	forwardedIdx := strings.Index(out, "Forwarded exec env")
	if credIdx == -1 || forwardedIdx == -1 || forwardedIdx <= credIdx {
		t.Fatalf("expected a \"Credential sources:\" section before \"Forwarded exec env\", got:\n%s", out)
	}
	return out[credIdx:forwardedIdx]
}

// TestWriteExplanation_CredentialPresentButNotStaged_NoStagedClaim covers
// the #598 acceptance criterion directly: a credential that is present on
// the host but not staged (e.g. codex creds during a claude audit —
// Applicable:false, Staged:false) must NOT be narrated as bind-mounted,
// copied, or staged — WriteExplanation must branch on Staged, never infer
// staging from Present alone.
func TestWriteExplanation_CredentialPresentButNotStaged_NoStagedClaim(t *testing.T) {
	p := baselinePosture()
	p.CredentialSources = []CredentialSource{
		{
			Type:       CredentialTypeCodex,
			HostPath:   "/home/user/.codex/auth.json",
			Present:    true,
			Probe:      CredentialProbePresent,
			Applicable: false,
			Staged:     false,
		},
	}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()
	credSection := credentialSection(t, out)

	if !strings.Contains(credSection, "codex") {
		t.Errorf("expected codex credentials named in the narrative, got:\n%s", credSection)
	}
	if strings.Contains(credSection, "bind-mounted") || strings.Contains(credSection, "copied") {
		t.Errorf("expected the narrative to NOT claim a present-but-not-staged codex credential is bind-mounted/copied, got:\n%s", credSection)
	}
}

// TestWriteExplanation_CredentialProbeError_DistinguishesFromAbsent covers
// the unreadable/error probe state: the narrative must distinguish it from
// plain absence (e.g. must not say the credential is simply "absent") —
// content-specific, per watch/docs/error-handling.md #446.
func TestWriteExplanation_CredentialProbeError_DistinguishesFromAbsent(t *testing.T) {
	p := baselinePosture()
	p.CredentialSources = []CredentialSource{
		{
			Type:       CredentialTypeCodex,
			HostPath:   "/home/user/.codex/auth.json",
			Present:    false,
			Probe:      CredentialProbeError,
			Applicable: true,
			Staged:     false,
		},
	}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()
	credSection := credentialSection(t, out)

	if !strings.Contains(credSection, "unreadable") && !strings.Contains(credSection, "error") {
		t.Errorf("expected the probe-error state to be narrated distinctly (unreadable/error), got:\n%s", credSection)
	}
	if strings.Contains(credSection, "absent") {
		t.Errorf("expected the probe-error state to NOT be narrated as plain absence, got:\n%s", credSection)
	}
}

// TestWriteExplanation_CredentialProbeUnrecognized_NotNarratedAsAbsent covers
// a silent-failure regression: Probe is a plain string, not a
// compiler-enforced enum, so a CredentialSource carrying an
// unrecognized/zero-value Probe must be narrated as inconclusive — never as
// the plain, most-reassuring "absent" wording used for a genuinely missing
// credential (content-specific per watch/docs/error-handling.md #446).
func TestWriteExplanation_CredentialProbeUnrecognized_NotNarratedAsAbsent(t *testing.T) {
	p := baselinePosture()
	p.CredentialSources = []CredentialSource{
		{
			Type:       CredentialTypeCodex,
			HostPath:   "/home/user/.codex/auth.json",
			Present:    false,
			Probe:      "some-future-probe-state",
			Applicable: true,
			Staged:     false,
		},
	}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()
	credSection := credentialSection(t, out)

	if !strings.Contains(credSection, "inconclusive") {
		t.Errorf("expected the unrecognized-probe state to be narrated as inconclusive, got:\n%s", credSection)
	}
	if strings.Contains(credSection, "are absent on the host") {
		t.Errorf("expected the unrecognized-probe state to NOT use the plain-missing 'absent on the host' wording, got:\n%s", credSection)
	}
	if !strings.Contains(credSection, "some-future-probe-state") {
		t.Errorf("expected the unrecognized-probe text to include the actual Probe value, got:\n%s", credSection)
	}
}

// -- 6. secret safety ---------------------------------------------------------

// TestWriteExplanation_NeverLeaksEnvVarValues covers the "never print
// credential values" requirement: a forwarded, secret-marked env var's NAME
// must be reported, but "NAME=value" must never appear (there is no Value
// field on ForwardedEnvVar/EnvVarName to leak from, but this pins the
// narrative-writer contract explicitly, matching audit_test.go's
// TestAudit_SecretLeakRegression_NeverEmitsSecretValues precedent).
func TestWriteExplanation_NeverLeaksEnvVarValues(t *testing.T) {
	p := baselinePosture()
	p.ForwardedEnv = []ForwardedEnvVar{{Name: "ANTHROPIC_API_KEY", Secret: true}}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Errorf("expected the ANTHROPIC_API_KEY name reported in the narrative, got:\n%s", out)
	}
	if strings.Contains(out, "ANTHROPIC_API_KEY=") {
		t.Errorf("expected no \"ANTHROPIC_API_KEY=value\" leak, got:\n%s", out)
	}
}

// -- ticket #627: observed-vs-planned basis, network rewording, inspect
// warning, and next-exec labeling ---------------------------------------
//
// NOTE (red phase): Posture.Basis/PostureBasisRunning/PostureBasisPlanned/
// Posture.InspectWarning do not exist yet (land in audit.go, a later
// phase); every test below fails to COMPILE until then. The narrative
// content assertions (bridge/host-network full sentences, next-exec
// qualifier, warning suppression) also fail against the CURRENT
// WriteExplanation body even once the fields exist, until explain.go's
// network paragraph and basis/warning rendering land — the intended
// red-phase state either way.

// -- 9. basis: observed vs planned -------------------------------------------

// TestWriteExplanation_Basis_StatesObservedVsPlanned covers AC #5: the
// narrative must explicitly say whether it is explaining observed running
// state or a plan, using content-specific wording for each basis (not a
// shared, ambiguous phrase).
func TestWriteExplanation_Basis_StatesObservedVsPlanned(t *testing.T) {
	tests := []struct {
		name    string
		basis   string
		wantSub string
	}{
		{name: "running", basis: PostureBasisRunning, wantSub: "observed"},
		{name: "planned", basis: PostureBasisPlanned, wantSub: "plan"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := baselinePosture()
			p.Basis = tc.basis

			var buf bytes.Buffer
			if err := p.WriteExplanation(&buf); err != nil {
				t.Fatalf("WriteExplanation: %v", err)
			}
			out := buf.String()

			if !strings.Contains(strings.ToLower(out), tc.wantSub) {
				t.Errorf("basis %q: expected the narrative to mention %q, got:\n%s", tc.basis, tc.wantSub, out)
			}
		})
	}
}

// TestWriteExplanation_Basis_RunningAndPlanned_UseDistinctFraming is a
// dedicated content-specific regression (watch/docs/error-handling.md
// #446): the running and planned intros must render visibly different
// text, not the same generic sentence with the basis word swapped out
// silently losing meaning.
func TestWriteExplanation_Basis_RunningAndPlanned_UseDistinctFraming(t *testing.T) {
	running := baselinePosture()
	running.Basis = PostureBasisRunning
	var runningBuf bytes.Buffer
	if err := running.WriteExplanation(&runningBuf); err != nil {
		t.Fatalf("WriteExplanation (running): %v", err)
	}

	planned := baselinePosture()
	planned.Basis = PostureBasisPlanned
	var plannedBuf bytes.Buffer
	if err := planned.WriteExplanation(&plannedBuf); err != nil {
		t.Fatalf("WriteExplanation (planned): %v", err)
	}

	if runningBuf.String() == plannedBuf.String() {
		t.Errorf("running-basis and planned-basis narratives must render distinct intros, got identical output:\n%s", runningBuf.String())
	}
}

// -- 10. bridge network: separate namespace, no published ports, outbound
// reachability (AC #6/#7/#10) ------------------------------------------------

// TestWriteExplanation_BridgeNetwork_SeparateNamespaceNoPublishedInboundPorts
// covers AC #6/#7 as a complete-sentence assertion (not an isolated
// keyword): bridge mode uses a separate network namespace and publishes no
// inbound ports by default.
func TestWriteExplanation_BridgeNetwork_SeparateNamespaceNoPublishedInboundPorts(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisPlanned

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "separate network namespace") {
		t.Errorf("expected the bridge narrative to state a separate network namespace as a complete claim, got:\n%s", out)
	}
	if !strings.Contains(out, "no published inbound ports") && !strings.Contains(out, "publishes no inbound ports") {
		t.Errorf("expected the bridge narrative to state no published inbound ports, got:\n%s", out)
	}
}

// TestWriteExplanation_BridgeNetwork_OutboundMayReachHostLanInternet covers
// AC #7's flip side: outbound connections may still reach routable host,
// LAN, or internet services depending on runtime/firewall configuration —
// this is the corrected wording replacing the old "outbound-only and
// isolated from the host" overclaim.
func TestWriteExplanation_BridgeNetwork_OutboundMayReachHostLanInternet(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisPlanned

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "outbound") {
		t.Errorf("expected the bridge narrative to discuss outbound connections, got:\n%s", out)
	}
	if !strings.Contains(out, "host") || !strings.Contains(out, "LAN") || (!strings.Contains(out, "internet") && !strings.Contains(out, "Internet")) {
		t.Errorf("expected the bridge narrative to name host, LAN, and internet reachability explicitly, got:\n%s", out)
	}
	if !strings.Contains(out, "firewall") {
		t.Errorf("expected the bridge narrative to qualify outbound reachability on runtime/firewall configuration, got:\n%s", out)
	}
}

// TestWriteExplanation_BridgeNetwork_NeverClaimsCompleteIsolationOrOutboundOnly
// is a dedicated regression test (per the ticket's Goal/AC #6): the removed
// overclaim "outbound-only and isolated from the host" must never resurface
// in the bridge narrative.
func TestWriteExplanation_BridgeNetwork_NeverClaimsCompleteIsolationOrOutboundOnly(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisPlanned

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "outbound-only and isolated from the host") {
		t.Errorf("bridge narrative must never claim complete host isolation / universal outbound-only enforcement, got:\n%s", out)
	}
}

// -- 11. host-network: blast radius, shared namespace, localhost exposure
// (AC #8) ---------------------------------------------------------------

// TestWriteExplanation_HostNetwork_SharedNamespaceAndLocalhostExposure
// covers what --host-network additionally changes, as complete sentences:
// a shared host network namespace, host-localhost exposure, and loss of
// namespace separation — continuing to identify the increased blast radius
// clearly (AC #8).
func TestWriteExplanation_HostNetwork_SharedNamespaceAndLocalhostExposure(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisPlanned
	p.Network = NetworkPosture{Mode: NetworkModeHost, Weakened: true}
	p.BoundaryWeakenings = []BoundaryWeakening{{
		Option: "--host-network",
		Active: true,
		Effect: "the container joins the host network namespace instead of an isolated bridge network",
	}}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "shared host network namespace") && !strings.Contains(out, "shared") {
		t.Errorf("expected the host-network narrative to state the shared host namespace, got:\n%s", out)
	}
	if !strings.Contains(out, "localhost") {
		t.Errorf("expected the host-network narrative to state host-localhost exposure, got:\n%s", out)
	}
	if !strings.Contains(out, "namespace separation") {
		t.Errorf("expected the host-network narrative to state the loss of namespace separation, got:\n%s", out)
	}
	if !strings.Contains(out, "blast radius") {
		t.Errorf("expected the host-network narrative to keep the blast-radius warning, got:\n%s", out)
	}
}

// -- 12. inspect warning: never a reassuring default -------------------------

// TestWriteExplanation_InspectWarning_SuppressesBaselineAndSurfacesWarning
// covers Q2/AC #9: when InspectWarning is set, the narrative must surface
// it prominently and must never print the default-safe baseline line, even
// when BoundaryWeakenings is empty.
func TestWriteExplanation_InspectWarning_SuppressesBaselineAndSurfacesWarning(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisPlanned
	p.InspectWarning = "the running container's actual posture could not be verified: malformed inspect output"

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, p.InspectWarning) {
		t.Errorf("expected the narrative to surface the InspectWarning text verbatim, got:\n%s", out)
	}
	if strings.Contains(out, "Boundary weakenings: none (default-safe baseline)") {
		t.Errorf("narrative must never print the default-safe baseline line when InspectWarning is set, got:\n%s", out)
	}
}

// -- 13. next-exec labeling (Q4) ----------------------------------------------

// TestWriteExplanation_NextExecQualifier_ForwardedEnvWhenRunning covers Q4:
// forwardedEnv has no inspect source, so a running-basis narrative must
// label it "next-exec — not observed" rather than presenting it as an
// observed fact of the running container.
func TestWriteExplanation_NextExecQualifier_ForwardedEnvWhenRunning(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisRunning
	p.ForwardedEnv = []ForwardedEnvVar{{Name: "ANTHROPIC_API_KEY", Secret: true}}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "next-exec") {
		t.Errorf("expected a \"next-exec\" qualifier on the forwarded-env section for a running-basis report, got:\n%s", out)
	}
	if !strings.Contains(out, "not observed") {
		t.Errorf("expected the next-exec qualifier to explicitly say these values are \"not observed\" from the running container, got:\n%s", out)
	}
}

// TestWriteExplanation_NextExecQualifier_AbsentWhenPlanned covers the
// converse of Q4: a planned-basis narrative must NOT render the "next-exec"
// qualifier at all — every field is already a hypothetical next-launch
// value in that mode, so singling out forwardedEnv would be misleading.
func TestWriteExplanation_NextExecQualifier_AbsentWhenPlanned(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisPlanned
	p.ForwardedEnv = []ForwardedEnvVar{{Name: "ANTHROPIC_API_KEY", Secret: true}}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "next-exec") {
		t.Errorf("expected no \"next-exec\" qualifier for a planned-basis report, got:\n%s", out)
	}
}

// -- 14. dind source unknown must not render as "disabled" (#598/#627) -------

// TestWriteExplanation_DindSourceUnknown_NotRenderedAsDisabled covers the
// review finding for #627: a running container whose cenci-sand.dind label
// is unrecognized reports DindPosture{Enabled:false, Source:DindSourceUnknown}
// — WriteExplanation must render this as indeterminate, never as the
// confident "Nested Docker is disabled" sentence it prints for a genuinely
// off dind (DindSourceOff/DindSourceObserved). This case has no
// InspectWarning (the inspect call itself succeeded; only the label's
// meaning is ambiguous), so the "default-safe baseline" suppression must key
// off Dind.Source too, not just InspectWarning.
func TestWriteExplanation_DindSourceUnknown_NotRenderedAsDisabled(t *testing.T) {
	p := baselinePosture()
	p.Basis = PostureBasisRunning
	p.Dind = DindPosture{
		Enabled: false,
		Source:  DindSourceUnknown,
		Note:    "the running container's cenci-sand.dind label is an unrecognized value; its nested-Docker posture could not be conclusively determined and must not be read as disabled.",
	}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "Nested Docker is disabled for this launch") {
		t.Errorf("narrative must not render an unrecognized dind label as a confident \"disabled\", got:\n%s", out)
	}
	if !strings.Contains(out, "could not be determined") {
		t.Errorf("expected a non-reassuring \"could not be determined\" narrative for an unrecognized dind label, got:\n%s", out)
	}
	if strings.Contains(out, "Boundary weakenings: none (default-safe baseline)") {
		t.Errorf("narrative must never print the default-safe baseline line when dind's state is indeterminate, got:\n%s", out)
	}
}

// -- 15. dind degraded by the host platform (#962) ---------------------------

// TestWriteExplanation_DindPlatformUnsupported_ExplainsTheDegrade covers the
// macOS degrade: dind was requested but the host can never register
// sysbox-runc, so the posture reports DindPosture{Enabled:false,
// Source:DindSourcePlatformUnsupported}. The narrative must explain that the
// request was dropped rather than printing the plain "Nested Docker is
// disabled" sentence, which reads as "nobody asked for it" and leaves the
// reader without an explanation for why in-container Docker fails. Unlike
// DindSourceUnknown, this state IS conclusive — dind is definitively off —
// so the default-safe baseline line must still print.
func TestWriteExplanation_DindPlatformUnsupported_ExplainsTheDegrade(t *testing.T) {
	p := baselinePosture()
	p.Dind = DindPosture{
		Enabled: false,
		Source:  DindSourcePlatformUnsupported,
		Note:    "nested Docker was requested (config) but is unavailable on macOS.",
	}

	var buf bytes.Buffer
	if err := p.WriteExplanation(&buf); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "Nested Docker is disabled for this launch") {
		t.Errorf("narrative rendered a degraded-by-platform dind as a plain \"disabled\", which hides that the launch dropped a request, got:\n%s", out)
	}
	if !strings.Contains(out, "requested for this launch but is unavailable on this host") {
		t.Errorf("expected the narrative to say the dind request was dropped by the host platform, got:\n%s", out)
	}
	if !strings.Contains(out, "Boundary weakenings: none (default-safe baseline)") {
		t.Errorf("expected the default-safe baseline line — a degraded dind is conclusively off, not indeterminate, got:\n%s", out)
	}
}
