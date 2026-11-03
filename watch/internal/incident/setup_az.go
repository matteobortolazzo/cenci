package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// azReadOnly is the closed set of az subcommands setup may run. runReadOnly
// refuses every other argv before anything is executed, so the preview cannot
// log in, create, update or delete whatever its callers pass.
var azReadOnly = [][]string{
	{"account", "show"},
	{"servicebus", "namespace", "list"},
	{"servicebus", "namespace", "show"},
	{"servicebus", "queue", "show"},
	{"servicebus", "topic", "subscription", "show"},
	{"monitor", "log-analytics", "workspace", "list"},
	{"monitor", "log-analytics", "workspace", "show"},
	{"ad", "sp", "show"},
	{"role", "assignment", "list"},
}

// ErrAzNotFound means the az executable is not on PATH.
var ErrAzNotFound = errors.New("az not found on PATH")

// AzError describes one failed az step. It carries the step and a failure
// class only: az's own stdout and stderr may hold tokens and are never kept.
type AzError struct {
	Step        string
	Reason      string
	Unavailable bool // az ran and exited non-zero: the value could not be read
}

func (e *AzError) Error() string { return "az " + e.Step + ": " + e.Reason }

// IsAzUnavailable reports whether err is an AzError for a call that exited
// non-zero. Every other AzError (timeout, output limit, malformed or
// unexpected output) is a failed discovery step.
func IsAzUnavailable(err error) bool {
	var e *AzError
	return errors.As(err, &e) && e.Unavailable
}

func malformed(step string) error {
	return &AzError{Step: step, Reason: "malformed or unexpected output"}
}

// Az runs the read-only discovery calls. Calls inherit the operator's
// environment, so their own Azure CLI login and AZURE_CONFIG_DIR are used, and
// read stdin from the null device.
//
// allow is the closed set of subcommands the Az may run; nil means azReadOnly,
// so NewAz and every preview caller stay read-only.
type Az struct {
	bin   string
	allow [][]string
}

// azApply is the closed set `cenci incident setup --apply` may run: exactly the
// preview's read-only subcommands plus the one mutating subcommand it needs.
// Nothing here logs in, deletes, removes or resets anything.
var azApply = append(append([][]string(nil), azReadOnly...), []string{"role", "assignment", "create"})

// azIdentityLookups are the three reads only the new-identity apply adds: they
// find the application, its service principal and its certificate credentials.
var azIdentityLookups = [][]string{
	{"ad", "app", "list"},
	{"ad", "sp", "list"},
	{"ad", "app", "credential", "list"},
}

// azApplyNewIdentity is the closed set `cenci incident setup --apply
// --identity new` may run: azApply, the three identity lookups and the three
// creates that make the identity. Nothing here deletes anything; `ad app
// credential reset` is further limited to the appending shape (see
// credentialResetShape) and `ad sp create-for-rbac` is not a prefix of any entry.
var azApplyNewIdentity = append(append(append([][]string(nil), azApply...), azIdentityLookups...),
	[]string{"ad", "app", "create"}, []string{"ad", "sp", "create"}, []string{"ad", "app", "credential", "reset"})

// NewIdentityApplyAz locates az on PATH and allows the new-identity apply set.
// Only `--apply --identity new` uses it.
func NewIdentityApplyAz() (*Az, error) {
	a, err := NewAz()
	if err != nil {
		return nil, err
	}
	a.allow = azApplyNewIdentity
	return a, nil
}

// NewAz locates az on PATH.
func NewAz() (*Az, error) {
	bin, err := exec.LookPath("az")
	if err != nil {
		return nil, ErrAzNotFound
	}
	return &Az{bin: bin}, nil
}

// NewApplyAz locates az on PATH and allows the apply set: the preview's
// read-only subcommands and role assignment create. Only the apply path uses it.
func NewApplyAz() (*Az, error) {
	a, err := NewAz()
	if err != nil {
		return nil, err
	}
	a.allow = azApply
	return a, nil
}

// azStep returns the read-only subcommand that args starts with.
func azStep(args []string) (string, bool) { return stepIn(azReadOnly, args) }

// stepIn returns the subcommand of set that args starts with.
func stepIn(set [][]string, args []string) (string, bool) {
	for _, prefix := range set {
		if len(args) >= len(prefix) && strings.Join(args[:len(prefix)], " ") == strings.Join(prefix, " ") {
			return strings.Join(prefix, " "), true
		}
	}
	return "", false
}

// runReadOnly runs a read-only az subcommand with JSON output. It refuses
// everything outside azReadOnly whatever this Az allows, so a read wrapper can
// never reach role assignment create. Callers pass only validated values
// (UUIDs, parsed resource IDs).
func (a *Az) runReadOnly(ctx context.Context, args ...string) (string, error) {
	if _, ok := azStep(args); !ok {
		return "", &AzError{Step: "(refused)", Reason: "not a read-only setup subcommand"}
	}
	return a.run(ctx, args...)
}

// runLookup runs one of the identity lookups, refusing every argv outside them
// and the read-only set whatever this Az allows.
func (a *Az) runLookup(ctx context.Context, args ...string) (string, error) {
	if _, ok := stepIn(azIdentityLookups, args); !ok {
		return "", &AzError{Step: "(refused)", Reason: "not an identity lookup subcommand"}
	}
	return a.run(ctx, args...)
}

// credentialResetShape reports whether args is exactly `ad app credential reset
// --id <uuid> --cert @<absolute path> --append`. Without --append a reset would
// drop the app's other credentials; --create-cert, --keyvault, --password and
// every other flag would put key material in az's hands, so any other shape is
// refused.
func credentialResetShape(args []string) bool {
	if len(args) != 9 || args[4] != "--id" || !uuidPattern.MatchString(args[5]) || args[6] != "--cert" || args[8] != "--append" {
		return false
	}
	file, ok := strings.CutPrefix(args[7], "@")
	return ok && filepath.IsAbs(file) && !strings.ContainsAny(file, "\x00\r\n")
}

// run runs a subcommand of this Az's allowlist with JSON output, refusing every
// other argv before anything is executed.
func (a *Az) run(ctx context.Context, args ...string) (string, error) {
	allow := a.allow
	if allow == nil {
		allow = azReadOnly
	}
	step, ok := stepIn(allow, args)
	if !ok {
		return "", &AzError{Step: "(refused)", Reason: "not an allowed setup subcommand"}
	}
	if step == "ad app credential reset" && !credentialResetShape(args) {
		return "", &AzError{Step: "(refused)", Reason: "a credential reset must append a certificate file"}
	}
	out, err := command(ctx, "", nil, nil, a.bin, append(append([]string(nil), args...), "-o", "json")...)
	if err == nil {
		return out, nil
	}
	var exit *exec.ExitError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "", &AzError{Step: step, Reason: "timed out"}
	case errors.Is(err, context.Canceled):
		return "", &AzError{Step: step, Reason: "interrupted"}
	case errors.Is(err, errOutputLimit):
		return "", &AzError{Step: step, Reason: "output limit exceeded"}
	case errors.As(err, &exit):
		if exit.ExitCode() < 0 {
			// Killed by a signal: the value was not refused, the call was cut short.
			return "", &AzError{Step: step, Reason: "terminated by signal"}
		}
		return "", &AzError{Step: step, Reason: fmt.Sprintf("exited with code %d", exit.ExitCode()), Unavailable: true}
	}
	return "", &AzError{Step: step, Reason: "could not be run"}
}

// decodeOne decodes exactly one JSON value; empty, truncated or trailing
// output is an error, never an empty result.
func decodeOne(step, out string, v any) error {
	d := json.NewDecoder(strings.NewReader(out))
	if err := d.Decode(v); err != nil {
		return malformed(step)
	}
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		return malformed(step)
	}
	return nil
}

// Account is the signed-in Azure CLI context.
type Account struct{ Subscription, Tenant string }

func (a *Az) AccountShow(ctx context.Context) (Account, error) {
	const step = "account show"
	out, err := a.runReadOnly(ctx, "account", "show")
	if err != nil {
		return Account{}, err
	}
	var doc struct {
		ID       string `json:"id"`
		TenantID string `json:"tenantId"`
	}
	if err := decodeOne(step, out, &doc); err != nil {
		return Account{}, err
	}
	if !uuidPattern.MatchString(doc.ID) || !uuidPattern.MatchString(doc.TenantID) {
		return Account{}, malformed(step)
	}
	return Account{Subscription: doc.ID, Tenant: doc.TenantID}, nil
}

type resourceDoc struct {
	ID string `json:"id"`
}

func (a *Az) NamespaceList(ctx context.Context, subscription string) ([]ARMID, error) {
	const step = "servicebus namespace list"
	if !uuidPattern.MatchString(subscription) {
		return nil, malformed(step)
	}
	out, err := a.runReadOnly(ctx, "servicebus", "namespace", "list", "--subscription", subscription)
	if err != nil {
		return nil, err
	}
	var docs []resourceDoc
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	ids := make([]ARMID, 0, len(docs))
	for _, d := range docs {
		id, err := ParseNamespaceID(d.ID)
		if err != nil {
			return nil, malformed(step)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// NamespaceShow confirms that the namespace exists and is the one asked for.
func (a *Az) NamespaceShow(ctx context.Context, id ARMID) error {
	const step = "servicebus namespace show"
	out, err := a.runReadOnly(ctx, "servicebus", "namespace", "show", "--ids", id.String())
	if err != nil {
		return err
	}
	return sameResource(step, out, id.String())
}

// EntityShow confirms that the configured queue or topic subscription exists.
func (a *Az) EntityShow(ctx context.Context, ns ARMID, c Config) error {
	scope, err := EntityScope(ns, c)
	if err != nil {
		return err
	}
	args := []string{"servicebus", "queue", "show"}
	if c.Topic != "" {
		args = []string{"servicebus", "topic", "subscription", "show"}
	}
	step, _ := azStep(args)
	out, err := a.runReadOnly(ctx, append(args, "--ids", scope)...)
	if err != nil {
		return err
	}
	return sameResource(step, out, scope)
}

func sameResource(step, out, want string) error {
	var doc resourceDoc
	if err := decodeOne(step, out, &doc); err != nil || !strings.EqualFold(doc.ID, want) {
		return malformed(step)
	}
	return nil
}

// Workspace is a Log Analytics workspace with its customer ID.
type Workspace struct {
	CustomerID string
	ID         ARMID
}

type workspaceDoc struct {
	CustomerID string `json:"customerId"`
	ID         string `json:"id"`
}

func (d workspaceDoc) validate(step string) (Workspace, error) {
	id, err := ParseWorkspaceID(d.ID)
	if err != nil || !uuidPattern.MatchString(d.CustomerID) {
		return Workspace{}, malformed(step)
	}
	return Workspace{CustomerID: d.CustomerID, ID: id}, nil
}

func (a *Az) WorkspaceList(ctx context.Context, subscription string) ([]Workspace, error) {
	const step = "monitor log-analytics workspace list"
	if !uuidPattern.MatchString(subscription) {
		return nil, malformed(step)
	}
	out, err := a.runReadOnly(ctx, "monitor", "log-analytics", "workspace", "list", "--subscription", subscription)
	if err != nil {
		return nil, err
	}
	var docs []workspaceDoc
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	list := make([]Workspace, 0, len(docs))
	for _, d := range docs {
		w, err := d.validate(step)
		if err != nil {
			return nil, err
		}
		list = append(list, w)
	}
	return list, nil
}

// WorkspaceShow reads one workspace and checks it is the one asked for.
func (a *Az) WorkspaceShow(ctx context.Context, id ARMID) (Workspace, error) {
	const step = "monitor log-analytics workspace show"
	out, err := a.runReadOnly(ctx, "monitor", "log-analytics", "workspace", "show", "--ids", id.String())
	if err != nil {
		return Workspace{}, err
	}
	var doc workspaceDoc
	if err := decodeOne(step, out, &doc); err != nil {
		return Workspace{}, err
	}
	w, err := doc.validate(step)
	if err != nil || !strings.EqualFold(w.ID.String(), id.String()) {
		return Workspace{}, malformed(step)
	}
	return w, nil
}

// ServicePrincipal is the identity az reports for a client or object ID.
type ServicePrincipal struct{ AppID, ObjectID string }

type servicePrincipalDoc struct {
	AppID string `json:"appId"`
	ID    string `json:"id"`
}

func (d servicePrincipalDoc) validate(step string) (ServicePrincipal, error) {
	if !uuidPattern.MatchString(d.AppID) || !uuidPattern.MatchString(d.ID) {
		return ServicePrincipal{}, malformed(step)
	}
	return ServicePrincipal{AppID: d.AppID, ObjectID: d.ID}, nil
}

// SPShow resolves a service principal by client ID or object ID, never by name.
func (a *Az) SPShow(ctx context.Context, id string) (ServicePrincipal, error) {
	const step = "ad sp show"
	if !uuidPattern.MatchString(id) {
		return ServicePrincipal{}, malformed(step)
	}
	out, err := a.runReadOnly(ctx, "ad", "sp", "show", "--id", id)
	if err != nil {
		return ServicePrincipal{}, err
	}
	var doc servicePrincipalDoc
	if err := decodeOne(step, out, &doc); err != nil {
		return ServicePrincipal{}, err
	}
	return doc.validate(step)
}

// RoleAssignmentList lists every assignment the principal holds, in all
// subscriptions.
func (a *Az) RoleAssignmentList(ctx context.Context, objectID string) ([]RoleAssignment, error) {
	const step = "role assignment list"
	if !uuidPattern.MatchString(objectID) {
		return nil, malformed(step)
	}
	out, err := a.runReadOnly(ctx, "role", "assignment", "list", "--assignee-object-id", objectID, "--all")
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Role  string `json:"roleDefinitionName"`
		Scope string `json:"scope"`
	}
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	list := make([]RoleAssignment, 0, len(docs))
	for _, d := range docs {
		if !PrintableBounded(d.Role, maxSetupTextRunes) || !PrintableBounded(d.Scope, maxSetupTextRunes) {
			return nil, malformed(step)
		}
		list = append(list, RoleAssignment{Role: d.Role, Scope: d.Scope})
	}
	return list, nil
}

// ScopedAssignment is one role assignment as az reports it for a scope: the
// principal, the trailing GUID of the role definition ID and the scope string.
type ScopedAssignment struct{ PrincipalID, RoleGUID, Scope string }

// Matches reports whether the assignment is exactly this principal, role and
// scope. Every part compares without case and the scope compares as a whole
// string, never as a prefix, so a parent scope or another principal never match.
func (s ScopedAssignment) Matches(principalID, roleGUID, scope string) bool {
	return strings.EqualFold(s.PrincipalID, principalID) && strings.EqualFold(s.RoleGUID, roleGUID) && strings.EqualFold(s.Scope, scope)
}

var scopePattern = regexp.MustCompile(`^/subscriptions/[a-fA-F0-9-]{36}/[A-Za-z0-9_./()-]+$`)

type assignmentDoc struct {
	PrincipalID      string `json:"principalId"`
	RoleDefinitionID string `json:"roleDefinitionId"`
	Scope            string `json:"scope"`
}

// validate checks every field and reduces the role definition ID to its
// trailing GUID.
func (d assignmentDoc) validate(step string) (ScopedAssignment, error) {
	parts := strings.Split(d.RoleDefinitionID, "/")
	if !uuidPattern.MatchString(d.PrincipalID) || len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "roleDefinitions") ||
		!uuidPattern.MatchString(parts[len(parts)-1]) || !PrintableBounded(d.Scope, maxSetupTextRunes) {
		return ScopedAssignment{}, malformed(step)
	}
	return ScopedAssignment{PrincipalID: d.PrincipalID, RoleGUID: parts[len(parts)-1], Scope: d.Scope}, nil
}

// RoleAssignmentsAt lists the assignments of one principal at one scope. az
// also reports inherited assignments, so callers must compare every record with
// Matches instead of trusting the list.
func (a *Az) RoleAssignmentsAt(ctx context.Context, objectID, scope string) ([]ScopedAssignment, error) {
	const step = "role assignment list"
	if !uuidPattern.MatchString(objectID) || !scopePattern.MatchString(scope) {
		return nil, malformed(step)
	}
	out, err := a.runReadOnly(ctx, "role", "assignment", "list", "--assignee-object-id", objectID, "--scope", scope)
	if err != nil {
		return nil, err
	}
	var docs []assignmentDoc
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	list := make([]ScopedAssignment, 0, len(docs))
	for _, d := range docs {
		rec, err := d.validate(step)
		if err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	return list, nil
}

const (
	// maxApplicationNameRunes bounds an application display name: a base of at most
	// 120 characters, a hyphen and a 16-character nonce.
	maxApplicationNameRunes = 137
	maxCertificatePathRunes = 4096
)

// Application is the app registration az reports for a client ID or display name.
type Application struct{ AppID, ObjectID, DisplayName string }

type applicationDoc struct {
	AppID       string `json:"appId"`
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

func (d applicationDoc) validate(step string) (Application, error) {
	if !uuidPattern.MatchString(d.AppID) || !uuidPattern.MatchString(d.ID) || !PrintableBounded(d.DisplayName, maxApplicationNameRunes) {
		return Application{}, malformed(step)
	}
	return Application{AppID: d.AppID, ObjectID: d.ID, DisplayName: d.DisplayName}, nil
}

// AppCreate creates an application with exactly this display name and checks
// that az echoes that name. The name is the journaled nonce name.
func (a *Az) AppCreate(ctx context.Context, name string) (Application, error) {
	const step = "ad app create"
	if !PrintableBounded(name, maxApplicationNameRunes) {
		return Application{}, malformed(step)
	}
	out, err := a.run(ctx, "ad", "app", "create", "--display-name", name)
	if err != nil {
		return Application{}, err
	}
	var doc applicationDoc
	if err := decodeOne(step, out, &doc); err != nil {
		return Application{}, err
	}
	app, err := doc.validate(step)
	if err != nil || app.DisplayName != name {
		return Application{}, malformed(step)
	}
	return app, nil
}

// AppListByName lists the applications whose display name is exactly name. The
// server-side filter is a name match az evaluates, so the answer is compared
// again here and a longer or shorter name is never counted. A quote in the name
// is doubled for OData; the filter stays one argv element.
func (a *Az) AppListByName(ctx context.Context, name string) ([]Application, error) {
	const step = "ad app list"
	if !PrintableBounded(name, maxApplicationNameRunes) {
		return nil, malformed(step)
	}
	out, err := a.runLookup(ctx, "ad", "app", "list", "--filter", "displayName eq '"+strings.ReplaceAll(name, "'", "''")+"'")
	if err != nil {
		return nil, err
	}
	var docs []applicationDoc
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	apps := make([]Application, 0, len(docs))
	for _, d := range docs {
		if d.DisplayName != name {
			continue
		}
		app, err := d.validate(step)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// AppListByID lists the applications with this client ID. Every item must carry
// that client ID, so a filter az ignored is a malformed answer, not a match.
func (a *Az) AppListByID(ctx context.Context, clientID string) ([]Application, error) {
	const step = "ad app list"
	if !uuidPattern.MatchString(clientID) {
		return nil, malformed(step)
	}
	out, err := a.runLookup(ctx, "ad", "app", "list", "--filter", "appId eq '"+clientID+"'")
	if err != nil {
		return nil, err
	}
	var docs []applicationDoc
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	apps := make([]Application, 0, len(docs))
	for _, d := range docs {
		app, err := d.validate(step)
		if err != nil || !strings.EqualFold(app.AppID, clientID) {
			return nil, malformed(step)
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// SPListByApp lists the service principals of an application: none or one.
func (a *Az) SPListByApp(ctx context.Context, clientID string) ([]ServicePrincipal, error) {
	const step = "ad sp list"
	if !uuidPattern.MatchString(clientID) {
		return nil, malformed(step)
	}
	out, err := a.runLookup(ctx, "ad", "sp", "list", "--filter", "appId eq '"+clientID+"'")
	if err != nil {
		return nil, err
	}
	var docs []servicePrincipalDoc
	if err := decodeOne(step, out, &docs); err != nil || docs == nil || len(docs) > 1 {
		return nil, malformed(step)
	}
	sps := make([]ServicePrincipal, 0, len(docs))
	for _, d := range docs {
		sp, err := d.validate(step)
		if err != nil || !strings.EqualFold(sp.AppID, clientID) {
			return nil, malformed(step)
		}
		sps = append(sps, sp)
	}
	return sps, nil
}

// SPCreate creates the service principal of an application and checks that az
// echoes that application.
func (a *Az) SPCreate(ctx context.Context, clientID string) (ServicePrincipal, error) {
	const step = "ad sp create"
	if !uuidPattern.MatchString(clientID) {
		return ServicePrincipal{}, malformed(step)
	}
	out, err := a.run(ctx, "ad", "sp", "create", "--id", clientID)
	if err != nil {
		return ServicePrincipal{}, err
	}
	var doc servicePrincipalDoc
	if err := decodeOne(step, out, &doc); err != nil {
		return ServicePrincipal{}, err
	}
	sp, err := doc.validate(step)
	if err != nil || !strings.EqualFold(sp.AppID, clientID) {
		return ServicePrincipal{}, malformed(step)
	}
	return sp, nil
}

// CertCredentials lists the certificate credentials of an application as the raw
// customKeyIdentifier strings; the caller decides whether it recognizes them.
func (a *Az) CertCredentials(ctx context.Context, clientID string) ([]string, error) {
	const step = "ad app credential list"
	if !uuidPattern.MatchString(clientID) {
		return nil, malformed(step)
	}
	out, err := a.runLookup(ctx, "ad", "app", "credential", "list", "--id", clientID, "--cert")
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Identifier string `json:"customKeyIdentifier"`
	}
	if err := decodeOne(step, out, &docs); err != nil || docs == nil {
		return nil, malformed(step)
	}
	identifiers := make([]string, 0, len(docs))
	for _, d := range docs {
		if !PrintableBounded(d.Identifier, maxSetupTextRunes) {
			return nil, malformed(step)
		}
		identifiers = append(identifiers, d.Identifier)
	}
	return identifiers, nil
}

// CertCredentialAppend registers the public certificate in the file at path on
// the application, keeping its other credentials (--append). Only the file path
// is passed to az, never certificate or key text, and az's answer is reduced to
// the check that it names this application.
func (a *Az) CertCredentialAppend(ctx context.Context, clientID, path string) error {
	const step = "ad app credential reset"
	if !uuidPattern.MatchString(clientID) || !filepath.IsAbs(path) || !PrintableBounded(path, maxCertificatePathRunes) {
		return malformed(step)
	}
	out, err := a.run(ctx, "ad", "app", "credential", "reset", "--id", clientID, "--cert", "@"+path, "--append")
	if err != nil {
		return err
	}
	var doc struct {
		AppID string `json:"appId"`
	}
	if err := decodeOne(step, out, &doc); err != nil {
		return err
	}
	if !uuidPattern.MatchString(doc.AppID) || !strings.EqualFold(doc.AppID, clientID) {
		return malformed(step)
	}
	return nil
}

// RoleAssignmentCreate creates one assignment of a built-in role to a service
// principal at an exact scope and checks that az echoes exactly that
// assignment. It is the only mutating call setup makes and is refused unless
// the Az was built by NewApplyAz.
func (a *Az) RoleAssignmentCreate(ctx context.Context, objectID, roleGUID, scope string) error {
	const step = "role assignment create"
	if !uuidPattern.MatchString(objectID) || !uuidPattern.MatchString(roleGUID) || !scopePattern.MatchString(scope) {
		return malformed(step)
	}
	out, err := a.run(ctx, "role", "assignment", "create", "--assignee-object-id", objectID,
		"--assignee-principal-type", "ServicePrincipal", "--role", roleGUID, "--scope", scope)
	if err != nil {
		return err
	}
	var doc assignmentDoc
	if err := decodeOne(step, out, &doc); err != nil {
		return err
	}
	got, err := doc.validate(step)
	if err != nil || !got.Matches(objectID, roleGUID, scope) {
		return malformed(step)
	}
	return nil
}
