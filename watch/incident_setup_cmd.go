package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/incident"
)

const (
	defaultWorkerDisplayName = "cenci-incident-worker"
	maxDisplayNameRunes      = 120
	maxPathRunes             = 4096
)

// setupFlags are the raw `cenci incident setup` flags; set records which were
// given, so an explicit empty value is not mistaken for an absent one.
type setupFlags struct {
	config, tenant, subscription, identity, clientID, principalObjectID string
	displayName, certificatePath, namespaceID                           string
	workspaces                                                          []string
	set                                                                 map[string]bool
}

// setupRequest is setupFlags after validation.
type setupRequest struct {
	setupFlags
	namespace  *incident.ARMID
	workspaces map[string]incident.ARMID // by lower-case configured workspace UUID
}

// runIncidentSetup is the read-only `cenci incident setup` preview: it
// validates its input, discovers values with read-only az calls when az is
// usable, and prints the steps. It never writes a file or changes Azure.
// Exit 2 is a usage error, 1 a failed step or unusable config, 0 a rendered
// preview, including a guidance-only one.
func runIncidentSetup(args []string) {
	fs := flag.NewFlagSet("incident setup", flag.ExitOnError)
	var f setupFlags
	fs.StringVar(&f.config, "config", "", "path to the incident worker JSON config (required)")
	fs.StringVar(&f.tenant, "tenant", "", "Entra tenant UUID the worker authenticates in (default: the az login tenant)")
	fs.StringVar(&f.subscription, "subscription", "", "subscription UUID to search for the namespace and workspaces (default: the az default)")
	fs.StringVar(&f.identity, "identity", incident.SetupIdentityNew, "worker identity: new, existing or managed")
	fs.StringVar(&f.clientID, "client-id", "", "client ID of the existing identity (required for --identity existing; optional for managed)")
	fs.StringVar(&f.principalObjectID, "principal-object-id", "", "object ID of the managed identity's principal")
	fs.StringVar(&f.displayName, "display-name", "", "display name for a new identity (default "+defaultWorkerDisplayName+")")
	fs.StringVar(&f.certificatePath, "certificate-path", "", "absolute path of the worker certificate and key PEM file")
	fs.StringVar(&f.namespaceID, "service-bus-namespace-id", "", "resource ID of the Service Bus namespace, when it cannot be discovered")
	fs.Func("workspace-resource-id", "<workspace-uuid>=<workspace-resource-id>, for a configured workspace that cannot be discovered (repeatable)", func(v string) error {
		f.workspaces = append(f.workspaces, v)
		return nil
	})
	_ = fs.Parse(args)
	rejectExtra("cenci incident setup", fs.Args())
	f.set = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })

	usage := func(err error) {
		fmt.Fprintf(os.Stderr, "cenci incident setup: %v (see cenci incident setup -h)\n", err)
		os.Exit(2)
	}
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "cenci incident setup: %v\n", err)
		os.Exit(1)
	}
	req, err := f.validate()
	if err != nil {
		usage(err)
	}
	cfg, err := incident.LoadSetupConfig(req.config)
	if err != nil {
		fail(err)
	}
	if err := req.crossCheck(cfg); err != nil {
		usage(err)
	}
	plan, err := req.basePlan(cfg)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = discoverSetup(ctx, cfg, &plan)
	stop()
	if err != nil {
		fail(fmt.Errorf("discovery failed: %w", err))
	}
	out, err := plan.Render()
	if err != nil {
		fail(err)
	}
	if _, err := io.WriteString(os.Stdout, out); err != nil {
		fail(fmt.Errorf("writing the preview: %w", err))
	}
}

func (f setupFlags) validate() (setupRequest, error) {
	req := setupRequest{setupFlags: f, workspaces: map[string]incident.ARMID{}}
	if f.config == "" {
		return req, fmt.Errorf("--config is required")
	}
	if !incident.PrintableBounded(f.config, maxPathRunes) {
		return req, fmt.Errorf("--config must be a printable path")
	}
	switch f.identity {
	case incident.SetupIdentityNew, incident.SetupIdentityExisting, incident.SetupIdentityManaged:
	default:
		return req, fmt.Errorf("--identity must be new, existing or managed")
	}
	for _, u := range []struct{ name, value string }{
		{"tenant", f.tenant}, {"subscription", f.subscription}, {"client-id", f.clientID}, {"principal-object-id", f.principalObjectID},
	} {
		if f.set[u.name] && !incident.IsUUID(u.value) {
			return req, fmt.Errorf("--%s must be a UUID", u.name)
		}
	}
	for _, c := range []struct {
		identity string
		flags    []string
	}{
		{incident.SetupIdentityNew, []string{"client-id", "principal-object-id"}},
		{incident.SetupIdentityExisting, []string{"principal-object-id", "display-name"}},
		{incident.SetupIdentityManaged, []string{"display-name", "certificate-path"}},
	} {
		for _, name := range c.flags {
			if f.identity == c.identity && f.set[name] {
				return req, fmt.Errorf("--%s cannot be used with --identity %s", name, f.identity)
			}
		}
	}
	if f.identity == incident.SetupIdentityExisting && !f.set["client-id"] {
		return req, fmt.Errorf("--identity existing requires --client-id")
	}
	if f.set["display-name"] && !incident.PrintableBounded(f.displayName, maxDisplayNameRunes) {
		return req, fmt.Errorf("--display-name must be printable text of at most %d characters", maxDisplayNameRunes)
	}
	if f.set["certificate-path"] && (!filepath.IsAbs(f.certificatePath) || !incident.PrintableBounded(f.certificatePath, maxPathRunes)) {
		return req, fmt.Errorf("--certificate-path must be an absolute printable path")
	}
	if f.set["service-bus-namespace-id"] {
		ns, err := incident.ParseNamespaceID(f.namespaceID)
		if err != nil {
			return req, fmt.Errorf("--service-bus-namespace-id: %w", err)
		}
		req.namespace = &ns
	}
	for _, v := range f.workspaces {
		uuid, id, ok := strings.Cut(v, "=")
		if !ok || !incident.IsUUID(uuid) {
			return req, fmt.Errorf("--workspace-resource-id must be <workspace-uuid>=<workspace-resource-id>")
		}
		ws, err := incident.ParseWorkspaceID(id)
		if err != nil {
			return req, fmt.Errorf("--workspace-resource-id %s: %w", uuid, err)
		}
		if _, dup := req.workspaces[strings.ToLower(uuid)]; dup {
			return req, fmt.Errorf("--workspace-resource-id %s is given twice", uuid)
		}
		req.workspaces[strings.ToLower(uuid)] = ws
	}
	return req, nil
}

// crossCheck compares the validated flags with the loaded config, before any
// az call: the namespace ID must name the configured namespace and every
// workspace UUID must be one the config queries.
func (r setupRequest) crossCheck(cfg incident.Config) error {
	if r.namespace != nil && !strings.EqualFold(r.namespace.Name, namespaceLabel(cfg)) {
		return fmt.Errorf("--service-bus-namespace-id names a different namespace than the configured one")
	}
	configured := map[string]bool{}
	for _, res := range cfg.Resources {
		configured[strings.ToLower(res.Workspace)] = true
	}
	for uuid := range r.workspaces {
		if !configured[uuid] {
			return fmt.Errorf("--workspace-resource-id %s matches no configured resources[].workspace", uuid)
		}
	}
	return nil
}

// namespaceLabel is the namespace name: the first DNS label of config.namespace.
func namespaceLabel(cfg incident.Config) string {
	label, _, _ := strings.Cut(cfg.Namespace, ".")
	return label
}

// basePlan holds what the flags and the config alone determine; discovery only
// refines it, so a guidance-only preview still shows every flag-supplied value.
func (r setupRequest) basePlan(cfg incident.Config) (incident.SetupPlan, error) {
	path, err := filepath.Abs(r.config)
	if err != nil {
		return incident.SetupPlan{}, err
	}
	p := incident.SetupPlan{
		Config: cfg, ConfigPath: path, Tenant: r.tenant, Subscription: r.subscription, Identity: r.identity,
		ClientID: r.clientID, ObjectID: r.principalObjectID, DisplayName: r.displayName, CertificatePath: r.certificatePath,
		Namespace: r.namespace,
	}
	if p.Identity == incident.SetupIdentityNew && !r.set["display-name"] {
		p.DisplayName = defaultWorkerDisplayName
	}
	if p.CertificatePath != "" {
		// Lstat only: the destination is never opened, so its key is never read.
		// Anything but "absent" counts as present, so apply is never promised
		// permission to create over something unreadable.
		_, err := os.Lstat(p.CertificatePath)
		p.CertificateExists = !os.IsNotExist(err)
	}
	seen := map[string]bool{}
	for _, res := range cfg.Resources {
		key := strings.ToLower(res.Workspace)
		if seen[key] {
			continue
		}
		seen[key] = true
		t := incident.WorkspaceTarget{UUID: res.Workspace}
		if id, ok := r.workspaces[key]; ok {
			t.ID = &id
		}
		p.Workspaces = append(p.Workspaces, t)
	}
	return p, nil
}

// discoverSetup fills the plan from read-only az calls. A missing az, a failed
// login probe, a tenant mismatch and any call that exits non-zero leave the
// plan with placeholders and no error; a call that succeeds with unusable
// output, or a value that contradicts the request, is returned as an error.
func discoverSetup(ctx context.Context, cfg incident.Config, p *incident.SetupPlan) error {
	az, err := incident.NewAz()
	if err != nil {
		p.AzMissing = true
		return nil
	}
	acct, err := az.AccountShow(ctx)
	if incident.IsAzUnavailable(err) {
		p.LoginFailed = true
		return nil
	}
	if err != nil {
		return err
	}
	p.AccountTenant = acct.Tenant
	if p.Tenant == "" {
		p.Tenant = acct.Tenant
	} else if !strings.EqualFold(p.Tenant, acct.Tenant) {
		p.TenantMismatch = true
		return nil
	}
	if p.Subscription == "" {
		p.Subscription, p.SubscriptionFromCLI = acct.Subscription, true
	}
	if err := discoverServiceBus(ctx, az, cfg, p); err != nil {
		return err
	}
	if err := discoverWorkspaces(ctx, az, p); err != nil {
		return err
	}
	return discoverPrincipal(ctx, az, p)
}

func discoverServiceBus(ctx context.Context, az *incident.Az, cfg incident.Config, p *incident.SetupPlan) error {
	if p.Namespace != nil {
		err := az.NamespaceShow(ctx, *p.Namespace)
		if incident.IsAzUnavailable(err) {
			p.NamespaceNote = "the namespace could not be read, so it was not confirmed"
		} else if err != nil {
			return err
		}
	} else {
		ids, err := az.NamespaceList(ctx, p.Subscription)
		if incident.IsAzUnavailable(err) {
			p.NamespaceNote = "the namespace list could not be read"
		} else if err != nil {
			return err
		}
		var match []incident.ARMID
		for _, id := range ids {
			if strings.EqualFold(id.Name, namespaceLabel(cfg)) {
				match = append(match, id)
			}
		}
		switch {
		case len(match) == 1:
			p.Namespace = &match[0]
		case len(match) > 1:
			p.NamespaceNote = "more than one namespace has the configured name"
		case p.NamespaceNote == "":
			p.NamespaceNote = "no namespace with the configured name was found in this subscription"
		}
	}
	if p.Namespace == nil {
		return nil
	}
	err := az.EntityShow(ctx, *p.Namespace, p.Config)
	if incident.IsAzUnavailable(err) {
		p.EntityNote = "the queue or topic subscription could not be read, so it was not confirmed"
		return nil
	}
	return err
}

func discoverWorkspaces(ctx context.Context, az *incident.Az, p *incident.SetupPlan) error {
	var listed []incident.Workspace
	var listErr error
	isListed := false
	for i := range p.Workspaces {
		t := &p.Workspaces[i]
		if t.ID != nil {
			w, err := az.WorkspaceShow(ctx, *t.ID)
			switch {
			case incident.IsAzUnavailable(err):
				t.Note = "the workspace could not be read, so it was not confirmed"
			case err != nil:
				return err
			case !strings.EqualFold(w.CustomerID, t.UUID):
				return &incident.AzError{Step: "monitor log-analytics workspace show", Reason: "the workspace's customer ID is not the configured one"}
			}
			continue
		}
		if !isListed {
			listed, listErr = az.WorkspaceList(ctx, p.Subscription)
			isListed = true
			if listErr != nil && !incident.IsAzUnavailable(listErr) {
				return listErr
			}
		}
		if listErr != nil {
			t.Note = "the workspace list could not be read"
			continue
		}
		var match []incident.ARMID
		for _, w := range listed {
			if strings.EqualFold(w.CustomerID, t.UUID) {
				match = append(match, w.ID)
			}
		}
		switch len(match) {
		case 1:
			t.ID = &match[0]
		case 0:
			t.Note = "no workspace in this subscription has that customer ID"
		default:
			t.Note = "more than one workspace reports that customer ID"
		}
	}
	return nil
}

// discoverPrincipal resolves an existing or managed identity by client ID or
// object ID and lists the roles it already holds. A new identity does not exist
// yet and is never looked up by display name.
func discoverPrincipal(ctx context.Context, az *incident.Az, p *incident.SetupPlan) error {
	if p.Identity == incident.SetupIdentityNew {
		return nil
	}
	key := p.ClientID
	if key == "" {
		key = p.ObjectID
	}
	if key != "" {
		sp, err := az.SPShow(ctx, key)
		switch {
		case incident.IsAzUnavailable(err):
			// An unconfirmed principal's assignments would describe an identity that
			// was never checked, so they are not listed at all.
			p.PrincipalNote = "the principal could not be looked up, so it was not confirmed"
			return nil
		case err != nil:
			return err
		case p.ClientID != "" && !strings.EqualFold(sp.AppID, p.ClientID), p.ObjectID != "" && !strings.EqualFold(sp.ObjectID, p.ObjectID):
			return &incident.AzError{Step: "ad sp show", Reason: "the principal is not the requested identity"}
		default:
			p.ObjectID = sp.ObjectID
		}
	}
	if p.ObjectID == "" {
		return nil
	}
	assignments, err := az.RoleAssignmentList(ctx, p.ObjectID)
	if incident.IsAzUnavailable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	p.Assignments, p.AssignmentsListed = assignments, true
	return nil
}
