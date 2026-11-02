package incident

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	stepConfig            = "config auth"
	stepVerify            = "verify"
	verifyPropagationWait = 10 * time.Minute
)

// ErrApplyDeclined means the operator did not confirm the plan. Nothing was
// created or written, not even the journal.
var ErrApplyDeclined = errors.New("apply declined: nothing was changed")

var errVerifyFailed = errors.New("a verify probe did not pass")

// ApplyOptions are the seams of Apply. Az must be NewApplyAz (or an Az with the
// apply allowlist), Confirm and Verify are required.
type ApplyOptions struct {
	Az          *Az
	ReplaceAuth bool
	// Confirm asks the operator after the plan was written to Stdout. Only true
	// continues; it is the only gate before the first write.
	Confirm func() bool
	// Verify runs the checks of `cenci incident verify` against the reloaded
	// config, waiting up to wait for authorization to propagate, and reports
	// whether every probe passed. Production binds it to verifyIncident, which
	// removes the ambient AZURE_* variables from the process: no az call may
	// follow it, so Apply invokes it as its last step.
	Verify         func(ctx context.Context, cfg Config, wait time.Duration) bool
	Now            func() time.Time
	Stdout, Stderr io.Writer
}

// Apply carries out a plan that has no blockers: it re-confirms the tenant and
// the principal, then creates exactly the missing role assignments, writes only
// the config's auth section and verifies. Every check and the prompt come before
// the first write. Azure is the source of truth on a rerun: each role step lists
// the assignments at its scope and creates one only when that exact principal,
// role and scope is missing. It never deletes anything and never starts the
// worker.
func Apply(ctx context.Context, p SetupPlan, opts ApplyOptions) error {
	return apply(ctx, p, opts, os.Getuid())
}

// apply is Apply with the invoking uid as a parameter, so tests can present
// files as owned by another user.
func apply(ctx context.Context, p SetupPlan, opts ApplyOptions, uid int) error {
	// Blockers first: they include a missing az, which callers pass as a nil Az.
	if blockers := p.ApplyBlockers(); len(blockers) > 0 {
		return fmt.Errorf("setup cannot apply yet:\n  - %s", strings.Join(blockers, "\n  - "))
	}
	if opts.Az == nil || opts.Confirm == nil || opts.Verify == nil {
		return errors.New("apply needs an Az, a Confirm and a Verify")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if !filepath.IsAbs(p.ConfigPath) || !filepath.IsAbs(p.Config.StateDir) {
		return errors.New("the config path and the state directory must be absolute")
	}
	targets, err := p.RoleTargets()
	if err != nil {
		return err
	}
	if err := reconfirmAzure(ctx, opts.Az, p); err != nil {
		return err
	}
	auth := p.ProposedAuth()
	_, warning, err := NewCredential(auth, CredentialOptions{Now: opts.Now})
	if err != nil {
		return fmt.Errorf("worker credential: %w", err)
	}
	if warning != "" {
		_, _ = fmt.Fprintln(opts.Stderr, "warning: "+warning)
	}
	if p.Config.Auth != nil && !holdsAuth(p.Config.Auth, auth) && !opts.ReplaceAuth {
		return errors.New("the config already has a different auth section; pass --replace-auth to replace it")
	}
	if err := checkConfigTarget(p.ConfigPath, uid); err != nil {
		return err
	}
	journal, err := readSetupJournal(p.Config.StateDir, uid)
	if err != nil {
		return err
	}
	if journal == nil {
		journal = NewSetupJournal(p.Tenant, p.Identity, p.ConfigPath, opts.Now())
	} else if err := journal.CheckBinding(p.Tenant, p.Identity, p.ConfigPath); err != nil {
		return err
	}

	plan, err := p.RenderApply(opts.ReplaceAuth)
	if err != nil {
		return err
	}
	// Confirm is the only authorization gate: never ask about a plan that was not shown.
	if _, err := fmt.Fprint(opts.Stdout, plan); err != nil {
		return fmt.Errorf("show the plan: %w", err)
	}
	if !opts.Confirm() {
		return ErrApplyDeclined
	}

	r := &applyRun{j: journal, stateDir: p.Config.StateDir, now: opts.Now, stderr: opts.Stderr, outcome: map[string]string{}}
	journal.PrincipalID, journal.ClientID, journal.Complete, journal.Steps = p.ObjectID, p.ClientID, false, journal.Steps[:0]
	for _, t := range targets {
		r.names = append(r.names, roleStepName(t))
	}
	r.names = append(r.names, stepConfig, stepVerify)
	for _, n := range r.names {
		journal.SetStep(n, StepPending, "", opts.Now())
	}
	if err := journal.Write(r.stateDir); err != nil {
		return fmt.Errorf("write the setup journal: %w", err)
	}

	created := 0
	for _, t := range targets {
		name := roleStepName(t)
		have, err := opts.Az.RoleAssignmentsAt(ctx, p.ObjectID, t.Scope)
		if err != nil {
			return r.fail(name, err)
		}
		if slices.ContainsFunc(have, func(a ScopedAssignment) bool { return a.Matches(p.ObjectID, t.RoleGUID, t.Scope) }) {
			if err := r.record(name, StepUnchanged); err != nil {
				return r.fail(name, err)
			}
			continue
		}
		if err := opts.Az.RoleAssignmentCreate(ctx, p.ObjectID, t.RoleGUID, t.Scope); err != nil {
			return r.fail(name, err)
		}
		created++
		if err := r.record(name, StepDone); err != nil {
			return r.fail(name, err)
		}
	}

	if err := ctx.Err(); err != nil {
		return r.fail(stepConfig, err)
	}
	status := StepUnchanged
	if !holdsAuth(p.Config.Auth, auth) {
		if err := WriteAuth(p.ConfigPath, auth); err != nil {
			return r.fail(stepConfig, err)
		}
		status = StepDone
	}
	cfg, err := LoadSetupConfig(p.ConfigPath)
	if err == nil && !holdsAuth(cfg.Auth, auth) {
		err = errors.New("the config does not hold the proposed auth section")
	}
	if err != nil {
		return r.fail(stepConfig, err)
	}
	if err := r.record(stepConfig, status); err != nil {
		return r.fail(stepConfig, err)
	}

	wait := time.Duration(0)
	if created > 0 {
		wait = verifyPropagationWait
	}
	// Verify is the last step: it unsets the ambient AZURE_* variables in this
	// process, so no az call may follow it.
	if !opts.Verify(ctx, cfg, wait) {
		if err := ctx.Err(); err != nil {
			return r.fail(stepVerify, err)
		}
		return r.fail(stepVerify, errVerifyFailed)
	}
	journal.Complete = true
	if err := r.record(stepVerify, StepDone); err != nil {
		return r.fail(stepVerify, err)
	}
	_, _ = fmt.Fprintf(opts.Stdout, "cenci incident setup: apply complete (%d role assignment(s) created).\n", created)
	return nil
}

func roleStepName(t RoleTarget) string { return "role assignment: " + t.RoleName + " at " + t.Scope }

// reconfirmAzure repeats the read-only checks that discovery made, immediately
// before the plan is shown: the signed-in tenant is the requested one and the
// principal is the requested identity.
func reconfirmAzure(ctx context.Context, az *Az, p SetupPlan) error {
	acct, err := az.AccountShow(ctx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(acct.Tenant, p.Tenant) {
		return fmt.Errorf("tenant mismatch: --tenant is %s but the Azure CLI is signed in to tenant %s; nothing was changed", p.Tenant, acct.Tenant)
	}
	key := p.ClientID
	if key == "" {
		key = p.ObjectID
	}
	sp, err := az.SPShow(ctx, key)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sp.ObjectID, p.ObjectID) || (p.ClientID != "" && !strings.EqualFold(sp.AppID, p.ClientID)) {
		return errors.New("the principal az reports is not the requested identity; nothing was changed")
	}
	return nil
}

// checkConfigTarget refuses, before anything is shown or written, a config path
// that is a symbolic link, is not a regular file or is not owned by uid.
func checkConfigTarget(path string, uid int) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config %s is a symbolic link; refusing to write it", path)
	}
	if why := notOwnedRegular(fi, uid); why != "" {
		return fmt.Errorf("config %s %s; refusing to write it", path, why)
	}
	return nil
}

// holdsAuth reports whether have is a present auth section equal to want.
func holdsAuth(have *AuthConfig, want AuthConfig) bool { return have != nil && *have == want }

// applyRun tracks the steps of one run so a failure can report what finished.
type applyRun struct {
	j        *SetupJournal
	stateDir string
	now      func() time.Time
	stderr   io.Writer
	names    []string
	outcome  map[string]string // step name to its status or failure class
}

func (r *applyRun) record(name, status string) error {
	r.outcome[name] = status
	r.j.SetStep(name, status, "", r.now())
	return r.j.Write(r.stateDir)
}

// fail records the step as failed in the journal, prints the completed, failed
// and remaining steps (names and failure classes only, never az output) and
// returns the error.
func (r *applyRun) fail(name string, err error) error {
	class := "failed"
	var azErr *AzError
	switch {
	case errors.As(err, &azErr):
		class = azErr.Reason
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		class = "interrupted"
	case errors.Is(err, errVerifyFailed):
		class = "a probe did not pass"
	}
	r.outcome[name] = StepFailed
	r.j.SetStep(name, StepFailed, class, r.now())
	r.j.Complete = false
	if werr := r.j.Write(r.stateDir); werr != nil {
		err = errors.Join(err, fmt.Errorf("write the setup journal: %w", werr))
	}
	var done, remaining []string
	for _, n := range r.names {
		switch r.outcome[n] {
		case StepDone, StepUnchanged:
			done = append(done, n)
		case StepFailed:
		default:
			remaining = append(remaining, n)
		}
	}
	list := func(s []string) string {
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, "; ")
	}
	_, _ = fmt.Fprintf(r.stderr, "cenci incident setup: apply stopped; run the same command again to resume.\ncompleted: %s\nfailed: %s (%s)\nremaining: %s\n", list(done), name, class, list(remaining))
	return fmt.Errorf("setup stopped at step %q: %w", name, err)
}
