package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
type Az struct{ bin string }

// NewAz locates az on PATH.
func NewAz() (*Az, error) {
	bin, err := exec.LookPath("az")
	if err != nil {
		return nil, ErrAzNotFound
	}
	return &Az{bin: bin}, nil
}

// azStep returns the allowlisted subcommand that args starts with.
func azStep(args []string) (string, bool) {
	for _, prefix := range azReadOnly {
		if len(args) >= len(prefix) && strings.Join(args[:len(prefix)], " ") == strings.Join(prefix, " ") {
			return strings.Join(prefix, " "), true
		}
	}
	return "", false
}

// runReadOnly runs an allowlisted az subcommand with JSON output. Callers pass
// only validated values (UUIDs, parsed resource IDs).
func (a *Az) runReadOnly(ctx context.Context, args ...string) (string, error) {
	step, ok := azStep(args)
	if !ok {
		return "", &AzError{Step: "(refused)", Reason: "not a read-only setup subcommand"}
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
	var doc struct {
		AppID string `json:"appId"`
		ID    string `json:"id"`
	}
	if err := decodeOne(step, out, &doc); err != nil {
		return ServicePrincipal{}, err
	}
	if !uuidPattern.MatchString(doc.AppID) || !uuidPattern.MatchString(doc.ID) {
		return ServicePrincipal{}, malformed(step)
	}
	return ServicePrincipal{AppID: doc.AppID, ObjectID: doc.ID}, nil
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
