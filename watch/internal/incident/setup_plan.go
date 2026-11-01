package incident

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Built-in Azure role definition IDs the preview grants (Azure built-in roles
// reference). There is deliberately no Monitoring Reader: only the Log
// Analytics query API is used.
const (
	RoleServiceBusDataReceiver = "4f6d3b9b-027b-4f4c-9142-0e5a2a2247e0"
	RoleLogAnalyticsReader     = "73c42c96-874c-492b-b04d-ab87d138a893"
)

// Worker identity choices of `cenci incident setup --identity`.
const (
	SetupIdentityNew      = "new"
	SetupIdentityExisting = "existing"
	SetupIdentityManaged  = "managed"
)

const (
	tenantPlaceholder       = "<tenant-id>"
	subscriptionPlaceholder = "<subscription-id>"
	appIDPlaceholder        = "<app-id>"
	objectPlaceholder       = "<object-id>"
	principalPlaceholder    = "<principal-object-id>"
	certPlaceholder         = "<certificate-path>"
	namespacePlaceholder    = "<service-bus-namespace-id>"
	workspacePlaceholder    = "<workspace-resource-id>"
	publicCertPlaceholder   = "<public-certificate-file>"
	privateKeyPlaceholder   = "<private-key-file>"
	maxSetupTextRunes       = 512
)

var (
	armSegment       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.()-]{0,89}$`)
	entityName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,259}$`)
	placeholderToken = regexp.MustCompile(`<[a-z][a-z0-9-]*>`)
)

type armKind struct{ provider, collection, noun string }

var (
	namespaceKind = armKind{"Microsoft.ServiceBus", "namespaces", "Service Bus namespace"}
	workspaceKind = armKind{"Microsoft.OperationalInsights", "workspaces", "Log Analytics workspace"}
)

// ARMID is a validated Azure resource ID of one of the two kinds setup consumes:
// a Service Bus namespace or a Log Analytics workspace.
type ARMID struct {
	Subscription  string
	ResourceGroup string
	Name          string
	kind          armKind
}

func ParseNamespaceID(s string) (ARMID, error) { return parseARMID(s, namespaceKind) }

func ParseWorkspaceID(s string) (ARMID, error) { return parseARMID(s, workspaceKind) }

// parseARMID accepts only /subscriptions/<uuid>/resourceGroups/<group>/providers/
// <provider>/<collection>/<name> with the fixed segments compared without case.
// The error never echoes the input.
func parseARMID(s string, k armKind) (ARMID, error) {
	p := strings.Split(s, "/")
	if len(p) != 9 || p[0] != "" || !strings.EqualFold(p[1], "subscriptions") || !uuidPattern.MatchString(p[2]) ||
		!strings.EqualFold(p[3], "resourceGroups") || !armSegment.MatchString(p[4]) || !strings.EqualFold(p[5], "providers") ||
		!strings.EqualFold(p[6], k.provider) || !strings.EqualFold(p[7], k.collection) || !armSegment.MatchString(p[8]) {
		return ARMID{}, fmt.Errorf("not a %s resource ID (want /subscriptions/<uuid>/resourceGroups/<group>/providers/%s/%s/<name>)", k.noun, k.provider, k.collection)
	}
	return ARMID{Subscription: p[2], ResourceGroup: p[4], Name: p[8], kind: k}, nil
}

// String renders the canonical ID (standard casing of the fixed segments).
func (a ARMID) String() string {
	return "/subscriptions/" + a.Subscription + "/resourceGroups/" + a.ResourceGroup + "/providers/" + a.kind.provider + "/" + a.kind.collection + "/" + a.Name
}

// InSubscription reports whether the ID lives in the given subscription UUID,
// ignoring case.
func (a ARMID) InSubscription(subscription string) bool {
	return strings.EqualFold(a.Subscription, subscription)
}

// IsUUID reports whether s is a UUID in the form the worker config accepts.
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// entitySuffix is the path of the configured queue or topic/subscription below
// its namespace.
func entitySuffix(c Config) (string, error) {
	switch {
	case c.Queue != "" && c.Topic == "" && c.Subscription == "" && entityName.MatchString(c.Queue):
		return "/queues/" + c.Queue, nil
	case c.Queue == "" && entityName.MatchString(c.Topic) && entityName.MatchString(c.Subscription):
		return "/topics/" + c.Topic + "/subscriptions/" + c.Subscription, nil
	}
	return "", fmt.Errorf("configure one valid queue or topic/subscription name")
}

// EntityScope returns the exact role scope for the configured queue or
// topic/subscription under the namespace, never the namespace or topic scope.
func EntityScope(ns ARMID, c Config) (string, error) {
	suffix, err := entitySuffix(c)
	if err != nil {
		return "", err
	}
	return ns.String() + suffix, nil
}

// DedupeWorkspaces keeps the first occurrence of each workspace, comparing IDs
// without regard to case.
func DedupeWorkspaces(ids []ARMID) []ARMID {
	seen := map[string]bool{}
	out := make([]ARMID, 0, len(ids))
	for _, id := range ids {
		if key := strings.ToLower(id.String()); !seen[key] {
			seen[key] = true
			out = append(out, id)
		}
	}
	return out
}

// ShellQuote returns s as one POSIX shell word that expands to exactly s.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PrintableBounded reports whether s is non-empty, within the rune bound and
// made only of printable characters, so it can neither forge output lines nor
// grow without bound.
func PrintableBounded(s string, max int) bool {
	if s == "" || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// safeText renders text that came from a file or from az for the terminal.
func safeText(s string) string {
	if PrintableBounded(s, maxSetupTextRunes) {
		return s
	}
	return strconv.Quote(s)
}

// WorkspaceTarget is one configured Log Analytics workspace and what discovery
// learned about it.
type WorkspaceTarget struct {
	UUID string // the configured resources[].workspace customer ID
	ID   *ARMID // nil when the workspace was not discovered
	Note string // why it was not discovered, or what could not be confirmed
}

// RoleAssignment is one existing assignment of the worker principal.
type RoleAssignment struct{ Role, Scope string }

// SetupPlan is everything the preview prints: validated flags, the config and
// the discovery results. Empty strings and nil pointers mean "not known" and
// render as <placeholder> tokens.
type SetupPlan struct {
	Config              Config
	ConfigPath          string
	Tenant              string // effective tenant UUID
	Subscription        string // effective subscription UUID
	SubscriptionFromCLI bool   // Subscription is the Azure CLI default, not --subscription
	Identity            string
	ClientID            string
	ObjectID            string
	DisplayName         string
	CertificatePath     string
	CertificateExists   bool

	AzMissing      bool
	LoginFailed    bool
	TenantMismatch bool
	AccountTenant  string

	Namespace     *ARMID
	NamespaceNote string
	EntityNote    string
	Workspaces    []WorkspaceTarget

	PrincipalNote     string // non-empty: the principal was not confirmed, so its assignments were not listed
	AssignmentsListed bool   // false: they could not be read
	Assignments       []RoleAssignment
}

type setupStep struct {
	title string
	lines []string
}

func orPlaceholder(v, placeholder string) string {
	if v == "" {
		return placeholder
	}
	return v
}

// wordKind says how a command word reaches the shell. The renderer, not the
// text, decides: only a word built by ph is ever emitted unquoted.
type wordKind int

const (
	wordRaw         wordKind = iota // fixed command text written by the renderer
	wordValue                       // a flag, config or discovered value: always single-quoted
	wordPlaceholder                 // a <token> the renderer substitutes for an unknown value
)

type word struct {
	text string
	kind wordKind
}

func raw(s string) word { return word{s, wordRaw} }

// val is a real value. It is single-quoted even when its text looks like a
// placeholder, so user-supplied or discovered input can never be mistaken for one.
func val(s string) word { return word{s, wordValue} }

// ph is a placeholder the renderer itself produced. It is emitted unquoted, so a
// command that still holds it is a shell redirection error instead of a run with
// the literal token. Call it only with the renderer's own tokens.
func ph(s string) word { return word{s, wordPlaceholder} }

// valOrPh is the real value v, or the placeholder token when v is unknown.
func valOrPh(v, token string) word {
	if v == "" {
		return ph(token)
	}
	return val(v)
}

func (w word) shell() string {
	if w.kind == wordValue {
		return ShellQuote(w.text)
	}
	return w.text
}

const fillPlaceholdersLabel = "# fill placeholders before running"

// cmdLine renders one indented command. A command that still holds a
// placeholder is preceded by a label line.
func cmdLine(parts ...word) string {
	words := make([]string, len(parts))
	labeled := false
	for i, w := range parts {
		words[i] = w.shell()
		labeled = labeled || w.kind == wordPlaceholder
	}
	line := "  " + strings.Join(words, " ")
	if labeled {
		return "  " + fillPlaceholdersLabel + "\n" + line
	}
	return line
}

func roleCommand(object word, role string, scope word) string {
	return cmdLine(raw("az role assignment create --assignee-object-id"), object, raw("--assignee-principal-type ServicePrincipal --role "+role+" --scope"), scope)
}

func (p SetupPlan) objectRef() word {
	if p.Identity == SetupIdentityManaged {
		return valOrPh(p.ObjectID, principalPlaceholder)
	}
	return valOrPh(p.ObjectID, objectPlaceholder)
}

// proposedAuth is the auth section the worker would be configured with.
func (p SetupPlan) proposedAuth() AuthConfig {
	a := AuthConfig{Mode: AuthModeCertificate, TenantID: orPlaceholder(p.Tenant, tenantPlaceholder), ClientID: p.ClientID, CertificatePath: orPlaceholder(p.CertificatePath, certPlaceholder)}
	switch p.Identity {
	case SetupIdentityNew:
		a.ClientID = appIDPlaceholder
	case SetupIdentityManaged:
		a.Mode, a.CertificatePath = AuthModeManagedIdentity, ""
	}
	return a
}

// Render returns the numbered preview. It fails only when the plan cannot be
// expressed safely (an invalid entity name or a proposed auth section that
// would not pass the worker's own validation).
func (p SetupPlan) Render() (string, error) {
	suffix, err := entitySuffix(p.Config)
	if err != nil {
		return "", err
	}
	auth := p.proposedAuth()
	var authJSON bytes.Buffer
	enc := json.NewEncoder(&authJSON)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(auth); err != nil {
		return "", err
	}
	if !placeholderToken.MatchString(authJSON.String()) {
		if err := auth.Validate(); err != nil {
			return "", fmt.Errorf("proposed auth section: %w", err)
		}
	}

	steps := []setupStep{p.prerequisites(), p.identityStep(), p.serviceBusStep(suffix), p.workspaceStep()}
	if p.Identity != SetupIdentityManaged {
		steps = append(steps, p.certificateStep())
	}
	steps = append(steps, p.authStep(authJSON.String()), p.filesStep())

	var body strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&body, "\n%d. %s\n", i+1, s.title)
		for _, l := range s.lines {
			for _, sub := range strings.Split(l, "\n") {
				body.WriteString("   " + sub + "\n")
			}
		}
	}

	var out strings.Builder
	out.WriteString("cenci incident setup: preview only. This run created, changed and wrote nothing; the commands below are for you to review and run yourself.\n\n")
	fmt.Fprintf(&out, "Config:       %s\n", safeText(p.ConfigPath))
	fmt.Fprintf(&out, "Tenant:       %s\n", orPlaceholder(p.Tenant, tenantPlaceholder))
	source := ""
	if p.SubscriptionFromCLI {
		source = " (Azure CLI default; pass --subscription to pin it)"
	}
	fmt.Fprintf(&out, "Subscription: %s%s\n", orPlaceholder(p.Subscription, subscriptionPlaceholder), source)
	fmt.Fprintf(&out, "Identity:     %s\n", p.Identity)
	if token := placeholderToken.FindString(body.String()); token != "" {
		fmt.Fprintf(&out, "\nValues in angle brackets, such as %s, could not be discovered. Replace each one before running the command that contains it.\n", token)
	}
	out.WriteString(body.String())
	return out.String(), nil
}

func (p SetupPlan) prerequisites() setupStep {
	s := setupStep{title: "Prerequisites", lines: []string{
		"Two different kinds of rights are needed, and different people often hold them:",
		"- Microsoft Entra: rights to create an app registration and its service principal (for example Application Administrator, Cloud Application Administrator or Application Developer). Only a new identity, or a new certificate credential on an existing one, needs this.",
		"- Azure RBAC: User Access Administrator or Role Based Access Control Administrator on each scope that receives a role assignment below (the Service Bus entity and every Log Analytics workspace).",
		"The Azure CLI login is used for setup only and is never written to the worker configuration. The worker authenticates only as the identity in its auth section.",
	}}
	login := cmdLine(raw("az login --tenant"), valOrPh(p.Tenant, tenantPlaceholder))
	switch {
	case p.AzMissing:
		s.lines = append(s.lines, "Azure CLI: not found on PATH, so nothing could be discovered. Install it, sign in, and run this preview again to fill in the values below.")
	case p.LoginFailed:
		s.lines = append(s.lines, "Azure CLI: az account show failed, so you are not signed in (or the CLI is unusable) and nothing could be discovered. Sign in, then run this preview again:", login)
	case p.TenantMismatch:
		s.lines = append(s.lines, fmt.Sprintf("Azure CLI: tenant mismatch. --tenant is %s but the CLI is signed in to tenant %s. Discovery was skipped because it would read the wrong tenant. Sign in to the right tenant, then run this preview again:", p.Tenant, p.AccountTenant), login)
	default:
		s.lines = append(s.lines, fmt.Sprintf("Azure CLI: signed in to tenant %s.", p.AccountTenant))
	}
	return s
}

func (p SetupPlan) identityStep() setupStep {
	s := setupStep{title: "Worker identity"}
	switch p.Identity {
	case SetupIdentityNew:
		app := ph(appIDPlaceholder)
		s.lines = []string{
			"Create a dedicated app registration and service principal for the worker. The display name is only a label: the identity is selected afterwards by client ID or object ID, never by name.",
			cmdLine(raw("az ad app create --display-name"), val(p.DisplayName), raw("--query appId -o tsv")),
			cmdLine(raw("az ad sp create --id"), app),
			cmdLine(raw("az ad sp show --id"), app, raw("--query id -o tsv")),
			"Replace " + appIDPlaceholder + " with the appId the first command prints, and " + objectPlaceholder + " in the role commands with the id the last command prints.",
		}
	case SetupIdentityExisting:
		s.lines = []string{
			"Use the existing identity with client ID " + p.ClientID + ". The role commands need its object ID; resolve it by client ID:",
			cmdLine(raw("az ad sp show --id"), val(p.ClientID), raw("--query id -o tsv")),
		}
		if p.ObjectID != "" {
			s.lines = append(s.lines, "Resolved object ID: "+p.ObjectID+".")
		} else {
			s.lines = append(s.lines, "The object ID could not be resolved here; replace "+objectPlaceholder+" with the output of the command above.")
		}
		s.lines = append(s.lines, p.assignmentLines()...)
	default:
		s.lines = []string{"The worker runs with a managed identity on an Azure host. Setup does not create or change it; it only grants it the roles below."}
		if p.ObjectID != "" {
			s.lines = append(s.lines, "Principal object ID: "+p.ObjectID+".")
		} else {
			s.lines = append(s.lines, "Pass the identity's principal (object) ID with --principal-object-id; until then "+principalPlaceholder+" stands in.")
		}
		if p.ClientID != "" {
			s.lines = append(s.lines, cmdLine(raw("az ad sp show --id"), val(p.ClientID), raw("--query id -o tsv")))
		}
		s.lines = append(s.lines, p.assignmentLines()...)
	}
	return s
}

// assignmentLines lists the existing principal's other role assignments, or
// says they could not be read, and always warns that RBAC is additive.
func (p SetupPlan) assignmentLines() []string {
	var l []string
	if p.PrincipalNote != "" {
		l = append(l, "Note: "+p.PrincipalNote+".")
	}
	switch {
	case !p.AssignmentsListed:
		l = append(l, "The principal's other role assignments could not be read; list them yourself with the object ID before relying on this identity.")
	case len(p.Assignments) == 0:
		l = append(l, "The principal holds no other role assignments.")
	default:
		l = append(l, "Other role assignments this principal already holds:")
		for _, a := range p.Assignments {
			l = append(l, "- "+a.Role+" at "+a.Scope)
		}
	}
	return append(l, "Azure RBAC is additive: every role the principal already holds stays in effect next to the two roles granted below, so review the assignments and remove any the worker should not have.")
}

func (p SetupPlan) serviceBusStep(suffix string) setupStep {
	s := setupStep{title: "Service Bus receive role", lines: []string{
		"Grant Azure Service Bus Data Receiver on exactly the queue or topic subscription the worker reads, never on the namespace or the topic:",
	}}
	scope := ph(namespacePlaceholder + suffix)
	if p.Namespace != nil {
		scope = val(p.Namespace.String() + suffix)
		if p.NamespaceNote != "" {
			s.lines = append(s.lines, "Note: "+p.NamespaceNote+".")
		}
	} else {
		s.lines = append(s.lines, "The Service Bus namespace was not discovered ("+orPlaceholder(p.NamespaceNote, "az was unavailable")+"). Pass --service-bus-namespace-id with its resource ID; until then "+namespacePlaceholder+" stands in.")
	}
	if p.EntityNote != "" {
		s.lines = append(s.lines, "Note: "+p.EntityNote+".")
	}
	s.lines = append(s.lines, roleCommand(p.objectRef(), RoleServiceBusDataReceiver, scope))
	return s
}

func (p SetupPlan) workspaceStep() setupStep {
	s := setupStep{title: "Log Analytics read role", lines: []string{
		"Grant Log Analytics Reader on each workspace the configured resources query. Resources that share a workspace need it once:",
	}}
	var found []ARMID
	missing := 0
	for _, t := range p.Workspaces {
		if t.ID == nil {
			missing++
			s.lines = append(s.lines, "Workspace "+t.UUID+" was not discovered ("+orPlaceholder(t.Note, "az was unavailable")+"). Pass --workspace-resource-id "+t.UUID+"=RESOURCE_ID; until then "+workspacePlaceholder+" stands in.")
			continue
		}
		found = append(found, *t.ID)
		if t.Note != "" {
			s.lines = append(s.lines, "Note: workspace "+t.UUID+": "+t.Note+".")
		}
	}
	for _, id := range DedupeWorkspaces(found) {
		if p.Subscription != "" && !id.InSubscription(p.Subscription) {
			s.lines = append(s.lines, "Workspace "+id.Name+" is in subscription "+id.Subscription+", not "+p.Subscription+" (cross-subscription). The assignment keeps the workspace's own scope, and the operator needs the Azure RBAC rights above on that subscription.")
		}
		s.lines = append(s.lines, roleCommand(p.objectRef(), RoleLogAnalyticsReader, val(id.String())))
	}
	for range missing {
		s.lines = append(s.lines, roleCommand(p.objectRef(), RoleLogAnalyticsReader, ph(workspacePlaceholder)))
	}
	return s
}

func (p SetupPlan) certificateStep() setupStep {
	s := setupStep{title: "Certificate credential"}
	cert := valOrPh(p.CertificatePath, certPlaceholder)
	app := ph(appIDPlaceholder)
	if p.Identity == SetupIdentityExisting {
		app = val(p.ClientID)
	}
	pub, pubFile, privFile := ph("@"+publicCertPlaceholder), ph(publicCertPlaceholder), ph(privateKeyPlaceholder)
	switch {
	case p.CertificatePath == "":
		s.lines = append(s.lines, "The worker reads one unencrypted PEM file holding its certificate and private key, mode 0600. Pass --certificate-path to name where it lives; until then "+certPlaceholder+" stands in.")
	case p.CertificateExists:
		s.lines = append(s.lines, "The worker reads one unencrypted PEM file holding its certificate and private key. "+safeText(p.CertificatePath)+" already exists: it is not read or changed here. Extract its public certificate and register that:",
			cmdLine(raw("openssl x509 -in"), cert, raw("-out"), pubFile))
	default:
		s.lines = append(s.lines, "The worker reads one unencrypted PEM file holding its certificate and private key, mode 0600. Generate it locally; the private key stays on this machine and is never printed or sent anywhere:",
			cmdLine(raw("umask 077")),
			cmdLine(raw("openssl req -x509 -newkey rsa:2048 -nodes -days 365 -subj '/CN=cenci-incident-worker' -keyout"), privFile, raw("-out"), pubFile),
			cmdLine(raw("cat"), privFile, pubFile, raw(">"), cert))
	}
	s.lines = append(s.lines, "Register only the public certificate on the app registration; --append keeps its other credentials:",
		cmdLine(raw("az ad app credential reset --id"), app, raw("--cert"), pub, raw("--append")))
	return s
}

func (p SetupPlan) authStep(authJSON string) setupStep {
	s := setupStep{title: "Proposed auth section", lines: []string{"Add this to the incident worker config. The worker authenticates only as this identity and never falls back to ambient Azure credentials:"}}
	s.lines = append(s.lines, strings.Split(strings.TrimRight(authJSON, "\n"), "\n")...)
	proposed := p.proposedAuth()
	switch {
	case p.Config.Auth == nil:
		s.lines = append(s.lines, "The config has no auth section yet.")
	case *p.Config.Auth == proposed:
		s.lines = append(s.lines, "The config's existing auth section matches this proposal.")
	default:
		s.lines = append(s.lines, "The config's existing auth section differs from this proposal.")
	}
	return s
}

func (p SetupPlan) filesStep() setupStep {
	s := setupStep{title: "Local files that apply would touch", lines: []string{
		"This preview touched none of them. A later apply would create or change:",
		"- " + safeText(p.ConfigPath) + ": add the auth section.",
	}}
	switch {
	case p.Identity == SetupIdentityManaged:
	case p.CertificatePath == "":
		s.lines = append(s.lines, "- "+certPlaceholder+": pass --certificate-path to name the certificate file.")
	case p.CertificateExists:
		s.lines = append(s.lines, "- "+safeText(p.CertificatePath)+": exists, so apply will not overwrite it.")
	default:
		s.lines = append(s.lines, "- "+safeText(p.CertificatePath)+": apply would create it (certificate and private key, mode 0600).")
	}
	s.lines = append(s.lines, "- "+safeText(filepath.Join(p.Config.StateDir, "setup-journal.json"))+": apply would create it to resume after an interruption.")
	return s
}
