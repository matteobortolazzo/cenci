package launcher

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// This file implements `cenci open --dry-run` (ticket #589, corrected by
// #620): a faithful, read-only preview of the exact launch branch
// Engine.Launch would take for a given Options — attach-only, create-then-
// attach, or the incompatible-container hard error — plus the full
// `cenci audit` Posture breakdown.
//
// DryRun mirrors Launch's own failure modes (agent validation, ComputeScope,
// ResolveDind, container-runtime resolution, dind preflight, container
// disposition, credential validation) rather than printing a best-effort
// argv, and its branch decision and argvs are built via the exact same
// resolveLaunchContext/planArgvs/buildRunArgv/buildAgentExecArgv helpers
// Launch/runAgent call — never a parallel/duplicate command-building path.
// Like Audit, DryRun never launches, creates a container/volume, or starts
// the daemon (cenciWiringReadOnly, not resolveCenciWiring); its
// containerRunning/containerHasSharedAgentMount disposition probes (via
// planArgvs) are reads (`ps`/`inspect`), never mutations.

// DryRunPlan is the faithful, read-only representation of what a real launch
// would run for a given Options: the branch a real launch would take
// (Mode: "create" or "attach"), the scoped container name, the create/attach
// argv (each after the runtime binary — Runtime carries the binary name
// itself; CreateArgv is nil in the attach branch), whether the create
// branch's cenci-wiring outcome could not be determined read-only
// (CenciWiringUnknown), and the full audit Posture breakdown.
type DryRunPlan struct {
	Runtime            string
	Mode               string
	ContainerName      string
	CreateArgv         []string
	AttachArgv         []string
	CenciWiringUnknown bool
	Posture            Posture
}

// DryRun performs the same non-side-effecting steps Launch performs, in the
// same order, and surfaces the same errors/exit-code classes (see
// launch.go's UsageError/IsUsage): agent validation, ComputeScope,
// ResolveDind (usage error), container-runtime resolution (docker-first
// under dind, else podman-first — mirrors Launch), dindPreflight when dind is
// on, the read-only container-disposition probe (containerRunning +
// containerHasSharedAgentMount, via the shared planArgvs), the same
// incompatible-running-container hard error Launch returns, and credential
// validation on the create branch (via buildRunArgv -> assembleRunArgs ->
// validateCredentials, a hard non-usage error). It skips every
// side-effecting step Launch performs (EnsureImage, EnsureAgentVolume,
// rm/run, waitUntilReady, runAgent/execAttach) and uses the read-only
// cenciWiringReadOnly (never resolveCenciWiring, which starts the daemon),
// exactly as Audit does.
//
// When the create branch's cenci-wiring outcome is genuinely indeterminate
// read-only (the events socket is merely missing, not unresolvable — a real
// launch's daemon.EnsureRunning() might bring it up or might not),
// CenciWiringUnknown is set and the create argv omits the wiring mounts
// (consistent with the read-only probe's determinate-false posture; #620
// Q1). The attach branch never sets it: an already-running container's
// mounts are fixed for its lifetime and are not re-derived read-only (no
// read-only equivalent of warnIfUnwired here — see the plan's Assumption).
//
// The Posture breakdown is composed by calling Audit on a shallow engine
// copy with Stderr discarded: DryRun's own create-argv build already prints
// the --host-network isolation warning once (on the real engine, e), so
// calling Audit on a Stderr-discarding clone avoids printing it a second
// time.
func (e *Engine) DryRun(opts Options) (DryRunPlan, error) {
	ctx, err := e.resolveLaunchContext(opts)
	if err != nil {
		return DryRunPlan{}, err
	}

	cenciBin, socketDir, cenciAvailable := cenciWiringReadOnly()

	createArgv, attachArgv, attaching, err := e.planArgvs(ctx, opts, cenciBin, socketDir, cenciAvailable)
	if err != nil {
		return DryRunPlan{}, err
	}

	mode := "create"
	if attaching {
		mode = "attach"
	}

	// CenciWiringUnknown only applies to the create branch: socketDir != ""
	// with cenciAvailable == false is cenciWiringReadOnly's signal that the
	// events socket is merely missing (not unresolvable), so a real launch's
	// daemon.EnsureRunning() outcome can't be determined read-only.
	cenciWiringUnknown := !attaching && cenciBin != "" && socketDir != "" && !cenciAvailable

	// Avoid printing the --host-network isolation warning twice: the create
	// argv build above already printed it once (to e.Stderr, the real
	// engine); the Audit call below reuses the same assembleOptionalFeatures
	// method, so it runs against a Stderr-discarding clone instead.
	//
	// The clone's Runtime is deliberately cleared (not copied from e) so
	// Audit's observed-mode dispatch (ticket #627, gated on e.Runtime != "")
	// never fires here: planArgvs above already performed the one
	// authoritative containerRunning/inspect disposition probe DryRun needs
	// (attaching reflects it), so calling observed Audit here would issue a
	// second, redundant containerRunning probe purely for the Posture
	// breakdown. DryRun's Posture stays the planned preview it always was —
	// consistent with "what the launcher WOULD apply", not a second
	// independent observation of the same running container.
	clone := *e
	clone.Stderr = io.Discard
	clone.Runtime = ""
	posture, err := clone.Audit(opts)
	if err != nil {
		return DryRunPlan{}, err
	}

	return DryRunPlan{
		Runtime:            e.Runtime,
		Mode:               mode,
		ContainerName:      ctx.Scope.ContainerName,
		CreateArgv:         createArgv,
		AttachArgv:         attachArgv,
		CenciWiringUnknown: cenciWiringUnknown,
		Posture:            posture,
	}, nil
}

// WriteText renders p as `cenci open --dry-run`'s human-readable report: an
// honest capabilities line (the launcher applies no --cap-add/--cap-drop
// today, so there is no capabilities argv content or Posture field to
// invent), the branch a real launch would take (attach-only vs create-then-
// attach, each redacted via renderArgv), an explicit indeterminacy caveat on
// the create branch when CenciWiringUnknown, and the full cenci audit
// Posture body verbatim (Posture.WriteText) — never a trimmed summary, so
// the breakdown can never drift from `cenci audit`.
func (p DryRunPlan) WriteText(w io.Writer) error {
	bw := bufio.NewWriter(w)

	_, _ = fmt.Fprintln(bw, "cenci open --dry-run: the launch branch a real launch would take, printed without executing it")
	_, _ = fmt.Fprintln(bw)
	_, _ = fmt.Fprintln(bw, "Capabilities: runtime defaults (launcher applies no --cap-add/--cap-drop)")
	_, _ = fmt.Fprintln(bw)

	if p.Mode == "attach" {
		_, _ = fmt.Fprintf(bw, "Container '%s' is already running and compatible — attaching, no create.\n\n", p.ContainerName)
	} else {
		_, _ = fmt.Fprintln(bw, "Container create (detached):")
		_, _ = fmt.Fprintf(bw, "  %s %s\n\n", p.Runtime, renderArgv(p.CreateArgv))
		if p.CenciWiringUnknown {
			_, _ = fmt.Fprintln(bw, "Note: the cenci events daemon is not yet running, so whether this launch would include cenci wiring mounts could not be determined read-only; a real launch may start the daemon and include them.")
			_, _ = fmt.Fprintln(bw)
		}
	}

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
// structurally visible. Since ticket #759, secret env forwards arrive as a
// bare "-e NAME" (value-less) token in the first place, so redactSecretEnv
// no-ops on them and they pass through unchanged; the "NAME=value" redaction
// path is retained as a regression guard in case a value-bearing form is
// ever reintroduced. Every other token, including non-secret env and host
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
// (including non-secret "NAME=value" env) passes through unchanged. Since
// ticket #759, secret env forwards arrive as a bare "NAME" (no "=") token,
// which has no index for '=' below and so passes through unchanged here too
// — this function only fires if a value-bearing form is ever reintroduced.
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
