package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGhJSONAcceptsChecksExitWithValidJSON(t *testing.T) {
	original := command
	command = func(string, ...string) ([]byte, error) {
		return []byte(`[{"bucket":"fail","name":"test"}]`), errors.New("exit status 1")
	}
	t.Cleanup(func() { command = original })
	var checks []check
	if err := ghJSON(&checks, "pr", "checks", "42"); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Bucket != "fail" {
		t.Fatalf("checks=%#v", checks)
	}
}

func withCommands(t *testing.T, responses []string, calls *[][]string) {
	t.Helper()
	original := command
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if name != "gh" {
			return []byte(""), nil
		}
		if i >= len(responses) {
			return nil, fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
		out := responses[i]
		i++
		return []byte(out), nil
	}
	t.Cleanup(func() { command = original })
}

func openPR() string {
	return `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[]}`
}

func TestTickQuietBacksOff(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{openPR(), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, delay, err := tick(&s)
	if err != nil || terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	if delay != 120*time.Second || s.CurrentDelaySeconds != 120 {
		t.Fatalf("delay = %v, state = %d", delay, s.CurrentDelaySeconds)
	}
	if s.LastHeadSHA != "abc" || s.FixAttempts != 0 {
		t.Fatalf("unexpected CI state: %#v", s)
	}
}

func TestTickLaunchesAddressReviewForNewFeedback(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{openPR(), `[]`, `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900}
	_, delay, err := tick(&s)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 300*time.Second || s.PendingCommentAt == "" || !reflect.DeepEqual(s.PendingKeys, []string{"comment:7"}) {
		t.Fatalf("unexpected state: %#v", s)
	}
	found := false
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == "address-review" && c[3] == "42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("address-review was not launched: %#v", calls)
	}
}

func TestTickMergedRelabelsClosingIssues(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	withCommands(t, []string{merged, `{}`, `{}`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	if err != nil || !terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	want := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("issue relabel missing: %#v", calls)
	}
}

func TestStatePathDoesNotExposeRepositoryName(t *testing.T) {
	p := statePath("/state", "secret-owner/private-repo", "42")
	if strings.Contains(p, "secret") || !strings.HasSuffix(p, "-42.json") {
		t.Fatalf("unsafe state path: %s", p)
	}
}

// -- close guard (#787) ------------------------------------------------------

// TestTickRecordsClosingIssuesAndCIStatus pins the two join keys the close
// guard reads off disk (#787): the issues the PR closes (so a ticket number
// resolves to a supervisor) and the collapsed CI verdict (so a green PR stops
// holding its window open). Both come from data tick already fetches.
func TestTickRecordsClosingIssuesAndCIStatus(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	for _, tc := range []struct {
		name   string
		checks string
		want   string
	}{
		{"failing beats pending", `[{"bucket":"pending","name":"a"},{"bucket":"fail","name":"b"}]`, "failing"},
		{"pending beats pass", `[{"bucket":"pass","name":"a"},{"bucket":"pending","name":"b"}]`, "pending"},
		{"all pass is green", `[{"bucket":"pass","name":"a"}]`, "green"},
		{"no checks stays empty", `[]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{prWithIssue, tc.checks, `[]`, `[]`}, &calls)
			s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.ClosingIssues, []int{782}) {
				t.Errorf("ClosingIssues = %v, want [782]", s.ClosingIssues)
			}
			if s.CIStatus != tc.want {
				t.Errorf("CIStatus = %q, want %q", s.CIStatus, tc.want)
			}
		})
	}
}

// TestRunWritesStateBeforeFirstTick covers the arm-to-first-poll window
// (#787): `cenci babysit` writes its state file before the supervisor loop
// starts polling, so a lazyboards cleanup firing between "supervisor armed"
// and "first tick completed" can already see that a supervisor owns the PR.
func TestRunWritesStateBeforeFirstTick(t *testing.T) {
	dir := t.TempDir()
	var atFirstTick *State
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "git":
			return []byte("/repo/root\n"), nil
		case name == "gh" && len(args) > 1 && args[0] == "repo":
			return []byte("o/r\n"), nil
		case name == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view":
			s := load(statePath(dir, "o/r", "42"))
			atFirstTick = &s
			return nil, errors.New("exit status 1")
		}
		return []byte(""), nil
	}
	t.Cleanup(func() { command = original })

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute, Once: true}); err == nil {
		t.Fatal("Run: want the stubbed gh failure to surface")
	}
	if atFirstTick == nil {
		t.Fatal("the first tick never ran")
	}
	if atFirstTick.PR != "42" {
		t.Errorf("state at first tick has PR %q, want the state file already written with PR 42", atFirstTick.PR)
	}
	if atFirstTick.RepoRoot != "/repo/root" {
		t.Errorf("state at first tick has RepoRoot %q, want /repo/root", atFirstTick.RepoRoot)
	}
	if atFirstTick.PID != os.Getpid() {
		t.Errorf("state at first tick has PID %d, want the supervisor's own pid %d", atFirstTick.PID, os.Getpid())
	}
}

// writeGuardState writes s as a supervisor state file in dir under an
// arbitrary name — BlocksClose globs the directory rather than recomputing a
// repo-hashed path, so the file name is deliberately not the production one.
func writeGuardState(t *testing.T, dir string, s State) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state-"+s.PR+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

// stubProcessOwned replaces the /proc-backed liveness check for the duration
// of a test so the guard matrix can model "live supervisor" and "dead
// supervisor" without spawning real processes.
func stubProcessOwned(t *testing.T, owned bool) {
	t.Helper()
	original := processOwned
	processOwned = func(int, string) bool { return owned }
	t.Cleanup(func() { processOwned = original })
}

func TestBlocksCloseMatrix(t *testing.T) {
	live := State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "failing", PID: 4242, Status: "running"}
	for _, tc := range []struct {
		name       string
		state      State
		procOwned  bool
		ticket     string
		repoRoot   string
		wantBlocks bool
	}{
		{"live supervisor with failing CI blocks", live, true, "782", "/repo/root", true},
		{"live supervisor with pending CI blocks", func() State { s := live; s.CIStatus = "pending"; return s }(), true, "782", "/repo/root", true},
		{"green CI allows", func() State { s := live; s.CIStatus = "green"; return s }(), true, "782", "/repo/root", false},
		{"unknown CI allows", func() State { s := live; s.CIStatus = ""; return s }(), true, "782", "/repo/root", false},
		{"dead supervisor allows", live, false, "782", "/repo/root", false},
		{"paused supervisor with no pid blocks", func() State { s := live; s.PID = 0; s.Status = "needs-input"; return s }(), false, "782", "/repo/root", true},
		{"another ticket allows", live, true, "999", "/repo/root", false},
		{"different repo root allows", live, true, "782", "/other/root", false},
		{"unknown caller repo root fails open to blocking", live, true, "782", "", true},
		{"unknown state repo root fails open to blocking", func() State { s := live; s.RepoRoot = ""; return s }(), true, "782", "/repo/root", true},
		{"non-numeric ticket allows", live, true, "add-dark-mode", "/repo/root", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubProcessOwned(t, tc.procOwned)
			dir := t.TempDir()
			writeGuardState(t, dir, tc.state)
			blocks, reason := BlocksClose(tc.ticket, tc.repoRoot, dir)
			if blocks != tc.wantBlocks {
				t.Fatalf("BlocksClose = %v (%q), want %v", blocks, reason, tc.wantBlocks)
			}
			if blocks && !strings.Contains(reason, "#790") {
				t.Errorf("reason = %q, want it to name the supervised PR", reason)
			}
			if !blocks && reason != "" {
				t.Errorf("reason = %q, want empty when nothing blocks", reason)
			}
		})
	}
}

// TestBlocksCloseFailsOpenOnUnreadableState covers the fail-open contract
// (#787): the guard must never wedge a window open because of an I/O or
// decode failure, so a missing state directory and a corrupt state file both
// allow the close.
func TestBlocksCloseFailsOpenOnUnreadableState(t *testing.T) {
	stubProcessOwned(t, true)

	missing := filepath.Join(t.TempDir(), "never-created")
	if blocks, _ := BlocksClose("782", "/repo/root", missing); blocks {
		t.Error("a missing state directory must fail open (allow the close)")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if blocks, _ := BlocksClose("782", "/repo/root", dir); blocks {
		t.Error("a corrupt state file must fail open (allow the close)")
	}
}

// TestBlocksCloseScansPastNonMatchingEntries asserts a corrupt or unrelated
// entry never short-circuits the scan: a real blocking supervisor later in
// the directory must still be found.
func TestBlocksCloseScansPastNonMatchingEntries(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aaa-corrupt.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	writeGuardState(t, dir, State{PR: "111", RepoRoot: "/repo/root", ClosingIssues: []int{1}, CIStatus: "green", PID: 1})
	writeGuardState(t, dir, State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "pending", PID: 4242})

	blocks, reason := BlocksClose("782", "/repo/root", dir)
	if !blocks {
		t.Fatalf("BlocksClose = false, want the later blocking supervisor to be found")
	}
	if !strings.Contains(reason, "#790") {
		t.Errorf("reason = %q, want it to name PR #790", reason)
	}
}

// TestDefaultProcessOwnedRejectsUnusedPID keeps the real liveness check
// honest: the stubbed matrix above proves the decision logic, this proves the
// production probe still says "not ours" for a pid that isn't running.
func TestDefaultProcessOwnedRejectsUnusedPID(t *testing.T) {
	pid := unusedPID(t)
	if defaultProcessOwned(pid, "790") {
		t.Errorf("defaultProcessOwned(%d) = true, want false for a pid that is not running", pid)
	}
}

// unusedPID returns a pid with no live process behind it.
func unusedPID(t *testing.T) int {
	t.Helper()
	for pid := 1 << 20; pid > 0; pid-- {
		if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
			return pid
		}
	}
	t.Skip("no unused pid available")
	return 0
}
