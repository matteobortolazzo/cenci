package launcher

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// This file implements `cenci audit`'s posture derivation (ticket #588): a
// read-only report of the effective sandbox security posture the launcher
// WOULD apply for a given Options — mounts (ro/rw), env var NAMES, network
// mode, nested-Docker (dind) mode, credential sources, named volumes, image
// type, and opt-in boundary weakenings. Posture is derived by reusing the
// same assemble* construction methods Launch (launch.go) already calls
// (assembleVolumeMounts, assembleEnv, validateCredentials,
// assembleOptionalFeatures) — Audit must never re-derive that construction
// independently, or the report and the real launch argv could silently
// drift apart. Audit never launches, attaches, or starts the daemon.

// -- Posture and its nested types ---------------------------------------
//
// Field names and JSON tags below are the STABLE, scriptable JSON schema
// (`cenci audit --json`) confirmed during planning; do not rename a field
// without updating both the JSON consumers and docs/cli-conventions.md.

// ImageType enumerates Posture.Image.Type.
const (
	ImageTypeMonolith = "monolith"
	ImageTypeRepo     = "repo"
)

// NetworkMode enumerates Posture.Network.Mode.
const (
	NetworkModeBridge = "bridge"
	NetworkModeHost   = "host"
)

// DindSource enumerates Posture.Dind.Source: which input decided whether
// dind is on, mirroring ResolveDind's own precedence (flag > repo config,
// with --no-dind always winning as "off").
const (
	DindSourceFlag   = "flag"
	DindSourceConfig = "config"
	DindSourceOff    = "off"
)

// Mount kind classifications for Posture.Mounts[].Kind. Every mount Audit
// reports must classify into exactly one of these — never a bare/unlabeled
// mount — so a reader can tell at a glance what each host path is for.
const (
	MountKindWorkspace      = "workspace"
	MountKindHomeVolume     = "home-volume"
	MountKindAgentCLIVolume = "agent-cli-volume"
	MountKindDindVolume     = "dind-volume"
	MountKindGitconfig      = "gitconfig"
	MountKindCenciBin       = "cenci-bin"
	MountKindCenciSocket    = "cenci-socket"
	MountKindClaudeCreds    = "claude-creds"
	MountKindCodexCreds     = "codex-creds"
	MountKindOpencodeCreds  = "opencode-creds"
	MountKindGhCreds        = "gh-creds"
	MountKindPencilCreds    = "pencil-creds"

	// MountKindUnknown is an explicit sentinel for a mount destination that
	// doesn't match any case in mountKindForDestination's switch — a visible
	// drift signal (rather than a silent, unlabeled "") if a future change to
	// assembleVolumeMounts/validateCredentials adds a new mount destination
	// without updating this switch.
	MountKindUnknown = "unknown"
)

// Credential source type identifiers for Posture.CredentialSources[].Type.
const (
	CredentialTypeClaude   = "claude"
	CredentialTypeCodex    = "codex"
	CredentialTypeOpencode = "opencode"
	CredentialTypeGh       = "gh"
	CredentialTypePencil   = "pencil"
)

// ImagePosture is Posture.Image: the resolved image reference and whether it
// is the shared monolith or a repo-opted-in image (scope.UsingRepoImage).
type ImagePosture struct {
	Reference string `json:"reference"`
	Type      string `json:"type"`
}

// WorkspacePosture is Posture.Workspace: the host directory bind-mounted
// into the container and its container-side mount point. The workspace bind
// is always read-write (the agent edits the checkout in place); ReadOnly is
// carried explicitly rather than assumed so a future read-only workspace
// mode doesn't silently go unreported.
type WorkspacePosture struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

// NetworkPosture is Posture.Network: the container network mode and whether
// it is weakened from the default-safe bridge mode (only --host-network
// weakens it; see BoundaryWeakenings' doc comment for why dind is never
// counted here).
type NetworkPosture struct {
	Mode     string `json:"mode"`
	Weakened bool   `json:"weakened"`
}

// DindPosture is Posture.Dind: nested-Docker (sysbox-isolated) mode.
// Runtime/StorageVolume/Note are only meaningful when Enabled is true.
//
// dind is deliberately reported in its OWN object here, outside
// BoundaryWeakenings — a confirmed Q&A decision from planning: dind gets its
// own "Nested Docker (sysbox-isolated)" section (WriteText) / top-level JSON
// object, not a boundary-weakening entry, because it is a sysbox-isolated
// opt-in feature with its own dedicated OCI runtime boundary, not a
// weakening of the container's own isolation the way --host-network is.
type DindPosture struct {
	Enabled       bool   `json:"enabled"`
	Source        string `json:"source"`
	Runtime       string `json:"runtime,omitempty"`
	StorageVolume string `json:"storageVolume,omitempty"`
	Note          string `json:"note,omitempty"`
}

// MountPosture is one entry of Posture.Mounts: a single bind or named-volume
// mount the launcher would create, classified by Kind (see the MountKind*
// consts). ReadOnly is derived from the trailing ":ro" suffix on the
// assemble* methods' "-v" argument value, never assumed from the mount's
// purpose.
type MountPosture struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
	Kind        string `json:"kind"`
}

// VolumePosture is one entry of Posture.Volumes: a named container-runtime
// volume (as opposed to a host bind mount) the launch would reference.
type VolumePosture struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// EnvVarName is one entry of Posture.Env: a create-time environment variable
// NAME the container would receive. It deliberately carries no Value field —
// not "a Value field that happens to stay empty," an actual absence at the
// type level, so a future accidental assignment can't silently reintroduce a
// secret-leak regression through this type.
type EnvVarName struct {
	Name string `json:"name"`
}

// ForwardedEnvVar is one entry of Posture.ForwardedEnv: a per-exec
// (not create-time) environment variable NAME actually forwarded for this
// agent/opts combination (mirrors Launch's execEnvArgs construction —
// provider API keys are scoped per agent and only listed here when they
// would actually be forwarded). Secret marks whether the variable's value is
// sensitive (true for provider API keys); like EnvVarName, this type never
// carries the value itself.
type ForwardedEnvVar struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret"`
}

// CredentialSource is one entry of Posture.CredentialSources: a host
// credential file (or absence of one) the launcher would stage into the
// container for Type. HostPath is always the resolved host-side path
// regardless of Present, so an operator can see exactly where cenci expects
// to find it even when it's missing. Present is false, never an error, when
// the file is absent — Audit's non-fatal credential handling.
type CredentialSource struct {
	Type     string `json:"type"`
	HostPath string `json:"hostPath"`
	Present  bool   `json:"present"`
}

// BoundaryWeakening is one entry of Posture.BoundaryWeakenings: an opt-in
// flag that weakens the container's isolation boundary from its
// default-safe configuration. Today the only such flag is --host-network
// (see assembleOptionalFeatures's own warning). dind is NEVER listed here —
// see DindPosture's doc comment.
type BoundaryWeakening struct {
	Option string `json:"option"`
	Active bool   `json:"active"`
	Effect string `json:"effect"`
}

// Posture is the full effective sandbox security posture `cenci audit`
// reports for a given Options: everything the launcher would apply for the
// current repo/agent/flags, human-readable via WriteText or as stable JSON.
// It never carries a secret/credential VALUE anywhere in its field set —
// only names, sources, and mount/staging status (see EnvVarName/
// ForwardedEnvVar/CredentialSource's doc comments).
type Posture struct {
	Agent    string `json:"agent"`
	Scope    string `json:"scope"`
	RepoRoot string `json:"repoRoot,omitempty"`

	Image     ImagePosture     `json:"image"`
	Workspace WorkspacePosture `json:"workspace"`
	Network   NetworkPosture   `json:"network"`
	Dind      DindPosture      `json:"dind"`

	Mounts             []MountPosture      `json:"mounts"`
	Volumes            []VolumePosture     `json:"volumes"`
	Env                []EnvVarName        `json:"env"`
	ForwardedEnv       []ForwardedEnvVar   `json:"forwardedEnv"`
	CredentialSources  []CredentialSource  `json:"credentialSources"`
	BoundaryWeakenings []BoundaryWeakening `json:"boundaryWeakenings"`

	ReseedCreds bool `json:"reseedCreds"`
}

// -- Audit -----------------------------------------------------------------

// Audit derives the effective sandbox security posture the launcher WOULD
// apply for opts — reusing the same assemble* construction methods Launch
// already calls (assembleVolumeMounts, assembleEnv, validateCredentials,
// assembleOptionalFeatures) rather than re-deriving them, plus ResolveDind
// for dind attribution and a read-only wiring probe (cenciWiringReadOnly)
// in place of Launch's resolveCenciWiring (which starts the daemon as a
// side effect — Audit never does). Audit never launches, attaches, or
// starts the daemon.
//
// Unlike validateCredentials' use inside Launch (a hard launch-blocking
// error when required auth is absent), Audit treats a missing credential as
// non-fatal: it is recorded in the returned Posture's CredentialSources as
// present:false, never surfaced as a returned error.
//
// A UsageError (see launch.go's UsageError/IsUsage) from ResolveDind (e.g.
// --dind combined with --no-dind, or --dind requested outside repo scope)
// is still returned as an error — that is a caller input-validation
// failure, not a credential/environment condition Audit degrades through.
func (e *Engine) Audit(opts Options) (Posture, error) {
	agent := opts.Agent
	if agent == "" {
		agent = "claude"
	}
	if err := ValidateAgent(agent); err != nil {
		return Posture{}, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Posture{}, fmt.Errorf("cannot determine working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Posture{}, fmt.Errorf("cannot determine home directory: %w", err)
	}
	scope := ComputeScope(agent, opts.Name, cwd, home)

	dindOn, err := ResolveDind(opts, scope)
	if err != nil {
		return Posture{}, err
	}

	cenciBin, socketDir, cenciAvailable := cenciWiringReadOnly()

	mountArgs := e.assembleVolumeMounts(agent, cenciBin, socketDir, cenciAvailable, scope, home, dindOn)
	envArgs := e.assembleEnv(agent, scope, opts, dindOn)
	featureArgs := e.assembleOptionalFeatures(opts)

	// Non-fatal per Audit's contract: a missing codex/opencode credential
	// would hard-fail Launch (validateCredentials' returned error), but
	// Audit only records absence in CredentialSources below — the emitted
	// mount args (nil on failure) are still reused for mount classification.
	credArgs, _ := e.validateCredentials(agent, home)

	allMountArgs := make([]string, 0, len(mountArgs)+len(credArgs))
	allMountArgs = append(allMountArgs, mountArgs...)
	allMountArgs = append(allMountArgs, credArgs...)
	mounts := classifyMounts(allMountArgs)

	allEnvArgs := make([]string, 0, len(mountArgs)+len(envArgs))
	allEnvArgs = append(allEnvArgs, mountArgs...)
	allEnvArgs = append(allEnvArgs, envArgs...)

	imageType := ImageTypeMonolith
	if scope.UsingRepoImage {
		imageType = ImageTypeRepo
	}

	return Posture{
		Agent:    agent,
		Scope:    scope.WorkspaceScope,
		RepoRoot: scope.RepoRoot,

		Image: ImagePosture{Reference: scope.Image, Type: imageType},
		Workspace: WorkspacePosture{
			HostPath:      scope.WorkspaceBindHost,
			ContainerPath: workspaceContainer,
			ReadOnly:      false,
		},
		Network: networkPosture(featureArgs),
		Dind:    dindPosture(dindOn, opts, scope),

		Mounts:             mounts,
		Volumes:            namedVolumes(mounts),
		Env:                classifyEnvNames(allEnvArgs),
		ForwardedEnv:       forwardedEnvVars(agent),
		CredentialSources:  credentialSources(home),
		BoundaryWeakenings: boundaryWeakenings(featureArgs),

		ReseedCreds: opts.ReseedCreds,
	}, nil
}

// classifyMounts parses every "-v <source>:<destination>[:ro]" pair out of
// args (as emitted by assembleVolumeMounts/validateCredentials) into a
// MountPosture, classified by destination via mountKindForDestination.
func classifyMounts(args []string) []MountPosture {
	mounts := make([]MountPosture, 0, len(args)/2)
	for i := 0; i < len(args); i++ {
		if args[i] != "-v" || i+1 >= len(args) {
			continue
		}
		parts := strings.SplitN(args[i+1], ":", 3)
		if len(parts) < 2 {
			continue
		}
		source, destination := parts[0], parts[1]
		mounts = append(mounts, MountPosture{
			Source:      source,
			Destination: destination,
			ReadOnly:    len(parts) == 3 && parts[2] == "ro",
			Kind:        mountKindForDestination(destination),
		})
	}
	return mounts
}

// mountKindForDestination classifies a mount's container-side destination
// path into exactly one MountKind* constant, mirroring every destination
// assembleVolumeMounts/validateCredentials ever emit — so classifyMounts
// never has to guess.
func mountKindForDestination(destination string) string {
	switch destination {
	case workspaceContainer:
		return MountKindWorkspace
	case "/home/dev":
		return MountKindHomeVolume
	case "/opt/cenci-agent":
		return MountKindAgentCLIVolume
	case "/var/lib/docker":
		return MountKindDindVolume
	case "/home/dev/.gitconfig":
		return MountKindGitconfig
	case "/usr/local/bin/cenci":
		return MountKindCenciBin
	case cenciSocketMountDest:
		return MountKindCenciSocket
	case "/tmp/host-claude-creds/.credentials.json":
		return MountKindClaudeCreds
	case "/tmp/host-codex-creds/auth.json":
		return MountKindCodexCreds
	case "/tmp/host-opencode-creds/auth.json":
		return MountKindOpencodeCreds
	case "/tmp/host-gh-config/hosts.yml":
		return MountKindGhCreds
	case "/tmp/host-pencil-creds/session-cli.json":
		return MountKindPencilCreds
	default:
		return MountKindUnknown
	}
}

// namedVolumes filters mounts down to the ones backed by a named
// container-runtime volume rather than a host bind mount — every host bind
// mount's Source is an absolute host path, while every named volume's
// Source (scope.VolumeName, AgentCLIVolumeName, scope.DindVolumeName) never
// starts with "/".
func namedVolumes(mounts []MountPosture) []VolumePosture {
	volumes := make([]VolumePosture, 0, len(mounts))
	for _, m := range mounts {
		if strings.HasPrefix(m.Source, "/") {
			continue
		}
		volumes = append(volumes, VolumePosture{Name: m.Source, Kind: m.Kind})
	}
	return volumes
}

// classifyEnvNames parses every "-e NAME=value" pair out of args (as emitted
// by assembleVolumeMounts/assembleEnv) into an EnvVarName carrying only the
// NAME — never the value.
func classifyEnvNames(args []string) []EnvVarName {
	envs := make([]EnvVarName, 0, len(args)/2)
	for i := 0; i < len(args); i++ {
		if args[i] != "-e" || i+1 >= len(args) {
			continue
		}
		name := args[i+1]
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		envs = append(envs, EnvVarName{Name: name})
	}
	return envs
}

// forwardedEnvVarNames are the provider API keys assembleExecEnv may forward
// per-exec — the only names ForwardedEnv ever reports (see
// ForwardedEnvVar's doc comment). This is the single source of truth for
// secret-env classification: both `cenci audit`'s reporting and
// `cenci open --dry-run`'s redaction (renderArgv/redactSecretEnv in
// dryrun.go) depend on it. Any new secret "-e" env forward added in
// assembleExecEnv/assembleEnv MUST also be added here, or it will render
// unredacted in dry-run output.
var forwardedEnvVarNames = map[string]bool{
	"CONTEXT7_API_KEY":  true,
	"ANTHROPIC_API_KEY": true,
	"OPENAI_API_KEY":    true,
	"PEN_CLI_KEY":       true,
}

// forwardedEnvVars reports the per-exec provider API keys assembleExecEnv
// would actually forward for agent — names only, Secret:true, and only when
// the corresponding host env var is actually set (matching assembleExecEnv's
// own "only listed when they would actually be forwarded" behavior).
func forwardedEnvVars(agent string) []ForwardedEnvVar {
	envNames := classifyEnvNames(assembleExecEnv(agent))
	forwarded := make([]ForwardedEnvVar, 0, len(envNames))
	for _, name := range envNames {
		if forwardedEnvVarNames[name.Name] {
			forwarded = append(forwarded, ForwardedEnvVar{Name: name.Name, Secret: true})
		}
	}
	return forwarded
}

// credentialSourceSpecs are the fixed host credential paths Audit always
// probes, regardless of which agent is being audited — an operator can see
// exactly where cenci expects each credential type even when it's missing
// or belongs to a different agent than the one currently selected.
var credentialSourceSpecs = []struct {
	typ      string
	pathFrom func(home string) string
}{
	{CredentialTypeClaude, func(home string) string { return filepath.Join(home, ".claude", ".credentials.json") }},
	{CredentialTypeCodex, func(home string) string { return filepath.Join(home, ".codex", "auth.json") }},
	{CredentialTypeOpencode, func(home string) string {
		return filepath.Join(home, ".local", "share", "opencode", "auth.json")
	}},
	{CredentialTypeGh, func(home string) string { return filepath.Join(home, ".config", "gh", "hosts.yml") }},
	{CredentialTypePencil, func(home string) string { return filepath.Join(home, ".pencil", "session-cli.json") }},
}

// credentialSources reports every credential type's resolved host path and
// whether it is actually staged (a regular file) there — never a secret
// value, and never an error for an absent file (Audit's non-fatal credential
// handling).
func credentialSources(home string) []CredentialSource {
	sources := make([]CredentialSource, 0, len(credentialSourceSpecs))
	for _, spec := range credentialSourceSpecs {
		path := spec.pathFrom(home)
		sources = append(sources, CredentialSource{Type: spec.typ, HostPath: path, Present: isRegularFile(path)})
	}
	return sources
}

// networkPosture classifies assembleOptionalFeatures' emitted args: a
// "--network host" pair means --host-network was requested, weakening the
// default-safe bridge network.
func networkPosture(featureArgs []string) NetworkPosture {
	for i := 0; i < len(featureArgs); i++ {
		if featureArgs[i] == "--network" && i+1 < len(featureArgs) && featureArgs[i+1] == "host" {
			return NetworkPosture{Mode: NetworkModeHost, Weakened: true}
		}
	}
	return NetworkPosture{Mode: NetworkModeBridge, Weakened: false}
}

// boundaryWeakenings reports every active opt-in isolation-weakening flag.
// Today the only one is --host-network (see BoundaryWeakening's doc
// comment) — dind is deliberately never included here (see DindPosture's
// doc comment). Returns a non-nil, zero-length slice (never a slice of
// inactive entries, and never nil) when nothing is active, so the
// default-safe baseline serializes as an explicitly empty JSON list ([])
// rather than null.
func boundaryWeakenings(featureArgs []string) []BoundaryWeakening {
	net := networkPosture(featureArgs)
	if !net.Weakened {
		return []BoundaryWeakening{}
	}
	return []BoundaryWeakening{{
		Option: "--host-network",
		Active: true,
		Effect: "the container joins the host network namespace instead of an isolated bridge network",
	}}
}

// dindSourceForOpts attributes dindOn's decision to its input, mirroring
// ResolveDind's own precedence: an explicit --dind flag wins the "flag"
// attribution over the repo config fallback.
func dindSourceForOpts(opts Options) string {
	if opts.Dind {
		return DindSourceFlag
	}
	return DindSourceConfig
}

// dindPosture classifies dindOn (already resolved by ResolveDind) into its
// own "Nested Docker (sysbox-isolated)" report section — runtime and
// storage-volume attribution mirror exactly what assembleRunArgs would
// append (scope.DindVolumeName, "--runtime=sysbox-runc") for this launch.
func dindPosture(dindOn bool, opts Options, scope Scope) DindPosture {
	if !dindOn {
		return DindPosture{Enabled: false, Source: DindSourceOff}
	}
	return DindPosture{
		Enabled:       true,
		Source:        dindSourceForOpts(opts),
		Runtime:       "sysbox-runc",
		StorageVolume: scope.DindVolumeName,
		Note:          "nested Docker runs under the sysbox-runc OCI runtime, isolated from the host Docker daemon and never touching the host's own container runtime.",
	}
}

// -- WriteText ---------------------------------------------------------

// WriteText renders p as the `cenci audit` human-readable report: every
// Posture field in a fixed, human-scannable order, with a clearly
// demarcated, visually-marked "Boundary weakenings" section and a
// SEPARATE "Nested Docker (sysbox-isolated)" section (dind is never listed
// under "Boundary weakenings" — see DindPosture's doc comment). Like
// Diagnose's report, WriteText never prints a credential/secret VALUE —
// only what Posture itself carries (names, sources, present/absent status).
func (p Posture) WriteText(w io.Writer) error {
	bw := bufio.NewWriter(w)

	_, _ = fmt.Fprintf(bw, "cenci audit: agent=%s scope=%s", p.Agent, p.Scope)
	if p.RepoRoot != "" {
		_, _ = fmt.Fprintf(bw, " repoRoot=%s", p.RepoRoot)
	}
	_, _ = fmt.Fprintln(bw)
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintf(bw, "Image: %s (%s)\n", p.Image.Reference, p.Image.Type)
	_, _ = fmt.Fprintf(bw, "Workspace: %s -> %s\n", p.Workspace.HostPath, p.Workspace.ContainerPath)
	_, _ = fmt.Fprintf(bw, "Network: %s (weakened=%t)\n", p.Network.Mode, p.Network.Weakened)
	_, _ = fmt.Fprintf(bw, "Reseed creds: %t\n", p.ReseedCreds)

	_, _ = fmt.Fprintln(bw, "\nMounts:")
	if len(p.Mounts) == 0 {
		_, _ = fmt.Fprintln(bw, "  (none)")
	}
	for _, m := range p.Mounts {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		_, _ = fmt.Fprintf(bw, "  [%s] %s -> %s (%s)\n", m.Kind, m.Source, m.Destination, mode)
	}

	_, _ = fmt.Fprintln(bw, "\nVolumes:")
	if len(p.Volumes) == 0 {
		_, _ = fmt.Fprintln(bw, "  (none)")
	}
	for _, v := range p.Volumes {
		_, _ = fmt.Fprintf(bw, "  [%s] %s\n", v.Kind, v.Name)
	}

	_, _ = fmt.Fprintln(bw, "\nEnv (create-time, names only — values are never shown):")
	if len(p.Env) == 0 {
		_, _ = fmt.Fprintln(bw, "  (none)")
	}
	for _, e := range p.Env {
		_, _ = fmt.Fprintf(bw, "  %s\n", e.Name)
	}

	_, _ = fmt.Fprintln(bw, "\nForwarded exec env (per-exec, names only — values are never shown):")
	if len(p.ForwardedEnv) == 0 {
		_, _ = fmt.Fprintln(bw, "  (none)")
	}
	for _, e := range p.ForwardedEnv {
		secretMark := ""
		if e.Secret {
			secretMark = " (secret)"
		}
		_, _ = fmt.Fprintf(bw, "  %s%s\n", e.Name, secretMark)
	}

	_, _ = fmt.Fprintln(bw, "\nCredential sources:")
	for _, c := range p.CredentialSources {
		status := "absent"
		if c.Present {
			status = "present"
		}
		_, _ = fmt.Fprintf(bw, "  %s: %s (%s)\n", c.Type, c.HostPath, status)
	}

	_, _ = fmt.Fprintln(bw, "\nNested Docker (sysbox-isolated):")
	_, _ = fmt.Fprintf(bw, "  enabled: %t\n", p.Dind.Enabled)
	_, _ = fmt.Fprintf(bw, "  source: %s\n", p.Dind.Source)
	if p.Dind.Enabled {
		_, _ = fmt.Fprintf(bw, "  runtime: %s\n", p.Dind.Runtime)
		_, _ = fmt.Fprintf(bw, "  storage volume: %s\n", p.Dind.StorageVolume)
		if p.Dind.Note != "" {
			_, _ = fmt.Fprintf(bw, "  note: %s\n", p.Dind.Note)
		}
	}

	if len(p.BoundaryWeakenings) == 0 {
		_, _ = fmt.Fprintln(bw, "\nBoundary weakenings: none (default-safe baseline)")
	} else {
		_, _ = fmt.Fprintln(bw, "\n⚠ Boundary weakenings (opt-in, reduces isolation):")
		for _, bwk := range p.BoundaryWeakenings {
			_, _ = fmt.Fprintf(bw, "  %s active=%t: %s\n", bwk.Option, bwk.Active, bwk.Effect)
		}
	}

	return bw.Flush()
}

// -- cenciWiringReadOnly -----------------------------------------------

// cenciWiringReadOnly is Audit's read-only counterpart to
// resolveCenciWiring (launch.go): it probes whether the cenci bin + host
// socket dir + a live event socket would be wired into a new launch, using
// only resolveHostBinary, ipc.DefaultSocketDir, and isSocket — the same
// building blocks resolveCenciWiring itself uses for everything except the
// daemon-start fallback. It must NEVER call daemon.EnsureRunning(): Audit
// is a read-only report and must not start the daemon as a side effect of
// running it.
func cenciWiringReadOnly() (cenciBin, socketDir string, available bool) {
	cenciBin, err := resolveHostBinary("cenci")
	if err != nil {
		// Not on PATH (dev build): the running executable itself is what
		// resolveCenciWiring would mount in this case.
		self, selfErr := os.Executable()
		if selfErr != nil {
			return "", "", false
		}
		if resolved, evalErr := filepath.EvalSymlinks(self); evalErr == nil {
			self = resolved
		}
		cenciBin = self
	}

	socketDir, dirErr := ipc.DefaultSocketDir()
	if dirErr != nil {
		return cenciBin, "", false
	}

	if !isSocket(ipc.DefaultEventSocketPath()) {
		return cenciBin, socketDir, false
	}
	return cenciBin, socketDir, true
}
