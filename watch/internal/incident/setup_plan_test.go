package incident

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	setupSub  = "44444444-4444-4444-8444-444444444444"
	setupNS   = "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/contoso"
	setupWS   = "/subscriptions/" + setupSub + "/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/ws-main"
	setupWSAx = "/subscriptions/" + setupSub + "/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/ws-aux"
)

func TestSetupParseNamespaceIDAcceptsOnlyServiceBusNamespaces(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		id, err := ParseNamespaceID(setupNS)
		if err != nil {
			t.Fatal(err)
		}
		if id.Subscription != setupSub || id.ResourceGroup != "rg-bus" || id.Name != "contoso" || id.String() != setupNS {
			t.Fatalf("parsed %+v (%s)", id, id.String())
		}
	})
	t.Run("fixed-segment-case-is-normalized", func(t *testing.T) {
		id, err := ParseNamespaceID("/subscriptions/" + setupSub + "/resourcegroups/rg-bus/providers/microsoft.servicebus/NAMESPACES/contoso")
		if err != nil {
			t.Fatal(err)
		}
		if id.String() != setupNS {
			t.Fatalf("canonical form %q, want %q", id.String(), setupNS)
		}
	})
	invalid := []struct{ name, in string }{
		{"empty", ""},
		{"relative", "subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/contoso"},
		{"double-leading-slash", "/" + setupNS},
		{"trailing-slash", setupNS + "/"},
		{"wrong-provider", "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.Web/sites/contoso"},
		{"workspace-id", setupWS},
		{"entity-suffix", setupNS + "/queues/alerts"},
		{"non-uuid-subscription", "/subscriptions/s/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/contoso"},
		{"empty-resource-group", "/subscriptions/" + setupSub + "/resourceGroups//providers/Microsoft.ServiceBus/namespaces/contoso"},
		{"empty-name", "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/"},
		{"dot-dot-name", "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/.."},
		{"quote-escape-name", "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/x';touch pwned;'"},
		{"semicolon-resource-group", "/subscriptions/" + setupSub + "/resourceGroups/rg;bus/providers/Microsoft.ServiceBus/namespaces/contoso"},
		{"space-in-name", "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/my ns"},
		{"newline-in-name", "/subscriptions/" + setupSub + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/contoso\nx"},
		{"query-string", setupNS + "?api-version=1"},
	}
	for _, tc := range invalid {
		t.Run("rejects-"+tc.name, func(t *testing.T) {
			_, err := ParseNamespaceID(tc.in)
			if err == nil {
				t.Fatalf("%q accepted", tc.in)
			}
			if tc.in != "" && strings.Contains(err.Error(), tc.in) {
				t.Fatalf("error echoes the raw input: %v", err)
			}
		})
	}
}

func TestSetupParseWorkspaceIDAcceptsOnlyLogAnalyticsWorkspaces(t *testing.T) {
	id, err := ParseWorkspaceID(setupWS)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subscription != setupSub || id.ResourceGroup != "rg-logs" || id.Name != "ws-main" || id.String() != setupWS {
		t.Fatalf("parsed %+v (%s)", id, id.String())
	}
	for name, in := range map[string]string{
		"namespace-id":  setupNS,
		"trailing":      setupWS + "/",
		"missing-name":  "/subscriptions/" + setupSub + "/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/",
		"bad-sub":       "/subscriptions/nope/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/ws-main",
		"quote-escape":  "/subscriptions/" + setupSub + "/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/w'x",
		"extra-segment": setupWS + "/savedSearches/s",
	} {
		t.Run("rejects-"+name, func(t *testing.T) {
			if _, err := ParseWorkspaceID(in); err == nil {
				t.Fatalf("%q accepted", in)
			}
		})
	}
}

func TestSetupEntityScopeIsTheExactQueueOrTopicSubscription(t *testing.T) {
	ns, err := ParseNamespaceID(setupNS)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		c    Config
		want string // empty means refused
	}{
		{"queue", Config{Queue: "alerts"}, setupNS + "/queues/alerts"},
		{"queue-with-dot-and-underscore", Config{Queue: "alerts-v2.prod_1"}, setupNS + "/queues/alerts-v2.prod_1"},
		{"topic-subscription", Config{Topic: "alerts-topic", Subscription: "incident-worker"}, setupNS + "/topics/alerts-topic/subscriptions/incident-worker"},
		{"neither", Config{}, ""},
		{"quote-escape-queue", Config{Queue: "alerts'; touch x"}, ""},
		{"path-climb-queue", Config{Queue: "a/../b"}, ""},
		{"space-in-subscription", Config{Topic: "t", Subscription: "a b"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EntityScope(ns, tc.c)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("scope %q accepted", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("scope %q (%v), want %q", got, err, tc.want)
			}
			if got == ns.String() || got == setupNS+"/topics/"+tc.c.Topic {
				t.Fatalf("scope %q is broader than the entity", got)
			}
		})
	}
}

func TestSetupDedupeWorkspacesComparesIDsWithoutCase(t *testing.T) {
	parse := func(s string) ARMID {
		t.Helper()
		id, err := ParseWorkspaceID(s)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	main := parse(setupWS)
	upper := parse(strings.ToUpper(setupWS))
	other := parse(setupWSAx)
	got := DedupeWorkspaces([]ARMID{main, other, upper})
	if len(got) != 2 || got[0].String() != main.String() || got[1].String() != other.String() {
		t.Fatalf("de-duplicated to %+v, want the first main workspace then aux", got)
	}
	if got := DedupeWorkspaces(nil); len(got) != 0 {
		t.Fatalf("empty input produced %+v", got)
	}
}

func TestSetupInSubscriptionIgnoresCase(t *testing.T) {
	ws, err := ParseWorkspaceID(setupWS)
	if err != nil {
		t.Fatal(err)
	}
	if !ws.InSubscription(strings.ToUpper(setupSub)) {
		t.Fatal("same subscription in another case reported as different")
	}
	if ws.InSubscription("55555555-5555-4555-8555-555555555555") {
		t.Fatal("different subscription reported as the same")
	}
}

const wantFillLabel = "# fill placeholders before running"

// azLines returns the rendering's az command lines, trimmed, each with the line
// that precedes it.
func azLines(out string) (cmds, before []string) {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "az ") {
			prev := ""
			if i > 0 {
				prev = strings.TrimSpace(lines[i-1])
			}
			cmds, before = append(cmds, l), append(before, prev)
		}
	}
	return cmds, before
}

// guidanceOnlyPlan is a new-identity plan where nothing was discovered, so the
// renderer itself supplies placeholder tokens.
func guidanceOnlyPlan() SetupPlan {
	return SetupPlan{
		Config: Config{Queue: "alerts"}, ConfigPath: "/etc/cenci/incidents.json", Identity: SetupIdentityNew, DisplayName: "cenci-incident-worker",
		Workspaces: []WorkspaceTarget{{UUID: "00000000-0000-0000-0000-000000000000"}},
	}
}

func TestSetupRenderLeavesRendererPlaceholdersUnquotedAndLabeled(t *testing.T) {
	out, err := guidanceOnlyPlan().Render()
	if err != nil {
		t.Fatal(err)
	}
	cmds, before := azLines(out)
	withPlaceholder := 0
	for i, c := range cmds {
		if !placeholderToken.MatchString(c) {
			continue
		}
		withPlaceholder++
		if strings.Contains(c, "'<") || strings.Contains(c, ">'") {
			t.Fatalf("placeholder is shell-quoted, so an unfilled copy would run with the literal text: %s", c)
		}
		if before[i] != wantFillLabel {
			t.Fatalf("command with a placeholder is not labeled %q (preceded by %q): %s", wantFillLabel, before[i], c)
		}
	}
	if withPlaceholder < 5 {
		t.Fatalf("want the identity, certificate and both role commands to carry placeholders, got %d:\n%s", withPlaceholder, out)
	}
	for _, want := range []string{
		"--assignee-object-id <object-id> ",
		"--scope <service-bus-namespace-id>/queues/alerts",
		"--scope <workspace-resource-id>",
		"az ad sp create --id <app-id>",
		"--cert @<public-certificate-file> --append",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendering lacks the unquoted %q:\n%s", want, out)
		}
	}
}

func TestSetupRenderQuotesRealValuesThatLookLikePlaceholders(t *testing.T) {
	t.Run("display-name", func(t *testing.T) {
		p := guidanceOnlyPlan()
		p.DisplayName = "<object-id>"
		out, err := p.Render()
		if err != nil {
			t.Fatal(err)
		}
		cmds, before := azLines(out)
		for i, c := range cmds {
			if strings.HasPrefix(c, "az ad app create") {
				if c != "az ad app create --display-name '<object-id>' --query appId -o tsv" {
					t.Fatalf("a display name shaped like a placeholder must be one quoted word: %s", c)
				}
				if before[i] == wantFillLabel {
					t.Fatalf("a quoted real value is not a placeholder, yet the command is labeled: %s", c)
				}
				return
			}
		}
		t.Fatalf("no app create command in:\n%s", out)
	})
	t.Run("client-id", func(t *testing.T) {
		p := guidanceOnlyPlan()
		p.Identity, p.ClientID, p.DisplayName = SetupIdentityExisting, "<app-id>", ""
		out, err := p.Render()
		if err != nil {
			t.Fatal(err)
		}
		requireRenders := []string{"az ad sp show --id '<app-id>' --query id -o tsv", "az ad app credential reset --id '<app-id>' "}
		for _, want := range requireRenders {
			if !strings.Contains(out, want) {
				t.Fatalf("rendering lacks the quoted %q:\n%s", want, out)
			}
		}
	})
	t.Run("discovered-object-id-and-scope", func(t *testing.T) {
		p := guidanceOnlyPlan()
		p.ObjectID = "<tenant-id>"
		out, err := p.Render()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "--assignee-object-id '<tenant-id>' ") {
			t.Fatalf("a known object ID is a real value and must be quoted:\n%s", out)
		}
	})
}

// An unfilled command must fail at the shell instead of running with the
// literal token, while the same command with every token filled runs.
func TestSetupUnfilledPlaceholderCommandFailsAtTheShell(t *testing.T) {
	out, err := guidanceOnlyPlan().Render()
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, command string) (string, error) {
		t.Helper()
		cmd := exec.Command("sh", "-c", "az() { echo RAN; }; "+command)
		cmd.Dir = t.TempDir()
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	cmds, _ := azLines(out)
	checked := 0
	for _, c := range cmds {
		if !placeholderToken.MatchString(c) {
			continue
		}
		checked++
		t.Run(c, func(t *testing.T) {
			if got, err := run(t, c); err == nil || strings.Contains(got, "RAN") {
				t.Fatalf("unfilled command ran (err=%v, output %q)", err, got)
			}
			filled := placeholderToken.ReplaceAllString(c, "x")
			if got, err := run(t, filled); err != nil || !strings.Contains(got, "RAN") {
				t.Fatalf("filled command did not run (err=%v, output %q): %s", err, got, filled)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no placeholder command was rendered")
	}
}

// Only a managed identity is pinned to the principal the roles go to; the other
// identity modes are already named by their clientId.
func TestSetupProposedAuthPinsOnlyTheManagedPrincipal(t *testing.T) {
	cases := []struct {
		name string
		plan SetupPlan
		want AuthConfig
	}{
		{"managed-with-an-object-id", SetupPlan{Identity: SetupIdentityManaged, Tenant: testTenantID, ObjectID: applyObjectID},
			AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID, PrincipalObjectID: applyObjectID}},
		{"managed-without-an-object-id-uses-the-placeholder", SetupPlan{Identity: SetupIdentityManaged, Tenant: testTenantID},
			AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID, PrincipalObjectID: principalPlaceholder}},
		{"existing-leaves-it-empty", SetupPlan{Identity: SetupIdentityExisting, Tenant: testTenantID, ClientID: testClientID, ObjectID: applyObjectID, CertificatePath: "/etc/cenci/w.pem"},
			AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: testClientID, CertificatePath: "/etc/cenci/w.pem"}},
		{"new-leaves-it-empty", SetupPlan{Identity: SetupIdentityNew, Tenant: testTenantID, ObjectID: applyObjectID, CertificatePath: "/etc/cenci/w.pem"},
			AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: appIDPlaceholder, CertificatePath: "/etc/cenci/w.pem"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.plan.ProposedAuth(); got != tc.want {
				t.Fatalf("proposed auth %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The wait rule the plan shows is the one apply follows: it depends on an attempt
// in this run or in the last 10 minutes, not only on what this run created.
func TestSetupRenderApplyStatesTheRecentAttemptWaitRule(t *testing.T) {
	out, err := newApplyWorld(t).plan.RenderApply(false)
	if err != nil {
		t.Fatal(err)
	}
	const old = "when it created role assignments it waits up to 10 minutes for them to propagate"
	if strings.Contains(out, old) {
		t.Fatalf("the plan still states the created-only wait rule:\n%s", out)
	}
	for _, want := range []string{"waits up to 10 minutes", "this run or a run in the last 10 minutes", "attempted a role assignment"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the plan lacks %q:\n%s", want, out)
		}
	}
}

func TestSetupShellQuoteExpandsToExactlyTheValue(t *testing.T) {
	for name, value := range map[string]string{
		"plain":         "alerts",
		"empty":         "",
		"space":         "my resource group",
		"single-quote":  "it's",
		"only-quotes":   "'''",
		"command-subst": "$(touch pwned)",
		"backtick":      "`touch pwned`",
		"semicolon":     "a;b",
		"newline":       "a\nb",
		"redirect":      "<object-id>",
		"glob":          "*",
		"backslash":     `a\b`,
		"unicode":       "café",
	} {
		t.Run(name, func(t *testing.T) {
			q := ShellQuote(value)
			if q == "" {
				t.Fatal("quoted value is empty; an empty value must still be one word")
			}
			out, err := exec.Command("sh", "-c", "printf %s "+q).CombinedOutput()
			if err != nil {
				t.Fatalf("sh rejected %q: %v %s", q, err, out)
			}
			if string(out) != value {
				t.Fatalf("shell expanded %q to %q, want %q", q, out, value)
			}
		})
	}
}

// ---- apply for a new identity (#1389) ---------------------------------------

const newIdentityCertPath = "/etc/cenci/worker.pem"

// discoveredNewIdentityPlan is a fully discovered --identity new plan: the app
// does not exist yet, so there is no client or object ID.
func discoveredNewIdentityPlan(t *testing.T, certExists bool) SetupPlan {
	t.Helper()
	cfg := writeSetupConfig(t, func(c *Config) { c.StateDir = filepath.Join(t.TempDir(), "state") }, "")
	return asNewIdentity(newApplyPlan(t, cfg, ""), newIdentityCertPath, certExists)
}

func TestSetupApplyBlockersForANewIdentity(t *testing.T) {
	for _, exists := range []bool{false, true} {
		blockers := discoveredNewIdentityPlan(t, exists).ApplyBlockers()
		if len(blockers) != 0 {
			t.Fatalf("a discovered new-identity plan (certificate file present=%v) has blockers: %q", exists, blockers)
		}
	}

	cases := []struct {
		name, flag string
		edit       func(p *SetupPlan)
	}{
		{"certificate-path-missing", "--certificate-path", func(p *SetupPlan) { p.CertificatePath = "" }},
		{"tenant-unknown", "--tenant", func(p *SetupPlan) { p.Tenant = "" }},
		{"subscription-unknown", "--subscription", func(p *SetupPlan) { p.Subscription = "" }},
		{"identity-unknown", "--identity", func(p *SetupPlan) { p.Identity = "service-principal" }},
		{"namespace-unresolved", "--service-bus-namespace-id", func(p *SetupPlan) { p.Namespace, p.NamespaceNote = nil, "not found" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := discoveredNewIdentityPlan(t, false)
			c.edit(&p)
			if got := strings.Join(p.ApplyBlockers(), "\n"); !strings.Contains(got, c.flag) {
				t.Fatalf("blockers %q do not name %q", got, c.flag)
			}
		})
	}

	t.Run("a-client-id-for-the-recorded-app-is-not-a-blocker", func(t *testing.T) {
		p := discoveredNewIdentityPlan(t, false)
		p.ClientID = newAppClientID
		if got := p.ApplyBlockers(); len(got) != 0 {
			t.Fatalf("blockers %q", got)
		}
	})
	t.Run("no-blocker-mentions-the-retired-restriction", func(t *testing.T) {
		p := discoveredNewIdentityPlan(t, false)
		p.CertificatePath = ""
		if got := strings.Join(p.ApplyBlockers(), "\n"); strings.Contains(got, "1389") || strings.Contains(got, "not available yet") {
			t.Fatalf("blockers still carry the #1389 restriction: %q", got)
		}
	})
	t.Run("existing-identity-still-needs-its-principal", func(t *testing.T) {
		p := newApplyPlan(t, writeSetupConfig(t, nil, ""), newIdentityCertPath)
		p.ObjectID = ""
		if got := strings.Join(p.ApplyBlockers(), "\n"); !strings.Contains(got, "--client-id") {
			t.Fatalf("blockers %q do not name --client-id", got)
		}
	})
}

func TestSetupProposedAuthOfANewIdentityUsesItsKnownClientID(t *testing.T) {
	p := discoveredNewIdentityPlan(t, false)
	if got := p.ProposedAuth(); got.ClientID != appIDPlaceholder || got.CertificatePath != newIdentityCertPath {
		t.Fatalf("an app that does not exist yet keeps the %s placeholder, got %+v", appIDPlaceholder, got)
	}
	p.ClientID = newAppClientID
	want := AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: newAppClientID, CertificatePath: newIdentityCertPath}
	if got := p.ProposedAuth(); got != want {
		t.Fatalf("proposed auth %+v, want %+v", got, want)
	}
}

func lineWith(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestSetupRenderApplyForANewIdentity(t *testing.T) {
	const nonce = "cenci-incident-worker-0123456789abcdef"
	render := func(t *testing.T, p SetupPlan) string {
		t.Helper()
		p.DisplayName = nonce
		out, err := p.RenderApply(false)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("create", func(t *testing.T) {
		out := render(t, discoveredNewIdentityPlan(t, false))
		for _, want := range []string{"Identity:     new", `application: create "` + nonce + `"`, "service principal: create", "certificate credential:", "--append"} {
			if !strings.Contains(out, want) {
				t.Fatalf("the apply plan lacks %q:\n%s", want, out)
			}
		}
		file := lineWith(out, newIdentityCertPath+":")
		for _, want := range []string{"created", "0600", "never overwritten"} {
			if !strings.Contains(file, want) {
				t.Fatalf("the certificate file line %q lacks %q:\n%s", file, want, out)
			}
		}
		if strings.Contains(file, "read only") {
			t.Fatalf("a file apply creates is not read only: %q", file)
		}
		if strings.Contains(out, "Principal:    (object ID)") {
			t.Fatalf("the plan prints an empty principal for an identity that does not exist yet:\n%s", out)
		}
	})
	t.Run("adopt", func(t *testing.T) {
		p := discoveredNewIdentityPlan(t, false)
		p.ClientID = newAppClientID
		out := render(t, p)
		for _, want := range []string{`application: adopt ` + newAppClientID + ` ("` + nonce + `")`, "Client ID:    " + newAppClientID, `"clientId": "` + newAppClientID + `"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("the apply plan lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "application: create") || strings.Contains(out, appIDPlaceholder) {
			t.Fatalf("an adopted application is not created, and its ID is known:\n%s", out)
		}
	})
	t.Run("existing-certificate-file", func(t *testing.T) {
		out := render(t, discoveredNewIdentityPlan(t, true))
		file := lineWith(out, newIdentityCertPath+":")
		if !strings.Contains(file, "exists") || !strings.Contains(file, "never overwritten") || strings.Contains(file, "created") {
			t.Fatalf("the certificate file line %q must say the file exists and is never overwritten:\n%s", file, out)
		}
		if !strings.Contains(out, "certificate credential:") {
			t.Fatalf("an existing file's public certificate is still registered:\n%s", out)
		}
	})
	// Q11 and #1402 together: a new identity's verify waits when this run created
	// any part of the identity or a role, or a role assignment was attempted recently.
	t.Run("wait-rule-names-identity-creates-and-recent-attempts", func(t *testing.T) {
		out := render(t, discoveredNewIdentityPlan(t, false))
		wait := lineWith(out, "Afterwards setup runs")
		for _, want := range []string{"waits up to 10 minutes", "the identity and role assignments", "this run created any of them", "this run or a run in the last 10 minutes", "attempted a role assignment"} {
			if !strings.Contains(wait, want) {
				t.Fatalf("the wait line %q lacks %q:\n%s", wait, want, out)
			}
		}
	})
	t.Run("existing-identity-is-unchanged", func(t *testing.T) {
		p := newApplyPlan(t, writeSetupConfig(t, nil, ""), newIdentityCertPath)
		out, err := p.RenderApply(false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "read only; the certificate and private key are never created or changed") ||
			strings.Contains(out, "application:") || strings.Contains(out, "service principal:") {
			t.Fatalf("an existing identity's plan must not describe identity creation:\n%s", out)
		}
	})
}
