package incident

import (
	"os/exec"
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
