package incident

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
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
	// echoName replaces %NAME% in out with the --display-name the call carried,
	// so a fake app create can echo the nonce name the engine chose.
	echoName bool
	// snapshot is a file the setup journal is copied to when the rule answers,
	// to show what the journal held at that moment.
	snapshot string
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
	// clock is what the injected clock returns; a test moves it to age the journal.
	clock time.Time
	// onNow runs on every clock read, before the time is returned, so a test can
	// react to what az.log holds at that moment (the clock is read right before
	// each step is recorded, and right before each create).
	onNow func()
	// verify, when set, decides the Verify seam's verdict instead of verifyOK; the
	// call is still logged and its wait and config recorded.
	verify func(ctx context.Context, cfg Config, wait time.Duration) bool
	// identityAz runs the world with NewIdentityApplyAz, as --identity new does.
	identityAz bool
	// onConfirm runs inside Confirm, after the plan was shown and before the
	// answer is returned: the last moment before the first write.
	onConfirm func()
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
	w := &applyWorld{t: t, bin: bin, confirm: true, verifyOK: true, uid: os.Getuid(), clock: applyNow}
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
		if r.echoName {
			files["echo"] = ""
		}
		if r.snapshot != "" {
			files["snap"] = r.snapshot
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
prev=
name=
for a in "$@"; do
  case "$a" in
    @/*)
      cp "${a#@}" '` + w.path("uploaded.pem") + `' 2>/dev/null
      stat -c %a "$(dirname "${a#@}")" > '` + w.path("uploaded.dirmode") + `' 2>/dev/null;;
  esac
  [ "$prev" = "--display-name" ] && name=$a
  prev=$a
done
key=unmatched
while IFS='|' read -r prefix needle k; do
  case "$*" in
    "$prefix"*)
      case "$*" in
        *"$needle"*) key=$k; break;;
      esac;;
  esac
done < "$FIX/rules"
[ -f "$FIX/$key.snap" ] && cp '` + w.journalPath() + `' "$(cat "$FIX/$key.snap")" 2>/dev/null
[ -f "$FIX/$key.hang" ] && exec sleep 30
if [ -f "$FIX/$key.echo" ]; then
  sed "s|%NAME%|$name|g" "$FIX/$key.out"
else
  [ -f "$FIX/$key.out" ] && cat "$FIX/$key.out"
fi
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
	newAz := NewApplyAz
	if w.identityAz {
		newAz = NewIdentityApplyAz
	}
	az, err := newAz()
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
		if w.onNow != nil {
			w.onNow()
		}
		return w.clock
	}
	opts := ApplyOptions{Az: az, ReplaceAuth: w.replaceAuth, Now: now, Stdout: stdout, Stderr: &w.stderr,
		Confirm: func() bool {
			w.confirms++
			w.planSeen = w.stdout.String()
			if w.onConfirm != nil {
				w.onConfirm()
			}
			return w.confirm
		},
		Verify: func(ctx context.Context, cfg Config, wait time.Duration) bool {
			appendFile(w.t, w.path("az.log"), "VERIFY\n")
			if w.cancelInVerify {
				w.cancel()
			}
			w.waits = append(w.waits, wait)
			w.verified = append(w.verified, cfg)
			if w.verify != nil {
				return w.verify(ctx, cfg, wait)
			}
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

// identityReads are the reads only the new-identity allowlist adds to the
// preview's set. They change nothing, so they are not mutating calls.
var identityReads = [][]string{{"ad", "app", "list"}, {"ad", "sp", "list"}, {"ad", "app", "credential", "list"}}

// mutating is every logged az call outside the read-only sets.
func (w *applyWorld) mutating() []string { return w.mutatingSince(0) }

// mutatingSince is mutating over the az calls logged after the first n.
func (w *applyWorld) mutatingSince(n int) []string {
	var out []string
	lines := w.lines("az.log")
	if n > len(lines) {
		n = len(lines)
	}
	for _, l := range lines[n:] {
		_, readOnly := stepIn(azReadOnly, strings.Fields(l))
		_, identityRead := stepIn(identityReads, strings.Fields(l))
		if !readOnly && !identityRead && l != "VERIFY" {
			out = append(out, l)
		}
	}
	return out
}

// creates is every logged role assignment create call.
func (w *applyWorld) creates() []string {
	var out []string
	for _, l := range w.lines("az.log") {
		if strings.HasPrefix(l, "role assignment create") {
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
	// The first run attempted its creates at applyNow; the rerun comes after the
	// propagation window, so it has nothing to wait for.
	w.clock = applyNow.Add(11 * time.Minute)
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

// newManagedApplyWorld is a world whose plan is for a managed identity: no client
// ID, no certificate, and a principal az reports under another application.
func newManagedApplyWorld(t *testing.T) *applyWorld {
	t.Helper()
	w := newApplyWorld(t)
	w.plan.Identity, w.plan.ClientID, w.plan.CertificatePath, w.plan.CertificateExists = SetupIdentityManaged, "", "", false
	w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, applyThirdUUID, applyObjectID))
	return w
}

func TestSetupApplyManagedIdentityConfirmsThePrincipalAndCreatesNoCredential(t *testing.T) {
	w := newManagedApplyWorld(t)
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
	a := w.plan.Config.Auth
	if a == nil || *a != (AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID, PrincipalObjectID: applyObjectID}) {
		t.Fatalf("written auth %+v", a)
	}
	// The pin names the principal every role went to, as the az log shows it.
	for _, l := range got {
		fields := strings.Fields(l)
		if i := slices.Index(fields, "--assignee-object-id"); i < 0 || i+1 == len(fields) || fields[i+1] != a.PrincipalObjectID {
			t.Errorf("auth.principalObjectId %q is not the assignee of %q", a.PrincipalObjectID, l)
		}
	}
	if entries := dirEntries(t, w.plan.Config.StateDir); len(entries) != 1 || entries[0] != SetupJournalFile {
		t.Fatalf("state directory holds %v, want only the journal", entries)
	}
	if entries := dirEntries(t, filepath.Dir(w.plan.ConfigPath)); len(entries) != 1 {
		t.Fatalf("config directory holds %v, want only the config", entries)
	}

	t.Run("another-principal-than-asked", func(t *testing.T) {
		w := newManagedApplyWorld(t)
		w.ok("ad sp show", "", fmt.Sprintf(`{"appId":%q,"id":%q}`, applyThirdUUID, applyOtherObjectID))
		if err := w.run(); err == nil {
			t.Fatal("a principal that is not the requested one must be refused")
		}
		requireNoMutation(t, w, "managed-mismatch")
		w.noJournal()
	})
}

// An auth section PR #1399 wrote for a managed identity has no pin, so it differs
// from the plan like any other differing section.
func TestSetupApplyRefusesAnUnpinnedManagedAuthWithoutReplaceAuth(t *testing.T) {
	unpinned := fmt.Sprintf(`{"mode":"managedIdentity","tenantId":%q}`, testTenantID)
	newWorld := func(t *testing.T) *applyWorld {
		w := newManagedApplyWorld(t)
		w.plan = newApplyPlan(t, writeSetupConfig(t, func(c *Config) { c.StateDir = w.plan.Config.StateDir }, unpinned), "")
		w.plan.Identity, w.plan.ClientID, w.plan.CertificatePath, w.plan.CertificateExists = SetupIdentityManaged, "", "", false
		if a := w.plan.Config.Auth; a == nil || *a != (AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID}) {
			t.Fatalf("control: the config must start with the unpinned managed auth, got %+v", a)
		}
		return w
	}
	w := newWorld(t)
	before := readBytes(t, w.plan.ConfigPath)
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "--replace-auth") {
		t.Fatalf("want an error naming --replace-auth, got %v", err)
	}
	requireNoMutation(t, w, "unpinned-managed-auth")
	w.noJournal()
	if w.confirms != 0 || !bytes.Equal(before, readBytes(t, w.plan.ConfigPath)) {
		t.Fatal("the refusal must come before the prompt and leave the config untouched")
	}

	control := newWorld(t)
	control.replaceAuth = true
	if err := control.run(); err != nil {
		t.Fatalf("control: --replace-auth must allow the rewrite: %v", err)
	}
	control.reloadConfig()
	if a := control.plan.Config.Auth; a == nil || *a != (AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID, PrincipalObjectID: applyObjectID}) {
		t.Fatalf("the rewritten auth is %+v, want the managed auth pinned to %s", a, applyObjectID)
	}
	// Once pinned, the section matches the plan and needs no flag.
	control.replaceAuth = false
	control.existAll()
	if err := control.run(); err != nil {
		t.Fatalf("a pinned auth section must not need --replace-auth on a rerun: %v", err)
	}
}

// The pin travels through the reloaded config into the Verify seam, so a host whose
// credential returns another identity cannot complete the apply.
func TestSetupApplyFailsWhenVerifyReportsAPrincipalMismatch(t *testing.T) {
	w := newManagedApplyWorld(t)
	cred := &scopeCredential{token: func(string) (string, error) {
		return identityToken(testTenantID, applyThirdUUID, applyOtherObjectID, nil), nil
	}}
	var verifyOut, verifyErr bytes.Buffer
	w.verify = func(ctx context.Context, cfg Config, wait time.Duration) bool {
		return Verify(ctx, cfg, cred, VerifyOptions{
			PropagationWait: wait,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("a workspace probe ran after an identity mismatch")
				return nil, errors.New("no network in this test")
			})},
			OpenBroker: func(Config, azcore.TokenCredential) (ServiceBusReceiver, error) {
				t.Error("a broker was opened after an identity mismatch")
				return nil, errors.New("no broker in this test")
			},
			Sleep: func(context.Context, time.Duration) error {
				t.Error("verify waited for propagation after an identity mismatch")
				return errors.New("no waiting in this test")
			},
			Stdout: &verifyOut,
			Stderr: &verifyErr,
		})
	}
	err := w.run()
	if err == nil {
		t.Fatal("an identity mismatch must fail the apply")
	}
	if !hasLine(verifyOut.String(), "identity (Service Bus): identity-mismatch") {
		t.Fatalf("verify did not report the identity mismatch:\n%s", verifyOut.String())
	}
	if !strings.Contains(verifyErr.String(), markPrincipalMismatch) {
		t.Fatalf("verify's reason lacks %q:\n%s", markPrincipalMismatch, verifyErr.String())
	}
	if len(w.verified) != 1 || w.verified[0].Auth == nil || w.verified[0].Auth.PrincipalObjectID != applyObjectID {
		t.Fatalf("verify must run against the reloaded config carrying the pin, got %+v", w.verified)
	}
	if strings.Join(cred.scopes, ",") != ServiceBusScope {
		t.Fatalf("token scopes requested %v, want only the Service Bus scope (stop at the first failure)", cred.scopes)
	}
	j := w.journal()
	var verifyStep *SetupStep
	for i, s := range j.Steps {
		if s.Name == stepVerify {
			verifyStep = &j.Steps[i]
		}
	}
	if j.Complete || verifyStep == nil || verifyStep.Status != StepFailed || verifyStep.Result != "a probe did not pass" {
		t.Fatalf("want complete=false and a failed %q step, got complete=%v step=%+v", stepVerify, j.Complete, verifyStep)
	}
	if !strings.Contains(w.stderr.String(), "failed: "+stepVerify) {
		t.Errorf("the report lacks the failed verify step:\n%s", w.stderr.String())
	}
	if !strings.Contains(err.Error(), stepVerify) {
		t.Errorf("the error does not name the verify step: %v", err)
	}
}

// The attempt time is written before the create call, so even a crash right after
// the call (or a failed create) leaves a rerun a reason to wait.
func TestSetupApplyRecordsTheRoleCreateAttemptBeforeEachCreate(t *testing.T) {
	t.Run("a-failed-journal-write-leaves-no-create", func(t *testing.T) {
		w := newApplyWorld(t)
		fired := false
		// Once the entity scope was listed, only the pre-create journal write is
		// left before its create: block that write by putting a non-empty directory
		// where the journal file is, so the atomic rename fails.
		w.onNow = func() {
			if fired {
				return
			}
			for _, l := range w.lines("az.log") {
				if strings.HasPrefix(l, "role assignment list") && strings.Contains(l, applyEntityScope) {
					fired = true
					if err := os.Remove(w.journalPath()); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Join(w.journalPath(), "blocker"), 0700); err != nil {
						t.Fatal(err)
					}
					return
				}
			}
		}
		err := w.run()
		if err == nil || !strings.Contains(err.Error(), "write the setup journal") {
			t.Fatalf("a failed journal write before a create must fail its step, got %v", err)
		}
		if !fired {
			t.Fatal("the entity scope was never listed, so the journal was never blocked")
		}
		if got := w.creates(); len(got) != 0 {
			t.Fatalf("a create ran although its attempt could not be recorded: %q", got)
		}
		if !strings.Contains(w.stderr.String(), "failed:") || !strings.Contains(w.stderr.String(), applyEntityScope) {
			t.Errorf("the report must name the failed role step:\n%s", w.stderr.String())
		}
	})

	t.Run("a-failed-create-leaves-the-time-of-that-attempt", func(t *testing.T) {
		w := newApplyWorld(t)
		base := applyNow.Add(90 * time.Minute)
		// The clock moves one minute with every create that was logged, so the
		// stamp tells which create it was written for.
		w.onNow = func() { w.clock = base.Add(time.Duration(len(w.creates())) * time.Minute) }
		w.on("role assignment create", applyMainWorkspace, "", 1)
		if err := w.run(); err == nil {
			t.Fatal("the failing create must fail the apply")
		}
		if got := w.creates(); len(got) != 2 {
			t.Fatalf("want the entity create and the failing workspace create, got %q", got)
		}
		j := w.journal()
		if want := base.Add(time.Minute); j.LastRoleCreateAt == nil || !j.LastRoleCreateAt.Equal(want) {
			t.Fatalf("lastRoleCreateAt %v, want %v (when the failing create was attempted)", j.LastRoleCreateAt, want)
		}
		if j.Complete {
			t.Fatal("a failed apply must not be complete")
		}
	})
}

// Every assignment already exists and nothing is created, so the wait comes only
// from when a role assignment was last attempted.
func TestSetupApplyWaitsAfterARecentRoleCreateAttempt(t *testing.T) {
	offset := func(d time.Duration) *time.Duration { return &d }
	cases := []struct {
		name      string
		attempted *time.Duration // lastRoleCreateAt relative to Now; nil leaves the key out
		failFirst bool           // a first run attempted a create that failed, 3 minutes earlier
		wait      time.Duration
	}{
		{"attempt-2-minutes-before", offset(-2 * time.Minute), false, 10 * time.Minute},
		{"attempt-just-under-10-minutes-before", offset(-(verifyPropagationWait - time.Second)), false, 10 * time.Minute},
		{"attempt-exactly-10-minutes-before", offset(-verifyPropagationWait), false, 0},
		{"attempt-11-minutes-before", offset(-11 * time.Minute), false, 0},
		{"attempt-in-the-future-clock-skew", offset(5 * time.Minute), false, 10 * time.Minute},
		{"legacy-journal-without-the-key", nil, false, 0},
		{"rerun-within-10-minutes-after-a-failed-create", nil, true, 10 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newApplyWorld(t)
			var prior *time.Time // what the journal holds before the rerun
			if c.failFirst {
				w.on("role assignment create", applyMainWorkspace, "", 1)
				if err := w.run(); err == nil {
					t.Fatal("control: the first run's create must fail")
				}
				prior = w.journal().LastRoleCreateAt
				if prior == nil {
					t.Fatal("control: the failed create left no lastRoleCreateAt")
				}
				w.clock = applyNow.Add(3 * time.Minute)
			} else {
				j := NewSetupJournal(w.plan.Tenant, w.plan.Identity, w.plan.ConfigPath, applyNow)
				if c.attempted != nil {
					at := applyNow.Add(*c.attempted)
					j.LastRoleCreateAt, prior = &at, &at
				}
				if err := j.Write(w.plan.Config.StateDir); err != nil {
					t.Fatal(err)
				}
				if hasKey := bytes.Contains(readBytes(t, w.journalPath()), []byte("lastRoleCreateAt")); hasKey != (c.attempted != nil) {
					t.Fatalf("control: the written journal has the lastRoleCreateAt key = %v, want %v", hasKey, c.attempted != nil)
				}
			}
			w.existAll()
			createdBefore := len(w.creates())
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			if got := w.creates(); len(got) != createdBefore {
				t.Fatalf("the rerun created an assignment: %q", got[createdBefore:])
			}
			if len(w.waits) != 1 || w.waits[0] != c.wait {
				t.Fatalf("verify waits %v, want [%v]", w.waits, c.wait)
			}
			after := w.journal()
			if !after.Complete {
				t.Fatal("the rerun must end complete")
			}
			// The reset at the start of a run leaves the attempt time alone, and a
			// run that attempts nothing writes none.
			switch {
			case prior == nil:
				if after.LastRoleCreateAt != nil || bytes.Contains(readBytes(t, w.journalPath()), []byte("lastRoleCreateAt")) {
					t.Fatalf("a rerun with no attempt wrote lastRoleCreateAt %v", after.LastRoleCreateAt)
				}
			case after.LastRoleCreateAt == nil || !after.LastRoleCreateAt.Equal(*prior):
				t.Fatalf("lastRoleCreateAt %v after the rerun, want the earlier attempt %v kept", after.LastRoleCreateAt, prior)
			}
		})
	}
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
		{"identity-unknown", "--identity", func(p *SetupPlan) { p.Identity = "service-principal" }},
		{"new-certificate-path-missing", "--certificate-path", func(p *SetupPlan) {
			p.Identity, p.ClientID, p.ObjectID, p.CertificatePath, p.CertificateExists = SetupIdentityNew, "", "", "", false
		}},
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

// ---- --identity new (#1389) -------------------------------------------------

const (
	newAppClientID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	newAppObjectID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	otherAppClientID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	identityBaseName = "cenci-incident-worker"
	seededNonceHex   = "0123456789abcdef"

	// The identity steps, in run order, as the journal and the failure report name them.
	stepCertFile   = "certificate file"
	stepApp        = "application"
	stepSP         = "service principal"
	stepCredential = "certificate credential"

	garbagePEM = "-----BEGIN PRIVATE KEY-----\nc2VudGluZWwta2V5LW1hdGVyaWFs\n-----END PRIVATE KEY-----\n"
)

var (
	identitySPDoc    = fmt.Sprintf(`{"appId":%q,"id":%q}`, newAppClientID, applyObjectID)
	identityResetDoc = fmt.Sprintf(`{"appId":%q,"password":null,"tenant":%q}`, newAppClientID, testTenantID)
)

func nonceNamePattern(base string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(base) + "-[0-9a-f]{16}$")
}

func appDoc(clientID, name string) string {
	return fmt.Sprintf(`{"appId":%q,"id":%q,"displayName":%q}`, clientID, newAppObjectID, name)
}

// asNewIdentity turns a plan into what the CLI hands Apply for --identity new:
// no client or object ID yet, a display name and a certificate path.
func asNewIdentity(p SetupPlan, cert string, exists bool) SetupPlan {
	p.Identity, p.ClientID, p.ObjectID, p.DisplayName = SetupIdentityNew, "", "", identityBaseName
	p.CertificatePath, p.CertificateExists = cert, exists
	return p
}

// rule sets a full rule, replacing the one with the same prefix and needle.
func (w *applyWorld) rule(r applyRule) {
	for i, old := range w.rules {
		if old.prefix == r.prefix && old.needle == r.needle {
			w.rules[i] = r
			return
		}
	}
	w.rules = append(w.rules, r)
}

// identityAzure scripts Azure for a first --identity new run: nothing exists
// yet and every create succeeds and echoes its request.
func (w *applyWorld) identityAzure() {
	w.ok("ad app list", "displayName eq", "[]")
	w.ok("ad app list", "appId eq", "[]")
	w.rule(applyRule{prefix: "ad app create", out: fmt.Sprintf(`{"appId":%q,"id":%q,"displayName":"%%NAME%%"}`, newAppClientID, newAppObjectID),
		echoName: true, snapshot: w.path("journal-at-create.json")})
	w.ok("ad sp list", "", "[]")
	w.ok("ad sp create", "", identitySPDoc)
	w.ok("ad app credential list", "", "[]")
	w.ok("ad app credential reset", "", identityResetDoc)
}

// holdApp makes Azure hold the application under name, found by name and by ID.
func (w *applyWorld) holdApp(name string) {
	w.ok("ad app list", "displayName eq", "["+appDoc(newAppClientID, name)+"]")
	w.ok("ad app list", "appId eq", "["+appDoc(newAppClientID, name)+"]")
}

func (w *applyWorld) holdSP() { w.ok("ad sp list", "", "["+identitySPDoc+"]") }

// holdCert makes Azure hold one certificate credential with this identifier.
func (w *applyWorld) holdCert(ident string) {
	w.ok("ad app credential list", "", fmt.Sprintf(`[{"customKeyIdentifier":%q,"keyId":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","type":"AsymmetricX509Cert"}]`, ident))
}

// failing makes a call exit 1 with the given stdout.
func (w *applyWorld) failing(prefix, out string) {
	w.rule(applyRule{prefix: prefix, out: out, code: 1})
}

func newIdentityWorld(t *testing.T) *applyWorld {
	t.Helper()
	w := newApplyWorld(t)
	w.identityAz = true
	w.plan = asNewIdentity(w.plan, filepath.Join(t.TempDir(), "worker.pem"), false)
	w.identityAzure()
	return w
}

// seedJournal writes the journal an interrupted --identity new apply leaves and
// returns its bytes.
func (w *applyWorld) seedJournal(clientID, nonce string) []byte {
	w.t.Helper()
	j := NewSetupJournal(w.plan.Tenant, w.plan.Identity, w.plan.ConfigPath, applyNow)
	j.ClientID, j.DisplayName = clientID, nonce
	if err := j.Write(w.plan.Config.StateDir); err != nil {
		w.t.Fatal(err)
	}
	return readBytes(w.t, w.journalPath())
}

// normalizedMutating is the mutating log with the uploaded public certificate's
// path replaced by <public-cert>; it returns that path as well.
func (w *applyWorld) normalizedMutating() (lines []string, uploaded string) {
	const flag = " --cert @"
	for _, l := range w.mutating() {
		if i := strings.Index(l, flag); i >= 0 {
			path, tail, _ := strings.Cut(l[i+len(flag):], " ")
			uploaded = path
			l = l[:i] + flag + "<public-cert> " + tail
		}
		lines = append(lines, l)
	}
	return lines, uploaded
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func indexFrom(lines []string, prefix string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], prefix) {
			return i
		}
	}
	return -1
}

func pemBlocks(data []byte) []*pem.Block {
	var blocks []*pem.Block
	for {
		var b *pem.Block
		if b, data = pem.Decode(data); b == nil {
			return blocks
		}
		blocks = append(blocks, b)
	}
}

// leafDER is the DER of the first CERTIFICATE block of a PEM file.
func leafDER(t *testing.T, data []byte) []byte {
	t.Helper()
	for _, b := range pemBlocks(data) {
		if b.Type == "CERTIFICATE" {
			return b.Bytes
		}
	}
	t.Fatal("no CERTIFICATE block")
	return nil
}

func requireRefusedBeforePrompt(t *testing.T, w *applyWorld, err error, journal []byte, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("the apply must be refused")
	}
	if strings.Contains(err.Error(), "cannot apply yet") {
		t.Fatalf("stopped by a plan blocker, not by the rule under test: %v", err)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal lacks %q: %v", want, err)
		}
	}
	if w.confirms != 0 {
		t.Fatal("the prompt was reached")
	}
	requireNoMutation(t, w, "refused")
	if journal == nil {
		w.noJournal()
	} else if !bytes.Equal(journal, readBytes(t, w.journalPath())) {
		t.Fatal("the journal changed")
	}
}

func requireNoCertificateFile(t *testing.T, w *applyWorld) {
	t.Helper()
	if _, err := os.Lstat(w.plan.CertificatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused apply must not create the certificate file: %v", err)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// mismatchedKeyCertFile writes a 0600 PEM whose certificate belongs to a
// different key than the PRIVATE KEY block it carries.
func mismatchedKeyCertFile(t *testing.T) string {
	t.Helper()
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(other)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER(t, certPEM(t, now.Add(-48*time.Hour), now.Add(400*24*time.Hour)))})
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	return writeFileMode(t, data, 0600)
}

func sha1Hex(der []byte) string {
	sum := sha1.Sum(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// P28b
func TestSetupApplyNewIdentityMakesExactlyTheIdentityCertificateAndRoleCreates(t *testing.T) {
	w := newIdentityWorld(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	j := w.journal()
	if !nonceNamePattern(identityBaseName).MatchString(j.DisplayName) {
		t.Fatalf("journal display name %q is not <display-name>-<16 hex>", j.DisplayName)
	}
	got, uploaded := w.normalizedMutating()
	want := []string{
		"ad app create --display-name " + j.DisplayName + " -o json",
		"ad sp create --id " + newAppClientID + " -o json",
		"ad app credential reset --id " + newAppClientID + " --cert @<public-cert> --append -o json",
		createLine(applyObjectID, RoleServiceBusDataReceiver, applyEntityScope),
		createLine(applyObjectID, RoleLogAnalyticsReader, applyMainWorkspace),
		createLine(applyObjectID, RoleLogAnalyticsReader, applyAuxScope),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mutating calls:\n%q\nwant:\n%q", got, want)
	}
	if !filepath.IsAbs(uploaded) || uploaded == w.plan.CertificatePath || filepath.Dir(uploaded) == filepath.Dir(w.plan.CertificatePath) {
		t.Fatalf("the public certificate must be uploaded from its own temporary directory, not from beside the key file: %q", uploaded)
	}

	// Each Azure step re-reads Azure at step time, in run order.
	log := w.lines("az.log")
	at := -1
	for _, prefix := range []string{"ad app create", "ad sp list", "ad sp create", "ad app credential list", "ad app credential reset", "role assignment create"} {
		if at = indexFrom(log, prefix, at+1); at < 0 {
			t.Fatalf("no %q call in run order:\n%s", prefix, strings.Join(log, "\n"))
		}
	}

	// The nonce name was journaled before az ad app create ran.
	snap, err := decodeSetupJournal(readBytes(t, w.path("journal-at-create.json")))
	if err != nil {
		t.Fatalf("the journal did not exist when az ad app create ran: %v", err)
	}
	if snap.DisplayName != j.DisplayName || snap.Complete {
		t.Fatalf("journal at az ad app create: displayName %q complete=%v, want %q and incomplete", snap.DisplayName, snap.Complete, j.DisplayName)
	}

	if j.ClientID != newAppClientID || j.PrincipalID != applyObjectID || !j.Complete {
		t.Fatalf("journal client %q principal %q complete=%v", j.ClientID, j.PrincipalID, j.Complete)
	}
	var names []string
	for i, s := range j.Steps {
		names = append(names, s.Name)
		if i < 4 && s.Status != StepDone {
			t.Errorf("identity step %q is %s, want done", s.Name, s.Status)
		}
	}
	if len(names) < 4 || strings.Join(names[:4], "|") != strings.Join([]string{stepCertFile, stepApp, stepSP, stepCredential}, "|") {
		t.Fatalf("journal steps %q do not start with the identity steps in order", names)
	}
	w.reloadConfig()
	wantAuth := AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: newAppClientID, CertificatePath: w.plan.CertificatePath}
	if a := w.plan.Config.Auth; a == nil || *a != wantAuth {
		t.Fatalf("written auth %+v, want %+v", a, wantAuth)
	}
	if len(w.waits) != 1 || w.waits[0] != 10*time.Minute {
		t.Fatalf("verify waits %v, want [10m] because this run created an application", w.waits)
	}
	if !strings.Contains(w.planSeen, `application: create "`+j.DisplayName+`"`) {
		t.Fatalf("the plan shown before the prompt does not name the nonce application:\n%s", w.planSeen)
	}

	t.Run("longest-display-name-gives-a-nonce-of-at-most-137-runes", func(t *testing.T) {
		w := newIdentityWorld(t)
		w.plan.DisplayName = strings.Repeat("wé", 60)
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		name := w.journal().DisplayName
		if n := utf8.RuneCountInString(name); n != 137 || !nonceNamePattern(w.plan.DisplayName).MatchString(name) {
			t.Fatalf("nonce name %q has %d runes, want 137 and <display-name>-<16 hex>", name, n)
		}
		if lines, _ := w.normalizedMutating(); len(lines) == 0 || lines[0] != "ad app create --display-name "+name+" -o json" {
			t.Fatalf("mutating calls %q", lines)
		}
	})
	t.Run("two-requests-get-different-nonces", func(t *testing.T) {
		a, b := newIdentityWorld(t), newIdentityWorld(t)
		for _, w := range []*applyWorld{a, b} {
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
		}
		if a.journal().DisplayName == b.journal().DisplayName {
			t.Fatalf("both requests drew the nonce name %q", a.journal().DisplayName)
		}
	})
}

func TestSetupApplyNewIdentityDeclinedConfirmationCreatesNothing(t *testing.T) {
	w := newIdentityWorld(t)
	w.confirm = false
	if err := w.run(); !errors.Is(err, ErrApplyDeclined) {
		t.Fatalf("want ErrApplyDeclined, got %v", err)
	}
	if !strings.Contains(w.planSeen, `application: create "`+identityBaseName+"-") || !strings.Contains(w.planSeen, w.plan.CertificatePath) {
		t.Fatalf("the plan shown before the prompt lacks the nonce application or the certificate path:\n%s", w.planSeen)
	}
	requireNoMutation(t, w, "declined")
	w.noJournal()
	requireNoCertificateFile(t, w)
}

// P29
func TestSetupApplyNewIdentityKeepsThePrivateKeyLocal(t *testing.T) {
	w := newIdentityWorld(t)
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
	cert := w.plan.CertificatePath
	info, err := os.Lstat(cert)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("certificate file mode %v, want a regular 0600 file", info.Mode())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		t.Fatalf("certificate file is not owned by uid %d", os.Getuid())
	}

	// One unencrypted PEM: a PKCS#8 PRIVATE KEY block, then the CERTIFICATE.
	data := readBytes(t, cert)
	blocks := pemBlocks(data)
	if len(blocks) != 2 || blocks[0].Type != "PRIVATE KEY" || blocks[1].Type != "CERTIFICATE" {
		t.Fatalf("certificate file holds %d blocks, want a PRIVATE KEY block then a CERTIFICATE block", len(blocks))
	}

	// The key's PEM body lines are the needles; the file holding them is the positive control.
	var keyLines []string
	for _, l := range strings.Split(string(pem.EncodeToMemory(blocks[0])), "\n") {
		if l != "" && !strings.HasPrefix(l, "-----") {
			keyLines = append(keyLines, l)
		}
	}
	if len(keyLines) == 0 {
		t.Fatal("control: no key body lines to search for")
	}
	for _, l := range keyLines {
		if !strings.Contains(string(data), l) {
			t.Fatal("control: the certificate file does not hold its own key lines")
		}
	}
	surfaces := map[string]string{
		"az argv":       strings.Join(w.lines("az.log"), "\n"),
		"uploaded copy": string(readBytes(t, w.path("uploaded.pem"))),
		"stdout":        w.stdout.String(),
		"stderr":        w.stderr.String(),
		"journal":       string(readBytes(t, w.journalPath())),
		"config":        string(readBytes(t, w.plan.ConfigPath)),
	}
	err = filepath.WalkDir(w.plan.Config.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			surfaces["state file "+filepath.Base(path)] = string(readBytes(t, path))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range surfaces {
		if strings.Contains(text, "PRIVATE KEY") {
			t.Errorf("%s mentions a private key block", name)
		}
		for _, l := range keyLines {
			if strings.Contains(text, l) {
				t.Errorf("the private key body reached the %s", name)
				break
			}
		}
	}
	if !bytes.Equal(ledger, readBytes(t, ledgerPath)) {
		t.Fatal("the incident ledger changed")
	}

	// The uploaded copy is the public certificate only, from a directory that is gone.
	uploaded := pemBlocks(readBytes(t, w.path("uploaded.pem")))
	if len(uploaded) != 1 || uploaded[0].Type != "CERTIFICATE" || !bytes.Equal(uploaded[0].Bytes, blocks[1].Bytes) {
		t.Fatalf("the uploaded file must hold exactly the certificate block of the PEM file, got %d blocks", len(uploaded))
	}
	_, uploadedPath := w.normalizedMutating()
	if _, err := os.Stat(filepath.Dir(uploadedPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary directory of the public certificate still exists: %v", err)
	}
	if runtime.GOOS == "linux" {
		if mode := strings.TrimSpace(string(readBytes(t, w.path("uploaded.dirmode")))); mode != "700" {
			t.Errorf("the public certificate's directory had mode %s while az ran, want 700", mode)
		}
		stdin := w.lines("az.stdin")
		azCalls := 0
		for _, l := range w.lines("az.log") {
			if l != "VERIFY" { // the Verify seam's marker is not an az call
				azCalls++
			}
		}
		if len(stdin) != azCalls {
			t.Fatalf("stdin was recorded for %d of %d calls", len(stdin), azCalls)
		}
		for _, target := range stdin {
			if target != "/dev/null" {
				t.Fatalf("an az call read stdin from %q, want /dev/null", target)
			}
		}
	}

	// The worker's own credential loader accepts the PKCS#8 file, and the
	// certificate has the documented parameters.
	if _, _, err := NewCredential(AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: newAppClientID, CertificatePath: cert},
		CredentialOptions{Now: func() time.Time { return applyNow }}); err != nil {
		t.Fatalf("NewCredential rejects the generated file: %v", err)
	}
	leaf, err := x509.ParseCertificate(blocks[1].Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := leaf.PublicKey.(*rsa.PublicKey)
	if leaf.Subject.CommonName != "cenci-incident-worker" || leaf.SignatureAlgorithm != x509.SHA256WithRSA || !ok || pub.N.BitLen() != 2048 {
		t.Fatalf("certificate CN %q algorithm %v key %T, want CN=cenci-incident-worker, SHA256WithRSA, RSA 2048", leaf.Subject.CommonName, leaf.SignatureAlgorithm, leaf.PublicKey)
	}
	if validity := leaf.NotAfter.Sub(leaf.NotBefore); validity < 365*24*time.Hour || validity > 366*24*time.Hour {
		t.Fatalf("certificate validity %v, want 365 days", validity)
	}
	if leaf.SerialNumber.Sign() <= 0 || leaf.SerialNumber.BitLen() <= 64 {
		t.Fatalf("certificate serial %v is not a random 128-bit number", leaf.SerialNumber)
	}
}

// firstIdentityRun applies a new identity once and leaves Azure as that run
// left it, ready for a rerun.
func firstIdentityRun(t *testing.T) (*applyWorld, string) {
	t.Helper()
	w := newIdentityWorld(t)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	name := w.journal().DisplayName
	w.holdApp(name)
	w.holdSP()
	w.existAll()
	w.reloadConfig()
	w.plan.CertificateExists = true
	return w, name
}

// P30b
func TestSetupApplyNewIdentityRerunAfterSuccessMakesNoMutatingCall(t *testing.T) {
	registered := map[string]func(der []byte) string{
		"hex-upper": sha1Hex,
		"hex-lower": func(der []byte) string { return strings.ToLower(sha1Hex(der)) },
		"base64": func(der []byte) string {
			sum := sha1.Sum(der)
			return base64.StdEncoding.EncodeToString(sum[:])
		},
	}
	for name, ident := range registered {
		t.Run(name, func(t *testing.T) {
			w, _ := firstIdentityRun(t)
			certBefore := readBytes(t, w.plan.CertificatePath)
			w.holdCert(ident(leafDER(t, certBefore)))
			logged := len(w.lines("az.log"))
			// The first run attempted its role creates at applyNow; the rerun comes
			// after the propagation window, so it has nothing to wait for.
			w.clock = applyNow.Add(11 * time.Minute)
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			if got := w.mutatingSince(logged); len(got) != 0 {
				t.Fatalf("the rerun made mutating calls: %q", got)
			}
			rerun := w.lines("az.log")[logged:]
			byID := false
			for _, l := range rerun {
				byID = byID || (strings.HasPrefix(l, "ad app list ") && strings.Contains(l, "appId eq '"+newAppClientID+"'"))
				if strings.Contains(l, "displayName eq") {
					t.Errorf("the application must be matched by the journal's client ID, not by name: %s", l)
				}
			}
			if !byID {
				t.Fatalf("the rerun never matched the application by its recorded client ID:\n%s", strings.Join(rerun, "\n"))
			}
			if !bytes.Equal(certBefore, readBytes(t, w.plan.CertificatePath)) {
				t.Fatal("the rerun changed the certificate file")
			}
			j := w.journal()
			for _, s := range j.Steps {
				switch s.Name {
				case stepCertFile, stepApp, stepSP, stepCredential:
					if s.Status != StepUnchanged {
						t.Errorf("step %q is %s on a rerun, want unchanged", s.Name, s.Status)
					}
				}
			}
			if !j.Complete || len(w.waits) != 2 || w.waits[1] != 0 {
				t.Fatalf("want complete with a second verify wait of 0, got complete=%v waits=%v", j.Complete, w.waits)
			}
		})
	}

	t.Run("unrecognized-shape-fails-the-step-and-appends-nothing", func(t *testing.T) {
		w, _ := firstIdentityRun(t)
		w.holdCert("not-a-thumbprint!")
		logged := len(w.lines("az.log"))
		err := w.run()
		if err == nil {
			t.Fatal("an identifier that is neither hex nor base64 of 20 bytes must fail the step")
		}
		if got := w.mutatingSince(logged); len(got) != 0 {
			t.Fatalf("the failed step made mutating calls: %q", got)
		}
		if !strings.Contains(w.stderr.String(), "failed: "+stepCredential+" (") || w.journal().Complete {
			t.Fatalf("want the %q step failed and complete=false:\n%s", stepCredential, w.stderr.String())
		}
	})
	t.Run("another-certificate-is-appended-to-never-replaced", func(t *testing.T) {
		w, _ := firstIdentityRun(t)
		w.holdCert(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, sha1.Size)))
		logged := len(w.lines("az.log"))
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		got := w.mutatingSince(logged)
		if len(got) != 1 || !strings.HasPrefix(got[0], "ad app credential reset ") || !strings.Contains(got[0], " --append ") {
			t.Fatalf("want exactly one appending reset because this certificate was not registered, got %q", got)
		}
	})
}

// Q11: the verify wait applies when this run created anything (app, service
// principal, credential or role) and not otherwise. The first run in each case
// created everything (wait 10m); the rerun finds Azure missing one thing. The
// rerun comes after the first run's role-attempt window unless rerunAt says
// otherwise, so its wait comes from what it created; within the window the
// recent role attempt alone still waits (#1402).
func TestSetupApplyNewIdentityVerifyWaitsWhenThisRunCreatedAnything(t *testing.T) {
	afterWindow := 11 * time.Minute
	cases := []struct {
		name    string
		arrange func(w *applyWorld, ident string)
		creates string        // the one mutating call the rerun must make, or "" for none
		rerunAt time.Duration // the rerun's clock, relative to the first run's applyNow
		wait    time.Duration
	}{
		{"service-principal-only", func(w *applyWorld, ident string) {
			w.ok("ad sp list", "", "[]")
			w.holdCert(ident)
		}, "ad sp create --id " + newAppClientID, afterWindow, 10 * time.Minute},
		{"credential-only", func(w *applyWorld, _ string) {}, "ad app credential reset --id " + newAppClientID, afterWindow, 10 * time.Minute},
		{"role-only", func(w *applyWorld, ident string) {
			w.holdCert(ident)
			w.listed(applyEntityScope)
			w.listed(applyMainWorkspace)
			w.listed(applyAuxScope)
		}, "role assignment create", afterWindow, 10 * time.Minute},
		{"nothing-created", func(w *applyWorld, ident string) { w.holdCert(ident) }, "", afterWindow, 0},
		{"nothing-created-within-the-role-attempt-window", func(w *applyWorld, ident string) { w.holdCert(ident) }, "", 2 * time.Minute, 10 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, _ := firstIdentityRun(t)
			c.arrange(w, sha1Hex(leafDER(t, readBytes(t, w.plan.CertificatePath))))
			logged := len(w.lines("az.log"))
			w.clock = applyNow.Add(c.rerunAt)
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			got := w.mutatingSince(logged)
			switch {
			case c.creates == "" && len(got) != 0:
				t.Fatalf("nothing was missing, yet the rerun made mutating calls: %q", got)
			case c.creates != "" && (len(got) == 0 || !strings.HasPrefix(got[0], c.creates)):
				t.Fatalf("want the rerun to create %q, got %q", c.creates, got)
			case c.creates != "" && c.name != "role-only" && len(got) != 1:
				t.Fatalf("want exactly one create, got %q", got)
			}
			if want := []time.Duration{10 * time.Minute, c.wait}; len(w.waits) != 2 || w.waits[0] != want[0] || w.waits[1] != want[1] {
				t.Fatalf("verify waits %v, want %v", w.waits, want)
			}
		})
	}
}

// P31b
func TestSetupApplyNewIdentityResumesAtTheFailedStep(t *testing.T) {
	cases := []struct {
		name, step        string
		arrange           func(w *applyWorld)
		azure             func(w *applyWorld, nonce, ident string)
		apps, sps, resets int // create calls across both runs
	}{
		{"application-created-but-az-exited-1", stepApp,
			func(w *applyWorld) { w.failing("ad app create", "") },
			func(w *applyWorld, nonce, _ string) { w.holdApp(nonce) }, 1, 1, 1},
		{"service-principal-create-exited-1", stepSP,
			func(w *applyWorld) { w.failing("ad sp create", "") },
			func(w *applyWorld, nonce, _ string) { w.holdApp(nonce) }, 1, 2, 1},
		{"service-principal-created-but-az-exited-1", stepSP,
			func(w *applyWorld) { w.failing("ad sp create", identitySPDoc) },
			func(w *applyWorld, nonce, _ string) { w.holdApp(nonce); w.holdSP() }, 1, 1, 1},
		{"certificate-credential-reset-exited-1", stepCredential,
			func(w *applyWorld) { w.failing("ad app credential reset", "") },
			func(w *applyWorld, nonce, _ string) { w.holdApp(nonce); w.holdSP() }, 1, 1, 2},
		{"certificate-credential-reset-succeeded-but-az-exited-1", stepCredential,
			func(w *applyWorld) { w.failing("ad app credential reset", identityResetDoc) },
			func(w *applyWorld, nonce, ident string) { w.holdApp(nonce); w.holdSP(); w.holdCert(ident) }, 1, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newIdentityWorld(t)
			c.arrange(w)
			if err := w.run(); err == nil {
				t.Fatal("the failing az call must fail the apply")
			}
			j := w.journal()
			var failed []SetupStep
			for _, s := range j.Steps {
				if s.Status == StepFailed {
					failed = append(failed, s)
				}
			}
			if j.Complete || len(failed) != 1 || failed[0].Name != c.step || failed[0].Result != "exited with code 1" {
				t.Fatalf("want one failed %q step classed %q and complete=false, got %+v complete=%v", c.step, "exited with code 1", failed, j.Complete)
			}
			report := w.stderr.String()
			for _, want := range []string{"completed: " + stepCertFile, "failed: " + c.step + " (exited with code 1)", "remaining:"} {
				if !strings.Contains(report, want) {
					t.Errorf("the report lacks %q:\n%s", want, report)
				}
			}
			if len(w.waits) != 0 {
				t.Fatal("verify must not run after a failed step")
			}
			cert := readBytes(t, w.plan.CertificatePath)

			// Azure now holds what the failed run left; the failing call is fixed.
			w.identityAzure()
			c.azure(w, j.DisplayName, sha1Hex(leafDER(t, cert)))
			w.plan.CertificateExists = true
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			all := w.lines("az.log")
			for prefix, want := range map[string]int{"ad app create": c.apps, "ad sp create": c.sps, "ad app credential reset": c.resets} {
				if got := countPrefix(all, prefix); got != want {
					t.Errorf("%d %q calls across both runs, want %d", got, prefix, want)
				}
			}
			if !bytes.Equal(cert, readBytes(t, w.plan.CertificatePath)) {
				t.Fatal("the resumed run changed the certificate file")
			}
			if after := w.journal(); !after.Complete || after.DisplayName != j.DisplayName || after.ClientID != newAppClientID {
				t.Fatalf("resumed journal complete=%v name %q client %q", after.Complete, after.DisplayName, after.ClientID)
			}
		})
	}
}

// P32b
func TestSetupApplyNewIdentityNeverOverwritesAnExistingCertificateFile(t *testing.T) {
	validFile := func(t *testing.T, mode os.FileMode) string {
		return writeCertFile(t, time.Now().Add(400*24*time.Hour), mode)
	}

	t.Run("a-valid-existing-file-is-uploaded-and-left-unchanged", func(t *testing.T) {
		w := newIdentityWorld(t)
		w.plan.CertificatePath, w.plan.CertificateExists = validFile(t, 0600), true
		before := readBytes(t, w.plan.CertificatePath)
		infoBefore := mustStat(t, w.plan.CertificatePath)
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		infoAfter := mustStat(t, w.plan.CertificatePath)
		if !os.SameFile(infoBefore, infoAfter) || !bytes.Equal(before, readBytes(t, w.plan.CertificatePath)) || infoAfter.Mode().Perm() != 0600 {
			t.Fatal("the existing certificate file was rewritten or re-moded")
		}
		uploaded := pemBlocks(readBytes(t, w.path("uploaded.pem")))
		if len(uploaded) != 1 || uploaded[0].Type != "CERTIFICATE" || !bytes.Equal(uploaded[0].Bytes, leafDER(t, before)) {
			t.Fatalf("the public certificate of the existing file was not uploaded: %d blocks", len(uploaded))
		}
		if got := w.mutating(); len(got) != 6 {
			t.Fatalf("want the app, SP, credential and 3 role creates, got %q", got)
		}
		for _, s := range w.journal().Steps {
			if s.Name == stepCertFile && s.Status != StepUnchanged {
				t.Errorf("the certificate file step is %s, want unchanged", s.Status)
			}
		}
		w.reloadConfig()
		if a := w.plan.Config.Auth; a == nil || a.CertificatePath != w.plan.CertificatePath || a.ClientID != newAppClientID {
			t.Fatalf("written auth %+v", a)
		}
	})

	refusals := []struct {
		name, want string
		arrange    func(t *testing.T, w *applyWorld) (path string)
	}{
		{"symlink", "symbolic link", func(t *testing.T, w *applyWorld) string {
			target := validFile(t, 0600)
			link := filepath.Join(t.TempDir(), "worker.pem")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			w.plan.CertificatePath = link
			return target
		}},
		{"mode-0644", "0644", func(t *testing.T, w *applyWorld) string {
			w.plan.CertificatePath = validFile(t, 0644)
			return w.plan.CertificatePath
		}},
		{"unparsable", "could not be parsed", func(t *testing.T, w *applyWorld) string {
			w.plan.CertificatePath = writeFileMode(t, []byte(garbagePEM), 0600)
			return w.plan.CertificatePath
		}},
		{"key-mismatch", "no certificate matching its private key", func(t *testing.T, w *applyWorld) string {
			w.plan.CertificatePath = mismatchedKeyCertFile(t)
			return w.plan.CertificatePath
		}},
		{"expired", "expired", func(t *testing.T, w *applyWorld) string {
			w.plan.CertificatePath = writeCertFile(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 0600)
			return w.plan.CertificatePath
		}},
		{"foreign-owner", "owned by", func(t *testing.T, w *applyWorld) string {
			path := validFile(t, 0600)
			if err := os.Chown(path, os.Getuid()+1, -1); err != nil {
				t.Skipf("handing a file to another uid needs root: %v", err)
			}
			w.plan.CertificatePath = path
			return path
		}},
	}
	for _, c := range refusals {
		t.Run(c.name+"-exits-before-the-prompt-and-is-left-untouched", func(t *testing.T) {
			w := newIdentityWorld(t)
			w.plan.CertificateExists = true
			target := c.arrange(t, w)
			before, modeBefore := readBytes(t, target), mustStat(t, target).Mode()
			requireRefusedBeforePrompt(t, w, w.run(), nil, c.want)
			if !bytes.Equal(before, readBytes(t, target)) || mustStat(t, target).Mode() != modeBefore {
				t.Fatal("the refused certificate file was changed")
			}
			if c.name == "symlink" {
				if fi, err := os.Lstat(w.plan.CertificatePath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("the symbolic link was replaced: %v", err)
				}
			}
		})
	}

	t.Run("a-file-that-appears-after-the-check-fails-the-step-with-its-bytes-unchanged", func(t *testing.T) {
		w := newIdentityWorld(t)
		late := readBytes(t, validFile(t, 0600))
		w.onConfirm = func() {
			if err := os.WriteFile(w.plan.CertificatePath, late, 0600); err != nil {
				t.Error(err)
			}
		}
		if err := w.run(); err == nil {
			t.Fatal("a certificate file created after the check must fail the apply")
		}
		if !bytes.Equal(late, readBytes(t, w.plan.CertificatePath)) {
			t.Fatal("the file that appeared was overwritten")
		}
		requireNoMutation(t, w, "late-file")
		if j := w.journal(); j.Complete || w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepCertFile+" (") {
			t.Fatalf("want the %q step failed after the prompt and complete=false:\n%s", stepCertFile, w.stderr.String())
		}
	})
	t.Run("a-symbolic-link-planted-after-the-check-is-not-followed", func(t *testing.T) {
		w := newIdentityWorld(t)
		target := filepath.Join(t.TempDir(), "elsewhere.pem")
		w.onConfirm = func() {
			if err := os.Symlink(target, w.plan.CertificatePath); err != nil {
				t.Error(err)
			}
		}
		if err := w.run(); err == nil {
			t.Fatal("a symbolic link planted after the check must fail the apply")
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the key was written through the planted symbolic link: %v", err)
		}
		requireNoMutation(t, w, "planted-link")
		if fi, err := os.Lstat(w.plan.CertificatePath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the planted symbolic link was replaced: %v", err)
		}
		if w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepCertFile+" (") {
			t.Fatalf("want the %q step failed after the prompt:\n%s", stepCertFile, w.stderr.String())
		}
	})
}

func TestSetupApplyNewIdentityMissingCertificateDirectoryFailsTheStepBeforeAnythingExistsInAzure(t *testing.T) {
	w := newIdentityWorld(t)
	dir := filepath.Join(t.TempDir(), "missing")
	w.plan.CertificatePath = filepath.Join(dir, "worker.pem")
	if err := w.run(); err == nil {
		t.Fatal("a certificate path in a missing directory must fail the apply")
	}
	requireNoMutation(t, w, "missing-directory")
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the parent directory was created: %v", err)
	}
	if j := w.journal(); j.Complete || w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepCertFile+" (") {
		t.Fatalf("want the %q step failed after the prompt and complete=false:\n%s", stepCertFile, w.stderr.String())
	}
	if name := w.journal().DisplayName; name != "" {
		t.Fatalf("the nonce name %q was journaled although nothing exists in Azure: a rerun would look for an application that was never created", name)
	}

	// The operator fixes the path; the plain rerun creates normally, with no --client-id.
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := w.run(); err != nil {
		t.Fatalf("the rerun after fixing the certificate directory must succeed: %v", err)
	}
	log := w.lines("az.log")
	if countPrefix(log, "ad app create") != 1 {
		t.Fatalf("the rerun must create the application once:\n%s", strings.Join(log, "\n"))
	}
	if j := w.journal(); !nonceNamePattern(identityBaseName).MatchString(j.DisplayName) || !j.Complete {
		t.Fatalf("journal name %q complete=%v after the rerun", j.DisplayName, j.Complete)
	}
}

// The application is re-read after the prompt: a different application that now
// carries the name must not be taken for the one the plan showed.
func TestSetupApplyNewIdentityRefusesADifferentAppThanTheOneThePlanAdopted(t *testing.T) {
	nonce := identityBaseName + "-" + seededNonceHex
	w := newIdentityWorld(t)
	w.seedJournal("", nonce)
	w.holdApp(nonce)
	w.onConfirm = func() {
		w.ok("ad app list", "displayName eq", "["+appDoc(otherAppClientID, nonce)+"]")
		w.install()
	}
	err := w.run()
	if err == nil {
		t.Fatal("an application other than the adopted one must fail the apply")
	}
	for _, want := range []string{newAppClientID, otherAppClientID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not name %s: %v", want, err)
		}
	}
	requireNoMutation(t, w, "different-app")
	if w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepApp+" (") {
		t.Fatalf("want the %q step failed after the prompt:\n%s", stepApp, w.stderr.String())
	}
	if j := w.journal(); j.Complete || j.DisplayName != nonce {
		t.Fatalf("journal complete=%v name %q, want incomplete and still bound to the recorded nonce", j.Complete, j.DisplayName)
	}
}

// The adopted application disappearing after the prompt must not turn into a
// create: that would make a different application than the one the plan showed.
func TestSetupApplyNewIdentityRefusesToCreateWhenTheAdoptedAppIsGoneAfterThePrompt(t *testing.T) {
	nonce := identityBaseName + "-" + seededNonceHex
	w := newIdentityWorld(t)
	w.seedJournal("", nonce)
	w.holdApp(nonce)
	w.onConfirm = func() {
		w.ok("ad app list", "displayName eq", "[]")
		w.install()
	}
	err := w.run()
	if err == nil {
		t.Fatal("an adopted application that is gone must fail the apply")
	}
	for _, want := range []string{newAppClientID, nonce} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not name %s: %v", want, err)
		}
	}
	requireNoMutation(t, w, "adopted-app-gone")
	if w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepApp+" (") {
		t.Fatalf("want the %q step failed after the prompt:\n%s", stepApp, w.stderr.String())
	}
}

// The nonce is journaled only when az ad app create is about to run: a failed
// lookup before it leaves no nonce for a rerun to search for.
func TestSetupApplyNewIdentityJournalsTheNonceOnlyRightBeforeCreatingTheApp(t *testing.T) {
	w := newIdentityWorld(t)
	w.rule(applyRule{prefix: "ad app list", needle: "displayName eq", code: 1})
	if err := w.run(); err == nil {
		t.Fatal("a failed application lookup must fail the apply")
	}
	requireNoMutation(t, w, "failed-lookup")
	if j := w.journal(); j.Complete || j.DisplayName != "" || j.ClientID != "" ||
		w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepApp+" (") {
		t.Fatalf("want the %q step failed with no nonce journaled (name %q, client %q, complete=%v):\n%s", stepApp, j.DisplayName, j.ClientID, j.Complete, w.stderr.String())
	}

	// The lookup works again; the plain rerun creates once, with no --client-id.
	w.ok("ad app list", "displayName eq", "[]")
	if err := w.run(); err != nil {
		t.Fatalf("the rerun after the lookup recovered must succeed: %v", err)
	}
	if n := countPrefix(w.lines("az.log"), "ad app create"); n != 1 {
		t.Fatalf("the rerun must create the application once, got %d", n)
	}
	if j := w.journal(); !nonceNamePattern(identityBaseName).MatchString(j.DisplayName) || !j.Complete {
		t.Fatalf("journal name %q complete=%v after the rerun", j.DisplayName, j.Complete)
	}
}

// The existing certificate file is re-read at its step: replacing it while the
// prompt is open must not upload the old leaf's thumbprint.
func TestSetupApplyNewIdentityRefusesAnExistingCertificateReplacedAfterThePrompt(t *testing.T) {
	w := newIdentityWorld(t)
	w.plan.CertificatePath, w.plan.CertificateExists = writeCertFile(t, time.Now().Add(400*24*time.Hour), 0600), true
	replacement := readBytes(t, writeCertFile(t, time.Now().Add(500*24*time.Hour), 0600))
	if bytes.Equal(replacement, readBytes(t, w.plan.CertificatePath)) {
		t.Fatal("the replacement must be a different certificate")
	}
	w.onConfirm = func() {
		if err := os.WriteFile(w.plan.CertificatePath, replacement, 0600); err != nil {
			t.Error(err)
		}
	}
	if err := w.run(); err == nil {
		t.Fatal("a certificate file replaced after the prompt must fail the apply")
	}
	if !bytes.Equal(replacement, readBytes(t, w.plan.CertificatePath)) {
		t.Fatal("the replaced file was changed")
	}
	requireNoMutation(t, w, "replaced-cert")
	if j := w.journal(); j.Complete || w.confirms != 1 || !strings.Contains(w.stderr.String(), "failed: "+stepCertFile+" (") {
		t.Fatalf("want the %q step failed after the prompt and complete=false:\n%s", stepCertFile, w.stderr.String())
	}
}

// P50
func TestSetupApplyNewIdentityAdoptsOnlyTheUniqueNonceMatch(t *testing.T) {
	nonce := identityBaseName + "-" + seededNonceHex

	t.Run("one-match-is-adopted-and-shown-in-the-confirmation", func(t *testing.T) {
		w := newIdentityWorld(t)
		w.seedJournal("", nonce)
		w.holdApp(nonce)
		w.ok("ad app list", "appId eq", "[]")
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		log := w.lines("az.log")
		if countPrefix(log, "ad app create") != 0 || indexFrom(log, "ad sp create --id "+newAppClientID, 0) < 0 {
			t.Fatalf("the unique match must be adopted, not created again:\n%s", strings.Join(log, "\n"))
		}
		if !strings.Contains(w.planSeen, newAppClientID) || !strings.Contains(w.planSeen, "adopt") {
			t.Fatalf("the adopted application is not shown in the confirmation:\n%s", w.planSeen)
		}
		if j := w.journal(); j.DisplayName != nonce || j.ClientID != newAppClientID || !j.Complete {
			t.Fatalf("journal name %q client %q complete=%v, want the recorded nonce and the adopted app", j.DisplayName, j.ClientID, j.Complete)
		}
	})

	refusals := []struct {
		name string
		list string // what the name search returns
	}{
		{"zero-matches", "[]"},
		{"two-matches", "[" + appDoc(newAppClientID, nonce) + "," + appDoc(otherAppClientID, nonce) + "]"},
		{"only-a-longer-name", "[" + appDoc(newAppClientID, nonce+"-extra") + "]"},
		{"only-a-prefix-of-the-name", "[" + appDoc(newAppClientID, identityBaseName) + "]"},
	}
	for _, c := range refusals {
		t.Run(c.name+"-exits-1-asking-for-client-id", func(t *testing.T) {
			w := newIdentityWorld(t)
			journal := w.seedJournal("", nonce)
			w.ok("ad app list", "displayName eq", c.list)
			requireRefusedBeforePrompt(t, w, w.run(), journal, "--client-id", nonce)
			requireNoCertificateFile(t, w)
		})
	}
}

// Q1
func TestSetupApplyNewIdentityClientIDAdoptsOnlyTheRecordedNonceApp(t *testing.T) {
	nonce := identityBaseName + "-" + seededNonceHex
	// world gives --client-id its app: the ID search finds it, the name search does not.
	world := func(t *testing.T, recordedID string) (*applyWorld, []byte) {
		w := newIdentityWorld(t)
		w.plan.ClientID = newAppClientID
		journal := w.seedJournal(recordedID, nonce)
		w.ok("ad app list", "appId eq", "["+appDoc(newAppClientID, nonce)+"]")
		return w, journal
	}

	for name, recorded := range map[string]string{"nonce-recorded-without-an-app-id": "", "recorded-id-equals-client-id": newAppClientID} {
		t.Run(name, func(t *testing.T) {
			w, _ := world(t, recorded)
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			if countPrefix(w.lines("az.log"), "ad app create") != 0 || w.journal().ClientID != newAppClientID {
				t.Fatalf("--client-id must adopt the recorded nonce app without creating one:\n%s", strings.Join(w.lines("az.log"), "\n"))
			}
		})
	}

	t.Run("no-journal", func(t *testing.T) {
		w := newIdentityWorld(t)
		w.plan.ClientID = newAppClientID
		w.ok("ad app list", "appId eq", "["+appDoc(newAppClientID, nonce)+"]")
		requireRefusedBeforePrompt(t, w, w.run(), nil)
		requireNoCertificateFile(t, w)
	})
	t.Run("journal-without-a-nonce", func(t *testing.T) {
		w, _ := world(t, "")
		journal := w.seedJournal("", "")
		requireRefusedBeforePrompt(t, w, w.run(), journal)
	})
	t.Run("another-client-id-is-recorded", func(t *testing.T) {
		w, _ := world(t, "")
		journal := w.seedJournal(otherAppClientID, nonce)
		requireRefusedBeforePrompt(t, w, w.run(), journal)
	})
	t.Run("the-app-has-another-name", func(t *testing.T) {
		w, journal := world(t, "")
		w.ok("ad app list", "appId eq", "["+appDoc(newAppClientID, "someone-elses-app")+"]")
		requireRefusedBeforePrompt(t, w, w.run(), journal)
	})
	t.Run("the-app-is-gone", func(t *testing.T) {
		w, journal := world(t, "")
		w.ok("ad app list", "appId eq", "[]")
		requireRefusedBeforePrompt(t, w, w.run(), journal)
	})
}

// Q10: a silent re-create would make a second identity.
func TestSetupApplyNewIdentityRecordedAppGoneStopsBeforeThePrompt(t *testing.T) {
	nonce := identityBaseName + "-" + seededNonceHex
	t.Run("control-the-recorded-app-exists", func(t *testing.T) {
		w := newIdentityWorld(t)
		w.seedJournal(newAppClientID, nonce)
		w.holdApp(nonce)
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("recorded-app-is-gone-even-when-another-app-carries-the-name", func(t *testing.T) {
		w := newIdentityWorld(t)
		journal := w.seedJournal(newAppClientID, nonce)
		w.ok("ad app list", "appId eq", "[]")
		w.ok("ad app list", "displayName eq", "["+appDoc(otherAppClientID, nonce)+"]")
		requireRefusedBeforePrompt(t, w, w.run(), journal)
		requireNoCertificateFile(t, w)
	})
}

// binding: the display-name base joins tenant, identity and config path.
func TestSetupApplyNewIdentityJournalRefusesAChangedDisplayName(t *testing.T) {
	nonce := "team-worker-" + seededNonceHex
	world := func(t *testing.T, base string) (*applyWorld, []byte) {
		w := newIdentityWorld(t)
		w.plan.DisplayName = base
		journal := w.seedJournal("", nonce)
		w.holdApp(nonce)
		return w, journal
	}
	t.Run("control-the-same-base-resumes", func(t *testing.T) {
		w, _ := world(t, "team-worker")
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
		if countPrefix(w.lines("az.log"), "ad app create") != 0 || w.journal().DisplayName != nonce {
			t.Fatal("the same display-name base must resume the recorded nonce application")
		}
	})
	t.Run("another-base-is-a-different-request", func(t *testing.T) {
		w, journal := world(t, "other-worker")
		requireRefusedBeforePrompt(t, w, w.run(), journal, "belongs to a different request", "display name")
		requireNoCertificateFile(t, w)
	})
}
