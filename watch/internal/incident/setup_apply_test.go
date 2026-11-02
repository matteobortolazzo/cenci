package incident

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	applySubscription  = "44444444-4444-4444-8444-444444444444"
	applyObjectID      = "33333333-3333-4333-8333-333333333333"
	applyOtherObjectID = "88888888-8888-4888-8888-888888888888"
	applyOtherTenant   = "77777777-7777-4777-8777-777777777777"
	applyAuxWorkspace  = "66666666-6666-4666-8666-666666666666"
	applyThirdUUID     = "99999999-9999-4999-8999-999999999999"
	applySentinel      = "eyJ0eXAiOiJKV1QifQ.c2VudGluZWw.sig-az-output-sentinel"

	applyNamespace     = "/subscriptions/" + applySubscription + "/resourceGroups/rg-bus/providers/Microsoft.ServiceBus/namespaces/contoso"
	applyEntityScope   = applyNamespace + "/queues/alerts"
	applyMainWorkspace = "/subscriptions/" + applySubscription + "/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/ws-main"
	applyAuxScope      = "/subscriptions/" + applySubscription + "/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/ws-aux"
)

var applyNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type applyRule struct {
	prefix, needle, out, errOut string
	code                        int
	hang                        bool
}

// applyWorld is a plan over a real config file, a rule-based fake az (re-reads
// its rules on every call, logs argv and the stdin target) and recorders for
// systemctl and cenci on PATH.
type applyWorld struct {
	t              *testing.T
	bin            string
	rules          []applyRule
	plan           SetupPlan
	stdout, stderr bytes.Buffer
	confirm        bool
	confirms       int
	planSeen       string // what Stdout held when Confirm was asked
	verifyOK       bool
	waits          []time.Duration
	verified       []Config
	replaceAuth    bool
	uid            int
	out            io.Writer // replaces Stdout when set
	// cancel is called once az.log holds cancelAfter (checked whenever the clock
	// is read, so right after the step that logged it is recorded) or, with
	// cancelInVerify, from inside Verify.
	cancel         context.CancelFunc
	cancelAfter    string
	cancelInVerify bool
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func newApplyWorld(t *testing.T) *applyWorld {
	t.Helper()
	bin := t.TempDir()
	for _, name := range []string{"systemctl", "cenci"} {
		recorder := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + filepath.Join(bin, name+".log") + "'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(recorder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := writeSetupConfig(t, func(c *Config) { c.StateDir = filepath.Join(t.TempDir(), "state") }, "")
	w := &applyWorld{t: t, bin: bin, confirm: true, verifyOK: true, uid: os.Getuid()}
	w.plan = newApplyPlan(t, path, writeCertFile(t, time.Now().Add(400*24*time.Hour), 0600))
	w.healthy()
	return w
}

func newApplyPlan(t *testing.T, path, cert string) SetupPlan {
	t.Helper()
	cfg, err := LoadSetupConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := func(id string) *ARMID {
		a, err := ParseWorkspaceID(id)
		if err != nil {
			t.Fatal(err)
		}
		return &a
	}
	ns, err := ParseNamespaceID(applyNamespace)
	if err != nil {
		t.Fatal(err)
	}
	return SetupPlan{Config: cfg, ConfigPath: path, Tenant: testTenantID, Subscription: applySubscription, Identity: SetupIdentityExisting,
		ClientID: testClientID, ObjectID: applyObjectID, CertificatePath: cert, CertificateExists: true, AccountTenant: testTenantID,
		Namespace: &ns, AssignmentsListed: true,
		Workspaces: []WorkspaceTarget{{UUID: cfg.Resources[0].Workspace, ID: workspace(applyMainWorkspace)}, {UUID: applyAuxWorkspace, ID: workspace(applyAuxScope)},
			{UUID: applyThirdUUID, ID: workspace(strings.Replace(applyMainWorkspace, "ws-main", "WS-MAIN", 1))}}}
}

func (w *applyWorld) on(prefix, needle, out string, code int) {
	r := applyRule{prefix: prefix, needle: needle, out: out, code: code}
	for i, old := range w.rules {
		if old.prefix == prefix && old.needle == needle {
			w.rules[i] = r
			return
		}
	}
	w.rules = append(w.rules, r)
}

func (w *applyWorld) ok(prefix, needle, out string) { w.on(prefix, needle, out, 0) }

func worldRoleDef(guid string) string {
	return "/subscriptions/" + applySubscription + "/providers/Microsoft.Authorization/roleDefinitions/" + guid
}

func worldAssignment(principal, role, scope string) string {
	return assignmentJSON(principal, worldRoleDef(role), scope)
}

// healthy scripts a signed-in tenant, the existing principal and, for every
// scope, an empty assignment list and a create that echoes the request.
func (w *applyWorld) healthy() {
	w.ok("account show", "", fmt.Sprintf(`{"id":%q,"tenantId":%q}`, applySubscription, testTenantID))
	w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, testClientID, applyObjectID))
	for scope, role := range map[string]string{applyEntityScope: RoleServiceBusDataReceiver, applyMainWorkspace: RoleLogAnalyticsReader, applyAuxScope: RoleLogAnalyticsReader} {
		w.listed(scope)
		w.ok("role assignment create", scope, worldAssignment(applyObjectID, role, scope))
	}
}

// listed scripts what az lists at a scope, as the assignment documents given.
func (w *applyWorld) listed(scope string, docs ...string) {
	w.ok("role assignment list", scope, "["+strings.Join(docs, ",")+"]")
}

func (w *applyWorld) exists(scope, role string) {
	w.listed(scope, worldAssignment(applyObjectID, role, scope))
}

func (w *applyWorld) existAll() {
	w.exists(applyEntityScope, RoleServiceBusDataReceiver)
	w.exists(applyMainWorkspace, RoleLogAnalyticsReader)
	w.exists(applyAuxScope, RoleLogAnalyticsReader)
}

func (w *applyWorld) path(name string) string { return filepath.Join(w.bin, name) }

func (w *applyWorld) install() {
	w.t.Helper()
	fix := w.path("fix")
	if err := os.RemoveAll(fix); err != nil {
		w.t.Fatal(err)
	}
	if err := os.MkdirAll(fix, 0700); err != nil {
		w.t.Fatal(err)
	}
	var rules strings.Builder
	for i, r := range w.rules {
		key := "r" + strconv.Itoa(i)
		fmt.Fprintf(&rules, "%s|%s|%s\n", r.prefix, r.needle, key)
		files := map[string]string{"out": r.out, "err": r.errOut, "code": strconv.Itoa(r.code)}
		if r.hang {
			files["hang"] = ""
		}
		for ext, body := range files {
			if err := os.WriteFile(filepath.Join(fix, key+"."+ext), []byte(body), 0600); err != nil {
				w.t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(fix, "rules"), []byte(rules.String()), 0600); err != nil {
		w.t.Fatal(err)
	}
	script := `#!/bin/sh
FIX='` + fix + `'
printf '%s\n' "$*" >> '` + w.path("az.log") + `'
(readlink /proc/self/fd/0 2>/dev/null || echo unknown) >> '` + w.path("az.stdin") + `'
key=unmatched
while IFS='|' read -r prefix needle k; do
  case "$*" in
    "$prefix"*)
      case "$*" in
        *"$needle"*) key=$k; break;;
      esac;;
  esac
done < "$FIX/rules"
[ -f "$FIX/$key.hang" ] && exec sleep 30
[ -f "$FIX/$key.out" ] && cat "$FIX/$key.out"
[ -f "$FIX/$key.err" ] && cat "$FIX/$key.err" >&2
code=99
[ -f "$FIX/$key.code" ] && code=$(cat "$FIX/$key.code")
exit "$code"
`
	if err := os.WriteFile(w.path("az"), []byte(script), 0700); err != nil {
		w.t.Fatal(err)
	}
}

func (w *applyWorld) run() error { return w.runCtx(context.Background()) }

func (w *applyWorld) runCtx(ctx context.Context) error {
	w.t.Helper()
	w.install()
	az, err := NewApplyAz()
	if err != nil {
		w.t.Fatal(err)
	}
	if az.bin != w.path("az") {
		w.t.Fatalf("az resolves to %q, want the fake %q", az.bin, w.path("az"))
	}
	w.stdout.Reset()
	w.stderr.Reset()
	var stdout io.Writer = &w.stdout
	if w.out != nil {
		stdout = w.out
	}
	now := func() time.Time {
		if w.cancel != nil && w.cancelAfter != "" && strings.Contains(strings.Join(w.lines("az.log"), "\n"), w.cancelAfter) {
			w.cancel()
		}
		return applyNow
	}
	opts := ApplyOptions{Az: az, ReplaceAuth: w.replaceAuth, Now: now, Stdout: stdout, Stderr: &w.stderr,
		Confirm: func() bool {
			w.confirms++
			w.planSeen = w.stdout.String()
			return w.confirm
		},
		Verify: func(_ context.Context, cfg Config, wait time.Duration) bool {
			appendFile(w.t, w.path("az.log"), "VERIFY\n")
			if w.cancelInVerify {
				w.cancel()
			}
			w.waits = append(w.waits, wait)
			w.verified = append(w.verified, cfg)
			return w.verifyOK
		}}
	if w.uid == os.Getuid() {
		return Apply(ctx, w.plan, opts)
	}
	return apply(ctx, w.plan, opts, w.uid)
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func (w *applyWorld) lines(name string) []string {
	b, err := os.ReadFile(w.path(name))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// mutating is every logged az call outside the preview's read-only set.
func (w *applyWorld) mutating() []string {
	var out []string
	for _, l := range w.lines("az.log") {
		if _, ok := stepIn(azReadOnly, strings.Fields(l)); !ok && l != "VERIFY" {
			out = append(out, l)
		}
	}
	return out
}

func createLine(oid, role, scope string) string {
	return "role assignment create --assignee-object-id " + oid + " --assignee-principal-type ServicePrincipal --role " + role + " --scope " + scope + " -o json"
}

func (w *applyWorld) journalPath() string { return SetupJournalPath(w.plan.Config.StateDir) }

func (w *applyWorld) journal() *SetupJournal {
	w.t.Helper()
	j, err := readSetupJournal(w.plan.Config.StateDir, os.Getuid())
	if err != nil || j == nil {
		w.t.Fatalf("setup journal: %v, %v", j, err)
	}
	return j
}

func (w *applyWorld) noJournal() {
	w.t.Helper()
	if _, err := os.Lstat(w.plan.Config.StateDir); !errors.Is(err, os.ErrNotExist) {
		w.t.Fatalf("a refused or declined apply must not create the state directory or journal: %v", err)
	}
}

func (w *applyWorld) reloadConfig() {
	w.t.Helper()
	cfg, err := LoadSetupConfig(w.plan.ConfigPath)
	if err != nil {
		w.t.Fatal(err)
	}
	w.plan.Config = cfg
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func requireNoMutation(t *testing.T, w *applyWorld, name string) {
	t.Helper()
	if got := w.mutating(); len(got) != 0 {
		t.Fatalf("%s: mutating az calls: %q", name, got)
	}
}

func TestSetupApplyDeclinedConfirmationMakesNoMutatingCall(t *testing.T) {
	w := newApplyWorld(t)
	w.confirm = false
	before := readBytes(t, w.plan.ConfigPath)
	if err := w.run(); !errors.Is(err, ErrApplyDeclined) {
		t.Fatalf("want ErrApplyDeclined, got %v", err)
	}
	if w.confirms != 1 {
		t.Fatalf("Confirm asked %d times", w.confirms)
	}
	for _, want := range []string{testTenantID, applySubscription, SetupIdentityExisting, applyObjectID, RoleServiceBusDataReceiver, RoleLogAnalyticsReader,
		applyEntityScope, applyMainWorkspace, applyAuxScope, w.plan.ConfigPath, w.journalPath(), w.plan.CertificatePath, "read only",
		`"mode": "servicePrincipalCertificate"`, "verify"} {
		if !strings.Contains(w.planSeen, want) {
			t.Errorf("the plan shown before the prompt lacks %q:\n%s", want, w.planSeen)
		}
	}
	requireNoMutation(t, w, "declined")
	w.noJournal()
	if !bytes.Equal(before, readBytes(t, w.plan.ConfigPath)) {
		t.Fatal("a declined apply changed the config")
	}

	control := newApplyWorld(t)
	if err := control.run(); err != nil {
		t.Fatal(err)
	}
	if got := len(control.mutating()); got != 3 {
		t.Fatalf("control: an accepted apply made %d role assignment creates, want 3", got)
	}
}

// The prompt is the only authorization gate: a plan the operator could not read
// must never reach it.
func TestSetupApplyUnwritablePlanStopsBeforeTheConfirmation(t *testing.T) {
	w := newApplyWorld(t)
	w.out = failingWriter{}
	before := readBytes(t, w.plan.ConfigPath)
	err := w.run()
	if err == nil || errors.Is(err, ErrApplyDeclined) || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("want the plan write error, got %v", err)
	}
	if w.confirms != 0 {
		t.Fatalf("Confirm was asked %d times for a plan that was never shown", w.confirms)
	}
	requireNoMutation(t, w, "unwritable-plan")
	w.noJournal()
	if !bytes.Equal(before, readBytes(t, w.plan.ConfigPath)) {
		t.Fatal("an unshown plan changed the config")
	}
}

func TestSetupApplyCancellationIsReportedAsInterrupted(t *testing.T) {
	lastCreate := createLine(applyObjectID, RoleLogAnalyticsReader, applyAuxScope)
	cases := []struct {
		name         string
		arrange      func(w *applyWorld, cancel context.CancelFunc)
		step, class  string
		configWrites bool
		verifyRuns   bool
	}{
		{"cancelled-after-the-last-role-step", func(w *applyWorld, cancel context.CancelFunc) { w.cancel, w.cancelAfter = cancel, lastCreate }, stepConfig, "interrupted", false, false},
		{"cancelled-during-verify", func(w *applyWorld, cancel context.CancelFunc) {
			w.cancel, w.cancelInVerify, w.verifyOK = cancel, true, false
		}, stepVerify, "interrupted", true, true},
		{"control-probe-fails-uncancelled", func(w *applyWorld, _ context.CancelFunc) { w.verifyOK = false }, stepVerify, "a probe did not pass", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newApplyWorld(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.arrange(w, cancel)
			before := readBytes(t, w.plan.ConfigPath)
			err := w.runCtx(ctx)
			if err == nil {
				t.Fatal("the apply must fail")
			}
			j := w.journal()
			var failed []SetupStep
			for _, s := range j.Steps {
				if s.Status == StepFailed {
					failed = append(failed, s)
				}
			}
			if j.Complete || len(failed) != 1 || failed[0].Name != c.step || failed[0].Result != c.class {
				t.Fatalf("want one failed %q step classed %q and complete=false, got %+v complete=%v", c.step, c.class, failed, j.Complete)
			}
			if !strings.Contains(w.stderr.String(), "("+c.class+")") {
				t.Errorf("the report lacks the class %q:\n%s", c.class, w.stderr.String())
			}
			if got := len(w.mutating()); got != 3 {
				t.Errorf("all role steps finished before the interruption, got %d creates", got)
			}
			if changed := !bytes.Equal(before, readBytes(t, w.plan.ConfigPath)); changed != c.configWrites {
				t.Errorf("config changed = %v, want %v", changed, c.configWrites)
			}
			if (len(w.waits) == 1) != c.verifyRuns {
				t.Errorf("verify ran %d times, want it to run = %v", len(w.waits), c.verifyRuns)
			}
		})
	}
}

func TestSetupApplyTenantMismatchMakesNoMutatingCall(t *testing.T) {
	cases := []struct {
		name, rule, out, want string
	}{
		{"account-tenant-differs", "account show", fmt.Sprintf(`{"id":%q,"tenantId":%q}`, applySubscription, applyOtherTenant), "tenant"},
		{"principal-is-another-object", "ad sp show", fmt.Sprintf(`{"appId":%q,"id":%q}`, testClientID, applyOtherObjectID), "principal"},
		{"principal-is-another-app", "ad sp show", fmt.Sprintf(`{"appId":%q,"id":%q}`, applyThirdUUID, applyObjectID), "principal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newApplyWorld(t)
			w.ok(c.rule, "", c.out)
			err := w.run()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error naming the %s, got %v", c.want, err)
			}
			if w.confirms != 0 {
				t.Fatal("the prompt must come after the tenant and principal re-checks")
			}
			requireNoMutation(t, w, c.name)
			w.noJournal()
		})
	}
	control := newApplyWorld(t)
	if err := control.run(); err != nil || len(control.mutating()) != 3 {
		t.Fatalf("control: a matching tenant and principal must apply: %v", err)
	}
}

func TestSetupApplyFailedOrTimedOutAzCallFailsItsStep(t *testing.T) {
	sbCreate := "role assignment create"
	cases := []struct {
		name, prefix, step, reason string
		edit                       func(w *applyWorld)
		timeout                    time.Duration
	}{
		{"create-exits-non-zero", sbCreate, sbCreate, "exited with code 1", func(w *applyWorld) {
			w.rules = append([]applyRule{{prefix: sbCreate, needle: applyEntityScope, out: applySentinel, errOut: applySentinel, code: 1}}, w.rules...)
		}, 0},
		{"list-exits-non-zero", "role assignment list", "role assignment list", "exited with code 1", func(w *applyWorld) {
			w.rules = append([]applyRule{{prefix: "role assignment list", needle: applyEntityScope, out: applySentinel, errOut: applySentinel, code: 1}}, w.rules...)
		}, 0},
		{"create-echoes-another-scope", sbCreate, sbCreate, "malformed or unexpected output", func(w *applyWorld) {
			w.ok(sbCreate, applyEntityScope, worldAssignment(applyObjectID, RoleServiceBusDataReceiver, applyNamespace))
		}, 0},
		{"create-times-out", sbCreate, sbCreate, "timed out", func(w *applyWorld) {
			w.rules = append([]applyRule{{prefix: sbCreate, needle: applyEntityScope, hang: true}}, w.rules...)
		}, 1500 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newApplyWorld(t)
			c.edit(w)
			ctx := context.Background()
			if c.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.timeout)
				defer cancel()
			}
			start := time.Now()
			err := w.runCtx(ctx)
			if time.Since(start) > 15*time.Second {
				t.Fatalf("the call was not bounded: %v", time.Since(start))
			}
			var azErr *AzError
			if !errors.As(err, &azErr) || azErr.Step != c.step || azErr.Reason != c.reason {
				t.Fatalf("want a %q step failing with %q, got %v", c.step, c.reason, err)
			}
			if len(w.waits) != 0 {
				t.Fatal("verify must not run after a failed step")
			}
			j := w.journal()
			var failed []SetupStep
			for _, s := range j.Steps {
				if s.Status == StepFailed {
					failed = append(failed, s)
				}
			}
			if j.Complete || len(failed) != 1 || failed[0].Result != c.reason {
				t.Fatalf("want one failed step with result %q and complete=false, got %+v complete=%v", c.reason, failed, j.Complete)
			}
			for _, label := range []string{"completed:", "failed:", "remaining:"} {
				if !strings.Contains(w.stderr.String(), label) {
					t.Errorf("the report lacks %q:\n%s", label, w.stderr.String())
				}
			}
			for name, text := range map[string]string{"stdout": w.stdout.String(), "stderr": w.stderr.String(), "error": err.Error(), "journal": string(readBytes(t, w.journalPath()))} {
				if strings.Contains(text, applySentinel) {
					t.Errorf("az output leaked into the %s", name)
				}
			}
			if runtime.GOOS == "linux" {
				stdin := w.lines("az.stdin")
				if len(stdin) != len(w.lines("az.log")) {
					t.Fatalf("stdin was recorded for %d of %d calls", len(stdin), len(w.lines("az.log")))
				}
				for _, target := range stdin {
					if target != "/dev/null" {
						t.Fatalf("an az call read stdin from %q, want /dev/null", target)
					}
				}
			}
		})
	}
	// Control: the fake does print the sentinel, so its absence above is a result.
	w := newApplyWorld(t)
	w.rules = append([]applyRule{{prefix: sbCreate, needle: applyEntityScope, out: applySentinel, errOut: applySentinel, code: 1}}, w.rules...)
	w.install()
	out, _ := exec.Command(w.path("az"), "role", "assignment", "create", "--scope", applyEntityScope).CombinedOutput()
	if !strings.Contains(string(out), applySentinel) {
		t.Fatal("control: the fake az did not print the sentinel")
	}
}

func TestSetupApplyCreatesExactlyTheMissingRoleAssignments(t *testing.T) {
	w := newApplyWorld(t)
	w.noJournal()
	w.exists(applyAuxScope, RoleLogAnalyticsReader)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{w.plan.Config.StateDir: 0700, w.journalPath(): 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o (the journal is private, and the state directory it creates is 0700)", path, got, want)
		}
	}
	want := []string{createLine(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope), createLine(applyObjectID, RoleLogAnalyticsReader, applyMainWorkspace)}
	if got := w.mutating(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mutating calls:\n%q\nwant:\n%q", got, want)
	}
	var lists []string
	for _, l := range w.lines("az.log") {
		if strings.HasPrefix(l, "role assignment list ") {
			lists = append(lists, l)
		}
	}
	if len(lists) != 3 || !strings.Contains(lists[2], applyAuxScope) {
		t.Fatalf("want one list per distinct scope (the duplicate workspace listed once), got %q", lists)
	}
	statuses := map[string]string{}
	for _, s := range w.journal().Steps {
		statuses[s.Name] = s.Status
	}
	var done, unchanged int
	for name, status := range statuses {
		switch {
		case strings.Contains(name, applyAuxScope) && status != StepUnchanged:
			t.Errorf("the existing assignment's step is %s", status)
		case strings.HasPrefix(name, "role assignment") && status == StepDone:
			done++
		case strings.HasPrefix(name, "role assignment") && status == StepUnchanged:
			unchanged++
		}
	}
	if done != 2 || unchanged != 1 {
		t.Fatalf("journal role steps: %d done, %d unchanged: %v", done, unchanged, statuses)
	}
}

func TestSetupApplyExistingAssignmentMatchesOnlyTheExactTriple(t *testing.T) {
	notMatches := map[string]string{
		"parent-scope":      worldAssignment(applyObjectID, RoleServiceBusDataReceiver, applyNamespace),
		"prefix-of-scope":   worldAssignment(applyObjectID, RoleServiceBusDataReceiver, strings.TrimSuffix(applyEntityScope, "s")),
		"child-scope":       worldAssignment(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope+"/extra"),
		"different-role":    worldAssignment(applyObjectID, RoleLogAnalyticsReader, applyEntityScope),
		"different-subject": worldAssignment(applyOtherObjectID, RoleServiceBusDataReceiver, applyEntityScope),
	}
	matches := map[string]string{
		"exact":         worldAssignment(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope),
		"case-differs":  worldAssignment(strings.ToUpper(applyObjectID), strings.ToUpper(RoleServiceBusDataReceiver), strings.ToUpper(applyEntityScope)),
		"among-records": worldAssignment(applyOtherObjectID, RoleServiceBusDataReceiver, applyEntityScope) + "," + worldAssignment(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope),
	}
	run := func(t *testing.T, doc string) []string {
		w := newApplyWorld(t)
		w.exists(applyMainWorkspace, RoleLogAnalyticsReader)
		w.exists(applyAuxScope, RoleLogAnalyticsReader)
		w.listed(applyEntityScope, doc)
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		return w.mutating()
	}
	for name, doc := range notMatches {
		t.Run("not-a-match/"+name, func(t *testing.T) {
			if got := run(t, doc); len(got) != 1 || got[0] != createLine(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope) {
				t.Fatalf("a record that is not the exact triple must not satisfy the step; mutating calls: %q", got)
			}
		})
	}
	for name, doc := range matches {
		t.Run("match/"+name, func(t *testing.T) {
			if got := run(t, doc); len(got) != 0 {
				t.Fatalf("the exact triple already exists; mutating calls: %q", got)
			}
		})
	}
}

func TestSetupApplyRerunAfterSuccessMakesNoMutatingCall(t *testing.T) {
	w := newApplyWorld(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	first := w.journal()
	if len(w.mutating()) != 3 || !first.Complete {
		t.Fatalf("control: the first apply must create 3 assignments and complete: %q", w.mutating())
	}
	// The journal holds the non-secret IDs and the injected clock's timestamps.
	if first.PrincipalID != applyObjectID || first.ClientID != testClientID || !first.CreatedAt.Equal(applyNow) || !first.UpdatedAt.Equal(applyNow) {
		t.Fatalf("want principal %s, client %s and both timestamps %v, got %+v", applyObjectID, testClientID, applyNow, first)
	}
	for _, s := range first.Steps {
		if !s.UpdatedAt.Equal(applyNow) {
			t.Errorf("step %q updatedAt %v, want %v", s.Name, s.UpdatedAt, applyNow)
		}
	}
	w.existAll()
	w.reloadConfig()
	configBefore, err := os.Stat(w.plan.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	bytesBefore := readBytes(t, w.plan.ConfigPath)
	logged := len(w.lines("az.log"))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	for _, l := range w.lines("az.log")[logged:] {
		if strings.HasPrefix(l, "role assignment create") {
			t.Fatalf("the rerun created an assignment: %s", l)
		}
	}
	if configAfter, err := os.Stat(w.plan.ConfigPath); err != nil || !os.SameFile(configBefore, configAfter) || !bytes.Equal(bytesBefore, readBytes(t, w.plan.ConfigPath)) {
		t.Fatalf("the rerun rewrote a config that already holds the plan's auth: %v", err)
	}
	j := w.journal()
	for _, s := range j.Steps {
		if s.Status == StepDone && s.Name != stepVerify {
			t.Errorf("step %q is done on a rerun; nothing should have changed", s.Name)
		}
	}
	if !j.Complete || len(w.waits) != 2 || w.waits[0] != 10*time.Minute || w.waits[1] != 0 {
		t.Fatalf("want complete with waits [10m 0], got complete=%v waits=%v", j.Complete, w.waits)
	}
}

// Azure is the source of truth: a role step the journal marks done is still
// listed on a rerun, and an assignment deleted out-of-band is created again.
func TestSetupApplyRerunRecreatesAnAssignmentDeletedOutOfBandDespiteADoneJournalStep(t *testing.T) {
	w := newApplyWorld(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	for _, s := range w.journal().Steps {
		if s.Name != stepVerify && s.Status != StepDone && s.Status != StepUnchanged {
			t.Fatalf("control: step %q is %q after a complete apply", s.Name, s.Status)
		}
	}
	w.exists(applyEntityScope, RoleServiceBusDataReceiver)
	w.exists(applyMainWorkspace, RoleLogAnalyticsReader)
	w.listed(applyAuxScope) // deleted out-of-band
	logged := len(w.lines("az.log"))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	listed, created := map[string]bool{}, []string{}
	for _, l := range w.lines("az.log")[logged:] {
		for _, scope := range []string{applyEntityScope, applyMainWorkspace, applyAuxScope} {
			if strings.HasPrefix(l, "role assignment list ") && strings.Contains(l, scope) {
				listed[scope] = true
			}
		}
		if strings.HasPrefix(l, "role assignment create") {
			created = append(created, l)
		}
	}
	if len(listed) != 3 {
		t.Fatalf("the rerun must list every role scope, listed %v", listed)
	}
	if want := createLine(applyObjectID, RoleLogAnalyticsReader, applyAuxScope); len(created) != 1 || created[0] != want {
		t.Fatalf("want exactly the deleted pair created again (%s), got %q", want, created)
	}
	if !w.journal().Complete {
		t.Fatal("the rerun must end complete")
	}
}

func TestSetupApplyResumesAfterAFailedRoleAssignment(t *testing.T) {
	w := newApplyWorld(t)
	w.on("role assignment create", applyMainWorkspace, "", 1)
	err := w.run()
	if err == nil {
		t.Fatal("the failing create must fail the apply")
	}
	j := w.journal()
	report := w.stderr.String()
	if j.Complete || !strings.Contains(report, "completed:") || !strings.Contains(report, applyEntityScope) || !strings.Contains(report, "remaining:") ||
		!strings.Contains(report, applyAuxScope) || !strings.Contains(report, "verify") {
		t.Fatalf("want complete=false and a report naming the done and remaining steps; complete=%v\n%s", j.Complete, report)
	}
	// Azure now holds the first assignment; the failing create is fixed.
	w.exists(applyEntityScope, RoleServiceBusDataReceiver)
	w.ok("role assignment create", applyMainWorkspace, worldAssignment(applyObjectID, RoleLogAnalyticsReader, applyMainWorkspace))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, l := range w.mutating() {
		count[l]++
	}
	if count[createLine(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope)] != 1 ||
		count[createLine(applyObjectID, RoleLogAnalyticsReader, applyMainWorkspace)] != 2 ||
		count[createLine(applyObjectID, RoleLogAnalyticsReader, applyAuxScope)] != 1 || len(count) != 3 {
		t.Fatalf("want the first pair created once across both runs, the failed pair retried once and the third created once: %v", count)
	}
	if !w.journal().Complete {
		t.Fatal("the resumed apply must complete")
	}
}

func TestSetupApplyRefusesADifferingAuthWithoutReplaceAuth(t *testing.T) {
	other := fmt.Sprintf(`{"mode":"servicePrincipalCertificate","tenantId":%q,"clientId":%q,"certificatePath":"/etc/cenci/other.pem"}`, testTenantID, applyThirdUUID)
	newWorld := func(t *testing.T) *applyWorld {
		w := newApplyWorld(t)
		w.plan = newApplyPlan(t, writeSetupConfig(t, func(c *Config) { c.StateDir = w.plan.Config.StateDir }, other), w.plan.CertificatePath)
		return w
	}
	w := newWorld(t)
	before := readBytes(t, w.plan.ConfigPath)
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "--replace-auth") {
		t.Fatalf("want an error naming --replace-auth, got %v", err)
	}
	requireNoMutation(t, w, "differing-auth")
	w.noJournal()
	if w.confirms != 0 || !bytes.Equal(before, readBytes(t, w.plan.ConfigPath)) {
		t.Fatal("the refusal must come before the prompt and leave the config untouched")
	}

	control := newWorld(t)
	control.replaceAuth = true
	if err := control.run(); err != nil {
		t.Fatalf("control: --replace-auth must allow the replacement: %v", err)
	}
	control.reloadConfig()
	if a := control.plan.Config.Auth; a == nil || *a != control.plan.ProposedAuth() {
		t.Fatalf("control: the config holds %+v, want the proposed auth", a)
	}
}

func TestSetupApplyVerifiesAndCompletesOnlyWhenEveryProbeIsOK(t *testing.T) {
	cases := []struct {
		name    string
		created bool
		ok      bool
		wait    time.Duration
	}{
		{"created-and-probes-ok", true, true, 10 * time.Minute},
		{"created-and-a-probe-fails", true, false, 10 * time.Minute},
		{"nothing-created-and-probes-ok", false, true, 0},
		{"nothing-created-and-a-probe-fails", false, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newApplyWorld(t)
			if !c.created {
				w.existAll()
			}
			w.verifyOK = c.ok
			err := w.run()
			if (err == nil) != c.ok {
				t.Fatalf("apply error %v with verify=%v", err, c.ok)
			}
			if len(w.waits) != 1 || w.waits[0] != c.wait {
				t.Fatalf("verify waits %v, want [%v]", w.waits, c.wait)
			}
			if w.journal().Complete != c.ok {
				t.Fatalf("complete=%v, want %v", w.journal().Complete, c.ok)
			}
			if got := w.verified[0].Auth; got == nil || *got != w.plan.ProposedAuth() {
				t.Fatalf("verify ran against %+v, want the freshly written auth", got)
			}
			if log := w.lines("az.log"); log[len(log)-1] != "VERIFY" {
				t.Fatalf("an az call ran after verify: %q", log[len(log)-2:])
			}
			if !c.ok && !strings.Contains(w.stderr.String(), "failed:") {
				t.Fatalf("a failed verification needs the report:\n%s", w.stderr.String())
			}
		})
	}
}

func TestSetupApplyManagedIdentityConfirmsThePrincipalAndCreatesNoCredential(t *testing.T) {
	managed := func(t *testing.T) *applyWorld {
		w := newApplyWorld(t)
		w.plan.Identity, w.plan.ClientID, w.plan.CertificatePath, w.plan.CertificateExists = SetupIdentityManaged, "", "", false
		w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, applyThirdUUID, applyObjectID))
		return w
	}
	w := managed(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(w.lines("az.log"), "\n")
	if !strings.Contains(log, "ad sp show --id "+applyObjectID+" -o json") {
		t.Fatalf("the principal was not confirmed with ad sp show --id:\n%s", log)
	}
	if strings.Contains(log, "ad app") || strings.Contains(log, "ad sp create") || strings.Contains(log, "credential") {
		t.Fatalf("managed identity mode must create no application or credential:\n%s", log)
	}
	want := []string{createLine(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope), createLine(applyObjectID, RoleLogAnalyticsReader, applyMainWorkspace), createLine(applyObjectID, RoleLogAnalyticsReader, applyAuxScope)}
	got := w.mutating()
	if len(got) != 3 {
		t.Fatalf("mutating calls %q", got)
	}
	for _, line := range want {
		if !strings.Contains(strings.Join(got, "\n"), line) {
			t.Errorf("missing %q (only the given principal is assigned)", line)
		}
	}
	w.reloadConfig()
	if a := w.plan.Config.Auth; a == nil || *a != (AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID}) {
		t.Fatalf("written auth %+v", a)
	}
	if entries := dirEntries(t, w.plan.Config.StateDir); len(entries) != 1 || entries[0] != SetupJournalFile {
		t.Fatalf("state directory holds %v, want only the journal", entries)
	}
	if entries := dirEntries(t, filepath.Dir(w.plan.ConfigPath)); len(entries) != 1 {
		t.Fatalf("config directory holds %v, want only the config", entries)
	}

	t.Run("another-principal-than-asked", func(t *testing.T) {
		w := managed(t)
		w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, applyThirdUUID, applyOtherObjectID))
		if err := w.run(); err == nil {
			t.Fatal("a principal that is not the requested one must be refused")
		}
		requireNoMutation(t, w, "managed-mismatch")
		w.noJournal()
	})
}

func TestSetupApplyJournalBindsTenantIdentityAndConfigPath(t *testing.T) {
	j := NewSetupJournal(testTenantID, SetupIdentityExisting, "/etc/cenci/incident.json", applyNow)
	for name, c := range map[string]struct {
		tenant, identity, path string
		ok                     bool
	}{
		"same-request":        {testTenantID, SetupIdentityExisting, "/etc/cenci/incident.json", true},
		"tenant-ignores-case": {strings.ToUpper(testTenantID), SetupIdentityExisting, "/etc/cenci/incident.json", true},
		"other-tenant":        {applyOtherTenant, SetupIdentityExisting, "/etc/cenci/incident.json", false},
		"other-identity":      {testTenantID, SetupIdentityManaged, "/etc/cenci/incident.json", false},
		"other-config-path":   {testTenantID, SetupIdentityExisting, "/etc/cenci/other.json", false},
		"unclean-path":        {testTenantID, SetupIdentityExisting, "/etc/cenci/../cenci/incident.json", false},
	} {
		if err := j.CheckBinding(c.tenant, c.identity, c.path); (err == nil) != c.ok {
			t.Errorf("%s: binding error %v, want ok=%v", name, err, c.ok)
		}
	}

	// A completed apply leaves a journal; a rerun for a different request stops.
	cases := map[string]func(w *applyWorld){
		"tenant": func(w *applyWorld) {
			w.replaceAuth = true // the proposed auth now differs; the binding must still stop the run
			w.plan.Tenant, w.plan.AccountTenant = applyOtherTenant, applyOtherTenant
			w.ok("account show", "", fmt.Sprintf(`{"id":%q,"tenantId":%q}`, applySubscription, applyOtherTenant))
		},
		"identity": func(w *applyWorld) {
			w.replaceAuth = true
			w.plan.Identity, w.plan.ClientID, w.plan.CertificatePath = SetupIdentityManaged, "", ""
		},
		"config path": func(w *applyWorld) {
			other := filepath.Join(t.TempDir(), "other.json")
			if err := os.WriteFile(other, readBytes(t, w.plan.ConfigPath), 0600); err != nil {
				t.Fatal(err)
			}
			w.plan.ConfigPath = other
		},
	}
	for name, change := range cases {
		t.Run("mismatch/"+name, func(t *testing.T) {
			w := newApplyWorld(t)
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			w.existAll()
			w.reloadConfig()
			journal := readBytes(t, w.journalPath())
			logged, confirms := len(w.lines("az.log")), w.confirms
			change(w)
			err := w.run()
			if err == nil || !strings.Contains(err.Error(), "belongs to a different request") {
				t.Fatalf("want a journal binding error, got %v", err)
			}
			if w.confirms != confirms || !bytes.Equal(journal, readBytes(t, w.journalPath())) {
				t.Fatal("a mismatching request must stop before the prompt and leave the journal untouched")
			}
			for _, l := range w.lines("az.log")[logged:] {
				if _, ok := stepIn(azReadOnly, strings.Fields(l)); !ok {
					t.Fatalf("mutating call after a binding mismatch: %s", l)
				}
			}
		})
	}
}

func TestSetupApplyNeverOverwritesACorruptOrForeignJournal(t *testing.T) {
	good := func(t *testing.T) *applyWorld {
		w := newApplyWorld(t)
		if err := os.MkdirAll(w.plan.Config.StateDir, 0700); err != nil {
			t.Fatal(err)
		}
		return w
	}
	for name, body := range map[string]string{
		"corrupt":          "{not json",
		"unknown-field":    `{"version":1,"tenant":"t","identity":"existing","configPath":"/c","steps":[],"complete":false,"extra":1}`,
		"unknown-status":   `{"version":1,"tenant":"t","identity":"existing","configPath":"/c","steps":[{"name":"x","status":"weird"}],"complete":false}`,
		"relative-binding": `{"version":1,"tenant":"t","identity":"existing","configPath":"c","steps":[],"complete":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := good(t)
			if err := os.WriteFile(w.journalPath(), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			err := w.run()
			if err == nil || !strings.Contains(err.Error(), "left untouched") {
				t.Fatalf("want a refusal that leaves the journal untouched, got %v", err)
			}
			if got := string(readBytes(t, w.journalPath())); got != body || w.confirms != 0 {
				t.Fatalf("journal rewritten (%q) or prompt reached (%d)", got, w.confirms)
			}
			requireNoMutation(t, w, name)
		})
	}
	t.Run("symlink", func(t *testing.T) {
		w := good(t)
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, w.journalPath()); err != nil {
			t.Fatal(err)
		}
		if err := w.run(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("want a symbolic link refusal, got %v", err)
		}
		if string(readBytes(t, target)) != "{}" {
			t.Fatal("the symlink target was written")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		// Valid and bound to this request, so only the size cap can refuse it.
		w := good(t)
		body := fmt.Sprintf(`{"version":1,"tenant":%q,"identity":%q,"configPath":%q,"steps":[],"complete":false}`, w.plan.Tenant, w.plan.Identity, w.plan.ConfigPath)
		body += strings.Repeat(" ", 1<<20+1-len(body)) // one byte over the 1 MiB cap
		if err := os.WriteFile(w.journalPath(), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := w.run(); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("want a size-cap refusal, got %v", err)
		}
		if string(readBytes(t, w.journalPath())) != body || w.confirms != 0 {
			t.Fatalf("journal rewritten or prompt reached (%d)", w.confirms)
		}
		requireNoMutation(t, w, "oversized")
	})
	for name, mk := range map[string]func(string) error{
		"directory": func(p string) error { return os.Mkdir(p, 0700) },
		"fifo":      func(p string) error { return syscall.Mkfifo(p, 0600) },
	} {
		t.Run("not-regular/"+name, func(t *testing.T) {
			w := good(t)
			if err := mk(w.journalPath()); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(w.journalPath())
			if err != nil {
				t.Fatal(err)
			}
			if err := w.run(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("want a not-a-regular-file refusal, got %v", err)
			}
			if after, err := os.Lstat(w.journalPath()); err != nil || after.Mode() != before.Mode() || w.confirms != 0 {
				t.Fatalf("the non-regular journal path was replaced (%v) or the prompt reached (%d)", err, w.confirms)
			}
			requireNoMutation(t, w, name)
		})
	}
	t.Run("foreign-owner", func(t *testing.T) {
		w := good(t)
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		before := readBytes(t, w.journalPath())
		stateDir := w.plan.Config.StateDir
		if j, err := readSetupJournal(stateDir, os.Getuid()); err != nil || j == nil {
			t.Fatalf("control: the owner reads its journal: %v", err)
		}
		if _, err := readSetupJournal(stateDir, os.Getuid()+1); err == nil || !strings.Contains(err.Error(), "left untouched") {
			t.Fatalf("a journal owned by another uid must be refused, got %v", err)
		}
		if !bytes.Equal(before, readBytes(t, w.journalPath())) {
			t.Fatal("reading a foreign journal must leave it byte-identical")
		}
	})
}

func TestSetupApplyRefusesASymlinkedOrForeignConfig(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		w := newApplyWorld(t)
		link := filepath.Join(t.TempDir(), "config.json")
		if err := os.Symlink(w.plan.ConfigPath, link); err != nil {
			t.Fatal(err)
		}
		w.plan.ConfigPath = link
		before := readBytes(t, w.plan.ConfigPath)
		if err := w.run(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("want a symbolic link refusal, got %v", err)
		}
		if w.confirms != 0 || !bytes.Equal(before, readBytes(t, w.plan.ConfigPath)) {
			t.Fatal("the config target check must come before the prompt")
		}
		requireNoMutation(t, w, "symlink")
		w.noJournal()
	})
	t.Run("foreign-owner", func(t *testing.T) {
		w := newApplyWorld(t)
		w.uid = os.Getuid() + 1
		if err := w.run(); err == nil || !strings.Contains(err.Error(), "owned by") {
			t.Fatalf("want an ownership refusal, got %v", err)
		}
		if w.confirms != 0 {
			t.Fatal("the config target check must come before the prompt")
		}
		requireNoMutation(t, w, "foreign")
		w.noJournal()
	})
	t.Run("control-regular-owned-file", func(t *testing.T) {
		w := newApplyWorld(t)
		if err := w.run(); err != nil || w.confirms != 1 {
			t.Fatalf("a regular file owned by the invoking user must apply: %v", err)
		}
	})
}

func TestSetupApplyNeverDeletesOrStartsTheWorker(t *testing.T) {
	w := newApplyWorld(t)
	ledger := []byte(`{"incidents":[]}`)
	if err := os.MkdirAll(w.plan.Config.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(w.plan.Config.StateDir, "incidents.json")
	if err := os.WriteFile(ledgerPath, ledger, 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	if len(w.mutating()) != 3 {
		t.Fatalf("control: the apply must have created its assignments: %q", w.mutating())
	}
	for _, l := range w.lines("az.log") {
		for _, word := range []string{"delete", "remove", "reset", "login", " ad app", "sp create"} {
			if strings.Contains(l, word) {
				t.Errorf("az call %q contains %q", l, word)
			}
		}
	}
	if !bytes.Equal(ledger, readBytes(t, ledgerPath)) {
		t.Fatal("the incident ledger changed")
	}
	for _, name := range []string{"systemctl", "cenci"} {
		if got := w.lines(name + ".log"); got != nil {
			t.Errorf("setup invoked %s: %q", name, got)
		}
		if err := exec.Command(name, "probe").Run(); err != nil {
			t.Fatal(err)
		}
		if got := w.lines(name + ".log"); len(got) != 1 || got[0] != "probe" {
			t.Fatalf("control: the %s recorder did not record a call: %q", name, got)
		}
	}
}

func TestSetupApplyBlockersNameTheFlagThatFixesEach(t *testing.T) {
	cases := []struct {
		name, flag string
		edit       func(p *SetupPlan)
	}{
		{"az-missing", "az", func(p *SetupPlan) { p.AzMissing = true }},
		{"login-failed", "az login", func(p *SetupPlan) { p.LoginFailed = true }},
		{"tenant-mismatch", "--tenant", func(p *SetupPlan) { p.TenantMismatch = true }},
		{"tenant-unknown", "--tenant", func(p *SetupPlan) { p.Tenant = "" }},
		{"subscription-unknown", "--subscription", func(p *SetupPlan) { p.Subscription = "" }},
		{"namespace-unresolved", "--service-bus-namespace-id", func(p *SetupPlan) { p.Namespace, p.NamespaceNote = nil, "not found" }},
		{"namespace-unconfirmed", "--service-bus-namespace-id", func(p *SetupPlan) { p.NamespaceNote = "the namespace could not be read, so it was not confirmed" }},
		{"entity-unconfirmed", "--service-bus-namespace-id", func(p *SetupPlan) { p.EntityNote = "the queue could not be read, so it was not confirmed" }},
		{"workspace-unresolved", "--workspace-resource-id " + applyAuxWorkspace, func(p *SetupPlan) { p.Workspaces[1].ID, p.Workspaces[1].Note = nil, "not found" }},
		{"workspace-unconfirmed", "--workspace-resource-id " + applyAuxWorkspace, func(p *SetupPlan) { p.Workspaces[1].Note = "the workspace could not be read, so it was not confirmed" }},
		{"existing-principal-unconfirmed", "--client-id", func(p *SetupPlan) { p.PrincipalNote, p.ObjectID = "the principal could not be looked up", "" }},
		{"managed-principal-unconfirmed", "--principal-object-id", func(p *SetupPlan) {
			p.Identity, p.ClientID, p.CertificatePath, p.PrincipalNote, p.ObjectID = SetupIdentityManaged, "", "", "the principal could not be looked up", ""
		}},
		{"certificate-path-missing", "--certificate-path", func(p *SetupPlan) { p.CertificatePath, p.CertificateExists = "", false }},
		{"certificate-file-missing", "--certificate-path", func(p *SetupPlan) { p.CertificateExists = false }},
		{"identity-new", "--identity", func(p *SetupPlan) { p.Identity = SetupIdentityNew }},
	}
	if got := newApplyWorld(t).plan.ApplyBlockers(); len(got) != 0 {
		t.Fatalf("control: a fully discovered plan has blockers: %q", got)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newApplyWorld(t)
			c.edit(&w.plan)
			blockers := w.plan.ApplyBlockers()
			if !strings.Contains(strings.Join(blockers, "\n"), c.flag) {
				t.Fatalf("blockers %q do not name %q", blockers, c.flag)
			}
			err := w.run()
			if err == nil || !strings.Contains(err.Error(), c.flag) {
				t.Fatalf("apply must stop on the blocker naming %q, got %v", c.flag, err)
			}
			if w.lines("az.log") != nil || w.confirms != 0 {
				t.Fatal("a blocked plan must stop before any az call and before the prompt")
			}
		})
	}
}
