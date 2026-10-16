package worktreeclean

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

// The pipeline guard reads <MainRoot>/.cenci/pipeline/<id>.json, a file in a
// tree a sandboxed agent can write. Mid-stage means: the file exists, its stage
// is not "finalized" and its updatedAt is within staleAfter of now. A file that
// cannot be trusted (any decode, type or filesystem anomaly) is an error, never
// a quiet "not mid-stage". The function returns (blocked, err); on err the
// caller keeps the worktree, so the table asserts the error and not the bool.

const guardTicket = "42"

// stateJSON renders a state file body with the given stage and updatedAt.
func stateJSON(stage string, updatedAt time.Time) string {
	return `{"schemaVersion":1,"id":"` + guardTicket + `","stage":"` + stage +
		`","updatedAt":"` + updatedAt.UTC().Format(time.RFC3339Nano) + `"}`
}

func TestPipelineGuard(t *testing.T) {
	now := fixedNow
	staleAfter := testStaleAfter

	// stateFile is the path of the ticket's state file inside a bare temp root.
	stateFile := func(root string) string {
		return filepath.Join(root, ".cenci", "pipeline", guardTicket+".json")
	}
	withState := func(body string) func(t *testing.T, root string) {
		return func(t *testing.T, root string) { writePipelineRaw(t, root, guardTicket, body) }
	}

	tests := []struct {
		name    string
		setup   func(t *testing.T, root string)
		blocked bool
		wantErr bool
	}{
		// --- no state file: nothing is running ---
		{name: "no .cenci directory", setup: func(*testing.T, string) {}},
		{name: "no pipeline directory", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, ".cenci"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no state file for this ticket", setup: func(t *testing.T, root string) {
			writePipelineRaw(t, root, "43", stateJSON("executed", now))
		}},

		// --- decoded states ---
		{name: "recent executed is mid-stage", setup: withState(stateJSON("executed", now.Add(-time.Hour))), blocked: true},
		{name: "recent finalized is not mid-stage", setup: withState(stateJSON("finalized", now.Add(-time.Hour)))},
		{name: "executed older than staleAfter is not mid-stage", setup: withState(stateJSON("executed", now.Add(-staleAfter-time.Second)))},
		{name: "executed exactly staleAfter old is still mid-stage", setup: withState(stateJSON("executed", now.Add(-staleAfter))), blocked: true},
		{name: "executed just inside staleAfter is mid-stage", setup: withState(stateJSON("executed", now.Add(-staleAfter+time.Second))), blocked: true},
		{name: "future-dated executed is mid-stage", setup: withState(stateJSON("executed", now.Add(48*time.Hour))), blocked: true},
		{name: "future-dated finalized is not mid-stage", setup: withState(stateJSON("finalized", now.Add(48*time.Hour)))},
		{name: "unknown stage recent reads as not finalized", setup: withState(stateJSON("bogus-stage", now.Add(-time.Hour))), blocked: true},
		{name: "unknown stage old is not mid-stage", setup: withState(stateJSON("bogus-stage", now.Add(-staleAfter-time.Hour)))},
		{name: "stage match is exact: Finalized is not finalized", setup: withState(stateJSON("Finalized", now.Add(-time.Hour))), blocked: true},
		{name: "extra fields are ignored", setup: withState(`{"schemaVersion":1,"stage":"executed","updatedAt":"` +
			now.Add(-time.Hour).UTC().Format(time.RFC3339Nano) + `","prUrl":"https://example.invalid/pr/1","labels":["a"],"session":{"k":"v"}}`), blocked: true},

		// --- unreadable state: error, so the caller keeps the worktree ---
		{name: "malformed JSON", setup: withState(`{"stage":`), wantErr: true},
		{name: "empty file", setup: withState(``), wantErr: true},
		{name: "JSON null", setup: withState(`null`), wantErr: true},
		{name: "JSON array", setup: withState(`[]`), wantErr: true},
		{name: "stage null", setup: withState(`{"stage":null,"updatedAt":"2050-06-15T11:00:00Z"}`), wantErr: true},
		{name: "stage absent", setup: withState(`{"updatedAt":"2050-06-15T11:00:00Z"}`), wantErr: true},
		{name: "stage empty", setup: withState(stateJSON("", now.Add(-time.Hour))), wantErr: true},
		{name: "stage not a string", setup: withState(`{"stage":7,"updatedAt":"2050-06-15T11:00:00Z"}`), wantErr: true},
		{name: "updatedAt null", setup: withState(`{"stage":"executed","updatedAt":null}`), wantErr: true},
		{name: "updatedAt absent", setup: withState(`{"stage":"executed"}`), wantErr: true},
		{name: "updatedAt zero time", setup: withState(`{"stage":"executed","updatedAt":"0001-01-01T00:00:00Z"}`), wantErr: true},
		{name: "updatedAt not a timestamp", setup: withState(`{"stage":"executed","updatedAt":"yesterday"}`), wantErr: true},
		{name: "keys are matched exactly, not case-insensitively", setup: withState(`{"Stage":"executed","UpdatedAt":"2050-06-15T11:00:00Z"}`), wantErr: true},
		{name: "state file is a directory", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(stateFile(root), 0o755); err != nil {
				t.Fatal(err)
			}
		}, wantErr: true},
		{name: "state file is a symlink to a valid state", setup: func(t *testing.T, root string) {
			target := filepath.Join(root, "elsewhere.json")
			writeFile(t, target, stateJSON("finalized", now))
			if err := os.MkdirAll(filepath.Dir(stateFile(root)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, stateFile(root)); err != nil {
				t.Fatal(err)
			}
		}, wantErr: true},
		{name: ".cenci/pipeline is a symlink", setup: func(t *testing.T, root string) {
			realDir := filepath.Join(root, "real-pipeline")
			writeFile(t, filepath.Join(realDir, guardTicket+".json"), stateJSON("finalized", now))
			if err := os.MkdirAll(filepath.Join(root, ".cenci"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(realDir, filepath.Join(root, ".cenci", "pipeline")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: true},
		{name: ".cenci is a symlink", setup: func(t *testing.T, root string) {
			realDir := filepath.Join(root, "real-cenci")
			writeFile(t, filepath.Join(realDir, "pipeline", guardTicket+".json"), stateJSON("finalized", now))
			if err := os.Symlink(realDir, filepath.Join(root, ".cenci")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: true},
		{name: ".cenci is a regular file", setup: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, ".cenci"), "not a directory\n")
		}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			repo := Repo{MainRoot: root, CommonDir: filepath.Join(root, ".git")}
			blocked, err := pipelineMidStage(repo, guardTicket, staleAfter, now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("pipelineMidStage = (%v, nil), want an error", blocked)
				}
				return
			}
			if err != nil {
				t.Fatalf("pipelineMidStage error: %v", err)
			}
			if blocked != tc.blocked {
				t.Fatalf("pipelineMidStage blocked = %v, want %v", blocked, tc.blocked)
			}
		})
	}

	t.Run("oversized state file is unreadable", func(t *testing.T) {
		root := t.TempDir()
		// A well-formed mid-stage state padded far past a small cap.
		body := `{"stage":"executed","updatedAt":"` + now.Add(-time.Hour).UTC().Format(time.RFC3339Nano) +
			`","pad":"` + strings.Repeat("x", 4096) + `"}`
		writePipelineRaw(t, root, guardTicket, body)
		repo := Repo{MainRoot: root, CommonDir: filepath.Join(root, ".git")}

		// Control: under the default cap the same file decodes and blocks, so the
		// error below can only come from the size cap.
		blocked, err := pipelineMidStage(repo, guardTicket, testStaleAfter, now)
		if err != nil || !blocked {
			t.Fatalf("control: pipelineMidStage = (%v, %v), want (true, nil)", blocked, err)
		}

		setVar(t, &maxPipelineStateBytes, 512)
		if blocked, err := pipelineMidStage(repo, guardTicket, testStaleAfter, now); err == nil {
			t.Fatalf("pipelineMidStage = (%v, nil) over the cap, want an error", blocked)
		}
	})

	t.Run("FIFO state file does not block the read", func(t *testing.T) {
		root := t.TempDir()
		fifo := stateFile(root)
		if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Skipf("mkfifo unsupported: %v", err)
		}
		repo := Repo{MainRoot: root, CommonDir: filepath.Join(root, ".git")}
		type result struct {
			blocked bool
			err     error
		}
		done := make(chan result, 1)
		go func() {
			blocked, err := pipelineMidStage(repo, guardTicket, testStaleAfter, now)
			done <- result{blocked, err}
		}()
		select {
		case res := <-done:
			if res.err == nil {
				t.Fatalf("pipelineMidStage = (%v, nil) for a FIFO, want an error", res.blocked)
			}
		case <-time.After(10 * time.Second):
			// Unstick the leaked reader so the test binary can exit.
			if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
				_ = f.Close()
			}
			t.Fatal("pipelineMidStage blocked on a FIFO state file")
		}
	})
}

// TestPipelineGuardReadsPipelineStateFormat pins the guard's local decoder to
// the format internal/pipeline really writes. This is the only file in the
// package that may import internal/pipeline (a test-only import: production
// code is a leaf and must not).
func TestPipelineGuardReadsPipelineStateFormat(t *testing.T) {
	if got, want := string(pipeline.StageFinalized), "finalized"; got != want {
		t.Fatalf("pipeline.StageFinalized = %q; the guard compares the stage to %q", got, want)
	}

	write := func(t *testing.T, root string, s pipeline.State) {
		t.Helper()
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		writePipelineRaw(t, root, guardTicket, string(data))
	}
	tests := []struct {
		name    string
		stage   pipeline.Stage
		age     time.Duration
		blocked bool
	}{
		{"every non-final stage that is recent blocks: prepared", pipeline.StagePrepared, time.Hour, true},
		{"waiting_for_input", pipeline.StageWaitingForInput, time.Hour, true},
		{"waiting_for_plan_approval", pipeline.StageWaitingForPlanApproval, time.Hour, true},
		{"plan_approved", pipeline.StagePlanApproved, time.Hour, true},
		{"executed", pipeline.StageExecuted, time.Hour, true},
		{"reviewed", pipeline.StageReviewed, time.Hour, true},
		{"finalized recent does not block", pipeline.StageFinalized, time.Hour, false},
		{"executed older than staleAfter does not block", pipeline.StageExecuted, testStaleAfter + time.Hour, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, pipeline.State{
				SchemaVersion: 1, ID: guardTicket, Stage: tc.stage,
				UpdatedAt: fixedNow.Add(-tc.age),
				Branch:    "feature/42-x", PRNumber: 7, Labels: []string{"Working"},
				Session: map[string]string{"k": "v"},
			})
			repo := Repo{MainRoot: root, CommonDir: filepath.Join(root, ".git")}
			blocked, err := pipelineMidStage(repo, guardTicket, testStaleAfter, fixedNow)
			if err != nil {
				t.Fatalf("pipelineMidStage on a real pipeline.State: %v", err)
			}
			if blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v", blocked, tc.blocked)
			}
		})
	}
}
