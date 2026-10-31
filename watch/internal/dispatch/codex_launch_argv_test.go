package dispatch

// Ticket #1232 A3: a babysit-generated `cenci run` self-exec for a Codex
// workflow resolves, through the run package's dry-run path, to the same
// sandbox argv shape as a hand-typed launch: `cenci open --agent codex --
// <prompt>`, the prompt one verbatim element. Unlike chainfake_test.go's
// recorder (one flattened `echo "$*"` line per call, which cannot prove argv
// boundaries) this file installs its own NUL-separated recorder over
// os.Args[0] after newChainHarness, leaving the shared fixture untouched.
//
// Never call t.Parallel() here: the os.Args[0] swap is process-global, and
// babysit.localRepoRoot needs a stable chainChdir (see chainfake_test.go).

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// installArgvRecorder swaps os.Args[0] for a script that appends its argv,
// NUL-terminated per element, to the returned log; t.Cleanup restores the
// previous value (registered after newChainHarness, so restore order is LIFO).
func installArgvRecorder(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	script := filepath.Join(dir, "cenci-recorder")
	exectest.WriteExecutable(t, script, "#!/bin/sh\nprintf '%s\\000' \"$@\" >> "+exectest.ShellQuote(logPath)+"\n")
	prev := os.Args[0]
	os.Args[0] = script
	t.Cleanup(func() { os.Args[0] = prev })
	return logPath
}

func TestBabysitCodexLaunchResolvesToSandboxAgentArgv(t *testing.T) {
	const ciRepairPrompt = "PR #103 (main) has failing CI checks: build. Diagnose, fix, test, commit, and push without force-pushing."
	for _, tc := range []struct {
		name, workflow, ticket, prompt string
		seed                           func(h *chainHarness)
	}{
		{"address-review", "address-review", "103", "$cenci:address-review 103",
			func(h *chainHarness) { h.seedReview(103, "reviewer", "CHANGES_REQUESTED", "2026-01-01T00:00:00Z") }},
		{"ci-repair", "ci-repair", ciRepairPrompt, "$cenci:ci-repair " + ciRepairPrompt,
			func(h *chainHarness) { h.seedChecks(103, ghCheck{Bucket: "fail", Name: "build", State: "FAILURE"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newChainHarness(t)
			h.commitAndPushConfig(chainRepoConfigLean)
			setFleetAutomergeEnabled(t, true)
			h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
			h.seedPR(ghPR{
				Number: 103, HeadRefName: "main", HeadRefOID: gitTest(t, h.local, "rev-parse", "HEAD"), BaseRefName: "main",
				ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
			})
			h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"})
			tc.seed(h)

			installFakeTmux(t)
			logPath := installArgvRecorder(t)
			restore := chainChdir(t, h.local)
			defer restore()
			if err := babysit.Run(babysit.Options{PR: "103", Agent: "codex", StateDir: t.TempDir(), Once: true, Session: chainFakeTmuxSession}); err != nil {
				t.Fatalf("babysit.Run: %v", err)
			}

			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("babysit never ran the recorded %s launch: %v", tc.workflow, err)
			}
			argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			wantHead := []string{"run", tc.workflow, tc.ticket, "--agent", "codex", "--session", chainFakeTmuxSession, "--dir"}
			if len(argv) != len(wantHead)+1 || !slices.Equal(argv[:len(wantHead)], wantHead) || argv[len(wantHead)] == "" {
				t.Fatalf("recorded self-exec argv = %q, want %q followed by the launch dir", argv, wantHead)
			}

			var out bytes.Buffer
			ctrl := &sessionProbeController{t: t}
			err = run.Run(run.Opts{
				Workflow: argv[1], Ticket: argv[2], Agent: flagValue(argv, "--agent"), Session: flagValue(argv, "--session"),
				Dir: flagValue(argv, "--dir"), ConfigPath: filepath.Join(t.TempDir(), "none.json"), DryRun: true, Out: &out,
			}, ctrl)
			if err != nil {
				t.Fatalf("run.Run dry-run of the recorded launch: %v", err)
			}
			if len(ctrl.hasSessionCalls) != 0 {
				t.Errorf("dry-run consulted the controller: %v", ctrl.hasSessionCalls)
			}
			got := exectest.ShellArgv(t, exectest.DryRunCommand(t, out.String()), "cenci")
			if want := []string{"cenci", "open", "--agent", "codex", "--", tc.prompt}; !slices.Equal(got, want) {
				t.Errorf("resolved argv = %q, want %q", got, want)
			}
		})
	}
}
