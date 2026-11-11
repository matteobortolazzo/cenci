package main_test

// Ticket #1472 (AC 75, real boundary): black-box tests of the finite review
// worker the supervisor launches. A built `cenci babysit --once` ticks against a
// fake `gh` that serves a green PR with one human conversation comment, self-execs
// `cenci run babysit-review <pr> --receipt-nonce <nonce> discussion:<id>=<sha256>`,
// and a fake `tmux` records the `new-window` command. The test then runs that
// captured command under `sh -c`, the way tmux would, against fake `claude` and
// `codex` clients across the host/sandbox x Claude/Codex matrix (the harness of
// babysit_ci_repair_launch_test.go: ciRepairWorld and its fake clients, which
// block like an interactive session unless invoked in their finite mode), and
// asserts the client process exited with the argv the template promises.
//
// It then closes the loop the supervisor depends on: the exited worker's pane is
// reported dead and the next `cenci babysit --once` sees the window ended (no
// receipt: it holds, naming `cenci babysit resolve`, and opens no second
// window); a nonce-bound receipt comment the worker would have posted, read by a
// later tick, addresses the key and consumes the nonce. The real nonce is random,
// so the receipt is built from the nonce read back out of the saved state, and
// its created_at is far in the future so it is after the launch whatever the
// wall clock says.
//
// Isolation (watch/docs/test-isolation.md): newCIRepairWorld supplies the
// isolated HOME/XDG_CONFIG_HOME, the asset fixture, the scripted runtime and a
// live event socket; every GH_* credential variable is neutralized.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

const (
	reviewCommentID   = 11
	reviewCommentBody = "please also cover the empty input"
	reviewCommenter   = "alice"
	reviewMaintainer  = "maintainer"
	reviewWindow      = ciRepairPR + "-babysit-review"
	// reviewReceiptAt is after any launch the wall clock can stamp.
	reviewReceiptAt = "2099-01-01T00:00:00Z"
	reviewHeadSHA   = "0123456789abcdef0123456789abcdef01234567"
)

var reviewNoncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func reviewRevision(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// reviewArg is the exact `cenci run babysit-review` argument for nonce.
func reviewArg(nonce string) string {
	return ciRepairPR + " --receipt-nonce " + nonce + " discussion:" + fmt.Sprint(reviewCommentID) + "=" + reviewRevision(reviewCommentBody)
}

// wantReviewArgv is the argv the fake client must receive: the host template
// verbatim, or, in the sandbox, the launcher's permission and model flags followed
// by the passthrough -- after `exec` for Codex. The worker reuses the
// address-review skill under the babysit-review window name.
func wantReviewArgv(c ciRepairCell, arg string) []string {
	model := launcher.DefaultModel(c.agent)
	switch {
	case c.agent == "claude" && !c.sandbox:
		return []string{"--print", "--", "/cenci:address-review " + arg}
	case c.agent == "codex" && !c.sandbox:
		return []string{"exec", "$cenci:address-review " + arg}
	case c.agent == "claude":
		return []string{"--dangerously-skip-permissions", "--model", model, "--print", "/cenci:address-review " + arg}
	default:
		return []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--model", model, "$cenci:address-review " + arg}
	}
}

// reviewComment renders one PR conversation comment object.
func reviewComment(id int, login, body, created, updated string) string {
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"id":%d,"user":{"login":%q},"body":%s,"created_at":%q,"updated_at":%q}`, id, login, b, created, updated)
}

const reviewHumanComment = `{"id":11,"user":{"login":"alice"},"body":"please also cover the empty input","created_at":"2026-11-10T11:00:00Z","updated_at":"2026-11-10T11:00:00Z"}`

// reviewReceiptComment is the receipt comment a babysit-review worker posts,
// bound to nonce, listing discussion:11 at its revision with outcome.
func reviewReceiptComment(nonce, outcome string) string {
	body := "> 🤖 **cenci** — review receipt posted by `/cenci:address-review` (babysit-review).\n" +
		"<!-- cenci-review-receipt:" + nonce + " -->\n" +
		"head: " + reviewHeadSHA + "\n" +
		"- discussion:" + fmt.Sprint(reviewCommentID) + " revision=" + reviewRevision(reviewCommentBody) + " outcome=" + outcome + "\n"
	return reviewComment(12, reviewMaintainer, body, reviewReceiptAt, reviewReceiptAt)
}

// reviewFields are the state fields these tests pin, by JSON name.
type reviewFields struct {
	Pending      []string `json:"pendingCommentKeys"`
	Launched     []string `json:"launchedFeedbackKeys"`
	Addressed    []string `json:"addressedCommentKeys"`
	ReviewLaunch *struct {
		Nonce string   `json:"nonce"`
		Keys  []string `json:"keys"`
	} `json:"reviewLaunch"`
	Consumed map[string]int64 `json:"consumedReviewNonces"`
	Receipts map[string]struct {
		Outcome          string `json:"outcome"`
		Source           string `json:"source"`
		ReceiptCommentID int64  `json:"receiptCommentId"`
		HeadSHA          string `json:"headSha"`
		Revision         string `json:"revision"`
		CommentID        int64  `json:"commentId"`
	} `json:"discussionReceipts"`
	AutomergeReason     string `json:"automergeReason"`
	AutomergeDetail     string `json:"automergeDetail"`
	AutomergeConditions []struct {
		Key  string `json:"key"`
		Pass bool   `json:"pass"`
	} `json:"automergeConditions"`
}

func (f reviewFields) reviewStagePassed() bool {
	for _, c := range f.AutomergeConditions {
		if c.Key == "review" && c.Pass {
			return true
		}
	}
	return false
}

func reviewState(t *testing.T, w *ciRepairWorld) reviewFields {
	t.Helper()
	data, err := os.ReadFile(babysitStatePath(w.stateDir, ciRepairRepo, ciRepairPR))
	if err != nil {
		t.Fatal(err)
	}
	var f reviewFields
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// newReviewWorld is a ciRepairWorld whose PR is green with one human conversation
// comment, automerge enabled (so a hold's reason is recorded), and a supervisor
// state with no feedback yet.
func newReviewWorld(t *testing.T, cell ciRepairCell) *ciRepairWorld {
	t.Helper()
	w := newCIRepairWorld(t, cell, ciRepairWorldOpts{automergeEnabled: true})
	ciRepairWrite(t, filepath.Join(w.fixtures, "checks.json"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`)
	w.setDiscussion(t, reviewHumanComment)

	// Fake gh: the world's PR, checks and discussion fixtures, plus what the
	// review path reads that the CI-repair world never needed: the closing
	// issue's labels and the receipt author's collaborator permission.
	exectest.WriteExecutable(t, filepath.Join(w.binDir, "gh"), "#!/bin/sh\n"+
		`case "$1 $2" in`+"\n"+
		`  "repo view") echo `+ciRepairRepo+` ;;`+"\n"+
		`  "pr view") cat `+exectest.ShellQuote(filepath.Join(w.fixtures, "pr.json"))+` ;;`+"\n"+
		`  "pr checks") cat `+exectest.ShellQuote(filepath.Join(w.fixtures, "checks.json"))+` ;;`+"\n"+
		`  "issue view") echo '{"labels":[{"name":"automerge:ok"}]}' ;;`+"\n"+
		`  api\ repos/*/pulls/*/comments*|api\ repos/*/pulls/*/reviews*) echo '[]' ;;`+"\n"+
		`  api\ repos/*/issues/*/comments*) cat `+exectest.ShellQuote(filepath.Join(w.fixtures, "discussion.json"))+` ;;`+"\n"+
		`  api\ repos/*/collaborators/`+reviewMaintainer+`/permission|api\ repos/*/collaborators/`+reviewCommenter+`/permission) echo '{"permission":"write"}' ;;`+"\n"+
		`  *) exit 1 ;;`+"\n"+
		"esac\n")

	// A Codex host launch probes the installed plugin root for the adapter of the
	// skill the worker runs: address-review (babysit-review has no skill of its own).
	for _, kv := range w.env {
		if codexHome, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok {
			adapter := filepath.Join(codexHome, "plugins", "cache", "cenci", "cenci", "1.2.3", "skills", "address-review", "codex.md")
			if err := os.MkdirAll(filepath.Dir(adapter), 0o755); err != nil {
				t.Fatal(err)
			}
			ciRepairWrite(t, adapter, "# address-review Codex procedure\n")
		}
	}

	data, err := json.Marshal(map[string]any{
		"schemaVersion": adapterLaunchSchema, "pr": ciRepairPR, "repo": ciRepairRepo, "agent": cell.agent,
		"intervalSeconds": 60, "currentDelaySeconds": 60, "status": "running", "updatedAt": "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(babysitStatePath(w.stateDir, ciRepairRepo, ciRepairPR), data, 0o600); err != nil {
		t.Fatal(err)
	}
	w.setPanes(t, ciRepairOtherRow)
	return w
}

// setDiscussion sets the PR conversation page the fake gh serves.
func (w *ciRepairWorld) setDiscussion(t *testing.T, comments ...string) {
	t.Helper()
	ciRepairWrite(t, filepath.Join(w.fixtures, "discussion.json"), "["+strings.Join(comments, ",")+"]")
}

// assertReviewWindowOptions asserts the babysit-review window got automatic-rename
// off and the recorded launch dir as @cenci-repo-dir.
func assertReviewWindowOptions(t *testing.T, w *ciRepairWorld) {
	t.Helper()
	target := "=" + ciRepairSession + ":=" + reviewWindow
	got := w.tmuxCalls(t, "set-window-option")
	want := [][]string{
		{"set-window-option", "-t", target, "automatic-rename", "off"},
		{"set-window-option", "-t", target, "@cenci-repo-dir", w.workDir},
	}
	if len(got) != len(want) || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Errorf("set-window-option calls = %q, want %q", got, want)
	}
}

func (w *ciRepairWorld) requireWindows(t *testing.T, want ...string) {
	t.Helper()
	if got := w.windowNames(t); !slices.Equal(got, want) {
		t.Fatalf("tmux new-window names = %q, want %q", got, want)
	}
}

func (w *ciRepairWorld) tickOnce(t *testing.T, step string) {
	t.Helper()
	if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
		t.Fatalf("%s: cenci babysit --once exit = %d, want 0; output:\n%s", step, code, out)
	}
}

// AC 75: in every cell the supervisor launches `42-babysit-review` with the
// receipt nonce and the comment's revision, the captured command runs the client in
// its finite mode with the argument intact, and the client process exits -- after
// success and after an explicit failure. The worker is pinned unattended, and the
// supervisor records the outstanding launch and the key as launched.
func TestBabysitReviewMatrix_WorkerExits(t *testing.T) {
	for _, cell := range ciRepairCells {
		for _, exit := range []string{"0", "1"} {
			t.Run(cell.name()+"/exit="+exit, func(t *testing.T) {
				w := newReviewWorld(t, cell)

				w.tickOnce(t, "launch tick")

				w.requireWindows(t, reviewWindow)
				shellCommand := w.lastWindowCommand(t)
				assertWindowCommandPinsUnattended(t, shellCommand)
				assertReviewWindowOptions(t, w)
				launched := reviewState(t, w)
				if !slices.Equal(launched.Pending, []string{"discussion:11"}) || !slices.Equal(launched.Launched, []string{"discussion:11"}) {
					t.Errorf("pending/launched = %v/%v, want discussion:11 in both", launched.Pending, launched.Launched)
				}
				if launched.ReviewLaunch == nil || !reviewNoncePattern.MatchString(launched.ReviewLaunch.Nonce) || !slices.Equal(launched.ReviewLaunch.Keys, []string{"discussion:11"}) {
					t.Fatalf("reviewLaunch = %+v, want a 32-hex nonce and the launched key", launched.ReviewLaunch)
				}
				arg := reviewArg(launched.ReviewLaunch.Nonce)
				if !strings.Contains(shellCommand, arg) {
					t.Errorf("window command %q does not carry the argument %q", shellCommand, arg)
				}

				code, out := w.runWorker(t, shellCommand, "FAKE_CLIENT_EXIT="+exit)
				if fmt.Sprint(code) != exit {
					t.Errorf("worker exit = %d, want the client's %s; output:\n%s", code, exit, out)
				}
				if got, want := w.clientArgv(t), wantReviewArgv(cell, arg); !slices.Equal(got, want) {
					t.Errorf("client argv =\n %q\nwant\n %q", got, want)
				}
			})
		}
	}
}

// A host worker that hits a permission denial reports it and exits: nothing is
// left waiting at a prompt for the supervisor to find live forever.
func TestBabysitReviewMatrix_HostPermissionDenialExits(t *testing.T) {
	for _, cell := range []ciRepairCell{{"claude", false}, {"codex", false}} {
		t.Run(cell.name(), func(t *testing.T) {
			w := newReviewWorld(t, cell)
			w.tickOnce(t, "launch tick")
			w.requireWindows(t, reviewWindow)
			code, out := w.runWorker(t, w.lastWindowCommand(t), "FAKE_CLIENT_DENY=1")
			if code != 1 || !strings.Contains(out, "permission denied") {
				t.Errorf("worker exit = %d, output %q; want exit 1 with the reported denial", code, out)
			}
		})
	}
}

// The window's life as the supervisor sees it, for the host and the sandbox: the
// worker exits and its pane is reported dead; the next tick sees the window ended
// with no receipt and holds without a relaunch, naming `cenci babysit resolve`;
// the receipt the worker posts, read by a later tick, addresses the key, consumes
// the nonce and lets the review stage pass, still without a relaunch.
func TestBabysitReviewMatrix_EndedWindowIsSeenAndTheReceiptReleasesTheHold(t *testing.T) {
	for _, cell := range []ciRepairCell{{"claude", false}, {"codex", false}, {"claude", true}, {"codex", true}} {
		t.Run(cell.name(), func(t *testing.T) {
			w := newReviewWorld(t, cell)
			w.tickOnce(t, "launch tick")
			w.requireWindows(t, reviewWindow)
			nonce := reviewState(t, w).ReviewLaunch.Nonce
			if code, out := w.runWorker(t, w.lastWindowCommand(t)); code != 0 {
				t.Fatalf("worker exit = %d, want 0; output:\n%s", code, out)
			}

			// Live pane: automerge is held by the worker, nothing else launches.
			w.setPanes(t, ciRepairOtherRow+w.workerRow(reviewWindow, false))
			w.tickOnce(t, "live-window tick")
			if got := reviewState(t, w); got.AutomergeReason != "branch repair worker still running" {
				t.Errorf("with the window live: automerge reason %q, want branch repair worker still running", got.AutomergeReason)
			}

			// The pane is reported dead and no receipt exists: the window ended.
			w.setPanes(t, w.workerRow(reviewWindow, true))
			w.tickOnce(t, "ended-window tick")
			ended := reviewState(t, w)
			if ended.AutomergeReason != "review worker ended without receipt" || !strings.Contains(ended.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
				t.Errorf("ended window: reason %q detail %q, want review worker ended without receipt naming `cenci babysit resolve 42 discussion:11`", ended.AutomergeReason, ended.AutomergeDetail)
			}
			if !slices.Equal(ended.Pending, []string{"discussion:11"}) || !slices.Equal(ended.Launched, []string{"discussion:11"}) || len(ended.Addressed) != 0 {
				t.Errorf("ended window: pending/launched/addressed = %v/%v/%v, want discussion:11 kept pending and launched", ended.Pending, ended.Launched, ended.Addressed)
			}
			w.requireWindows(t, reviewWindow)

			// The receipt the worker posted (bound to its nonce) is read by the next tick.
			w.setDiscussion(t, reviewHumanComment, reviewReceiptComment(nonce, "addressed"))
			w.tickOnce(t, "receipt tick")
			done := reviewState(t, w)
			if len(done.Pending) != 0 || !slices.Equal(done.Addressed, []string{"discussion:11"}) || len(done.Launched) != 0 {
				t.Fatalf("after the receipt: pending/addressed/launched = %v/%v/%v, want discussion:11 addressed only", done.Pending, done.Addressed, done.Launched)
			}
			if got := done.Receipts["discussion:11"]; got.Outcome != "addressed" || got.Source != "receipt" || got.ReceiptCommentID != 12 || got.HeadSHA != reviewHeadSHA || got.Revision != reviewRevision(reviewCommentBody) || got.CommentID != reviewCommentID {
				t.Errorf("receipt record = %+v, want the per-key evidence of comment 12", got)
			}
			if done.Consumed[nonce] != 12 {
				t.Errorf("consumedReviewNonces = %v, want %s consumed by comment 12", done.Consumed, nonce)
			}
			if done.ReviewLaunch != nil || !done.reviewStagePassed() {
				t.Errorf("after the receipt: reviewLaunch %+v review stage passed %v, want the launch settled and the stage passed", done.ReviewLaunch, done.reviewStagePassed())
			}
			w.requireWindows(t, reviewWindow)
		})
	}
}

// An unreadable pane inventory opens no window and records `workflow launch
// failed`, keeping the key pending and unlaunched; a live same-name window of this
// repository suppresses a second launch.
func TestBabysitReviewLaunchGuards(t *testing.T) {
	t.Run("unreadable pane inventory", func(t *testing.T) {
		w := newReviewWorld(t, ciRepairCell{"claude", false})
		_ = os.Remove(filepath.Join(w.ctlDir, "panes"))

		out, code := w.babysitOnce(t, ciRepairSession)
		if code != 1 {
			t.Errorf("cenci babysit --once exit = %d, want 1 (a failed launch); output:\n%s", code, out)
		}
		if windows := w.tmuxCalls(t, "new-window"); len(windows) != 0 {
			t.Errorf("a refused launch spawned windows: %q", windows)
		}
		got := reviewState(t, w)
		if got.AutomergeReason != "workflow launch failed" || len(got.Launched) != 0 || got.ReviewLaunch != nil {
			t.Errorf("state: reason %q launched %v launch %+v, want workflow launch failed with nothing recorded as launched", got.AutomergeReason, got.Launched, got.ReviewLaunch)
		}
	})
	t.Run("a live window suppresses a second launch", func(t *testing.T) {
		w := newReviewWorld(t, ciRepairCell{"claude", false})
		w.tickOnce(t, "launch tick")
		w.setPanes(t, ciRepairOtherRow+w.workerRow(reviewWindow, false))
		w.setDiscussion(t, reviewHumanComment, reviewComment(13, reviewCommenter, "and a second ask", "2026-11-10T11:05:00Z", "2026-11-10T11:05:00Z"))

		w.tickOnce(t, "tick while the window is live")

		w.requireWindows(t, reviewWindow)
		got := reviewState(t, w)
		if !slices.Equal(got.Pending, []string{"discussion:11", "discussion:13"}) || !slices.Equal(got.Launched, []string{"discussion:11"}) {
			t.Errorf("pending/launched = %v/%v, want the new comment pending and unlaunched while the window is live", got.Pending, got.Launched)
		}
	})
}
