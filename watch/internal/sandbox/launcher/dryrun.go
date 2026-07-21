package launcher

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// This file implements `cenci open --dry-run` (ticket #589): a faithful,
// read-only preview of the exact create/attach argv Engine.Launch would run
// for a given Options, plus the full `cenci audit` Posture breakdown.
//
// DryRun mirrors Launch's own failure modes (agent validation, ComputeScope,
// ResolveDind, container-runtime resolution, dind preflight, credential
// validation) rather than printing a best-effort argv, and its create/attach
// argv is built via the exact same buildRunArgv/buildAgentExecArgv helpers
// Launch/runAgent call — never a parallel/duplicate command-building path.
// Like Audit, DryRun never launches, attaches, creates a container/volume, or
// starts the daemon (cenciWiringReadOnly, not resolveCenciWiring).

// DryRunPlan is the faithful, read-only representation of what a real launch
// would run for a given Options: the exact create/attach argv (each after the
// runtime binary — Runtime carries the binary name itself) plus the full
// audit Posture breakdown.
type DryRunPlan struct {
	Runtime    string
	CreateArgv []string
	AttachArgv []string
	Posture    Posture
}

// DryRun performs the same non-side-effecting steps Launch performs, in the
// same order, and surfaces the same errors/exit-code classes (see
// launch.go's UsageError/IsUsage): agent validation, ComputeScope,
// ResolveDind (usage error), container-runtime resolution (docker-first
// under dind, else podman-first — mirrors Launch), dindPreflight when dind is
// on, and credential validation (via buildRunArgv -> assembleRunArgs ->
// validateCredentials, a hard non-usage error). It skips every
// side-effecting step Launch performs (EnsureImage, EnsureAgentVolume,
// containerRunning/rm/run, waitUntilReady, runAgent/execAttach) and uses the
// read-only cenciWiringReadOnly (never resolveCenciWiring, which starts the
// daemon), exactly as Audit does.
//
// The Posture breakdown is composed by calling Audit on a shallow engine
// copy with Stderr discarded: DryRun's own create-argv build already prints
// the --host-network isolation warning once (on the real engine, e), so
// calling Audit on a Stderr-discarding clone avoids printing it a second
// time.
func (e *Engine) DryRun(opts Options) (DryRunPlan, error) {
	agent := opts.Agent
	if agent == "" {
		agent = "claude"
	}
	if err := ValidateAgent(agent); err != nil {
		return DryRunPlan{}, err
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel(agent)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return DryRunPlan{}, fmt.Errorf("cannot determine working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return DryRunPlan{}, fmt.Errorf("cannot determine home directory: %w", err)
	}
	scope := ComputeScope(agent, opts.Name, cwd, home)

	dindOn, err := ResolveDind(opts, scope)
	if err != nil {
		return DryRunPlan{}, err
	}
	if e.Runtime == "" {
		if dindOn {
			e.Runtime, err = sandbox.ContainerRuntimePreferDocker()
		} else {
			e.Runtime, err = sandbox.ContainerRuntime()
		}
		if err != nil {
			return DryRunPlan{}, err
		}
	}
	if dindOn {
		if err := e.dindPreflight(); err != nil {
			return DryRunPlan{}, err
		}
	}

	cenciBin, socketDir, cenciAvailable := cenciWiringReadOnly()

	createArgv, err := e.buildRunArgv(agent, cenciBin, socketDir, cenciAvailable, scope, opts, home, dindOn)
	if err != nil {
		return DryRunPlan{}, err
	}

	agentCmdArgs := buildAgentCmdArgs(agent, model)
	execEnvArgs := assembleExecEnv(agent)
	attachArgv := buildAgentExecArgv(scope.ContainerName, agent, agentCmdArgs, execEnvArgs, opts)

	// Avoid printing the --host-network isolation warning twice: the create
	// argv build above already printed it once (to e.Stderr, the real
	// engine); the Audit call below reuses the same assembleOptionalFeatures
	// method, so it runs against a Stderr-discarding clone instead.
	clone := *e
	clone.Stderr = io.Discard
	posture, err := clone.Audit(opts)
	if err != nil {
		return DryRunPlan{}, err
	}

	return DryRunPlan{
		Runtime:    e.Runtime,
		CreateArgv: createArgv,
		AttachArgv: attachArgv,
		Posture:    posture,
	}, nil
}

// WriteText renders p as `cenci open --dry-run`'s human-readable report: an
// honest capabilities line (the launcher applies no --cap-add/--cap-drop
// today, so there is no capabilities argv content or Posture field to
// invent), both labeled argvs (redacted via renderArgv), and the full
// cenci audit Posture body verbatim (Posture.WriteText) — never a trimmed
// summary, so the breakdown can never drift from `cenci audit`.
func (p DryRunPlan) WriteText(w io.Writer) error {
	bw := bufio.NewWriter(w)

	_, _ = fmt.Fprintln(bw, "cenci open --dry-run: the exact launch commands, printed without executing them")
	_, _ = fmt.Fprintln(bw)
	_, _ = fmt.Fprintln(bw, "Capabilities: runtime defaults (launcher applies no --cap-add/--cap-drop)")
	_, _ = fmt.Fprintln(bw)

	_, _ = fmt.Fprintln(bw, "Container create (detached):")
	_, _ = fmt.Fprintf(bw, "  %s %s\n\n", p.Runtime, renderArgv(p.CreateArgv))

	_, _ = fmt.Fprintln(bw, "Agent attach (exec):")
	_, _ = fmt.Fprintf(bw, "  %s %s\n\n", p.Runtime, renderArgv(p.AttachArgv))

	if err := bw.Flush(); err != nil {
		return err
	}
	return p.Posture.WriteText(w)
}

// renderArgv shell-quotes every token of argv and redacts any "-e NAME=value"
// token whose NAME is a forwarded secret (forwardedEnvVarNames, the single
// source of truth audit.go already uses for secret classification) to
// "-e NAME=<redacted>" — masking the value while keeping the token
// structurally visible. Every other token, including non-secret env and host
// paths, renders literally (consistent with audit/diagnose's own
// value-free-but-path-visible discipline).
func renderArgv(argv []string) string {
	rendered := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if tok == "-e" && i+1 < len(argv) {
			rendered = append(rendered, shellQuote(tok))
			i++
			rendered = append(rendered, shellQuote(redactSecretEnv(argv[i])))
			continue
		}
		rendered = append(rendered, shellQuote(tok))
	}
	return strings.Join(rendered, " ")
}

// redactSecretEnv masks the value of a "NAME=value" token to
// "NAME=<redacted>" when NAME is in forwardedEnvVarNames; every other token
// (including non-secret "NAME=value" env) passes through unchanged.
func redactSecretEnv(tok string) string {
	idx := strings.IndexByte(tok, '=')
	if idx < 0 {
		return tok
	}
	name := tok[:idx]
	if !forwardedEnvVarNames[name] {
		return tok
	}
	return name + "=<redacted>"
}

// shellQuote single-quotes s when it contains anything outside a conservative
// safe set, escaping embedded single quotes. Safe words pass through
// unquoted. Mirrors internal/run's own shellQuote convention for rendering an
// argv as a copy-pasteable shell command line.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !isShellSafeRune(r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// isShellSafeRune reports whether r may appear unquoted in a POSIX shell
// word.
func isShellSafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("-_./:=@%+", r)
	}
}
