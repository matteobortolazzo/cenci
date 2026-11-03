package incident

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
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
	// config, waiting up to wait for authorization to propagate (the full
	// window when this run created an identity object or a role assignment, or
	// this run or a run in the last 10 minutes attempted a role assignment,
	// else 0), and reports whether every probe passed. The reloaded
	// config carries the pinned principal, so a token whose oid differs fails
	// it as an identity mismatch. Production binds it to verifyIncident, which
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
// role and scope is missing. Before each create it records the attempt time in
// the journal, and the verify step waits for propagation when this run or a run
// in the last 10 minutes attempted one, or this run created any part of a new
// identity. It never deletes anything and never starts the worker.
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
	creating := p.Identity == SetupIdentityNew
	var journal *SetupJournal
	var ident *newIdentity
	if creating {
		// A new identity does not exist yet: only the tenant is re-confirmed, and the
		// journal is read first because it records which application is ours.
		if err := reconfirmTenant(ctx, opts.Az, p); err != nil {
			return err
		}
		if journal, err = openJournal(p, uid, opts.Now()); err != nil {
			return err
		}
		if ident, err = resolveNewIdentity(ctx, opts.Az, &p, journal, opts.Now()); err != nil {
			return err
		}
		if ident.warning != "" {
			_, _ = fmt.Fprintln(opts.Stderr, "warning: "+ident.warning)
		}
	} else if err := reconfirmAzure(ctx, opts.Az, p); err != nil {
		return err
	}
	auth := p.ProposedAuth()
	if !creating {
		_, warning, err := NewCredential(auth, CredentialOptions{Now: opts.Now})
		if err != nil {
			return fmt.Errorf("worker credential: %w", err)
		}
		if warning != "" {
			_, _ = fmt.Fprintln(opts.Stderr, "warning: "+warning)
		}
	}
	if p.Config.Auth != nil && !holdsAuth(p.Config.Auth, auth) && !opts.ReplaceAuth {
		return errors.New("the config already has a different auth section; pass --replace-auth to replace it")
	}
	if err := checkConfigTarget(p.ConfigPath, uid); err != nil {
		return err
	}
	if !creating {
		if journal, err = openJournal(p, uid, opts.Now()); err != nil {
			return err
		}
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
	journal.Complete, journal.Steps = false, journal.Steps[:0]
	if creating {
		// A nonce name this setup already recorded stays. A fresh one is journaled by
		// createIdentity immediately before az ad app create, after the local
		// certificate file step and the Azure lookup, so a failure before then leaves
		// nothing recorded for an application that was never created. A fresh run has
		// no client ID either (p.ClientID is only set from a recorded or adopted app),
		// so the journal never holds a client ID without its name.
		journal.ClientID = p.ClientID
		r.names = append(r.names, identityStepCertFile, identityStepApp, identityStepSP, identityStepCredential)
	} else {
		journal.PrincipalID, journal.ClientID = p.ObjectID, p.ClientID
	}
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

	identityCreated := 0
	if creating {
		if identityCreated, err = r.createIdentity(ctx, opts, &p, ident); err != nil {
			return err
		}
		auth = p.ProposedAuth()
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
		// Stamp the attempt before the call: a create that fails or is cut off
		// still leaves its time, so a rerun knows propagation may be pending.
		at := opts.Now()
		journal.LastRoleCreateAt, journal.UpdatedAt = &at, at
		if err := journal.Write(r.stateDir); err != nil {
			return r.fail(name, fmt.Errorf("write the setup journal: %w", err))
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
	if creating {
		// The client ID is known now, so the worker's own credential loader judges the file.
		_, warning, err := NewCredential(auth, CredentialOptions{Now: opts.Now})
		if err != nil {
			return r.fail(stepConfig, fmt.Errorf("worker credential: %w", err))
		}
		if warning != "" {
			_, _ = fmt.Fprintln(opts.Stderr, "warning: "+warning)
		}
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

	wait := propagationWait(created+identityCreated, journal.LastRoleCreateAt, opts.Now())
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

// propagationWait is how long Verify may wait for authorization to propagate:
// the full window when this run created anything (a role assignment, or for a
// new identity its application, service principal or credential), or when the last create
// attempt (this or an earlier run) is under a window old or lies in the future
// (clock skew fails toward waiting); otherwise none.
func propagationWait(created int, lastCreate *time.Time, now time.Time) time.Duration {
	if created > 0 || (lastCreate != nil && now.Sub(*lastCreate) < verifyPropagationWait) {
		return verifyPropagationWait
	}
	return 0
}

func roleStepName(t RoleTarget) string { return "role assignment: " + t.RoleName + " at " + t.Scope }

// openJournal reads the journal of the state directory and checks that it was
// written for this request, or starts an in-memory one (nothing is written yet).
func openJournal(p SetupPlan, uid int, now time.Time) (*SetupJournal, error) {
	j, err := readSetupJournal(p.Config.StateDir, uid)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return NewSetupJournal(p.Tenant, p.Identity, p.ConfigPath, now), nil
	}
	if err := j.CheckBinding(p.Tenant, p.Identity, p.ConfigPath); err != nil {
		return nil, err
	}
	if p.Identity == SetupIdentityNew {
		if err := j.CheckDisplayName(p.DisplayName); err != nil {
			return nil, err
		}
	}
	return j, nil
}

// The steps that make a new identity, in run order, before the role steps.
const (
	identityStepCertFile   = "certificate file"
	identityStepApp        = "application"
	identityStepSP         = "service principal"
	identityStepCredential = "certificate credential"
)

// newIdentity is what a --identity new apply resolved before its prompt.
type newIdentity struct {
	nonce   string            // the application's display name, <display-name>-<16 hex>
	appID   string            // the recorded client ID, re-read by ID at its step; empty when the application is created or found by name
	adopted string            // the client ID of the unique nonce-name match, shown in the plan; the step finds it by name again
	cert    *x509.Certificate // the leaf of the existing certificate file; nil when apply creates the file
	warning string            // the existing certificate's expiry warning
}

// nonceName appends 16 random hex digits to the display name. The name is
// journaled before az ad app create, so an interrupted creation can be found
// again by exactly this name.
func nonceName(base string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("draw the application name nonce: %w", err)
	}
	name := base + "-" + hex.EncodeToString(b[:])
	if !PrintableBounded(name, maxApplicationNameRunes) {
		return "", fmt.Errorf("--display-name is too long: the application name is the display name, a hyphen and 16 hex digits, at most %d characters", maxApplicationNameRunes)
	}
	return name, nil
}

// resolveNewIdentity decides, before the prompt and without changing anything,
// which application a new-identity apply works on and what the certificate file
// is. The application is the one the journal recorded (by client ID, or by its
// unique nonce name), the one --client-id names when that is the journal's own
// nonce application, or else a new one under a fresh nonce name. It sets the
// plan's client ID, display name (now the nonce name) and certificate presence.
func resolveNewIdentity(ctx context.Context, az *Az, p *SetupPlan, j *SetupJournal, now time.Time) (*newIdentity, error) {
	id := &newIdentity{nonce: j.DisplayName}
	switch {
	case p.ClientID != "":
		if j.DisplayName == "" || (j.ClientID != "" && !strings.EqualFold(j.ClientID, p.ClientID)) {
			return nil, errors.New("--client-id adopts only the application this setup recorded in its journal before creating it, and the journal records no such application for that client ID; nothing was changed")
		}
		id.appID = p.ClientID
	case j.ClientID != "":
		if j.DisplayName == "" {
			return nil, errors.New("the setup journal records a client ID without the application name; nothing was changed")
		}
		id.appID = j.ClientID
	case j.DisplayName != "":
		apps, err := az.AppListByName(ctx, j.DisplayName)
		if err != nil {
			return nil, err
		}
		if len(apps) != 1 {
			return nil, fmt.Errorf("%d applications are named %q, the name this setup recorded before creating the application, so the right one cannot be chosen (a new application may still be replicating in Azure: try again shortly); pass --client-id with its application ID to adopt it. Nothing was changed", len(apps), j.DisplayName)
		}
		id.adopted = apps[0].AppID
	default:
		nonce, err := nonceName(p.DisplayName)
		if err != nil {
			return nil, err
		}
		id.nonce = nonce
	}
	if id.appID != "" {
		if err := requireApplication(ctx, az, id.appID, id.nonce); err != nil {
			return nil, err
		}
	}

	// Anything at the certificate path other than "absent" is checked as the worker
	// will read it and is never written: apply creates the file only when it is absent.
	_, statErr := os.Lstat(p.CertificatePath)
	exists := statErr == nil || !errors.Is(statErr, os.ErrNotExist)
	if exists {
		var err error
		if id.cert, id.warning, err = loadExistingCertificate(p.CertificatePath, now); err != nil {
			return nil, err
		}
	}
	p.CertificateExists, p.ClientID, p.DisplayName = exists, cmp.Or(id.appID, id.adopted), id.nonce
	return id, nil
}

// requireApplication checks that exactly one application has this client ID and
// that its display name is exactly the nonce name.
func requireApplication(ctx context.Context, az *Az, clientID, nonce string) error {
	apps, err := az.AppListByID(ctx, clientID)
	if err != nil {
		return err
	}
	if len(apps) != 1 || apps[0].DisplayName != nonce {
		return fmt.Errorf("client ID %s does not name exactly one application called %q (the name this setup recorded); nothing was changed", clientID, nonce)
	}
	return nil
}

// createIdentity runs the four identity steps: the certificate file (created
// only when absent), the application, its service principal and the certificate
// credential. Each Azure step reads Azure first and creates only what is
// missing. On success the plan holds the client and object IDs; on failure the
// step is recorded and the returned error is final. It returns how many Azure
// objects this run created.
func (r *applyRun) createIdentity(ctx context.Context, opts ApplyOptions, p *SetupPlan, id *newIdentity) (int, error) {
	az, created := opts.Az, 0

	status := StepUnchanged
	if id.cert == nil {
		leaf, err := createCertificateFile(p.CertificatePath, opts.Now())
		if err != nil {
			return created, r.fail(identityStepCertFile, err)
		}
		id.cert, status = leaf, StepDone
	} else {
		// The prompt may have been open for a while: the file must still hold the
		// certificate the plan was judged on, or Azure would get a stale thumbprint.
		leaf, _, err := loadExistingCertificate(p.CertificatePath, opts.Now())
		if err != nil {
			return created, r.fail(identityStepCertFile, err)
		}
		if !bytes.Equal(leaf.Raw, id.cert.Raw) {
			return created, r.fail(identityStepCertFile, fmt.Errorf("certificate %s changed after the plan was shown; nothing was changed in Azure", p.CertificatePath))
		}
	}
	if err := r.record(identityStepCertFile, status); err != nil {
		return created, r.fail(identityStepCertFile, err)
	}

	var apps []Application
	var err error
	if id.appID != "" {
		apps, err = az.AppListByID(ctx, id.appID)
	} else {
		apps, err = az.AppListByName(ctx, id.nonce)
	}
	if err != nil {
		return created, r.fail(identityStepApp, err)
	}
	appID, status := id.appID, StepUnchanged
	switch {
	case len(apps) == 1 && apps[0].DisplayName == id.nonce:
		if id.adopted != "" && !strings.EqualFold(apps[0].AppID, id.adopted) {
			return created, r.fail(identityStepApp, fmt.Errorf("the plan adopted application %s but %s now carries the name %q; nothing was changed", id.adopted, apps[0].AppID, id.nonce))
		}
		appID = apps[0].AppID
	case len(apps) == 0 && appID == "" && id.adopted != "":
		return created, r.fail(identityStepApp, fmt.Errorf("the plan adopted application %s but no application carries the name %q any more; nothing was changed", id.adopted, id.nonce))
	case len(apps) == 0 && appID == "":
		// The nonce name is journaled only now, after the local certificate file step
		// and the Azure lookup succeeded and immediately before az ad app create, so a
		// failure before this point leaves no name for a rerun to look for. A name
		// recorded by an earlier run stays.
		if r.j.DisplayName == "" {
			r.j.DisplayName = id.nonce
			if err := r.j.Write(r.stateDir); err != nil {
				r.j.DisplayName = ""
				return created, r.fail(identityStepApp, fmt.Errorf("write the setup journal: %w", err))
			}
		}
		app, err := az.AppCreate(ctx, id.nonce)
		if err != nil {
			return created, r.fail(identityStepApp, err)
		}
		appID, status = app.AppID, StepDone
		created++
	default:
		return created, r.fail(identityStepApp, fmt.Errorf("%d applications match the recorded client ID or the name %q, not exactly one", len(apps), id.nonce))
	}
	r.j.ClientID = appID
	if err := r.record(identityStepApp, status); err != nil {
		return created, r.fail(identityStepApp, err)
	}

	sps, err := az.SPListByApp(ctx, appID)
	if err != nil {
		return created, r.fail(identityStepSP, err)
	}
	var sp ServicePrincipal
	status = StepUnchanged
	if len(sps) == 1 {
		sp = sps[0]
	} else {
		if sp, err = az.SPCreate(ctx, appID); err != nil {
			return created, r.fail(identityStepSP, err)
		}
		status = StepDone
		created++
	}
	r.j.PrincipalID = sp.ObjectID
	if err := r.record(identityStepSP, status); err != nil {
		return created, r.fail(identityStepSP, err)
	}

	identifiers, err := az.CertCredentials(ctx, appID)
	if err != nil {
		return created, r.fail(identityStepCredential, err)
	}
	registered, err := certificateRegistered(identifiers, id.cert)
	if err != nil {
		return created, r.fail(identityStepCredential, err)
	}
	status = StepUnchanged
	if !registered {
		if err := withPublicCertificate(id.cert, func(path string) error { return az.CertCredentialAppend(ctx, appID, path) }); err != nil {
			return created, r.fail(identityStepCredential, err)
		}
		status = StepDone
		created++
	}
	if err := r.record(identityStepCredential, status); err != nil {
		return created, r.fail(identityStepCredential, err)
	}

	p.ClientID, p.ObjectID = appID, sp.ObjectID
	return created, nil
}

// reconfirmTenant checks that the signed-in tenant is the requested one.
func reconfirmTenant(ctx context.Context, az *Az, p SetupPlan) error {
	acct, err := az.AccountShow(ctx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(acct.Tenant, p.Tenant) {
		return fmt.Errorf("tenant mismatch: --tenant is %s but the Azure CLI is signed in to tenant %s; nothing was changed", p.Tenant, acct.Tenant)
	}
	return nil
}

// reconfirmAzure repeats the read-only checks that discovery made, immediately
// before the plan is shown: the signed-in tenant is the requested one and the
// principal is the requested identity.
func reconfirmAzure(ctx context.Context, az *Az, p SetupPlan) error {
	if err := reconfirmTenant(ctx, az, p); err != nil {
		return err
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
