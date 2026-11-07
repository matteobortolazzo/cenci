package autostart

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testUID       = 501
	testLabel     = "io.github.matteobortolazzo.cenci-watch"
	testUnitName  = "cenci-watch.service"
	handWritten   = "[Service]\nExecStart=/hand/written\n"
	testPathValue = "/opt/tools/bin:/usr/bin"
)

// rule makes a fake service-manager binary answer a call with a fixed exit
// status. match is compared against the call's argument string; a trailing `*`
// makes it a prefix match, anything else must match exactly. The first matching
// rule wins and every unmatched call exits 0. A sleeping rule never answers: the
// fake replaces itself with a long sleep, so only the backend's own command
// timeout ends the call.
type rule struct {
	match  string
	code   int
	stderr string
	stdout string
	sleep  bool
}

// commandTimeoutForTests is the per-command bound the test backends run with, so
// a sleeping fake costs a fraction of a second instead of the 10s default.
const commandTimeoutForTests = 300 * time.Millisecond

// world is one isolated host: temp HOME and XDG dirs, an injected environment,
// a PATH replaced (never prepended) with a dir holding only the fake binaries,
// and one ordered call log shared by the fakes and the daemon seam.
type world struct {
	t       *testing.T
	home    string
	xdg     string
	bin     string
	log     string
	state   string // one present/absent line per fake call, for the file at watch
	env     map[string]string
	alive   bool // a daemon was already running before Install
	stopErr error
	// neverReady keeps the daemon unreachable even after the unit was started.
	neverReady bool
	watch      string // file whose presence each fake call records
	// sleepPath is the absolute sleep binary a sleeping fake execs into; it is
	// resolved before PATH is replaced.
	sleepPath string
	// bootedDir stands in for /run/systemd/system: absent unless booted() is called.
	bootedDir string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary to build a hanging fake: %v", err)
	}
	root := t.TempDir()
	w := &world{
		t:         t,
		home:      filepath.Join(root, "home"),
		xdg:       filepath.Join(root, "xdg"),
		bin:       filepath.Join(root, "bin"),
		log:       filepath.Join(root, "calls.log"),
		state:     filepath.Join(root, "state.log"),
		sleepPath: sleepPath,
		bootedDir: filepath.Join(root, "run-systemd-system"),
	}
	w.env = map[string]string{"PATH": testPathValue, "XDG_CONFIG_HOME": w.xdg}
	for _, dir := range []string{w.home, w.xdg, w.bin, filepath.Join(w.home, ".local", "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(w.home, ".local", "bin", "cenci"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", w.bin)
	return w
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fake installs a logging fake binary named name on the replaced PATH.
func (w *world) fake(name string, rules ...rule) {
	w.t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("printf '%s\\n' \"" + name + " $*\" >> " + shellQuote(w.log) + "\n")
	b.WriteString("if [ -e " + shellQuote(w.watch) + " ]; then echo present; else echo absent; fi >> " + shellQuote(w.state) + "\n")
	b.WriteString("case \"$*\" in\n")
	for _, r := range rules {
		pattern := shellQuote(r.match)
		if strings.HasSuffix(r.match, "*") {
			pattern = shellQuote(strings.TrimSuffix(r.match, "*")) + "*"
		}
		b.WriteString(pattern + ")\n")
		if r.sleep {
			b.WriteString("exec " + shellQuote(w.sleepPath) + " 30 ;;\n")
			continue
		}
		if r.stdout != "" {
			b.WriteString("echo " + shellQuote(r.stdout) + "\n")
		}
		if r.stderr != "" {
			b.WriteString("echo " + shellQuote(r.stderr) + " >&2\n")
		}
		b.WriteString("exit " + strconv.Itoa(r.code) + " ;;\n")
	}
	b.WriteString("esac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(w.bin, name), []byte(b.String()), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) logCall(line string) {
	f, err := os.OpenFile(w.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) calls() []string { return readLines(w.t, w.log) }

// started reports whether the service manager was asked to start the unit, which
// is the point the fake daemon becomes reachable (unless neverReady).
func (w *world) started() bool {
	for _, call := range w.calls() {
		if strings.Contains(call, "enable --now") || strings.Contains(call, " bootstrap ") {
			return true
		}
	}
	return false
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func readTemplate(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "plugin", "service", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (w *world) backend(goos string) Backend {
	w.t.Helper()
	return New(Options{
		GOOS: goos,
		Home: w.home,
		LookupEnv: func(k string) (string, bool) {
			v, ok := w.env[k]
			return v, ok
		},
		UID: testUID,
		Templates: Templates{
			Systemd: readTemplate(w.t, "cenci-watch.service"),
			Launchd: readTemplate(w.t, "cenci-watch.plist"),
		},
		Daemon: Daemon{
			Alive: func() bool { return w.alive || (!w.neverReady && w.started()) },
			Stop: func() error {
				w.logCall("STOP")
				return w.stopErr
			},
		},
		ReadyTimeout:     100 * time.Millisecond,
		CommandTimeout:   commandTimeoutForTests,
		SystemdBootedDir: w.bootedDir,
	})
}

// booted marks the host as booted with systemd (the stand-in for
// /run/systemd/system exists).
func (w *world) booted() {
	w.t.Helper()
	if err := os.MkdirAll(w.bootedDir, 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) unitPath() string {
	return filepath.Join(w.xdg, "systemd", "user", testUnitName)
}

func (w *world) plistPath() string {
	return filepath.Join(w.home, "Library", "LaunchAgents", testLabel+".plist")
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func notExist(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func equalLines(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("call log mismatch\n got: %q\nwant: %q", got, want)
	}
}

func assertNoMutation(t *testing.T, calls []string, verbs ...string) {
	t.Helper()
	for _, call := range calls {
		for _, verb := range verbs {
			if strings.Contains(call, verb) {
				t.Errorf("unexpected mutation call %q in %q", call, calls)
			}
		}
	}
}

// flavor describes one backend for the tests that assert the same contract on
// both: the OS the backend is injected as, its fake binary and where its file
// lives.
type flavor struct {
	name      string
	goos      string
	binary    string
	path      func(*world) string
	marker    string
	fresh     []rule // fake answers for a host where nothing is loaded yet
	mutations []string
}

var flavors = []flavor{
	{
		name: "systemd", goos: "linux", binary: "systemctl",
		path:      (*world).unitPath,
		marker:    "# Managed by cenci daemon install-service",
		mutations: []string{"daemon-reload", "enable", "disable", "restart"},
	},
	{
		name: "launchd", goos: "darwin", binary: "launchctl",
		path:      (*world).plistPath,
		marker:    "<!-- Managed by cenci daemon install-service -->",
		fresh:     []rule{{match: "print gui/501/*", code: 113}},
		mutations: []string{"bootstrap", "bootout", "kickstart"},
	},
}

func (f flavor) setup(t *testing.T) (*world, Backend) {
	t.Helper()
	w := newWorld(t)
	w.watch = f.path(w)
	w.fake(f.binary, f.fresh...)
	return w, w.backend(f.goos)
}

func TestTemplates_CarryTheContractedDirectives(t *testing.T) {
	unit := readTemplate(t, "cenci-watch.service")
	if first, _, _ := strings.Cut(unit, "\n"); first != "# Managed by cenci daemon install-service" {
		t.Errorf("unit first line = %q, want the marker", first)
	}
	for _, want := range []string{"ExecStart=%h/.local/bin/cenci daemon start", "Restart=on-failure", "KillMode=process", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	plist := readTemplate(t, "cenci-watch.plist")
	lines := strings.Split(plist, "\n")
	if !strings.HasPrefix(lines[0], "<?xml") || lines[1] != "<!-- Managed by cenci daemon install-service -->" {
		t.Errorf("plist must carry the marker comment right after the XML declaration, got %q / %q", lines[0], lines[1])
	}
	for _, want := range []string{
		"<string>" + testLabel + "</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
		"<key>AbandonProcessGroup</key>\n\t<true/>",
		"<string>@CENCI_HOME@/.local/bin/cenci</string>\n\t\t<string>daemon</string>\n\t\t<string>start</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
}

// -- systemd ------------------------------------------------------------------

func TestSystemdInstall_WritesMarkedUnitThenReloadsAndEnables(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")
	w.env["XDG_STATE_HOME"] = "/state/dir"
	b := w.backend("linux")

	outcome, err := b.Install(context.Background(), false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if outcome != OutcomeInstalled {
		t.Errorf("outcome = %v, want installed", outcome)
	}

	unit := mustRead(t, w.unitPath())
	if first, _, _ := strings.Cut(unit, "\n"); first != "# Managed by cenci daemon install-service" {
		t.Errorf("first line = %q, want the marker", first)
	}
	for _, want := range []string{
		"ExecStart=%h/.local/bin/cenci daemon start",
		`Environment="PATH=` + testPathValue + `"`,
		`Environment="XDG_STATE_HOME=/state/dir"`,
		`Environment="XDG_CONFIG_HOME=` + w.xdg + `"`,
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "CENCI_SOCKET_DIR") || strings.Contains(unit, "@CENCI_") {
		t.Errorf("unit must omit unset variables and leave no placeholder:\n%s", unit)
	}
	info, err := os.Stat(w.unitPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("unit mode = %v, want 0644", info.Mode().Perm())
	}
	equalLines(t, w.calls(), []string{
		"systemctl --user show-environment",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now cenci-watch.service",
	})
}

func TestSystemdInstall_DefaultsUnitDirToHomeConfig(t *testing.T) {
	w := newWorld(t)
	delete(w.env, "XDG_CONFIG_HOME")
	w.watch = filepath.Join(w.home, ".config", "systemd", "user", testUnitName)
	w.fake("systemctl")

	if _, err := w.backend("linux").Install(context.Background(), false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	unit := mustRead(t, w.watch)
	if strings.Contains(unit, "XDG_CONFIG_HOME") {
		t.Errorf("an unset XDG_CONFIG_HOME must not be captured:\n%s", unit)
	}
}

func TestSystemdInstall_EscapesPercentAsDoublePercent(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")
	w.env["PATH"] = "/odd%h/bin"

	if _, err := w.backend("linux").Install(context.Background(), false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if unit := mustRead(t, w.unitPath()); !strings.Contains(unit, `Environment="PATH=/odd%%h/bin"`) {
		t.Errorf("a %% in a value must be written as %%%%:\n%s", unit)
	}
}

func TestSystemdInstall_SecondRunKeepsFileAndStillEnables(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")
	b := w.backend("linux")
	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(w.unitPath(), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.log); err != nil {
		t.Fatal(err)
	}

	outcome, err := b.Install(context.Background(), false)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if outcome != OutcomeAlreadyInstalled {
		t.Errorf("outcome = %v, want already-installed", outcome)
	}
	info, err := os.Stat(w.unitPath())
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("mtime changed to %v; an identical file must not be rewritten", info.ModTime())
	}
	equalLines(t, w.calls(), []string{
		"systemctl --user show-environment",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now cenci-watch.service",
	})
}

func TestSystemdInstall_ChangedCaptureRewritesMarkedFile(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")
	b := w.backend("linux")
	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	w.env["PATH"] = "/new/path"

	outcome, err := b.Install(context.Background(), false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if outcome != OutcomeInstalled {
		t.Errorf("outcome = %v, want installed (file differs)", outcome)
	}
	if unit := mustRead(t, w.unitPath()); !strings.Contains(unit, `"PATH=/new/path"`) {
		t.Errorf("the marked file must be rewritten with the new PATH:\n%s", unit)
	}
}

func TestSystemdUninstall_DisablesRemovesFileThenReloads(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")
	b := w.backend("linux")
	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{w.log, w.state} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := b.Uninstall(context.Background(), false)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if outcome != OutcomeRemoved {
		t.Errorf("outcome = %v, want removed", outcome)
	}
	if !notExist(t, w.unitPath()) {
		t.Error("unit file must be removed")
	}
	equalLines(t, w.calls(), []string{
		"systemctl --user show-environment",
		"systemctl --user disable --now cenci-watch.service",
		"systemctl --user daemon-reload",
	})
	// The file must still exist for show-environment and disable, and be gone
	// by the final daemon-reload.
	equalLines(t, readLines(t, w.state), []string{"present", "present", "absent"})
}

func TestSystemdInstall_HandsOverFromAnUnsupervisedDaemon(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl", rule{match: "--user is-active*", code: 3})
	w.alive = true

	if _, err := w.backend("linux").Install(context.Background(), false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"systemctl --user show-environment",
		"systemctl --user daemon-reload",
		"systemctl --user is-active cenci-watch.service",
		"STOP",
		"systemctl --user enable --now cenci-watch.service",
	})
}

func TestSystemdInstall_KeepsADaemonTheUnitAlreadyOwns(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl") // is-active exits 0: the unit supervises the daemon
	w.alive = true

	if _, err := w.backend("linux").Install(context.Background(), false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, call := range w.calls() {
		if call == "STOP" {
			t.Errorf("a daemon the active unit owns must not be stopped: %q", w.calls())
		}
	}
}

func TestSystemdInstall_StopFailureAbortsBeforeEnable(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl", rule{match: "--user is-active*", code: 3})
	w.alive = true
	w.stopErr = errors.New("stuck")

	_, err := w.backend("linux").Install(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("err = %v, want the stop failure", err)
	}
	assertNoMutation(t, w.calls(), "enable")
}

func TestSystemdInstall_NamesTheFailingCommand(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl", rule{match: "--user enable*", code: 1, stderr: "boom from systemd"})

	_, err := w.backend("linux").Install(context.Background(), false)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"systemctl --user enable --now cenci-watch.service", "boom from systemd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q", err, want)
		}
	}
}

func TestSystemdInstall_ReportsADaemonThatNeverBecomesReachable(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")
	w.neverReady = true // the unit started but nothing listens

	_, err := w.backend("linux").Install(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "did not become reachable") {
		t.Fatalf("err = %v, want a not-reachable error", err)
	}
}

func TestSystemdRestart_RestartsTheUnit(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl")

	if err := w.backend("linux").Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	equalLines(t, w.calls(), []string{"systemctl --user restart cenci-watch.service"})
}

func TestSystemdRestart_HandsOverFromAnUnsupervisedDaemon(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl", rule{match: "--user is-active*", code: 3})
	w.alive = true

	if err := w.backend("linux").Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"systemctl --user is-active cenci-watch.service",
		"STOP",
		"systemctl --user restart cenci-watch.service",
	})
}

func TestSystemdRestart_KeepsADaemonTheUnitAlreadyOwns(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl") // is-active exits 0: the unit supervises the daemon
	w.alive = true

	if err := w.backend("linux").Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"systemctl --user is-active cenci-watch.service",
		"systemctl --user restart cenci-watch.service",
	})
}

func TestSystemdRestart_StopFailureAbortsBeforeRestart(t *testing.T) {
	w := newWorld(t)
	w.watch = w.unitPath()
	w.fake("systemctl", rule{match: "--user is-active*", code: 3})
	w.alive = true
	w.stopErr = errors.New("stuck")

	err := w.backend("linux").Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("err = %v, want the stop failure", err)
	}
	assertNoMutation(t, w.calls(), "restart")
}

func TestSystemdStatus_Tokens(t *testing.T) {
	cases := []struct {
		name      string
		installed bool
		noBinary  bool
		rules     []rule
		want      State
	}{
		{name: "not installed", want: StateNotInstalled},
		{name: "enabled", installed: true, want: StateEnabled},
		{name: "disabled", installed: true, rules: []rule{{match: "--user is-enabled*", code: 1}}, want: StateDisabled},
		{name: "no user manager", installed: true, rules: []rule{{match: "--user show-environment", code: 1}}, want: StateUnsupported},
		{name: "no user manager and no file", rules: []rule{{match: "--user show-environment", code: 1}}, want: StateUnsupported},
		{name: "systemctl absent", installed: true, noBinary: true, want: StateUnsupported},
		{name: "show-environment times out", installed: true, rules: []rule{{match: "--user show-environment", sleep: true}}, want: StateUnsupported},
		{name: "is-enabled times out", installed: true, rules: []rule{{match: "--user is-enabled*", sleep: true}}, want: StateUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.watch = w.unitPath()
			if !tc.noBinary {
				w.fake("systemctl", tc.rules...)
			}
			if tc.installed {
				mustWrite(t, w.unitPath(), handWritten) // a file at the path counts, whoever wrote it
			}
			if got := w.backend("linux").Status(context.Background()); got != tc.want {
				t.Errorf("Status = %q, want %q", got, tc.want)
			}
		})
	}
}

// probeCase is one row of a Probe classification table. wantDoubt means the read
// is doubtful: a *ProbeError whose Reason contains every wantReason fragment and
// whose Remedy is the manual restart command. Otherwise the error must be nil.
type probeCase struct {
	name       string
	installed  bool
	noBinary   bool
	booted     bool
	rules      []rule
	wantState  State
	wantDoubt  bool
	wantReason []string
}

func runProbeCases(t *testing.T, goos, binary, wantRemedy string, pathOf func(*world) string, cases []probeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.watch = pathOf(w)
			if !tc.noBinary {
				w.fake(binary, tc.rules...)
			}
			if tc.installed {
				mustWrite(t, pathOf(w), handWritten) // a file at the path counts, whoever wrote it
			}
			if tc.booted {
				w.booted()
			}

			got, err := w.backend(goos).Probe(context.Background())
			if got != tc.wantState {
				t.Errorf("Probe state = %q, want %q (err %v)", got, tc.wantState, err)
			}
			if !tc.wantDoubt {
				if err != nil {
					t.Errorf("Probe err = %v, want a definitive read", err)
				}
				return
			}
			var probeErr *ProbeError
			if !errors.As(err, &probeErr) {
				t.Fatalf("Probe err = %v, want a *ProbeError", err)
			}
			for _, fragment := range tc.wantReason {
				if !strings.Contains(probeErr.Reason, fragment) {
					t.Errorf("Reason %q must contain %q", probeErr.Reason, fragment)
				}
			}
			if strings.Contains(probeErr.Reason, "\n") {
				t.Errorf("Reason must be one line, got %q", probeErr.Reason)
			}
			if probeErr.Remedy != wantRemedy {
				t.Errorf("Remedy = %q, want %q", probeErr.Remedy, wantRemedy)
			}
			if err.Error() != probeErr.Reason {
				t.Errorf("Error() = %q, want the Reason %q", err.Error(), probeErr.Reason)
			}
		})
	}
}

func TestSystemdProbe_Classification(t *testing.T) {
	showEnv := "--user show-environment"
	cases := []probeCase{
		// Doubtful reads: a cenci unit file exists but the manager did not answer cleanly.
		{name: "show-environment times out with a unit file", installed: true, rules: []rule{{match: showEnv, sleep: true}},
			wantState: StateUnsupported, wantDoubt: true, wantReason: []string{"show-environment", "timed out"}},
		{name: "no user bus on a systemd-booted host with a unit file", installed: true, booted: true,
			rules:     []rule{{match: showEnv, code: 1, stderr: "Failed to connect to bus:\nNo medium found"}},
			wantState: StateUnsupported, wantDoubt: true, wantReason: []string{"show-environment", "Failed to connect to bus: No medium found"}},
		{name: "is-enabled times out", installed: true, rules: []rule{{match: "--user is-enabled*", sleep: true}},
			wantState: StateUnsupported, wantDoubt: true, wantReason: []string{"is-enabled", "timed out"}},
		// Definitive reads.
		{name: "show-environment times out with no unit file", rules: []rule{{match: showEnv, sleep: true}}, wantState: StateUnsupported},
		{name: "no user bus on a booted host with no unit file", booted: true, rules: []rule{{match: showEnv, code: 1}}, wantState: StateUnsupported},
		{name: "unit file synced onto a host not booted with systemd", installed: true, rules: []rule{{match: showEnv, code: 1}}, wantState: StateUnsupported},
		{name: "systemctl missing with a unit file on a booted host", installed: true, booted: true, noBinary: true, wantState: StateUnsupported},
		{name: "manager answers and no unit file", wantState: StateNotInstalled},
		{name: "is-enabled exits non-zero without a timeout", installed: true, rules: []rule{{match: "--user is-enabled*", code: 1}}, wantState: StateDisabled},
		{name: "enabled", installed: true, wantState: StateEnabled},
	}
	runProbeCases(t, "linux", "systemctl", "systemctl --user restart cenci-watch.service", (*world).unitPath, cases)
}

func TestLaunchdProbe_Classification(t *testing.T) {
	remedy := "launchctl kickstart -k gui/501/" + testLabel
	cases := []probeCase{
		// Doubtful reads: a plist exists but launchctl timed out.
		{name: "print of the gui domain times out with a plist", installed: true, rules: []rule{{match: "print gui/501", sleep: true}},
			wantState: StateUnsupported, wantDoubt: true, wantReason: []string{"print gui/501", "timed out"}},
		{name: "print of the target times out", installed: true, rules: []rule{{match: "print gui/501/*", sleep: true}},
			wantState: StateUnsupported, wantDoubt: true, wantReason: []string{"print gui/501/" + testLabel, "timed out"}},
		// Definitive reads.
		{name: "print of the gui domain times out with no plist", rules: []rule{{match: "print gui/501", sleep: true}}, wantState: StateUnsupported},
		{name: "gui domain missing (an SSH-only session)", installed: true, rules: []rule{{match: "print gui/501", code: 113}}, wantState: StateUnsupported},
		{name: "launchctl missing", installed: true, noBinary: true, wantState: StateUnsupported},
		{name: "domain answers and no plist", wantState: StateNotInstalled},
		{name: "print of the target fails without a timeout", installed: true, rules: []rule{{match: "print gui/501/*", code: 113}}, wantState: StateDisabled},
		{name: "loaded", installed: true, wantState: StateEnabled},
	}
	runProbeCases(t, "darwin", "launchctl", remedy, (*world).plistPath, cases)
}

func TestSystemdInstall_UnsupportedHostFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		noBinary bool
		rules    []rule
	}{
		{name: "systemctl absent", noBinary: true},
		{name: "show-environment fails", rules: []rule{{match: "--user show-environment", code: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.watch = w.unitPath()
			if !tc.noBinary {
				w.fake("systemctl", tc.rules...)
			}
			_, err := w.backend("linux").Install(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), "no systemd user manager available") {
				t.Fatalf("err = %v, want the no-user-manager message", err)
			}
			if !notExist(t, w.unitPath()) {
				t.Error("nothing may be written on an unsupported host")
			}
			assertNoMutation(t, w.calls(), "daemon-reload", "enable")
		})
	}
}

// -- launchd ------------------------------------------------------------------

func TestLaunchdInstall_WritesMarkedPlistThenBootstraps(t *testing.T) {
	w := newWorld(t)
	// A HOME with XML metacharacters proves the plist is escaped.
	w.home = filepath.Join(filepath.Dir(w.home), "h&<m>")
	mustWrite(t, filepath.Join(w.home, ".local", "bin", "cenci"), "#!/bin/sh\n")
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", code: 113})
	w.env["PATH"] = "/a&b:/usr/bin"
	w.env["XDG_STATE_HOME"] = "/state/dir"
	delete(w.env, "XDG_CONFIG_HOME")

	outcome, err := w.backend("darwin").Install(context.Background(), false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if outcome != OutcomeInstalled {
		t.Errorf("outcome = %v, want installed", outcome)
	}

	plist := mustRead(t, w.plistPath())
	lines := strings.Split(plist, "\n")
	if lines[1] != "<!-- Managed by cenci daemon install-service -->" {
		t.Errorf("second line = %q, want the marker comment", lines[1])
	}
	for _, want := range []string{
		"<string>" + testLabel + "</string>",
		"<string>" + filepath.Join(filepath.Dir(w.home), "h&amp;&lt;m&gt;") + "/.local/bin/cenci</string>",
		"<key>PATH</key>\n\t\t<string>/a&amp;b:/usr/bin</string>",
		"<key>XDG_STATE_HOME</key>\n\t\t<string>/state/dir</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "CENCI_SOCKET_DIR") || strings.Contains(plist, "XDG_CONFIG_HOME") || strings.Contains(plist, "@CENCI_") {
		t.Errorf("plist must omit unset variables and leave no placeholder:\n%s", plist)
	}
	equalLines(t, w.calls(), []string{
		"launchctl print gui/501",
		"launchctl print gui/501/" + testLabel,
		"launchctl bootstrap gui/501 " + w.plistPath(),
	})
}

func TestLaunchdInstall_LoadedAndUnchangedMakesNoReload(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", code: 113})
	b := w.backend("darwin")
	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// From here on the agent is loaded (print exits 0) and owns the daemon.
	w.fake("launchctl")
	w.alive = true
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(w.plistPath(), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.log); err != nil {
		t.Fatal(err)
	}

	outcome, err := b.Install(context.Background(), false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if outcome != OutcomeAlreadyInstalled {
		t.Errorf("outcome = %v, want already-installed", outcome)
	}
	if info, err := os.Stat(w.plistPath()); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("an identical plist must not be rewritten (err=%v)", err)
	}
	assertNoMutation(t, w.calls(), "bootstrap", "bootout")
}

func TestLaunchdInstall_LoadedWithChangedPlistBootsOutThenBootstraps(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", code: 113})
	b := w.backend("darwin")
	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	w.fake("launchctl") // loaded
	w.env["PATH"] = "/new/path"
	if err := os.Remove(w.log); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"launchctl print gui/501",
		"launchctl print gui/501/" + testLabel,
		"launchctl bootout gui/501/" + testLabel,
		"launchctl bootstrap gui/501 " + w.plistPath(),
	})
}

func TestLaunchdInstall_HandsOverFromAnUnsupervisedDaemon(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", code: 113})
	w.alive = true

	if _, err := w.backend("darwin").Install(context.Background(), false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"launchctl print gui/501",
		"launchctl print gui/501/" + testLabel,
		"STOP",
		"launchctl bootstrap gui/501 " + w.plistPath(),
	})
}

func TestLaunchdUninstall_BootsOutOnlyWhenLoadedThenRemovesPlist(t *testing.T) {
	loaded := []rule{}
	notLoaded := []rule{{match: "print gui/501/*", code: 113}}
	cases := []struct {
		name  string
		rules []rule
		want  []string
	}{
		{"loaded", loaded, []string{
			"launchctl print gui/501",
			"launchctl print gui/501/" + testLabel,
			"launchctl bootout gui/501/" + testLabel,
		}},
		{"not loaded", notLoaded, []string{
			"launchctl print gui/501",
			"launchctl print gui/501/" + testLabel,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.watch = w.plistPath()
			w.fake("launchctl", tc.rules...)
			mustWrite(t, w.plistPath(), "<?xml version=\"1.0\"?>\n<!-- Managed by cenci daemon install-service -->\n")

			outcome, err := w.backend("darwin").Uninstall(context.Background(), false)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if outcome != OutcomeRemoved {
				t.Errorf("outcome = %v, want removed", outcome)
			}
			if !notExist(t, w.plistPath()) {
				t.Error("plist must be removed")
			}
			equalLines(t, w.calls(), tc.want)
		})
	}
}

func TestLaunchdRestart_KickstartsTheAgent(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl")

	if err := w.backend("darwin").Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	equalLines(t, w.calls(), []string{"launchctl kickstart -k gui/501/" + testLabel})
}

// A loaded agent whose job is not running does not own a live daemon: the
// daemon exited 0 through the already-running path, which KeepAlive leaves down.
func TestLaunchdRestart_HandsOverFromAnUnsupervisedDaemon(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", stdout: "\tstate = not running"})
	w.alive = true

	if err := w.backend("darwin").Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"launchctl print gui/501/" + testLabel,
		"STOP",
		"launchctl kickstart -k gui/501/" + testLabel,
	})
}

func TestLaunchdRestart_KeepsADaemonTheAgentAlreadyOwns(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", stdout: "\tstate = running"})
	w.alive = true

	if err := w.backend("darwin").Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	equalLines(t, w.calls(), []string{
		"launchctl print gui/501/" + testLabel,
		"launchctl kickstart -k gui/501/" + testLabel,
	})
}

// An identical marked plist whose agent is not loaded is left untouched but is
// still bootstrapped, with no bootout of something that is not there.
func TestLaunchdInstall_IdenticalPlistNotLoadedStillBootstraps(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501/*", code: 113})
	b := w.backend("darwin")
	if _, err := b.Install(context.Background(), false); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(w.plistPath(), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.log); err != nil {
		t.Fatal(err)
	}

	outcome, err := b.Install(context.Background(), false)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if outcome != OutcomeAlreadyInstalled {
		t.Errorf("outcome = %v, want already-installed", outcome)
	}
	if info, err := os.Stat(w.plistPath()); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("an identical plist must not be rewritten (err=%v)", err)
	}
	equalLines(t, w.calls(), []string{
		"launchctl print gui/501",
		"launchctl print gui/501/" + testLabel,
		"launchctl bootstrap gui/501 " + w.plistPath(),
	})
}

func TestLaunchdStatus_Tokens(t *testing.T) {
	cases := []struct {
		name      string
		installed bool
		noBinary  bool
		rules     []rule
		want      State
	}{
		{name: "not installed", want: StateNotInstalled},
		{name: "enabled", installed: true, want: StateEnabled},
		{name: "disabled", installed: true, rules: []rule{{match: "print gui/501/*", code: 113}}, want: StateDisabled},
		{name: "no gui domain", installed: true, rules: []rule{{match: "print gui/501", code: 113}}, want: StateUnsupported},
		{name: "launchctl absent", installed: true, noBinary: true, want: StateUnsupported},
		{name: "gui domain print times out", installed: true, rules: []rule{{match: "print gui/501", sleep: true}}, want: StateUnsupported},
		{name: "target print times out", installed: true, rules: []rule{{match: "print gui/501/*", sleep: true}}, want: StateUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.watch = w.plistPath()
			if !tc.noBinary {
				w.fake("launchctl", tc.rules...)
			}
			if tc.installed {
				mustWrite(t, w.plistPath(), "<?xml version=\"1.0\"?>\n")
			}
			if got := w.backend("darwin").Status(context.Background()); got != tc.want {
				t.Errorf("Status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLaunchdInstall_UnsupportedHostFailsClosed(t *testing.T) {
	w := newWorld(t)
	w.watch = w.plistPath()
	w.fake("launchctl", rule{match: "print gui/501", code: 113})

	_, err := w.backend("darwin").Install(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "no launchd user session available") {
		t.Fatalf("err = %v, want the no-launchd-session message", err)
	}
	if !notExist(t, w.plistPath()) {
		t.Error("nothing may be written on an unsupported host")
	}
	assertNoMutation(t, w.calls(), "bootstrap")
}

// -- both backends --------------------------------------------------------------

func TestInstall_RejectsUnsafeEnvironmentValuesAndWritesNothing(t *testing.T) {
	values := map[string]string{"newline": "/a\n/b", "double quote": `/a"b`, "backslash": `/a\b`}
	for _, f := range flavors {
		for name, value := range values {
			t.Run(f.name+"/"+name, func(t *testing.T) {
				w, b := f.setup(t)
				w.env["XDG_STATE_HOME"] = value

				_, err := b.Install(context.Background(), false)
				if err == nil || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
					t.Fatalf("err = %v, want one naming XDG_STATE_HOME", err)
				}
				if !notExist(t, f.path(w)) {
					t.Error("nothing may be written when a value is rejected")
				}
				assertNoMutation(t, w.calls(), f.mutations...)
			})
		}
	}
}

func TestInstall_MissingLauncherNamesPathAndWritesNothing(t *testing.T) {
	for _, f := range flavors {
		t.Run(f.name, func(t *testing.T) {
			w, b := f.setup(t)
			launcher := filepath.Join(w.home, ".local", "bin", "cenci")
			if err := os.Remove(launcher); err != nil {
				t.Fatal(err)
			}

			_, err := b.Install(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), launcher) {
				t.Fatalf("err = %v, want one naming %s", err, launcher)
			}
			if !notExist(t, f.path(w)) {
				t.Error("no unit or plist may be written without a launcher")
			}
			assertNoMutation(t, w.calls(), f.mutations...)
		})
	}
}

func TestInstall_UnmarkedFileIsRefusedWithoutForceAndReplacedWithIt(t *testing.T) {
	for _, f := range flavors {
		t.Run(f.name, func(t *testing.T) {
			w, b := f.setup(t)
			path := f.path(w)
			mustWrite(t, path, handWritten)

			_, err := b.Install(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("err = %v, want one naming %s and --force", err, path)
			}
			if got := mustRead(t, path); got != handWritten {
				t.Errorf("a refused install must leave the file byte-identical, got:\n%s", got)
			}
			assertNoMutation(t, w.calls(), f.mutations...)

			outcome, err := b.Install(context.Background(), true)
			if err != nil {
				t.Fatalf("Install --force: %v", err)
			}
			if outcome != OutcomeInstalled {
				t.Errorf("outcome = %v, want installed", outcome)
			}
			if got := mustRead(t, path); !strings.Contains(got, f.marker) {
				t.Errorf("--force must replace the file with the marked template:\n%s", got)
			}
		})
	}
}

func TestUninstall_UnmarkedFileIsKeptWithoutForceAndRemovedWithIt(t *testing.T) {
	for _, f := range flavors {
		t.Run(f.name, func(t *testing.T) {
			w, b := f.setup(t)
			path := f.path(w)
			mustWrite(t, path, handWritten)

			_, err := b.Uninstall(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("err = %v, want one naming %s and --force", err, path)
			}
			if got := mustRead(t, path); got != handWritten {
				t.Errorf("a refused uninstall must leave the file byte-identical, got:\n%s", got)
			}
			assertNoMutation(t, w.calls(), f.mutations...)

			outcome, err := b.Uninstall(context.Background(), true)
			if err != nil {
				t.Fatalf("Uninstall --force: %v", err)
			}
			if outcome != OutcomeRemoved || !notExist(t, path) {
				t.Errorf("--force must remove the file (outcome=%v)", outcome)
			}
		})
	}
}

func TestUninstall_NothingInstalledMakesNoServiceManagerCalls(t *testing.T) {
	for _, f := range flavors {
		t.Run(f.name, func(t *testing.T) {
			w, b := f.setup(t)

			outcome, err := b.Uninstall(context.Background(), false)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if outcome != OutcomeNotInstalled {
				t.Errorf("outcome = %v, want not-installed", outcome)
			}
			if calls := w.calls(); len(calls) != 0 {
				t.Errorf("nothing installed must make no service-manager calls, got %q", calls)
			}
		})
	}
}

func TestSandbox_MutatingVerbsFailClosedAndStatusIsUnsupported(t *testing.T) {
	for _, f := range flavors {
		t.Run(f.name, func(t *testing.T) {
			w, _ := f.setup(t)
			w.env["CENCI_SANDBOX"] = "1"
			b := w.backend(f.goos)

			if _, err := b.Install(context.Background(), false); err == nil || !strings.Contains(err.Error(), "not supported inside the cenci sandbox") {
				t.Errorf("Install err = %v, want the sandbox message", err)
			}
			if _, err := b.Uninstall(context.Background(), false); err == nil || !strings.Contains(err.Error(), "not supported inside the cenci sandbox") {
				t.Errorf("Uninstall err = %v, want the sandbox message", err)
			}
			if got := b.Status(context.Background()); got != StateUnsupported {
				t.Errorf("Status = %q, want unsupported", got)
			}
			if got, err := b.Probe(context.Background()); got != StateUnsupported || err != nil {
				t.Errorf("Probe = %q, %v; want a definitive unsupported", got, err)
			}
			if !notExist(t, f.path(w)) {
				t.Error("nothing may be written inside the sandbox")
			}
			if calls := w.calls(); len(calls) != 0 {
				t.Errorf("the sandbox must make no service-manager calls, got %q", calls)
			}
		})
	}
}

func TestNew_UnknownOSIsUnsupported(t *testing.T) {
	w := newWorld(t)
	b := w.backend("plan9")

	if got := b.Status(context.Background()); got != StateUnsupported {
		t.Errorf("Status = %q, want unsupported", got)
	}
	if got, err := b.Probe(context.Background()); got != StateUnsupported || err != nil {
		t.Errorf("Probe = %q, %v; want a definitive unsupported", got, err)
	}
	if _, err := b.Install(context.Background(), false); err == nil || !strings.Contains(err.Error(), "plan9") {
		t.Errorf("Install err = %v, want one naming the OS", err)
	}
	if _, err := b.Uninstall(context.Background(), false); err == nil {
		t.Error("Uninstall must fail on an unsupported OS")
	}
	if err := b.Restart(context.Background()); err == nil {
		t.Error("Restart must fail on an unsupported OS")
	}
}

// An unknown home directory is a definitive unsupported read, never a doubt: the
// backend cannot locate the unit or plist, so restart takes the legacy
// stop-then-Spawn path instead of refusing. A service file sits at the path the
// world's home would resolve to, so a backend that ignored the empty Home would
// read it (systemd) or fall through to not-installed (launchd) rather than
// answer unsupported.
func TestNew_UnknownHomeIsDefinitivelyUnsupported(t *testing.T) {
	for _, f := range flavors {
		t.Run(f.name, func(t *testing.T) {
			w, _ := f.setup(t)
			mustWrite(t, f.path(w), handWritten)
			b := New(Options{
				GOOS: f.goos,
				Home: "",
				LookupEnv: func(k string) (string, bool) {
					v, ok := w.env[k]
					return v, ok
				},
				UID: testUID,
			})
			ctx := context.Background()

			if got, err := b.Probe(ctx); got != StateUnsupported || err != nil {
				t.Errorf("Probe = %q, %v; want a definitive unsupported with a nil error", got, err)
			}
			if got := b.Status(ctx); got != StateUnsupported {
				t.Errorf("Status = %q, want unsupported", got)
			}
			if _, err := b.Install(ctx, false); err == nil || !strings.Contains(err.Error(), "home directory") {
				t.Errorf("Install err = %v, want one naming the home directory", err)
			}
			if err := b.Restart(ctx); err == nil || !strings.Contains(err.Error(), "home directory") {
				t.Errorf("Restart err = %v, want one naming the home directory", err)
			}
			if got := mustRead(t, f.path(w)); got != handWritten {
				t.Errorf("an unknown home must leave the service file untouched, got:\n%s", got)
			}
			if calls := w.calls(); len(calls) != 0 {
				t.Errorf("an unknown home must make no service-manager calls, got %q", calls)
			}
		})
	}
}

func TestStateLine_IsTheStableFirstLineToken(t *testing.T) {
	want := map[State]string{
		StateEnabled:      "service: enabled",
		StateDisabled:     "service: disabled",
		StateNotInstalled: "service: not-installed",
		StateUnsupported:  "service: unsupported",
	}
	for state, line := range want {
		if got := state.Line(); got != line {
			t.Errorf("%q.Line() = %q, want %q", string(state), got, line)
		}
	}
}
