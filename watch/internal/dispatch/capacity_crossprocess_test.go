package dispatch

// The shared worker limit, proved across real processes (#1469, P4/P26 and P6).
// An in-process goroutine test shares one address space, one clock and one set of
// seams; it cannot show that two separate launchers (a one-shot `cenci dispatch
// --once`, the daemon's interval pass, a babysit supervisor) really contend on
// one ledger file. Each test here starts separate OS processes through the helper
// hook in main_test.go (this test binary re-exec'd as a dispatch pass or a babysit
// tick), all reading one $XDG_STATE_HOME ledger, one fleet config.json, one fake
// `gh` world and one fake tmux, and asserts what the processes printed and what
// they launched.
//
// The race is made deterministic without a sleep: both children wait on a barrier
// file and the parent releases them together. Which one wins is the kernel's
// choice, so every assertion is symmetric: exactly one launches, and whichever
// lost reports the cap. The fake tmux lists a worker's window from the moment its
// launcher recorded the launch (a real tmux window exists as soon as it is
// created), so a winner that has already exited still holds its slot against a
// late loser, exactly as in production.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

const (
	// crossProcessBranch is the branch both launchers write: the babysit PR's head
	// ref and the dispatch ticket's recorded pipeline branch.
	crossProcessBranch = "fix/shared-branch"
	// crossProcessPR is the babysit supervisor's PR, whose CI is failing.
	crossProcessPR = 103
	// crossProcessBudget bounds one helper process: a hung child is killed, not
	// waited for.
	crossProcessBudget = 2 * time.Minute
)

// -- the world the processes share ----------------------------------------------

type crossWorld struct {
	h         *chainHarness
	stateHome string // $XDG_STATE_HOME: the ledger lives under it
	launchLog string // one `launch <ticket>` line per dispatch launch
	runLog    string // one `run <workflow> <PR> ...` line per babysit `cenci run`
	fleet     string // the fleet config.json: babysit's limits and dispatch's config
}

// newCrossWorld builds the shared world: a git origin and checkout, a fake gh with
// one eligible dispatch ticket (#201, Refined, label "alpha") and one open PR (#103
// on crossProcessBranch, failing CI, so a babysit tick wants a ci-repair), a fake
// tmux whose live windows follow the launch logs, and a fleet config with the
// machine cap.
func newCrossWorld(t *testing.T, machineCap int) *crossWorld {
	t.Helper()
	h := newChainHarness(t)
	stateHome := t.TempDir()
	if err := os.Chmod(stateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	logs := t.TempDir()
	w := &crossWorld{h: h, stateHome: stateHome, launchLog: filepath.Join(logs, "launches.log"), runLog: filepath.Join(logs, "runs.log")}

	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedPermission("octocat", "admin")
	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	h.seedPR(ghPR{
		Number: crossProcessPR, HeadRefName: crossProcessBranch, HeadRefOID: gitTest(t, h.local, "rev-parse", "HEAD"), BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(crossProcessPR, ghCheck{Bucket: "fail", Name: "build", State: "FAILURE"})
	h.seedIssue(201, "Alpha", "", []string{"Refined", "alpha"}, []string{"octocat"})

	setFleetAutomergeEnabled(t, true)
	w.fleet = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "cenci", "config.json")
	body := fmt.Sprintf(`{"automerge":{"enabled":true},"dispatch":{"concurrencyCap":%d,"planRefined":true,"repos":[{"repo":%q,"dir":%q,"session":"chain-session"}]}}`,
		machineCap, h.repo, h.local)
	if err := os.WriteFile(w.fleet, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w.installTmux(t)
	return w
}

// installTmux puts a fake `tmux` first on PATH whose live windows are one unrelated
// window plus every worker window a launcher has recorded: `<ticket>-implement` for
// each line of the dispatch launch log and `<PR>-<workflow>` for each line of the
// babysit run log. The two `-a` listings tmux.ListCleanupInventory joins and
// babysit's own pane inventory answer from the same set, and a leading `-S <socket>`
// pair is dropped so a socketed call answers like an ambient one.
func (w *crossWorld) installTmux(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "windows")
	if err := os.WriteFile(base, []byte("1-implement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"base=" + shellQuoteChain(base) + "\nlaunches=" + shellQuoteChain(w.launchLog) + "\nruns=" + shellQuoteChain(w.runLog) + "\n" +
		"live() {\n  cat \"$base\"\n" +
		"  if [ -f \"$launches\" ]; then while read -r _ n; do echo \"$n-implement\"; done < \"$launches\"; fi\n" +
		"  if [ -f \"$runs\" ]; then while read -r _ wf pr _; do echo \"$pr-$wf\"; done < \"$runs\"; fi\n}\n" +
		"if [ \"$1\" = \"-S\" ]; then shift 2; fi\n" +
		"case \"$1\" in\n  has-session) exit 0 ;;\n  display-message) echo " + chainFakeTmuxSession + " ;;\n" +
		"  list-panes)\n    i=0\n    live | while read -r w; do\n      i=$((i+1))\n" +
		"      case \"$*\" in\n        *window_id*) printf '@%d\\t%%%d\\t0\\n' \"$i\" \"$i\" ;;\n        *) printf '%s\\t0\\t\\n' \"$w\" ;;\n      esac\n    done ;;\n" +
		"  list-windows)\n    i=0\n    live | while read -r w; do\n      i=$((i+1))\n      printf '@%d\\t%s\\t\\n' \"$i\" \"$w\"\n    done ;;\n" +
		"  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// dispatchConfig writes a fleet-shaped config for one dispatch process that only
// picks up tickets carrying label, so two processes can contend for the slot with
// different tickets (and the loser's refusal is the admission's, not the same-ticket
// exclusivity's).
func (w *crossWorld) dispatchConfig(t *testing.T, machineCap int, label string) string {
	t.Helper()
	body := fmt.Sprintf(`{"dispatch":{"concurrencyCap":%d,"planRefined":true,"repos":[{"repo":%q,"dir":%q,"session":"chain-session","filters":{"labelsAll":[%q]}}]}}`,
		machineCap, w.h.repo, w.h.local, label)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordBranch records branch as ticket's pipeline branch, which is where a
// dispatch pickup's branch comes from.
func (w *crossWorld) recordBranch(t *testing.T, ticket int, branch string) {
	t.Helper()
	if _, err := pipeline.SetArtifacts(pipeline.ArtifactOpts{ID: strconv.Itoa(ticket), RepoRoot: w.h.local, Branch: branch}); err != nil {
		t.Fatalf("recording the pipeline branch of #%d: %v", ticket, err)
	}
}

// -- the processes ----------------------------------------------------------------

type crossProc struct {
	name string
	cmd  *exec.Cmd
	out  bytes.Buffer
}

func startProc(t *testing.T, mode string, env ...string) *crossProc {
	t.Helper()
	p := &crossProc{name: mode}
	p.cmd = capacityHelperCmd(mode, env...)
	p.cmd.Stdout, p.cmd.Stderr = &p.out, &p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("starting the %s helper: %v", mode, err)
	}
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	return p
}

// wait returns the process's combined output once it has exited 0. A process that
// exits non-zero, or outlives crossProcessBudget, fails the test.
func (p *crossProc) wait(t *testing.T) string {
	t.Helper()
	timer := time.AfterFunc(crossProcessBudget, func() { _ = p.cmd.Process.Kill() })
	defer timer.Stop()
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("the %s helper failed: %v\n%s", p.name, err, p.out.String())
	}
	return p.out.String()
}

// dispatchProc starts one real dispatch pass (the one-shot run and the daemon's
// interval tick both run this same RunOnce) over config.
func (w *crossWorld) dispatchProc(t *testing.T, config string, extra ...string) *crossProc {
	t.Helper()
	return startProc(t, "dispatch", append([]string{capacityConfigEnv + "=" + config, capacityLaunchLogEnv + "=" + w.launchLog}, extra...)...)
}

// babysitProc starts one real babysit supervisor tick for crossProcessPR: its
// `cenci run` self-exec is a fake that logs its argv and exits 0.
func (w *crossWorld) babysitProc(t *testing.T, extra ...string) *crossProc {
	t.Helper()
	env := []string{capacityStateDirEnv + "=" + t.TempDir(), capacityChdirEnv + "=" + w.h.local, capacityRunLogEnv + "=" + w.runLog}
	return startProc(t, "babysit", append(env, extra...)...)
}

// barrierFile returns the env entry that gives a child a barrier and the release
// of that barrier: children given the entry wait for the file, so releasing it
// starts every waiting child together.
func barrierFile(t *testing.T) (env string, release func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "go")
	return capacityBarrierEnv + "=" + path, func() {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("releasing the barrier: %v", err)
		}
	}
}

// -- reading the outcome ------------------------------------------------------------

// launched is every worker a launcher started: the tickets of the dispatch launch
// log and the babysit run log's argv lines.
func (w *crossWorld) launched(t *testing.T) (dispatch, babysit []string) {
	t.Helper()
	for _, line := range readLines(t, w.launchLog) {
		dispatch = append(dispatch, strings.TrimPrefix(line, "launch "))
	}
	return dispatch, readLines(t, w.runLog)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// entries is what the shared ledger holds, read the way `cenci dispatch status`
// does (no lock, nothing created).
func (w *crossWorld) entries(t *testing.T) []capacity.Entry {
	t.Helper()
	l := capacity.NewLedger(filepath.Join(w.stateHome, "cenci", "capacity"), capacity.Options{})
	return heldEntries(t, l)
}

func describe(entries []capacity.Entry) string {
	var parts []string
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%s %s#%d window=%s state=%s branch=%q", e.Kind, e.Repo, e.Ticket, e.Window, e.State, e.Branch))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// -- P4/P26: the final slot ---------------------------------------------------------

// With concurrencyCap 1, two real dispatch processes (a one-shot and an interval
// pass, each on its own ticket) race for the one slot. Exactly one launches; the
// other logs `skip: concurrency cap reached` for its own ticket, and the ledger
// holds the winner's single entry.
func TestCrossProcessDispatchPassesRaceForTheFinalSlot(t *testing.T) {
	w := newCrossWorld(t, 1)
	w.h.seedIssue(202, "Beta", "", []string{"Refined", "beta"}, []string{"octocat"})
	configs := map[int]string{201: w.dispatchConfig(t, 1, "alpha"), 202: w.dispatchConfig(t, 1, "beta")}
	barrier, release := barrierFile(t)

	procs := map[int]*crossProc{}
	for _, ticket := range []int{201, 202} {
		procs[ticket] = w.dispatchProc(t, configs[ticket], barrier)
	}
	release()
	outs := map[int]string{}
	for ticket, p := range procs {
		outs[ticket] = p.wait(t)
	}

	dispatched, babysat := w.launched(t)
	if len(dispatched) != 1 || len(babysat) != 0 {
		t.Fatalf("two cap-1 dispatch processes launched %v (babysit %v), want exactly one ticket\n#201:\n%s\n#202:\n%s", dispatched, babysat, outs[201], outs[202])
	}
	winner := dispatched[0]
	for ticket, out := range outs {
		refused := hasLine(out, skipLine(w.h.repo, ticket, "concurrency cap reached"))
		switch won := strconv.Itoa(ticket) == winner; {
		case won && refused:
			t.Errorf("#%d launched and was also refused:\n%s", ticket, out)
		case !won && !refused:
			t.Errorf("#%d lost the slot but its pass did not log `skip: concurrency cap reached`:\n%s", ticket, out)
		}
	}
	held := w.entries(t)
	if len(held) != 1 || held[0].Ticket != mustAtoi(t, winner) || held[0].Window != winner+"-implement" || held[0].State != capacity.StateConfirmed {
		t.Errorf("the ledger holds %s, want the winner #%s's one confirmed entry", describe(held), winner)
	}
}

// With concurrencyCap 1, a real dispatch process and a real babysit-launch process
// race for the one slot. Exactly one starts a worker; the other reports `concurrency
// cap reached`: dispatch as `skip: concurrency cap reached` on its ticket, babysit as
// the supervisor line that opens no window.
func TestCrossProcessDispatchAndBabysitRaceForTheFinalSlot(t *testing.T) {
	w := newCrossWorld(t, 1)
	barrier, release := barrierFile(t)

	dispatchProc := w.dispatchProc(t, w.fleet, barrier)
	babysitProc := w.babysitProc(t, barrier)
	release()
	dispatchOut, babysitOut := dispatchProc.wait(t), babysitProc.wait(t)

	dispatched, babysat := w.launched(t)
	if len(dispatched)+len(babysat) != 1 {
		t.Fatalf("a dispatch and a babysit process on a cap-1 ledger started %v and %v, want exactly one worker\ndispatch:\n%s\nbabysit:\n%s", dispatched, babysat, dispatchOut, babysitOut)
	}
	held := w.entries(t)
	if len(held) != 1 {
		t.Fatalf("the ledger holds %s, want exactly the winner's entry", describe(held))
	}
	if len(dispatched) == 1 {
		t.Logf("dispatch won the final slot")
		if !strings.Contains(babysitOut, "concurrency cap reached") {
			t.Errorf("babysit lost the slot but did not report `concurrency cap reached`:\n%s", babysitOut)
		}
		if held[0].Ticket != 201 || held[0].Kind.Repair() {
			t.Errorf("the held entry is %s, want dispatch's #201", describe(held))
		}
		return
	}
	t.Logf("babysit won the final slot")
	if !hasLine(dispatchOut, skipLine(w.h.repo, 201, "concurrency cap reached")) {
		t.Errorf("dispatch lost the slot but did not log `skip: concurrency cap reached` for #201:\n%s", dispatchOut)
	}
	if !strings.HasPrefix(babysat[0], fmt.Sprintf("run ci-repair %d ", crossProcessPR)) || held[0].Kind != capacity.KindCIRepair || held[0].Ticket != crossProcessPR {
		t.Errorf("babysit's launch %q, held entry %s: want a ci-repair of PR %d", babysat[0], describe(held), crossProcessPR)
	}
}

// The race above is won by whichever launcher takes the ledger lock first, and in
// practice by the babysit tick (a dispatch pass collects first). These two runs fix
// each order: the first launcher has already exited, its worker's window is live,
// and the late launcher on another process is refused. The slot outlives the process
// that took it.
func TestCrossProcessALateLauncherIsRefusedWhileTheFirstWorkerHoldsTheSlot(t *testing.T) {
	t.Run("dispatch first, then babysit", func(t *testing.T) {
		w := newCrossWorld(t, 1)
		out := w.dispatchProc(t, w.fleet).wait(t)
		if dispatched, _ := w.launched(t); len(dispatched) != 1 {
			t.Fatalf("the first dispatch pass launched %v, want #201:\n%s", dispatched, out)
		}

		babysitOut := w.babysitProc(t).wait(t)

		if !strings.Contains(babysitOut, "concurrency cap reached") {
			t.Errorf("babysit did not report `concurrency cap reached`:\n%s", babysitOut)
		}
		if _, babysat := w.launched(t); len(babysat) != 0 {
			t.Errorf("babysit ran %v with the one slot held by dispatch", babysat)
		}
		if held := w.entries(t); len(held) != 1 || held[0].Ticket != 201 {
			t.Errorf("the ledger holds %s, want only dispatch's #201", describe(held))
		}
	})
	t.Run("babysit first, then dispatch", func(t *testing.T) {
		w := newCrossWorld(t, 1)
		out := w.babysitProc(t).wait(t)
		if _, babysat := w.launched(t); len(babysat) != 1 {
			t.Fatalf("the first babysit tick ran %v, want one ci-repair:\n%s", babysat, out)
		}

		dispatchOut := w.dispatchProc(t, w.fleet).wait(t)

		if !hasLine(dispatchOut, skipLine(w.h.repo, 201, "concurrency cap reached")) {
			t.Errorf("dispatch did not log `skip: concurrency cap reached` for #201:\n%s", dispatchOut)
		}
		if dispatched, _ := w.launched(t); len(dispatched) != 0 {
			t.Errorf("dispatch launched %v with the one slot held by a babysit repair", dispatched)
		}
		if held := w.entries(t); len(held) != 1 || held[0].Kind != capacity.KindCIRepair {
			t.Errorf("the ledger holds %s, want only the babysit ci-repair", describe(held))
		}
	})
}

// -- P6: the branch writer ----------------------------------------------------------

// A babysit process holds a ci-repair on a branch. A real dispatch process whose
// ticket recorded that same pipeline branch skips it with `branch writer active`
// and launches nothing, even though the machine has slots to spare.
func TestCrossProcessDispatchSkipsABranchWithALiveBabysitWriter(t *testing.T) {
	w := newCrossWorld(t, 5)
	out := w.babysitProc(t).wait(t)
	if _, babysat := w.launched(t); len(babysat) != 1 || !strings.HasPrefix(babysat[0], fmt.Sprintf("run ci-repair %d ", crossProcessPR)) {
		t.Fatalf("the babysit process did not launch the ci-repair (%v):\n%s", babysat, out)
	}
	w.recordBranch(t, 201, crossProcessBranch)

	dispatchOut := w.dispatchProc(t, w.fleet).wait(t)

	if !hasLine(dispatchOut, skipLine(w.h.repo, 201, "branch writer active")) {
		t.Errorf("dispatch did not log `skip: branch writer active` for #201:\n%s", dispatchOut)
	}
	if dispatched, _ := w.launched(t); len(dispatched) != 0 {
		t.Errorf("dispatch launched %v onto a branch a babysit repair is writing", dispatched)
	}
	if held := w.entries(t); len(held) != 1 || held[0].Kind != capacity.KindCIRepair || held[0].Branch != crossProcessBranch {
		t.Errorf("the ledger holds %s, want only the babysit ci-repair on %s", describe(held), crossProcessBranch)
	}
}

// A dispatch process holds a launch on a branch. A real babysit process whose PR is
// on that same branch defers its ci-repair with `branch writer active`, opens no
// window and leaves the ledger as it was.
func TestCrossProcessBabysitDefersABranchWithALiveDispatchWriter(t *testing.T) {
	w := newCrossWorld(t, 5)
	w.recordBranch(t, 201, crossProcessBranch)
	out := w.dispatchProc(t, w.fleet).wait(t)
	if dispatched, _ := w.launched(t); len(dispatched) != 1 || dispatched[0] != "201" {
		t.Fatalf("the dispatch process did not launch #201 (%v):\n%s", dispatched, out)
	}

	babysitOut := w.babysitProc(t).wait(t)

	if !strings.Contains(babysitOut, "branch writer active") {
		t.Errorf("babysit did not report `branch writer active`:\n%s", babysitOut)
	}
	if _, babysat := w.launched(t); len(babysat) != 0 {
		t.Errorf("babysit ran %v on a branch a dispatch worker is writing", babysat)
	}
	if held := w.entries(t); len(held) != 1 || held[0].Ticket != 201 || held[0].Branch != crossProcessBranch {
		t.Errorf("the ledger holds %s, want only dispatch's #201 on %s", describe(held), crossProcessBranch)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%q is not a ticket number: %v", s, err)
	}
	return n
}
