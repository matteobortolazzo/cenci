package procancestry

// Ticket #1530. The reader's parser, walk and classifier run against fake `ps`
// output: no integration path can inject a marker or a depth extreme into the
// real `ps`, so this edge-case matrix is the one place those are pinned. One
// test at the end runs the real `ps`.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ps renders `ps -A -o pid= -o ppid= -o args=` output from "pid ppid args" rows.
func ps(rows ...string) []byte {
	return []byte("  " + strings.Join(rows, "\n  ") + "\n")
}

func staticPS(out []byte) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return out, nil }
}

// chain builds n rows, the first being self (pid n) and the last pid 1 (ppid 0),
// each process's parent being pid-1. Its pids are n, n-1, ..., 1.
func chain(n int) []byte {
	rows := make([]string, 0, n)
	for pid := n; pid >= 1; pid-- {
		rows = append(rows, fmt.Sprintf("%d %d proc%d", pid, pid-1, pid))
	}
	return ps(rows...)
}

func wantPIDs(n int) []string {
	var pids []string
	for pid := n; pid >= 1; pid-- {
		pids = append(pids, strconv.Itoa(pid))
	}
	return pids
}

func read(t *testing.T, self int, out []byte, markers ...string) (Result, error) {
	t.Helper()
	return ReadWith(context.Background(), Options{Self: self, Run: staticPS(out), Markers: markers})
}

// The chain starts at the hook itself and walks parents to pid 1, pids only.
func TestReadWithWalksTheChainFromSelfToPID1(t *testing.T) {
	out := ps(
		"1 0 /sbin/init",
		"20 1 tmux: server",
		"30 20 -bash",
		"40 30 codex --some-flag",
		"50 40 /usr/bin/cenci notify -agent codex",
		"99 1 unrelated",
	)
	got, err := read(t, 50, out)
	if err != nil {
		t.Fatalf("ReadWith: %v", err)
	}
	if want := []string{"50", "40", "30", "20", "1"}; !reflect.DeepEqual(got.PIDs, want) {
		t.Errorf("PIDs = %v, want %v (self first, unrelated rows left out)", got.PIDs, want)
	}
}

// A chain may end at a process whose parent is 0 without passing pid 1.
func TestReadWithStopsAtAParentOfZero(t *testing.T) {
	got, err := read(t, 10, ps("10 5 notify", "5 0 daemonized"))
	if err != nil {
		t.Fatalf("ReadWith: %v", err)
	}
	if want := []string{"10", "5"}; !reflect.DeepEqual(got.PIDs, want) {
		t.Errorf("PIDs = %v, want %v", got.PIDs, want)
	}
}

// P21: the cap is on the number of entries, the hook included. 32 is accepted
// (the control), 33 is an error, never a truncated chain.
func TestReadWithDepthCapAcceptsMaxDepthAndRejectsOneMore(t *testing.T) {
	if MaxDepth != 32 {
		t.Fatalf("MaxDepth = %d, want 32", MaxDepth)
	}
	got, err := read(t, MaxDepth, chain(MaxDepth))
	if err != nil {
		t.Fatalf("a chain of %d entries: %v, want it accepted", MaxDepth, err)
	}
	if want := wantPIDs(MaxDepth); !reflect.DeepEqual(got.PIDs, want) {
		t.Errorf("PIDs = %v, want %v", got.PIDs, want)
	}

	got, err = read(t, MaxDepth+1, chain(MaxDepth+1))
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("a chain of %d entries: err %v, want ErrTooDeep", MaxDepth+1, err)
	}
	if len(got.PIDs) != 0 || got.SingleClient {
		t.Errorf("result %+v on a too-deep chain, want the zero result", got)
	}
}

func TestReadWithRejectsACycle(t *testing.T) {
	got, err := read(t, 10, ps("10 11 a", "11 12 b", "12 11 c"))
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
	if len(got.PIDs) != 0 {
		t.Errorf("PIDs = %v, want none", got.PIDs)
	}
}

func TestReadWithMissingProcessesAreErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		self int
		out  []byte
		want error
	}{
		{"self is not listed", 10, ps("1 0 init", "20 1 other"), ErrSelfNotListed},
		{"empty output", 10, nil, ErrSelfNotListed},
		{"a parent is not listed", 10, ps("10 77 notify", "1 0 init"), ErrParentNotListed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := read(t, tc.self, tc.out)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(got.PIDs) != 0 || got.SingleClient {
				t.Errorf("result %+v, want the zero result", got)
			}
		})
	}
}

// Malformed output is an error, not a partial chain: a row needs two decimal
// pids; its args may hold anything.
func TestReadWithMalformedOutputIsAnError(t *testing.T) {
	for _, tc := range []struct{ name, row string }{
		{"non-numeric pid", "abc 1 notify"},
		{"non-numeric ppid", "10 x notify"},
		{"one field only", "10"},
		{"negative ppid", "10 -1 notify"},
		{"garbage line", "ps: some warning on stdout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := read(t, 10, ps("10 1 notify", "1 0 init", tc.row)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	// The control: arguments with spaces, tabs, quotes and digits parse fine.
	got, err := read(t, 10, ps("10 1 sh -c 'echo 5 6' \"x\"", "1 0 init"))
	if err != nil || !reflect.DeepEqual(got.PIDs, []string{"10", "1"}) {
		t.Errorf("control: %v, %v, want the two-entry chain", got.PIDs, err)
	}
}

func TestReadWithOutputOverTheCapIsAnError(t *testing.T) {
	big := append(ps("10 1 notify", "1 0 init"), []byte(strings.Repeat("x", MaxOutputBytes))...)
	if _, err := read(t, 10, big); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOutputTooLarge", err)
	}
}

// A deadline, or exec's WaitDelay error, is a timeout; any other failure of the
// command is an error that is not one.
func TestReadWithMapsCommandFailures(t *testing.T) {
	t.Run("the context deadline is a timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := ReadWith(ctx, Options{Self: 10, Run: func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("the read took %s, want it bounded by the context", time.Since(start))
		}
	})
	t.Run("exec WaitDelay is a timeout", func(t *testing.T) {
		_, err := ReadWith(context.Background(), Options{Self: 10, Run: func(context.Context) ([]byte, error) {
			return nil, fmt.Errorf("ps: %w", exec.ErrWaitDelay)
		}})
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
	})
	t.Run("another failure is an error but not a timeout", func(t *testing.T) {
		_, err := ReadWith(context.Background(), Options{Self: 10, Run: func(context.Context) ([]byte, error) {
			return nil, errors.New("exit status 1")
		}})
		if err == nil || errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want a non-timeout error", err)
		}
	})
}

// Classification against an injected marker table (the production table is
// empty): the nearest Codex ancestor above the hook decides, and only an exact
// argv[0] basename of "codex" is Codex.
func TestReadWithClassifiesTheNearestCodexAncestor(t *testing.T) {
	const marker = "app-server"
	for _, tc := range []struct {
		name string
		rows []string
		want bool
	}{
		{"the nearest Codex ancestor matches the marker: the shared server",
			[]string{"50 40 cenci notify -agent codex", "40 30 /usr/local/bin/codex app-server --x", "30 1 -bash", "1 0 init"}, false},
		{"the nearest Codex ancestor matches no marker: a single client",
			[]string{"50 40 cenci notify -agent codex", "40 30 codex --model x", "30 1 -bash", "1 0 init"}, true},
		{"a client below the shared server: the nearest one decides",
			[]string{"50 40 cenci notify", "40 30 codex", "30 20 codex app-server", "20 1 -bash", "1 0 init"}, true},
		{"a shared server below a client: the nearest one decides",
			[]string{"50 40 cenci notify", "40 30 codex app-server", "30 20 codex", "20 1 -bash", "1 0 init"}, false},
		{"no Codex ancestor at all is never a single client",
			[]string{"50 40 cenci notify", "40 30 -bash", "30 1 tmux", "1 0 init"}, false},
		{"the Node wrapper is not the Codex binary",
			[]string{"50 40 cenci notify", "40 30 node /usr/lib/node_modules/@openai/codex/bin/codex.js", "30 1 -bash", "1 0 init"}, false},
		{"a different binary whose name merely contains codex",
			[]string{"50 40 cenci notify", "40 30 /usr/bin/codex-helper", "30 1 /usr/bin/nocodex", "1 0 init"}, false},
		{"the hook's own argv is never the Codex ancestor",
			[]string{"50 40 /opt/codex notify", "40 30 -bash", "30 1 tmux", "1 0 init"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := read(t, 50, ps(tc.rows...), marker)
			if err != nil {
				t.Fatalf("ReadWith: %v", err)
			}
			if got.SingleClient != tc.want {
				t.Errorf("SingleClient = %v, want %v", got.SingleClient, tc.want)
			}
		})
	}
}

// The production marker table is empty until the native record names a marker,
// so no read reports a single client: every host Codex pane stays unproven. The
// second case shows an injected table is what changes the answer.
func TestProductionMarkerTableIsEmptyAndNeverReportsASingleClient(t *testing.T) {
	if len(sharedServerMarkers) != 0 {
		t.Fatalf("sharedServerMarkers = %v, want it empty until the native record fills the marker row", sharedServerMarkers)
	}
	out := ps("50 40 cenci notify", "40 30 codex --model x", "30 1 -bash", "1 0 init")
	got, err := ReadWith(context.Background(), Options{Self: 50, Run: staticPS(out)})
	if err != nil {
		t.Fatalf("ReadWith: %v", err)
	}
	if got.SingleClient {
		t.Error("the production marker table reported a single client")
	}
	if !reflect.DeepEqual(got.PIDs, []string{"50", "40", "30", "1"}) {
		t.Errorf("PIDs = %v, want the chain still reported", got.PIDs)
	}
	got, err = ReadWith(context.Background(), Options{Self: 50, Run: staticPS(out), Markers: []string{"app-server"}})
	if err != nil || !got.SingleClient {
		t.Errorf("with an injected marker: %+v, %v, want a single client", got, err)
	}
}

// The real `ps` reports the test process first and the whole chain as pids. It
// skips where `ps` is missing.
func TestReadRunsTheRealPS(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.PIDs) < 2 || got.PIDs[0] != strconv.Itoa(os.Getpid()) {
		t.Fatalf("PIDs = %v, want this process first and at least its parent", got.PIDs)
	}
	for _, pid := range got.PIDs {
		if n, err := strconv.Atoi(pid); err != nil || n < 1 {
			t.Errorf("pid %q is not a positive decimal", pid)
		}
	}
	if got.SingleClient {
		t.Error("the empty production marker table reported a single client")
	}
}
