package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/incident"
)

const (
	setupSubscription      = "44444444-4444-4444-8444-444444444444"
	setupOtherSubscription = "55555555-5555-4555-8555-555555555555"
	setupObjectID          = "33333333-3333-4333-8333-333333333333"
	setupConfigWorkspace   = "00000000-0000-0000-0000-000000000000"
	setupSecondWorkspace   = "66666666-6666-4666-8666-666666666666"
	setupOtherTenant       = "77777777-7777-4777-8777-777777777777"

	// Built-in role definition IDs (Azure built-in roles reference).
	roleServiceBusReceiver = "4f6d3b9b-027b-4f4c-9142-0e5a2a2247e0"
	roleLogAnalyticsReader = "73c42c96-874c-492b-b04d-ab87d138a893"

	setupTokenStdout = "eyJ0eXAiOiJKV1QifQ.c2VudGluZWwtc3Rkb3V0.sig-stdout-sentinel"
	setupTokenStderr = "eyJ0eXAiOiJKV1QifQ.c2VudGluZWwtc3RkZXJy.sig-stderr-sentinel"
)

var (
	setupNamespaceARM = armNamespace(setupSubscription, "rg-bus", "contoso")
	setupWorkspaceARM = armWorkspace(setupSubscription, "rg-logs", "ws-main")
	setupAuxARM       = armWorkspace(setupSubscription, "rg-logs", "ws-aux")

	// The az subcommands the preview may run (P16).
	readOnlyAz = []string{
		"account show",
		"servicebus namespace list", "servicebus namespace show",
		"servicebus queue show", "servicebus topic subscription show",
		"monitor log-analytics workspace list", "monitor log-analytics workspace show",
		"ad sp show", "role assignment list",
	}

	placeholderPattern = regexp.MustCompile(`<[a-z][a-z0-9-]*>`)
	stepHeaderPattern  = regexp.MustCompile(`(?m)^\s*(\d+)\. \S`)
)

func armNamespace(sub, group, name string) string {
	return "/subscriptions/" + sub + "/resourceGroups/" + group + "/providers/Microsoft.ServiceBus/namespaces/" + name
}

func armWorkspace(sub, group, name string) string {
	return "/subscriptions/" + sub + "/resourceGroups/" + group + "/providers/Microsoft.OperationalInsights/workspaces/" + name
}

// azRule answers one family of az invocations: the argv must start with prefix
// and contain needle. The first matching rule wins; an unmatched call exits 99.
type azRule struct {
	prefix, needle string
	code           int
	stdout, stderr string
}

// setupWorld is a cliWorld whose az is a scripted fake: it appends its argv to
// the trace, picks a rule by argv, and prints that rule's fixture files.
type setupWorld struct {
	*cliWorld
	rules []azRule
	cert  string // worker certificate destination; absent unless a test creates it
}

func newSetupWorld(t *testing.T) *setupWorld {
	t.Helper()
	return &setupWorld{cliWorld: newCLIWorld(t), cert: filepath.Join(t.TempDir(), "worker.pem")}
}

func (w *setupWorld) on(prefix, needle string, code int, stdout, stderr string) {
	rule := azRule{prefix, needle, code, stdout, stderr}
	for i, r := range w.rules {
		if r.prefix == prefix && r.needle == needle {
			w.rules[i] = rule
			return
		}
	}
	w.rules = append(w.rules, rule)
}

func (w *setupWorld) ok(prefix, needle, stdout string) { w.on(prefix, needle, 0, stdout, "") }

// discoverAll scripts a fully readable Azure tenant for the default config.
func (w *setupWorld) discoverAll() {
	w.ok("account show", "", fmt.Sprintf(`{"environmentName":"AzureCloud","id":%q,"tenantId":%q,"user":{"name":"operator@contoso.com","type":"user"}}`, setupSubscription, cliTenantID))
	ns := fmt.Sprintf(`{"id":%q,"name":"contoso","location":"westeurope","serviceBusEndpoint":"https://contoso.servicebus.windows.net:443/"}`, setupNamespaceARM)
	w.ok("servicebus namespace list", "", "["+ns+"]")
	w.ok("servicebus namespace show", "", ns)
	w.ok("servicebus queue show", "", fmt.Sprintf(`{"id":%q,"name":"alerts"}`, setupNamespaceARM+"/queues/alerts"))
	w.ok("servicebus topic subscription show", "", fmt.Sprintf(`{"id":%q,"name":"incident-worker"}`, setupNamespaceARM+"/topics/alerts-topic/subscriptions/incident-worker"))
	w.ok("monitor log-analytics workspace list", "", "["+workspaceJSON(setupConfigWorkspace, setupWorkspaceARM, "ws-main")+","+workspaceJSON(setupSecondWorkspace, setupAuxARM, "ws-aux")+"]")
	w.ok("monitor log-analytics workspace show", "ws-main", workspaceJSON(setupConfigWorkspace, setupWorkspaceARM, "ws-main"))
	w.ok("monitor log-analytics workspace show", "ws-aux", workspaceJSON(setupSecondWorkspace, setupAuxARM, "ws-aux"))
	w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"displayName":"cenci-incident-worker","id":%q,"servicePrincipalType":"Application"}`, cliClientID, setupObjectID))
	w.ok("role assignment list", "", fmt.Sprintf(`[{"principalId":%q,"roleDefinitionName":"Contributor","scope":"/subscriptions/%s/resourceGroups/rg-app"}]`, setupObjectID, setupSubscription))
}

func workspaceJSON(customerID, arm, name string) string {
	return fmt.Sprintf(`{"customerId":%q,"id":%q,"location":"westeurope","name":%q}`, customerID, arm, name)
}

// installAz writes the scripted az and its fixtures. Rules are re-read on every
// call, so a test may change them between runs.
func (w *setupWorld) installAz(t *testing.T) {
	t.Helper()
	fix := filepath.Join(w.bin, "az-fixtures")
	if err := os.MkdirAll(fix, 0700); err != nil {
		t.Fatal(err)
	}
	var rules strings.Builder
	for i, r := range w.rules {
		if strings.Contains(r.prefix+r.needle, "|") {
			t.Fatalf("rule %q|%q contains the separator", r.prefix, r.needle)
		}
		fmt.Fprintf(&rules, "%s|%s|r%d\n", r.prefix, r.needle, i)
		for ext, body := range map[string]string{"out": r.stdout, "err": r.stderr, "code": strconv.Itoa(r.code)} {
			if err := os.WriteFile(filepath.Join(fix, "r"+strconv.Itoa(i)+"."+ext), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(fix, "rules"), []byte(rules.String()), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
FIX='` + fix + `'
printf '%s\n' "$*" >> '` + w.azTrace + `'
key=unmatched
while IFS='|' read -r prefix needle k; do
  case "$*" in
    "$prefix"*)
      case "$*" in
        *"$needle"*) key=$k; break;;
      esac;;
  esac
done < "$FIX/rules"
[ -f "$FIX/$key.out" ] && cat "$FIX/$key.out"
[ -f "$FIX/$key.err" ] && cat "$FIX/$key.err" >&2
code=99
[ -f "$FIX/$key.code" ] && code=$(cat "$FIX/$key.code")
exit "$code"
`
	if err := os.WriteFile(filepath.Join(w.bin, "az"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

// setupEnv is the child environment: the fakes first on PATH, no ambient Azure
// variable, and HOME and every config/state/cache directory isolated.
func (w *setupWorld) setupEnv(t *testing.T) []string {
	t.Helper()
	return append(w.env(), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir(), "XDG_STATE_HOME="+t.TempDir(), "XDG_CACHE_HOME="+t.TempDir(), "AZURE_CONFIG_DIR="+t.TempDir())
}

// resolveOnPath is exec.LookPath against the child's own PATH.
func resolveOnPath(env []string, name string) string {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.Mode().IsRegular() && fi.Mode()&0111 != 0 {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

type setupResult struct {
	code           int
	stdout, stderr string
}

func (w *setupWorld) execSetup(t *testing.T, env []string, args ...string) setupResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Env = env
	// A stdin that is an open pipe and never written or closed: a child that
	// inherited it would block on a read or see a pipe, not the null device.
	stdinR, stdinW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	defer stdinR.Close()
	defer stdinW.Close()
	cmd.Stdin = stdinR
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := setupResult{stdout: stdout.String(), stderr: stderr.String()}
	if ctx.Err() != nil {
		t.Fatalf("cenci %v did not exit; stderr so far: %s", args, res.stderr)
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		res.code = exit.ExitCode()
	}
	return res
}

// runSetup runs the built binary against the scripted az, and first proves the
// az on the child's PATH is the fake, not an ambient real one.
func (w *setupWorld) runSetup(t *testing.T, args ...string) setupResult {
	t.Helper()
	w.installAz(t)
	env := w.setupEnv(t)
	if got := resolveOnPath(env, "az"); got != filepath.Join(w.bin, "az") {
		t.Fatalf("az resolves to %q, want the fake %q", got, filepath.Join(w.bin, "az"))
	}
	return w.execSetup(t, env, args...)
}

// writeConfig writes a setup-time config: not yet enabled and without an auth
// section unless edit gives one.
func (w *setupWorld) writeConfig(t *testing.T, edit func(*incident.Config)) string {
	t.Helper()
	cfg := incident.Config{StateDir: w.stateDir, Namespace: "contoso.servicebus.windows.net", Queue: "alerts", Concurrency: 1, TimeoutSeconds: 30, MaxAttempts: 2, BudgetUSD: 1,
		Agent:     incident.ContainerConfig{Runtime: "podman", Image: "tools:reviewed", APIKeyEnv: "ANTHROPIC_API_KEY", Network: "incident-internal", ProxyURL: "http://proxy:3128"},
		Resources: []incident.Resource{w.resource(t, "app", "owner/repo", setupConfigWorkspace)}}
	if edit != nil {
		edit(&cfg)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if cfg.Auth == nil {
		delete(doc, "auth")
	}
	if b, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (w *setupWorld) resource(t *testing.T, name, repo, workspace string) incident.Resource {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return incident.Resource{ID: "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/" + name, Repo: repo, Dir: dir, Base: "main", Environment: "production",
		Deployments: []incident.Deployment{{Commit: strings.Repeat("a", 40), From: time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)}},
		Workspace:   workspace, Query: "AppExceptions | take 10", Workflow: "Use cenci stages.",
		Runbooks: []string{"RUNBOOK.md"}, Regression: []string{"go", "test", "./..."}, Checks: [][]string{{"go", "test", "./..."}}, TestPaths: []string{"*_test.go"}}
}

// args is a full preview invocation with every optional flag supplied.
func (w *setupWorld) args(cfg string, extra ...string) []string {
	return append([]string{"incident", "setup", "--config", cfg, "--tenant", cliTenantID, "--subscription", setupSubscription, "--certificate-path", w.cert}, extra...)
}

func existingIdentity() []string {
	return []string{"--identity", "existing", "--client-id", cliClientID}
}

func (w *setupWorld) traceLines(t *testing.T) []string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(readCLITrace(t, w.azTrace), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func isReadOnlyAz(line string) bool {
	for _, p := range readOnlyAz {
		if line == p || strings.HasPrefix(line, p+" ") {
			return true
		}
	}
	return false
}

func hasTrace(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// azCommands returns the copyable az commands in a rendering: one per line, with
// shell quoting removed so the assertions hold for any quoting style.
func azCommands(out string) []string {
	var cmds []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.ReplaceAll(strings.TrimSpace(l), "'", "")
		if strings.HasPrefix(l, "az ") {
			cmds = append(cmds, l)
		}
	}
	return cmds
}

func roleCmd(object, role, scope string) string {
	return "az role assignment create --assignee-object-id " + object + " --assignee-principal-type ServicePrincipal --role " + role + " --scope " + scope
}

func roleCommands(out string) []string {
	var cmds []string
	for _, c := range azCommands(out) {
		if strings.HasPrefix(c, "az role assignment create") {
			cmds = append(cmds, c)
		}
	}
	sort.Strings(cmds)
	return cmds
}

func sorted(in ...string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func requireEqualLists(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n got  %q\n want %q", what, got, want)
	}
}

func (r setupResult) requireExit(t *testing.T, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
}

func requireContains(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("%s lacks %q:\n%s", what, want, text)
		}
	}
}

func requireLacks(t *testing.T, what, text string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Fatalf("%s must not contain %q:\n%s", what, u, text)
		}
	}
}

// renderedSteps splits a rendering into its numbered steps and checks they are
// numbered 1..n.
func renderedSteps(t *testing.T, out string) []string {
	t.Helper()
	locs := stepHeaderPattern.FindAllStringSubmatchIndex(out, -1)
	var steps []string
	for i, loc := range locs {
		if n, _ := strconv.Atoi(out[loc[2]:loc[3]]); n != i+1 {
			t.Fatalf("step %d is numbered %d:\n%s", i+1, n, out)
		}
		end := len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		steps = append(steps, out[loc[0]:end])
	}
	return steps
}

// proposedAuth decodes the first auth object in a rendering.
func proposedAuth(t *testing.T, out string) incident.AuthConfig {
	t.Helper()
	i := strings.Index(out, `"mode"`)
	if i < 0 {
		t.Fatalf("no proposed auth JSON in:\n%s", out)
	}
	var a incident.AuthConfig
	if err := json.NewDecoder(strings.NewReader(out[strings.LastIndex(out[:i], "{"):])).Decode(&a); err != nil {
		t.Fatalf("proposed auth JSON does not decode: %v\n%s", err, out)
	}
	return a
}

var oldTime = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// ageTree gives every entry under root an old modification time, so a rewrite
// or a touch is visible even on a coarse clock.
func ageTree(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return
	}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, oldTime, oldTime)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// snapshotPaths records mode, size, mtime and content hash of everything under
// each path, and which paths are absent.
func snapshotPaths(t *testing.T, paths ...string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range paths {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			fmt.Fprintf(&b, "absent %s\n", p)
			continue
		}
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			sum := ""
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				sum = fmt.Sprintf("%x", sha256.Sum256(data))
			}
			fmt.Fprintf(&b, "%s %v %d %d %s\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano(), sum)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

const fakePrivateKeyPEM = "-----BEGIN PRIVATE KEY-----\nc2VudGluZWwta2V5LW1hdGVyaWFs\n-----END PRIVATE KEY-----\n"

func TestIncidentSetupPreviewInvokesOnlyReadOnlyAzAndChangesNoFile(t *testing.T) {
	t.Run("positive-control", func(t *testing.T) {
		// The snapshot comparison must be able to fail: a touched file, a rewrite
		// with identical bytes, and a created file each change it; and the az
		// allowlist must refuse mutating and login subcommands.
		dir := t.TempDir()
		file := filepath.Join(dir, "config.json")
		if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		ageTree(t, dir)
		base := snapshotPaths(t, dir)
		if snapshotPaths(t, dir) != base {
			t.Fatal("snapshot is not stable across two reads")
		}
		if err := os.Chtimes(file, time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if snapshotPaths(t, dir) == base {
			t.Fatal("snapshot missed a touched file")
		}
		ageTree(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "created"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if snapshotPaths(t, dir) == base {
			t.Fatal("snapshot missed a created file")
		}
		for _, line := range []string{"role assignment create --role x", "ad app create --display-name x", "ad sp create-for-rbac", "login", "account set --subscription x", "ad app credential reset --id x"} {
			if isReadOnlyAz(line) {
				t.Fatalf("allowlist accepts %q", line)
			}
		}
	})
	for _, tc := range []struct {
		name        string
		stateExists bool
		certExists  bool
	}{{"state-and-certificate-exist", true, true}, {"state-and-certificate-absent", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSetupWorld(t)
			w.discoverAll()
			stateDir := w.stateDir
			if !tc.stateExists {
				stateDir = filepath.Join(w.stateDir, "not-created-yet")
			} else if err := os.WriteFile(filepath.Join(stateDir, "incidents.json"), []byte(`{"version":1,"incidents":{}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.certExists {
				if err := os.WriteFile(w.cert, []byte(fakePrivateKeyPEM), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := w.writeConfig(t, func(c *incident.Config) { c.StateDir = stateDir })
			watched := []string{filepath.Dir(cfg), filepath.Dir(w.cert), stateDir}
			for _, p := range watched {
				ageTree(t, p)
			}
			before := snapshotPaths(t, watched...)

			res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
			res.requireExit(t, 0)

			if after := snapshotPaths(t, watched...); after != before {
				t.Fatalf("preview changed the config, certificate destination or state directory:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if tc.certExists {
				requireContains(t, "files listing", res.stdout, "exists, so apply will not overwrite it")
				requireLacks(t, "files listing", res.stdout, "apply would create it (certificate")
			} else {
				requireContains(t, "files listing", res.stdout, "apply would create it (certificate and private key, mode 0600)")
				requireLacks(t, "files listing", res.stdout, "apply will not overwrite")
			}
			lines := w.traceLines(t)
			for _, l := range lines {
				if !isReadOnlyAz(l) {
					t.Fatalf("az ran a subcommand outside the read-only allowlist: %q", l)
				}
			}
			// Not vacuous: every read-only family the plan relies on was reached.
			for _, p := range []string{"account show", "servicebus namespace ", "servicebus queue show", "monitor log-analytics workspace ", "ad sp show", "role assignment list"} {
				if !hasTrace(lines, p) {
					t.Fatalf("expected a %q call in the trace: %q", p, lines)
				}
			}
		})
	}
}

func TestIncidentSetupPreviewRendersNumberedExplainedStepsAuthJSONAndLocalFiles(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)

	t.Run("existing-identity", func(t *testing.T) {
		res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
		res.requireExit(t, 0)
		out := res.stdout

		steps := renderedSteps(t, out)
		if len(steps) < 4 {
			t.Fatalf("want at least 4 numbered steps (prerequisites, identity, Service Bus role, Log Analytics role), got %d:\n%s", len(steps), out)
		}
		for i, s := range steps {
			explained := false
			for _, l := range strings.Split(s, "\n")[1:] {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "az ") && !strings.HasPrefix(l, "'az ") {
					explained = true
				}
			}
			if !explained {
				t.Fatalf("step %d has no plain-language explanation:\n%s", i+1, s)
			}
		}

		// P21: the prerequisites separate the two kinds of rights and the login's scope.
		lower := strings.ToLower(out)
		requireContains(t, "prerequisites", out, "Entra", "User Access Administrator", "Role Based Access Control Administrator")
		requireContains(t, "prerequisites", lower, "app registration", "used for setup only", "never written to the worker configuration")

		// P22: exact least-privilege role commands, an ID lookup and never create-for-rbac.
		requireEqualLists(t, "role commands", roleCommands(out), sorted(
			roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(setupObjectID, roleLogAnalyticsReader, setupWorkspaceARM)))
		requireContains(t, "identity lookup", strings.Join(azCommands(out), "\n"), "az ad sp show --id "+cliClientID)
		requireLacks(t, "rendering", out, "create-for-rbac", "az ad app create", "az ad sp create")

		// P17: the proposed auth JSON, then the local files apply would touch.
		wantAuth := incident.AuthConfig{Mode: incident.AuthModeCertificate, TenantID: cliTenantID, ClientID: cliClientID, CertificatePath: w.cert}
		if got := proposedAuth(t, out); got != wantAuth {
			t.Fatalf("proposed auth %+v, want %+v", got, wantAuth)
		}
		after := strings.Index(out, `"mode"`)
		for _, path := range []string{cfg, w.cert, filepath.Join(w.stateDir, "setup-journal.json")} {
			if strings.LastIndex(out, path) < after {
				t.Fatalf("local file %q is not listed after the proposed auth JSON:\n%s", path, out)
			}
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, w.cert) && strings.Contains(strings.ToLower(l), "create") {
				return
			}
		}
		t.Fatalf("the absent certificate destination is not listed as created:\n%s", out)
	})

	t.Run("new-identity", func(t *testing.T) {
		for _, name := range []string{"cenci-incident-worker", "incident-worker-prod"} {
			args := w.args(cfg, "--identity", "new")
			if name != "cenci-incident-worker" {
				args = append(args, "--display-name", name)
			}
			res := w.runSetup(t, args...)
			res.requireExit(t, 0)
			cmds := strings.Join(azCommands(res.stdout), "\n")
			requireContains(t, "identity commands", cmds, "az ad app create --display-name "+name, "az ad sp create --id ", "az ad app credential reset", "--cert @", "--append")
			requireLacks(t, "rendering", res.stdout, "create-for-rbac", "PRIVATE KEY")
			// Neither the object ID nor the app ID exists yet: placeholders stand in.
			for _, c := range roleCommands(res.stdout) {
				if !regexp.MustCompile(`--assignee-object-id <[a-z-]+> `).MatchString(c) {
					t.Fatalf("role command for a not-yet-created identity must carry a placeholder object ID: %s", c)
				}
			}
			if len(roleCommands(res.stdout)) != 2 {
				t.Fatalf("want one receiver and one reader command:\n%s", res.stdout)
			}
		}
	})

	t.Run("identity-defaults-to-new", func(t *testing.T) {
		// No --identity at all: the same commands as an explicit --identity new.
		res := w.runSetup(t, w.args(cfg)...)
		res.requireExit(t, 0)
		requireContains(t, "header", res.stdout, "Identity:     new")
		cmds := strings.Join(azCommands(res.stdout), "\n")
		requireContains(t, "identity commands", cmds, "az ad app create --display-name cenci-incident-worker", "az ad sp create --id ", "az ad app credential reset", "--append")
		requireLacks(t, "rendering", res.stdout, "create-for-rbac")
		for _, c := range roleCommands(res.stdout) {
			if !regexp.MustCompile(`--assignee-object-id <[a-z-]+> `).MatchString(c) {
				t.Fatalf("role command for a not-yet-created identity must carry a placeholder object ID: %s", c)
			}
		}
		requireContains(t, "proposed auth", res.stdout, `"mode": "`+incident.AuthModeCertificate+`"`)
	})
}

func TestIncidentSetupPreviewWithoutAzOrLoginExitsZeroWithPlaceholders(t *testing.T) {
	check := func(t *testing.T, res setupResult, prerequisite string) {
		t.Helper()
		res.requireExit(t, 0)
		requireContains(t, "stdout", strings.ToLower(res.stdout), prerequisite)
		if len(renderedSteps(t, res.stdout)) < 4 {
			t.Fatalf("guidance-only preview must still render numbered steps:\n%s", res.stdout)
		}
		if !placeholderPattern.MatchString(res.stdout) {
			t.Fatalf("undiscovered values must be <placeholder> tokens:\n%s", res.stdout)
		}
		requireContains(t, "stdout", res.stdout, cliTenantID, setupSubscription)
	}
	t.Run("az-not-on-path", func(t *testing.T) {
		w := newSetupWorld(t)
		cfg := w.writeConfig(t, nil)
		env := append(w.setupEnv(t), "PATH="+t.TempDir())
		if got := resolveOnPath(env, "az"); got != "" {
			t.Fatalf("test is not hermetic: az resolves to %q", got)
		}
		check(t, w.execSetup(t, env, w.args(cfg)...), "not found on path")
		if got := readCLITrace(t, w.azTrace); got != "" {
			t.Fatalf("az trace exists although az is absent: %s", got)
		}
	})
	t.Run("az-account-show-fails", func(t *testing.T) {
		w := newSetupWorld(t)
		installers := filepath.Join(w.bin, "installer-calls")
		for _, name := range []string{"brew", "apt", "apt-get", "dnf", "yum", "pip", "pip3", "curl", "wget", "npm", "sudo"} {
			writeCLIFake(t, filepath.Join(w.bin, name), installers, 0)
		}
		w.on("account show", "", 1, "", "ERROR: Please run 'az login' to setup account.")
		cfg := w.writeConfig(t, nil)
		check(t, w.runSetup(t, w.args(cfg)...), "az account show failed")
		lines := w.traceLines(t)
		if !hasTrace(lines, "account show") {
			t.Fatalf("expected the login probe in the trace: %q", lines)
		}
		for _, l := range lines {
			if strings.Contains(l, "login") || strings.Contains(l, "install") {
				t.Fatalf("preview ran %q", l)
			}
		}
		if got := readCLITrace(t, installers); got != "" {
			t.Fatalf("preview invoked an installer: %s", got)
		}
	})
}

func TestIncidentSetupRejectsInvalidInputBeforeAnyAzCall(t *testing.T) {
	foreignNS := armNamespace(setupSubscription, "rg-bus", "other")
	cases := []struct {
		name string
		args []string
	}{
		{"tenant-not-uuid", []string{"--tenant", "not-a-uuid"}},
		{"subscription-not-uuid", []string{"--subscription", "sub"}},
		{"client-id-not-uuid", []string{"--identity", "existing", "--client-id", "nope"}},
		{"principal-object-id-not-uuid", []string{"--identity", "managed", "--principal-object-id", "nope"}},
		{"namespace-id-malformed", []string{"--service-bus-namespace-id", "/subscriptions/x"}},
		{"namespace-id-is-a-workspace", []string{"--service-bus-namespace-id", setupWorkspaceARM}},
		{"namespace-id-names-another-namespace", []string{"--service-bus-namespace-id", foreignNS}},
		{"workspace-id-malformed", []string{"--workspace-resource-id", setupConfigWorkspace + "=/subscriptions/x"}},
		{"workspace-flag-without-uuid", []string{"--workspace-resource-id", setupWorkspaceARM}},
		{"workspace-key-not-uuid", []string{"--workspace-resource-id", "ws-main=" + setupWorkspaceARM}},
		{"workspace-uuid-matches-no-configured-workspace", []string{"--workspace-resource-id", setupSecondWorkspace + "=" + setupAuxARM}},
		{"identity-unknown", []string{"--identity", "service-principal"}},
		{"client-id-without-existing", []string{"--client-id", cliClientID}},
		{"new-with-client-id", []string{"--identity", "new", "--client-id", cliClientID}},
		{"new-with-principal-object-id", []string{"--identity", "new", "--principal-object-id", setupObjectID}},
		{"existing-without-client-id", []string{"--identity", "existing"}},
		{"existing-with-principal-object-id", []string{"--identity", "existing", "--client-id", cliClientID, "--principal-object-id", setupObjectID}},
		{"existing-with-display-name", []string{"--identity", "existing", "--client-id", cliClientID, "--display-name", "x"}},
		{"managed-with-display-name", []string{"--identity", "managed", "--display-name", "x"}},
		{"managed-with-certificate-path", []string{"--identity", "managed", "--certificate-path", "/etc/cenci/w.pem"}},
		{"certificate-path-relative", []string{"--certificate-path", "worker.pem"}},
		{"positional-argument", []string{"extra"}},
	}
	t.Run("valid-control", func(t *testing.T) {
		// The same config and a fully valid flag set must not be rejected, or the
		// exit-2 cases below would pass for the wrong reason (an unknown verb).
		w := newSetupWorld(t)
		w.discoverAll()
		w.runSetup(t, w.args(w.writeConfig(t, nil), existingIdentity()...)...).requireExit(t, 0)
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newSetupWorld(t)
			w.discoverAll()
			cfg := w.writeConfig(t, nil)
			res := w.runSetup(t, append([]string{"incident", "setup", "--config", cfg}, tc.args...)...)
			res.requireExit(t, 2)
			hint := strings.TrimRight(res.stderr, "\n")
			if hint == "" || strings.Contains(hint, "\n") || !strings.HasPrefix(hint, "cenci incident") {
				t.Fatalf("want a one-line cenci incident hint on stderr, got %q", res.stderr)
			}
			if res.stdout != "" {
				t.Fatalf("a rejected invocation printed a preview:\n%s", res.stdout)
			}
			if got := readCLITrace(t, w.azTrace); got != "" {
				t.Fatalf("az was invoked before the input was rejected: %s", got)
			}
		})
	}
	for _, flag := range []string{"--apply", "--yes", "--replace-auth"} {
		t.Run("unregistered-"+flag, func(t *testing.T) {
			w := newSetupWorld(t)
			w.discoverAll()
			res := w.runSetup(t, "incident", "setup", "--config", w.writeConfig(t, nil), flag)
			res.requireExit(t, 2)
			if got := readCLITrace(t, w.azTrace); got != "" {
				t.Fatalf("az was invoked: %s", got)
			}
		})
	}
}

func TestIncidentSetupInvalidConfigExitsOneBeforeAnyAzCall(t *testing.T) {
	const secret = "s3cr3t-never-echoed"
	w := newSetupWorld(t)
	w.discoverAll()
	good := w.writeConfig(t, nil)
	unknownAuthField := strings.Replace(mustRead(t, good), `{`, `{"auth":{"mode":"servicePrincipalCertificate","tenantId":"`+cliTenantID+`","clientId":"`+cliClientID+`","certificatePath":"/etc/w.pem","clientSecret":"`+secret+`"},`, 1)
	badAuth := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(badAuth, []byte(unknownAuthField), 0600); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]string{
		"policy-violation": w.writeConfig(t, func(c *incident.Config) { c.Concurrency = 0 }),
		"missing-file":     filepath.Join(t.TempDir(), "absent.json"),
		"auth-unknown-key": badAuth,
	} {
		t.Run(name, func(t *testing.T) {
			res := w.runSetup(t, w.args(cfg)...)
			res.requireExit(t, 1)
			if !strings.HasPrefix(res.stderr, "cenci incident") || res.stdout != "" {
				t.Fatalf("stdout %q stderr %q", res.stdout, res.stderr)
			}
			requireLacks(t, "stderr", res.stderr, secret)
			if got := readCLITrace(t, w.azTrace); got != "" {
				t.Fatalf("az was invoked for an unusable config: %s", got)
			}
		})
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestIncidentSetupExistingIdentityListsOtherAssignmentsAndWarns(t *testing.T) {
	t.Run("assignments-listed", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		res := w.runSetup(t, w.args(w.writeConfig(t, nil), existingIdentity()...)...)
		res.requireExit(t, 0)
		requireContains(t, "stdout", res.stdout, "Contributor", "/subscriptions/"+setupSubscription+"/resourceGroups/rg-app")
		requireContains(t, "stdout", strings.ToLower(res.stdout), "additive")
		lines := w.traceLines(t)
		var spShow, listing = -1, -1
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "ad "):
				if !strings.HasPrefix(l, "ad sp show") || !strings.Contains(l, "--id "+cliClientID) {
					t.Fatalf("the identity may be resolved only by client ID with ad sp show --id: %q", l)
				}
				spShow = i
			case strings.HasPrefix(l, "role assignment list"):
				if !strings.Contains(l, "--assignee-object-id "+setupObjectID) || !strings.Contains(l, "--all") {
					t.Fatalf("assignments must be listed for the resolved object ID with --all: %q", l)
				}
				listing = i
			}
		}
		if spShow < 0 || listing < 0 || spShow > listing {
			t.Fatalf("want ad sp show before role assignment list: %q", lines)
		}
	})
	t.Run("assignments-could-not-be-read", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		w.on("role assignment list", "", 1, "", "ERROR: AuthorizationFailed")
		res := w.runSetup(t, w.args(w.writeConfig(t, nil), existingIdentity()...)...)
		res.requireExit(t, 0)
		requireContains(t, "stdout", res.stdout, "could not be read")
		requireContains(t, "stdout", strings.ToLower(res.stdout), "additive")
		requireLacks(t, "stdout", res.stdout, "AuthorizationFailed")
	})
	t.Run("client-id-must-match-the-resolved-principal", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, "88888888-8888-4888-8888-888888888888", setupObjectID))
		w.runSetup(t, w.args(w.writeConfig(t, nil), existingIdentity()...)...).requireExit(t, 1)
	})
}

func TestIncidentSetupOutputCarriesNoKeyOrTokenFromTheFakeBoundary(t *testing.T) {
	inject := func(doc string) string { return strings.Replace(doc, "{", `{"accessToken":"`+setupTokenStdout+`",`, 1) }
	cases := []struct {
		name string
		code int
		set  func(w *setupWorld)
	}{
		{"tokens-in-successful-output-and-stderr", 0, func(w *setupWorld) {
			for i, r := range w.rules {
				w.rules[i].stdout = inject(r.stdout)
				w.rules[i].stderr = "WARNING: " + setupTokenStderr
			}
		}},
		{"tokens-with-every-call-failing", 0, func(w *setupWorld) {
			for i := range w.rules {
				w.rules[i].code = 1
				w.rules[i].stdout = setupTokenStdout
				w.rules[i].stderr = "ERROR: " + setupTokenStderr
			}
		}},
		{"tokens-with-malformed-output", 1, func(w *setupWorld) {
			w.on("servicebus namespace list", "", 0, "not json "+setupTokenStdout, setupTokenStderr)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newSetupWorld(t)
			w.discoverAll()
			tc.set(w)
			if err := os.WriteFile(w.cert, []byte(fakePrivateKeyPEM), 0600); err != nil {
				t.Fatal(err)
			}
			res := w.runSetup(t, w.args(w.writeConfig(t, nil), existingIdentity()...)...)
			res.requireExit(t, tc.code)
			if len(w.traceLines(t)) == 0 {
				t.Fatal("az was never called, so the boundary was not exercised")
			}
			for stream, text := range map[string]string{"stdout": res.stdout, "stderr": res.stderr} {
				requireLacks(t, stream, text, setupTokenStdout, setupTokenStderr, "c2VudGluZWw", "PRIVATE KEY", "BEGIN")
			}
		})
	}
}

func TestIncidentSetupSharedWorkspaceProducesOneReaderCommand(t *testing.T) {
	t.Run("two-resources-one-workspace", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		cfg := w.writeConfig(t, func(c *incident.Config) {
			c.Resources = append(c.Resources, w.resource(t, "api", "owner/api", setupConfigWorkspace))
		})
		res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
		res.requireExit(t, 0)
		requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
			roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(setupObjectID, roleLogAnalyticsReader, setupWorkspaceARM)))
	})
	t.Run("two-workspaces-two-commands", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		cfg := w.writeConfig(t, func(c *incident.Config) {
			c.Resources = append(c.Resources, w.resource(t, "api", "owner/api", setupSecondWorkspace))
		})
		res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
		res.requireExit(t, 0)
		requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
			roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(setupObjectID, roleLogAnalyticsReader, setupWorkspaceARM),
			roleCmd(setupObjectID, roleLogAnalyticsReader, setupAuxARM)))
	})
}

func TestIncidentSetupTopicSubscriptionScopeIsExact(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, func(c *incident.Config) {
		c.Queue, c.Topic, c.Subscription = "", "alerts-topic", "incident-worker"
	})
	res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
	res.requireExit(t, 0)
	requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
		roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/topics/alerts-topic/subscriptions/incident-worker"),
		roleCmd(setupObjectID, roleLogAnalyticsReader, setupWorkspaceARM)))
	lines := w.traceLines(t)
	if !hasTrace(lines, "servicebus topic subscription show") || hasTrace(lines, "servicebus queue show") {
		t.Fatalf("a topic config must read the subscription, not a queue: %q", lines)
	}
}

func TestIncidentSetupMalformedDiscoveryOutputFailsTheStep(t *testing.T) {
	withNamespace := []string{"--service-bus-namespace-id", setupNamespaceARM}
	withWorkspace := []string{"--workspace-resource-id", setupConfigWorkspace + "=" + setupWorkspaceARM}
	const truncated = `{"id":"/subscriptions/`
	cases := []struct {
		name, prefix, needle, stdout, step string
		flags                              []string
	}{
		{"account-show-not-json", "account show", "", "Welcome to the Azure CLI", "account show", nil},
		{"account-show-truncated", "account show", "", truncated, "account show", nil},
		{"account-show-empty-output", "account show", "", "", "account show", nil},
		{"account-show-trailing-data", "account show", "", `{"id":"a","tenantId":"b"} {"id":"c"}`, "account show", nil},
		{"namespace-list-not-json", "servicebus namespace list", "", "Traceback (most recent call last):", "servicebus namespace list", nil},
		{"namespace-show-truncated", "servicebus namespace show", "", truncated, "servicebus namespace show", withNamespace},
		{"queue-show-truncated", "servicebus queue show", "", truncated, "servicebus queue show", nil},
		{"workspace-list-not-json", "monitor log-analytics workspace list", "", "no workspaces", "workspace list", nil},
		{"workspace-show-truncated", "monitor log-analytics workspace show", "ws-main", truncated, "workspace show", withWorkspace},
		{"sp-show-truncated", "ad sp show", "", truncated, "ad sp show", existingIdentity()},
		{"role-assignment-list-not-json", "role assignment list", "", "<html>gateway</html>", "role assignment list", existingIdentity()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newSetupWorld(t)
			w.discoverAll()
			w.ok(tc.prefix, tc.needle, tc.stdout)
			res := w.runSetup(t, w.args(w.writeConfig(t, nil), tc.flags...)...)
			res.requireExit(t, 1)
			requireContains(t, "stderr", res.stderr, tc.step)
			requireLacks(t, "stderr", res.stderr, "Welcome to the Azure CLI", "Traceback", "gateway", "no workspaces")
		})
	}
}

func TestIncidentSetupMismatchedDiscoveryFailsTheStep(t *testing.T) {
	t.Run("workspace-customer-id-differs-from-the-configured-workspace", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		w.ok("monitor log-analytics workspace show", "ws-main", workspaceJSON(setupSecondWorkspace, setupWorkspaceARM, "ws-main"))
		res := w.runSetup(t, w.args(w.writeConfig(t, nil), "--workspace-resource-id", setupConfigWorkspace+"="+setupWorkspaceARM)...)
		res.requireExit(t, 1)
		requireContains(t, "stderr", res.stderr, "workspace show")
	})
	t.Run("workspace-resource-names-no-readable-workspace", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		w.on("monitor log-analytics workspace show", "ws-main", 0, `{"name":"ws-main"}`, "")
		w.runSetup(t, w.args(w.writeConfig(t, nil), "--workspace-resource-id", setupConfigWorkspace+"="+setupWorkspaceARM)...).requireExit(t, 1)
	})
}

func TestIncidentSetupCrossSubscriptionWorkspaceKeepsItsScope(t *testing.T) {
	otherWorkspace := armWorkspace(setupOtherSubscription, "rg-logs", "ws-main")
	run := func(t *testing.T, workspaceARM string) setupResult {
		w := newSetupWorld(t)
		w.discoverAll()
		w.ok("monitor log-analytics workspace show", "ws-main", workspaceJSON(setupConfigWorkspace, workspaceARM, "ws-main"))
		res := w.runSetup(t, w.args(w.writeConfig(t, nil), append(existingIdentity(), "--workspace-resource-id", setupConfigWorkspace+"="+workspaceARM)...)...)
		res.requireExit(t, 0)
		return res
	}
	t.Run("other-subscription", func(t *testing.T) {
		res := run(t, otherWorkspace)
		requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
			roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(setupObjectID, roleLogAnalyticsReader, otherWorkspace)))
		requireContains(t, "stdout", strings.ToLower(res.stdout), "cross-subscription")
	})
	t.Run("same-subscription-is-not-flagged", func(t *testing.T) {
		requireLacks(t, "stdout", strings.ToLower(run(t, setupWorkspaceARM).stdout), "cross-subscription")
	})
}

func TestIncidentSetupTenantMismatchWarnsAndSkipsDiscovery(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)
	args := w.args(cfg, existingIdentity()...)
	for i, a := range args {
		if a == cliTenantID {
			args[i] = setupOtherTenant
		}
	}
	res := w.runSetup(t, args...)
	res.requireExit(t, 0)
	requireContains(t, "stdout", strings.ToLower(res.stdout+res.stderr), "tenant mismatch")
	requireContains(t, "stdout", res.stdout, setupOtherTenant)
	if lines := w.traceLines(t); len(lines) != 1 || !strings.HasPrefix(lines[0], "account show") {
		t.Fatalf("discovery against the wrong tenant must stop after the account probe: %q", lines)
	}
	if len(renderedSteps(t, res.stdout)) < 4 {
		t.Fatalf("the preview must still render its steps:\n%s", res.stdout)
	}
}

func TestIncidentSetupZeroOrAmbiguousDiscoveryNamesTheFlagToSupply(t *testing.T) {
	duplicate := fmt.Sprintf(`[{"id":%q,"name":"contoso"},{"id":%q,"name":"contoso"}]`, setupNamespaceARM, armNamespace(setupOtherSubscription, "rg-bus", "contoso"))
	twoWorkspaces := "[" + workspaceJSON(setupConfigWorkspace, setupWorkspaceARM, "ws-main") + "," + workspaceJSON(setupConfigWorkspace, setupAuxARM, "ws-aux") + "]"
	cases := []struct{ name, prefix, stdout, flag, guessed string }{
		{"no-namespace", "servicebus namespace list", `[]`, "--service-bus-namespace-id", "rg-bus"},
		{"ambiguous-namespace", "servicebus namespace list", duplicate, "--service-bus-namespace-id", "rg-bus"},
		{"no-workspace", "monitor log-analytics workspace list", `[]`, "--workspace-resource-id", "/workspaces/"},
		{"ambiguous-workspace", "monitor log-analytics workspace list", twoWorkspaces, "--workspace-resource-id", "/workspaces/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newSetupWorld(t)
			w.discoverAll()
			w.ok(tc.prefix, "", tc.stdout)
			res := w.runSetup(t, w.args(w.writeConfig(t, nil), existingIdentity()...)...)
			res.requireExit(t, 0)
			requireContains(t, "stdout", res.stdout, tc.flag)
			// Nothing was guessed: no role command carries a scope for the undiscovered resource.
			for _, c := range roleCommands(res.stdout) {
				requireLacks(t, "role command", c, tc.guessed)
			}
		})
	}
}

func TestIncidentSetupReportsWhetherConfiguredAuthDiffers(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	same := incident.AuthConfig{Mode: incident.AuthModeCertificate, TenantID: cliTenantID, ClientID: cliClientID, CertificatePath: w.cert}
	other := same
	other.ClientID = "99999999-9999-4999-8999-999999999999"
	for name, tc := range map[string]struct {
		auth incident.AuthConfig
		want string
	}{"identical": {same, "matches"}, "different": {other, "differs"}} {
		t.Run(name, func(t *testing.T) {
			auth := tc.auth
			cfg := w.writeConfig(t, func(c *incident.Config) { c.Auth = &auth })
			res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
			res.requireExit(t, 0)
			requireContains(t, "stdout", strings.ToLower(res.stdout), tc.want)
		})
	}
}

func TestIncidentSetupReportsAnAbsentAuthSection(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)
	if strings.Contains(mustRead(t, cfg), `"auth"`) {
		t.Fatal("test config must have no auth section")
	}
	res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
	res.requireExit(t, 0)
	requireContains(t, "stdout", res.stdout, "The config has no auth section yet.")
	requireLacks(t, "stdout", strings.ToLower(res.stdout), "existing auth section")
}

// stepNamed returns the rendered step whose title starts with title.
func stepNamed(t *testing.T, out, title string) string {
	t.Helper()
	for _, s := range renderedSteps(t, out) {
		if strings.Contains(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0], title) {
			return s
		}
	}
	t.Fatalf("no step titled %q in:\n%s", title, out)
	return ""
}

func TestIncidentSetupWithoutCertificatePathShowsThePlaceholderAndInventsNoDefault(t *testing.T) {
	const placeholder = "<certificate-path>"
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)
	res := w.runSetup(t, "incident", "setup", "--config", cfg, "--tenant", cliTenantID, "--subscription", setupSubscription, "--identity", "new")
	res.requireExit(t, 0)

	requireContains(t, "certificate step", stepNamed(t, res.stdout, "Certificate credential"), placeholder)
	requireContains(t, "local files step", stepNamed(t, res.stdout, "Local files"), placeholder)
	auth := proposedAuth(t, res.stdout)
	if auth.CertificatePath != placeholder {
		t.Fatalf("proposed auth certificatePath %q, want %q", auth.CertificatePath, placeholder)
	}
	// No default location is invented: nothing in the rendering names a PEM file
	// or a path under the isolated home or config directories.
	if m := regexp.MustCompile(`(?i)\.pem\b`).FindString(res.stdout); m != "" {
		t.Fatalf("a certificate file name was invented (%q):\n%s", m, res.stdout)
	}
	if got := w.traceLines(t); !hasTrace(got, "account show") {
		t.Fatalf("discovery did not run: %q", got)
	}
}

func TestIncidentSetupPlaceholderCommandsAreUnquotedAndLabeledWhileRealValuesStayQuoted(t *testing.T) {
	const label = "fill placeholders before running"
	quotedPlaceholder := regexp.MustCompile(`'[^']*<[a-z][a-z0-9-]*>[^']*'`)
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)

	t.Run("renderer-placeholders", func(t *testing.T) {
		res := w.runSetup(t, w.args(cfg)...)
		res.requireExit(t, 0)
		lines := strings.Split(res.stdout, "\n")
		labeled := 0
		for i, l := range lines {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "az ") || !placeholderPattern.MatchString(l) {
				continue
			}
			if quotedPlaceholder.MatchString(l) {
				t.Fatalf("a placeholder is shell-quoted, so an unfilled copy would not fail: %s", l)
			}
			if i == 0 || !strings.Contains(lines[i-1], label) {
				t.Fatalf("command with a placeholder is not labeled %q: %s", label, l)
			}
			labeled++
		}
		if labeled < 3 {
			t.Fatalf("want the app, role and credential commands to carry placeholders, got %d:\n%s", labeled, res.stdout)
		}
		requireContains(t, "stdout", res.stdout,
			"--assignee-object-id <object-id> --assignee-principal-type ServicePrincipal",
			"az ad sp create --id <app-id>",
			"--cert @<public-certificate-file> --append")
	})

	t.Run("flag-value-shaped-like-a-placeholder-stays-quoted", func(t *testing.T) {
		res := w.runSetup(t, w.args(cfg, "--display-name", "<object-id>")...)
		res.requireExit(t, 0)
		requireContains(t, "stdout", res.stdout, "az ad app create --display-name '<object-id>' --query appId -o tsv")
	})

	t.Run("fully-discovered-role-commands-have-no-label", func(t *testing.T) {
		res := w.runSetup(t, w.args(cfg, existingIdentity()...)...)
		res.requireExit(t, 0)
		requireContains(t, "stdout", res.stdout, "--assignee-object-id '"+setupObjectID+"' ")
		lines := strings.Split(res.stdout, "\n")
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "az role assignment create") && strings.Contains(lines[i-1], label) {
				t.Fatalf("a command with no placeholder is labeled %q: %s", label, l)
			}
		}
	})
}

func (w *setupWorld) managedArgs(cfg string, extra ...string) []string {
	return append([]string{"incident", "setup", "--config", cfg, "--tenant", cliTenantID, "--subscription", setupSubscription, "--identity", "managed"}, extra...)
}

func TestIncidentSetupManagedIdentityPreview(t *testing.T) {
	const placeholder = "<principal-object-id>"
	t.Run("no-principal-object-id-uses-the-placeholder", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		res := w.runSetup(t, w.managedArgs(w.writeConfig(t, nil))...)
		res.requireExit(t, 0)
		requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
			roleCmd(placeholder, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(placeholder, roleLogAnalyticsReader, setupWorkspaceARM)))
		requireContains(t, "stdout", res.stdout, "--principal-object-id", `"mode": "`+incident.AuthModeManagedIdentity+`"`)
		requireContains(t, "stdout", strings.ToLower(res.stdout), "additive")
		requireLacks(t, "stdout", res.stdout, "create-for-rbac", "az ad app create", "az ad app credential reset")
		lines := w.traceLines(t)
		if hasTrace(lines, "ad sp show") || hasTrace(lines, "role assignment list") {
			t.Fatalf("no object ID is known, so the principal cannot be looked up: %q", lines)
		}
	})
	t.Run("principal-object-id-lists-other-assignments-and-warns", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		res := w.runSetup(t, w.managedArgs(w.writeConfig(t, nil), "--principal-object-id", setupObjectID)...)
		res.requireExit(t, 0)
		requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
			roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(setupObjectID, roleLogAnalyticsReader, setupWorkspaceARM)))
		requireContains(t, "stdout", res.stdout, "Contributor", "/subscriptions/"+setupSubscription+"/resourceGroups/rg-app")
		requireContains(t, "stdout", strings.ToLower(res.stdout), "additive")
		lines := w.traceLines(t)
		if !hasTrace(lines, "ad sp show --id "+setupObjectID) || !hasTrace(lines, "role assignment list --assignee-object-id "+setupObjectID) {
			t.Fatalf("want the principal resolved by object ID and its assignments listed: %q", lines)
		}
	})
	t.Run("unreadable-principal-is-not-claimed-to-hold-no-other-assignments", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		w.on("ad sp show", "", 1, "", "ERROR: Insufficient privileges")
		w.ok("role assignment list", "", "[]")
		res := w.runSetup(t, w.managedArgs(w.writeConfig(t, nil), "--principal-object-id", setupObjectID)...)
		res.requireExit(t, 0)
		requireContains(t, "stdout", res.stdout, "could not be looked up", "could not be read")
		requireLacks(t, "stdout", res.stdout, "holds no other role assignments")
		requireContains(t, "stdout", strings.ToLower(res.stdout), "additive")
		requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
			roleCmd(setupObjectID, roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
			roleCmd(setupObjectID, roleLogAnalyticsReader, setupWorkspaceARM)))
		if hasTrace(w.traceLines(t), "role assignment list") {
			t.Fatal("the assignments of an unconfirmed principal must not be listed")
		}
		// Control: when the principal is readable the empty list is reported.
		w.discoverAll()
		w.ok("role assignment list", "", "[]")
		res = w.runSetup(t, w.managedArgs(w.writeConfig(t, nil), "--principal-object-id", setupObjectID)...)
		res.requireExit(t, 0)
		requireContains(t, "control stdout", res.stdout, "holds no other role assignments")
	})
	t.Run("client-id-must-match-the-principal-app-id", func(t *testing.T) {
		w := newSetupWorld(t)
		w.discoverAll()
		cfg := w.writeConfig(t, nil)
		// Control: the fake's appId is cliClientID, so the matching flag succeeds.
		w.runSetup(t, w.managedArgs(cfg, "--client-id", cliClientID)...).requireExit(t, 0)

		w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, "88888888-8888-4888-8888-888888888888", setupObjectID))
		res := w.runSetup(t, w.managedArgs(cfg, "--client-id", cliClientID)...)
		res.requireExit(t, 1)
		requireContains(t, "stderr", res.stderr, "ad sp show")
	})
}

func TestIncidentSetupDefaultSubscriptionIsOnlyADiscoveredCandidate(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)
	// No --subscription: az account show supplies the default.
	res := w.runSetup(t, "incident", "setup", "--config", cfg, "--tenant", cliTenantID, "--certificate-path", w.cert)
	res.requireExit(t, 0)
	requireContains(t, "header", res.stdout, "Subscription: "+setupSubscription, "Azure CLI default")

	lines := w.traceLines(t)
	for _, p := range []string{"servicebus namespace list", "monitor log-analytics workspace list"} {
		found := false
		for _, l := range lines {
			found = found || (strings.HasPrefix(l, p) && strings.Contains(l, "--subscription "+setupSubscription))
		}
		if !found {
			t.Fatalf("%q must carry the discovered subscription explicitly: %q", p, lines)
		}
	}
	// The generated commands never rely on the CLI default: each role command has
	// the full ARM scope.
	requireEqualLists(t, "role commands", roleCommands(res.stdout), sorted(
		roleCmd("<object-id>", roleServiceBusReceiver, setupNamespaceARM+"/queues/alerts"),
		roleCmd("<object-id>", roleLogAnalyticsReader, setupWorkspaceARM)))
	for _, c := range azCommands(res.stdout) {
		if strings.HasPrefix(c, "az role assignment create") && !strings.Contains(c, "--scope /subscriptions/") {
			t.Fatalf("role command without a full ARM scope: %s", c)
		}
		if strings.HasPrefix(c, "az account set") {
			t.Fatalf("preview must not rewrite the CLI default: %s", c)
		}
	}

	// Control: a pinned --subscription is not labeled as the CLI default.
	pinned := w.runSetup(t, w.args(cfg)...)
	pinned.requireExit(t, 0)
	requireLacks(t, "header", pinned.stdout, "Azure CLI default")
}

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if s, ok := strings.CutPrefix(kv, key+"="); ok {
			v = s
		}
	}
	return v
}

func TestIncidentSetupAzCallsInheritTheOperatorEnvironmentAndReadNullStdin(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)
	w.installAz(t)

	// The scripted az becomes az-fake; the az on PATH records its stdin and
	// AZURE_CONFIG_DIR, then runs it.
	fakeAz := filepath.Join(w.bin, "az-fake")
	if err := os.Rename(filepath.Join(w.bin, "az"), fakeAz); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(w.bin, "az-probe")
	wrapper := `#!/bin/sh
if [ /dev/stdin -ef /dev/null ]; then s=null; else s=other; fi
printf 'stdin=%s config=%s\n' "$s" "$AZURE_CONFIG_DIR" >> '` + probe + `'
exec '` + fakeAz + `' "$@"
`
	if err := os.WriteFile(filepath.Join(w.bin, "az"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}

	// Positive control: the probe reports a non-null stdin when there is one.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	ctl := exec.Command(filepath.Join(w.bin, "az"), "account", "show")
	ctl.Env = []string{"PATH=" + os.Getenv("PATH"), "AZURE_CONFIG_DIR=control-dir"}
	ctl.Stdin = pr
	_ = ctl.Run()
	if got := strings.TrimSpace(mustRead(t, probe)); got != "stdin=other config=control-dir" {
		t.Fatalf("probe control reported %q", got)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	env := w.setupEnv(t)
	operatorDir := envValue(env, "AZURE_CONFIG_DIR")
	if operatorDir == "" {
		t.Fatal("the test environment has no AZURE_CONFIG_DIR")
	}
	res := w.execSetup(t, env, w.args(cfg, existingIdentity()...)...)
	res.requireExit(t, 0)
	calls := strings.Split(strings.TrimSpace(mustRead(t, probe)), "\n")
	if len(calls) < 6 {
		t.Fatalf("expected the full discovery sequence, got %q", calls)
	}
	for _, c := range calls {
		if c != "stdin=null config="+operatorDir {
			t.Fatalf("an az call saw %q, want the null device and the operator's AZURE_CONFIG_DIR %q", c, operatorDir)
		}
	}
}

func TestIncidentSetupAzOutputOverTheBoundFailsTheStep(t *testing.T) {
	w := newSetupWorld(t)
	w.discoverAll()
	// The bound is 4 MiB per call.
	w.ok("account show", "", strings.Repeat("x", 5<<20))
	res := w.runSetup(t, w.args(w.writeConfig(t, nil))...)
	res.requireExit(t, 1)
	requireContains(t, "stderr", res.stderr, "account show", "output limit exceeded")
	requireLacks(t, "stderr", res.stderr, "xxxxxxxx")
	if res.stdout != "" {
		t.Fatalf("a failed discovery step printed a preview:\n%.200s", res.stdout)
	}
}

// A preview that cannot be written to stdout must not exit 0 as if it had been
// shown. /dev/full accepts the open and fails every write with ENOSPC.
func TestIncidentSetupUnwritablePreviewExitsOne(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no /dev/full on this platform: %v", err)
	}
	defer full.Close()
	w := newSetupWorld(t)
	w.discoverAll()
	cfg := w.writeConfig(t, nil)
	w.installAz(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, w.args(cfg)...)
	cmd.Env = w.setupEnv(t)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = full, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit 1 for an unwritable preview, got %v (stderr %q)", err, stderr.String())
	}
	requireContains(t, "stderr", stderr.String(), "writing the preview")
}
