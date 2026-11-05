package hookhealth

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var wantRecordRE = regexp.MustCompile(`^[0-9]+ (claude|codex)\n$`)

var allowedNames = map[string]bool{
	"missing-binary":     true,
	"bootstrap-failure":  true,
	"malformed-input":    true,
	"socket-unavailable": true,
	"delivery-failure":   true,
	"last-ok":            true,
}

// isolate points the store at a throwaway state home so no test can touch the
// real one (watch/docs/test-isolation.md), and returns the store directory.
func isolate(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	return filepath.Join(state, "cenci", "hook-health")
}

// mkStore creates the store directory chain with safe permissions.
func mkStore(t *testing.T, dir string) {
	t.Helper()
	root := filepath.Dir(dir)
	for _, d := range []string{root, dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func seedRaw(t *testing.T, dir, name, content string) {
	t.Helper()
	mkStore(t, dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T, dir, name, client string, at time.Time) {
	t.Helper()
	seedRaw(t, dir, name, fmt.Sprintf("%d %s\n", at.Unix(), client))
}

func readStamp(t *testing.T, dir, name string) (int64, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	if !wantRecordRE.Match(b) {
		t.Fatalf("%s holds %q, not a valid record", name, b)
	}
	n, err := strconv.ParseInt(strings.Fields(string(b))[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n, true
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRead_States(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	type rec struct {
		name   string
		client string
		at     time.Time
	}
	tests := []struct {
		name string
		recs []rec
		raw  map[string]string
		want State
	}{
		{name: "no records", want: StateNoEvidence},
		{name: "failure only", recs: []rec{{"delivery-failure", "claude", ago(time.Minute)}}, want: StateCurrentFailure},
		{name: "ok only", recs: []rec{{"last-ok", "claude", ago(time.Minute)}}, want: StateHealthy},
		{name: "failure older than ok", recs: []rec{
			{"socket-unavailable", "codex", ago(2 * time.Minute)},
			{"last-ok", "codex", ago(time.Minute)},
		}, want: StateRecovered},
		{name: "failure newer than ok", recs: []rec{
			{"socket-unavailable", "codex", ago(time.Minute)},
			{"last-ok", "codex", ago(2 * time.Minute)},
		}, want: StateCurrentFailure},
		{name: "tie is current failure", recs: []rec{
			{"malformed-input", "claude", ago(time.Minute)},
			{"last-ok", "claude", ago(time.Minute)},
		}, want: StateCurrentFailure},
		{name: "one of several failures newer than ok", recs: []rec{
			{"missing-binary", "claude", ago(10 * time.Minute)},
			{"delivery-failure", "claude", ago(30 * time.Second)},
			{"last-ok", "claude", ago(time.Minute)},
		}, want: StateCurrentFailure},
		{name: "stale failure only is no evidence", recs: []rec{{"missing-binary", "claude", ago(25 * time.Hour)}}, want: StateNoEvidence},
		{name: "stale ok only is no evidence", recs: []rec{{"last-ok", "claude", ago(25 * time.Hour)}}, want: StateNoEvidence},
		{name: "stale failure with fresh ok is healthy", recs: []rec{
			{"missing-binary", "claude", ago(26 * time.Hour)},
			{"last-ok", "claude", ago(time.Minute)},
		}, want: StateHealthy},
		{name: "fresh failure with stale ok is current failure", recs: []rec{
			{"bootstrap-failure", "codex", ago(time.Minute)},
			{"last-ok", "codex", ago(30 * time.Hour)},
		}, want: StateCurrentFailure},
		{name: "just inside the stale window counts", recs: []rec{{"missing-binary", "claude", ago(24*time.Hour - 10*time.Second)}}, want: StateCurrentFailure},
		{name: "future failure is absent", recs: []rec{{"missing-binary", "claude", now.Add(time.Minute)}}, want: StateNoEvidence},
		{name: "future failure does not break health", recs: []rec{
			{"missing-binary", "claude", now.Add(time.Hour)},
			{"last-ok", "claude", ago(time.Minute)},
		}, want: StateHealthy},
		{name: "future ok is absent", recs: []rec{{"last-ok", "claude", now.Add(time.Hour)}}, want: StateNoEvidence},
		{name: "slightly future failure within skew is valid", recs: []rec{{"delivery-failure", "claude", now.Add(2 * time.Second)}}, want: StateCurrentFailure},
		{name: "bad client", raw: map[string]string{"delivery-failure": fmt.Sprintf("%d opencode\n", now.Unix())}, want: StateNoEvidence},
		{name: "missing newline", raw: map[string]string{"delivery-failure": fmt.Sprintf("%d claude", now.Unix())}, want: StateNoEvidence},
		{name: "extra field", raw: map[string]string{"delivery-failure": fmt.Sprintf("%d claude extra\n", now.Unix())}, want: StateNoEvidence},
		{name: "non numeric epoch", raw: map[string]string{"delivery-failure": "abc claude\n"}, want: StateNoEvidence},
		{name: "negative epoch", raw: map[string]string{"delivery-failure": "-5 claude\n"}, want: StateNoEvidence},
		{name: "empty file", raw: map[string]string{"delivery-failure": ""}, want: StateNoEvidence},
		{name: "oversized record", raw: map[string]string{"delivery-failure": fmt.Sprintf("%d claude\n", now.Unix()) + strings.Repeat("x", 64)}, want: StateNoEvidence},
		{name: "overflowing epoch", raw: map[string]string{"delivery-failure": "99999999999999999999 claude\n"}, want: StateNoEvidence},
		{name: "invalid failure does not break health", raw: map[string]string{
			"delivery-failure": "garbage\n",
			"last-ok":          fmt.Sprintf("%d claude\n", now.Unix()),
		}, want: StateHealthy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			mkStore(t, dir)
			for _, r := range tc.recs {
				seed(t, dir, r.name, r.client, r.at)
			}
			for name, content := range tc.raw {
				seedRaw(t, dir, name, content)
			}
			got := Read(dir, now)
			if got.State != tc.want {
				t.Fatalf("state = %q, want %q (report %+v)", got.State, tc.want, got)
			}
		})
	}
}

func TestRead_NonRegularRecordsAreAbsent(t *testing.T) {
	now := time.Now()
	dir := isolate(t)
	mkStore(t, dir)

	// A directory in place of a record.
	if err := os.Mkdir(filepath.Join(dir, "delivery-failure"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A symlink to a perfectly valid record elsewhere.
	elsewhere := filepath.Join(t.TempDir(), "valid")
	if err := os.WriteFile(elsewhere, []byte(fmt.Sprintf("%d claude\n", now.Unix())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "missing-binary")); err != nil {
		t.Fatal(err)
	}
	if got := Read(dir, now).State; got != StateNoEvidence {
		t.Fatalf("state = %q, want %q", got, StateNoEvidence)
	}
}

func TestRead_ReportsCategoryClientAndHint(t *testing.T) {
	now := time.Now()
	dir := isolate(t)
	at := now.Add(-time.Minute).Truncate(time.Second)
	seed(t, dir, "socket-unavailable", "codex", at)

	r := Read(dir, now)
	if r.State != StateCurrentFailure || len(r.Failures) != 1 {
		t.Fatalf("report = %+v", r)
	}
	f := r.Failures[0]
	if f.Category != SocketUnavailable || f.Client != "codex" || !f.At.Equal(at) {
		t.Fatalf("failure = %+v", f)
	}
	if f.Category.Hint() == "" {
		t.Fatal("category has no hint")
	}
}

func TestRead_NeverCreatesAnything(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())

	if got := Read(Dir(), time.Now()).State; got != StateNoEvidence {
		t.Fatalf("state = %q, want no-evidence", got)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Read created %d entries under the state home", len(entries))
	}
}

func TestRead_EmptyDirIsNoEvidence(t *testing.T) {
	if got := Read("", time.Now()).State; got != StateNoEvidence {
		t.Fatalf("state = %q, want no-evidence", got)
	}
}

func TestDir_Resolution(t *testing.T) {
	t.Run("absolute XDG_STATE_HOME wins", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("HOME", t.TempDir())
		if got, want := Dir(), filepath.Join(state, "cenci", "hook-health"); got != want {
			t.Fatalf("Dir() = %q, want %q", got, want)
		}
	})
	t.Run("relative XDG_STATE_HOME falls back to HOME", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("XDG_STATE_HOME", "relative/state")
		t.Setenv("HOME", home)
		if got, want := Dir(), filepath.Join(home, ".local", "state", "cenci", "hook-health"); got != want {
			t.Fatalf("Dir() = %q, want %q", got, want)
		}
	})
	t.Run("unset XDG_STATE_HOME falls back to HOME", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", home)
		if got, want := Dir(), filepath.Join(home, ".local", "state", "cenci", "hook-health"); got != want {
			t.Fatalf("Dir() = %q, want %q", got, want)
		}
	})
	t.Run("nothing resolvable is empty and the writer is a no-op", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		cwd := t.TempDir()
		t.Chdir(cwd)
		if got := Dir(); got != "" {
			t.Fatalf("Dir() = %q, want empty", got)
		}
		RecordFailure(DeliveryFailure, "claude")
		RecordOK("claude")
		entries, err := os.ReadDir(cwd)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("writer created %d entries in the working directory", len(entries))
		}
	})
}

func TestWriter_CreatesHardenedStore(t *testing.T) {
	dir := isolate(t)
	RecordFailure(MissingBinary, "claude")

	for _, d := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s mode = %04o, want 0700", d, got)
		}
	}
	info, err := os.Lstat(filepath.Join(dir, "missing-binary"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("record mode = %04o, want 0600", got)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("record is not a regular file")
	}
}

func TestWriter_RefusesUnsafeStores(t *testing.T) {
	setups := []struct {
		name  string
		setup func(t *testing.T, root, leaf string) (watched string)
	}{
		{"symlinked leaf", func(t *testing.T, root, leaf string) string {
			target := t.TempDir()
			mkRoot(t, root)
			if err := os.Symlink(target, leaf); err != nil {
				t.Fatal(err)
			}
			return target
		}},
		{"symlinked root", func(t *testing.T, root, leaf string) string {
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, root); err != nil {
				t.Fatal(err)
			}
			return target
		}},
		{"leaf is a file", func(t *testing.T, root, leaf string) string {
			mkRoot(t, root)
			if err := os.WriteFile(leaf, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"world writable leaf 0777", func(t *testing.T, root, leaf string) string {
			mkRoot(t, root)
			chmodDir(t, leaf, 0o777)
			return leaf
		}},
		{"group writable leaf 0770", func(t *testing.T, root, leaf string) string {
			mkRoot(t, root)
			chmodDir(t, leaf, 0o770)
			return leaf
		}},
		{"world writable root 0777", func(t *testing.T, root, leaf string) string {
			chmodDir(t, root, 0o777)
			chmodDir(t, leaf, 0o700)
			return leaf
		}},
	}
	for _, tc := range setups {
		t.Run(tc.name, func(t *testing.T) {
			leaf := isolate(t)
			root := filepath.Dir(leaf)
			watched := tc.setup(t, root, leaf)

			RecordFailure(DeliveryFailure, "claude")
			RecordOK("claude")

			info, err := os.Lstat(watched)
			if err != nil {
				t.Fatal(err)
			}
			if !info.IsDir() {
				return // "leaf is a file": still a file, nothing was written through it
			}
			for _, name := range listNames(t, watched) {
				if allowedNames[name] || strings.HasPrefix(name, ".") {
					t.Fatalf("writer wrote %q into a refused store", name)
				}
			}
		})
	}
}

func TestWriter_RefusesForeignOwner(t *testing.T) {
	dir := isolate(t)
	mkStore(t, dir)
	orig := ownerUID
	ownerUID = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { ownerUID = orig })

	RecordFailure(DeliveryFailure, "claude")
	RecordOK("claude")

	if got := listNames(t, dir); len(got) != 0 {
		t.Fatalf("writer wrote %v into a foreign-owned store", got)
	}
}

// requireUntrusted asserts the report of an untrusted store: no evidence, the
// Untrusted flag set, and no failures listed even when records were seeded.
func requireUntrusted(t *testing.T, got Report) {
	t.Helper()
	if got.State != StateNoEvidence {
		t.Fatalf("state = %q, want no-evidence", got.State)
	}
	if !got.Untrusted {
		t.Fatalf("Untrusted = false, want true (report %+v)", got)
	}
	if len(got.Failures) != 0 || got.LastOK != nil {
		t.Fatalf("an untrusted store's records were read: %+v", got)
	}
}

func TestReader_RefusesUnsafeStores(t *testing.T) {
	now := time.Now()
	foreignOwner := func(t *testing.T) {
		t.Helper()
		orig := ownerUID
		ownerUID = func() int { return os.Getuid() + 1 }
		t.Cleanup(func() { ownerUID = orig })
	}
	tests := []struct {
		name  string
		setup func(t *testing.T) (dir string)
	}{
		{"world writable leaf 0777 with a fresh missing-binary record", func(t *testing.T) string {
			dir := isolate(t)
			seed(t, dir, "missing-binary", "claude", now)
			chmodDir(t, dir, 0o777)
			return dir
		}},
		{"group writable leaf 0770", func(t *testing.T) string {
			dir := isolate(t)
			seed(t, dir, "delivery-failure", "claude", now)
			chmodDir(t, dir, 0o770)
			return dir
		}},
		{"symlinked leaf to a trusted dir", func(t *testing.T) string {
			dir := isolate(t)
			real := t.TempDir()
			chmodDir(t, real, 0o700)
			seed(t, real, "delivery-failure", "claude", now)
			mkRoot(t, filepath.Dir(dir))
			if err := os.Symlink(real, dir); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		{"leaf is a regular file", func(t *testing.T) string {
			dir := isolate(t)
			mkRoot(t, filepath.Dir(dir))
			if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		{"foreign owner leaf", func(t *testing.T) string {
			dir := isolate(t)
			seed(t, dir, "delivery-failure", "claude", now)
			foreignOwner(t)
			return dir
		}},
		{"world writable root 0777 with the leaf absent", func(t *testing.T) string {
			dir := isolate(t)
			chmodDir(t, filepath.Dir(dir), 0o777)
			return dir
		}},
		{"foreign owner root with the leaf absent", func(t *testing.T) string {
			dir := isolate(t)
			mkRoot(t, filepath.Dir(dir))
			foreignOwner(t)
			return dir
		}},
		{"non-ENOENT Lstat failure on the root (state home is a file)", func(t *testing.T) string {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.WriteFile(state, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", state)
			return filepath.Join(state, "cenci", "hook-health")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.setup(t)
			requireUntrusted(t, Read(dir, now))
		})
	}
}

func TestRead_AbsentIsNotUntrusted(t *testing.T) {
	now := time.Now()
	t.Run("state root absent", func(t *testing.T) {
		dir := isolate(t)
		got := Read(dir, now)
		if got.State != StateNoEvidence || got.Untrusted {
			t.Fatalf("report = %+v, want plain no-evidence", got)
		}
	})
	t.Run("trusted root with the leaf absent", func(t *testing.T) {
		dir := isolate(t)
		mkRoot(t, filepath.Dir(dir))
		got := Read(dir, now)
		if got.State != StateNoEvidence || got.Untrusted {
			t.Fatalf("report = %+v, want plain no-evidence", got)
		}
	})
	t.Run("empty dir", func(t *testing.T) {
		got := Read("", now)
		if got.State != StateNoEvidence || got.Untrusted {
			t.Fatalf("report = %+v, want plain no-evidence", got)
		}
	})
	t.Run("leaf mode 0750 stays trusted and classifies its records", func(t *testing.T) {
		dir := isolate(t)
		seed(t, dir, "delivery-failure", "claude", now.Add(-time.Minute))
		chmodDir(t, dir, 0o750)
		got := Read(dir, now)
		if got.Untrusted || got.State != StateCurrentFailure || len(got.Failures) != 1 {
			t.Fatalf("report = %+v, want a trusted current-failure", got)
		}
	})
}

func mkRoot(t *testing.T, root string) {
	t.Helper()
	chmodDir(t, root, 0o700)
}

// chmodDir creates dir (and parents) and sets its exact mode, bypassing umask.
func chmodDir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func TestRecordFailure_RateLimit(t *testing.T) {
	now := time.Now()
	t.Run("record younger than 60s is not rewritten", func(t *testing.T) {
		dir := isolate(t)
		at := now.Add(-30 * time.Second)
		seed(t, dir, "delivery-failure", "codex", at)
		RecordFailure(DeliveryFailure, "claude")
		b, err := os.ReadFile(filepath.Join(dir, "delivery-failure"))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("%d codex\n", at.Unix()); string(b) != want {
			t.Fatalf("record = %q, want unchanged %q", b, want)
		}
	})
	t.Run("record older than 60s is rewritten", func(t *testing.T) {
		dir := isolate(t)
		seed(t, dir, "delivery-failure", "codex", now.Add(-2*time.Minute))
		RecordFailure(DeliveryFailure, "claude")
		b, err := os.ReadFile(filepath.Join(dir, "delivery-failure"))
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(b))
		ts, _ := strconv.ParseInt(fields[0], 10, 64)
		if fields[1] != "claude" || time.Since(time.Unix(ts, 0)) > 30*time.Second {
			t.Fatalf("record = %q, want a fresh claude record", b)
		}
	})
	t.Run("invalid existing record is replaced", func(t *testing.T) {
		dir := isolate(t)
		seedRaw(t, dir, "delivery-failure", "garbage")
		RecordFailure(DeliveryFailure, "codex")
		if _, ok := readStamp(t, dir, "delivery-failure"); !ok {
			t.Fatal("record missing")
		}
	})
	t.Run("future dated record is replaced", func(t *testing.T) {
		dir := isolate(t)
		seed(t, dir, "delivery-failure", "codex", now.Add(time.Hour))
		RecordFailure(DeliveryFailure, "claude")
		ts, ok := readStamp(t, dir, "delivery-failure")
		if !ok || ts > time.Now().Unix()+5 {
			t.Fatalf("future-dated record was not replaced (ts=%d)", ts)
		}
	})
	t.Run("first failure creates the record", func(t *testing.T) {
		dir := isolate(t)
		RecordFailure(MalformedInput, "codex")
		if _, ok := readStamp(t, dir, "malformed-input"); !ok {
			t.Fatal("record missing")
		}
	})
}

func TestRecordOK_Rules(t *testing.T) {
	now := time.Now()
	t.Run("absent last-ok is written", func(t *testing.T) {
		dir := isolate(t)
		RecordOK("claude")
		if _, ok := readStamp(t, dir, "last-ok"); !ok {
			t.Fatal("last-ok missing")
		}
	})
	t.Run("recent last-ok without newer failure is kept", func(t *testing.T) {
		dir := isolate(t)
		at := now.Add(-5 * time.Minute)
		seed(t, dir, "last-ok", "codex", at)
		seed(t, dir, "delivery-failure", "codex", at.Add(-time.Minute))
		RecordOK("claude")
		b, _ := os.ReadFile(filepath.Join(dir, "last-ok"))
		if want := fmt.Sprintf("%d codex\n", at.Unix()); string(b) != want {
			t.Fatalf("last-ok = %q, want unchanged %q", b, want)
		}
	})
	t.Run("last-ok older than 10 minutes is rewritten", func(t *testing.T) {
		dir := isolate(t)
		seed(t, dir, "last-ok", "codex", now.Add(-11*time.Minute))
		RecordOK("claude")
		b, _ := os.ReadFile(filepath.Join(dir, "last-ok"))
		if !strings.HasSuffix(string(b), " claude\n") {
			t.Fatalf("last-ok = %q, want a fresh claude record", b)
		}
	})
	t.Run("a newer failure forces a rewrite", func(t *testing.T) {
		dir := isolate(t)
		okAt := now.Add(-2 * time.Minute)
		seed(t, dir, "last-ok", "codex", okAt)
		seed(t, dir, "socket-unavailable", "codex", now.Add(-time.Minute))
		RecordOK("claude")
		ts, _ := readStamp(t, dir, "last-ok")
		if ts <= okAt.Unix() {
			t.Fatalf("last-ok ts = %d, want newer than %d", ts, okAt.Unix())
		}
		if got := Read(dir, time.Now().Add(2*time.Second)).State; got != StateRecovered {
			t.Fatalf("state = %q, want recovered", got)
		}
	})
	t.Run("a failure tied with last-ok forces a rewrite", func(t *testing.T) {
		dir := isolate(t)
		at := time.Now().Add(-3 * time.Second)
		seed(t, dir, "last-ok", "codex", at)
		seed(t, dir, "socket-unavailable", "codex", at)
		RecordOK("claude")
		b, _ := os.ReadFile(filepath.Join(dir, "last-ok"))
		if !strings.HasSuffix(string(b), " claude\n") {
			t.Fatalf("last-ok = %q, want rewritten by claude", b)
		}
	})
	t.Run("invalid last-ok is replaced", func(t *testing.T) {
		dir := isolate(t)
		seedRaw(t, dir, "last-ok", "junk")
		RecordOK("codex")
		if _, ok := readStamp(t, dir, "last-ok"); !ok {
			t.Fatal("last-ok missing")
		}
	})
}

func TestWriters_RejectOutsideTheClosedSets(t *testing.T) {
	dir := isolate(t)
	RecordFailure("bogus", "claude")
	RecordFailure(LastOK, "claude")
	RecordFailure(DeliveryFailure, "opencode")
	RecordFailure(DeliveryFailure, "")
	RecordFailure("../escape", "claude")
	RecordOK("opencode")
	RecordOK("")

	if _, err := os.Lstat(dir); err == nil {
		t.Fatalf("store created for rejected input: %v", listNames(t, dir))
	}
}

func TestStore_StaysBoundedUnderRepeatedFailures(t *testing.T) {
	dir := isolate(t)
	failures := []Category{MissingBinary, BootstrapFailure, MalformedInput, SocketUnavailable, DeliveryFailure}
	old := time.Now().Add(-5 * time.Minute)
	for _, c := range failures {
		seed(t, dir, string(c), "claude", old)
	}
	seed(t, dir, "last-ok", "claude", old)
	sizes := map[string]int64{}
	for _, n := range listNames(t, dir) {
		sizes[n] = fileSize(t, dir, n)
	}

	for i := 0; i < 50; i++ {
		for _, c := range failures {
			RecordFailure(c, "claude")
		}
		RecordOK("claude")
		// Age the records again so the next round rewrites them.
		for _, c := range failures {
			seed(t, dir, string(c), "claude", old)
		}
		seed(t, dir, "last-ok", "claude", old)
	}
	for _, c := range failures {
		RecordFailure(c, "claude")
	}
	RecordOK("claude")

	names := listNames(t, dir)
	if len(names) != len(sizes) {
		t.Fatalf("store holds %v, want exactly %d records", names, len(sizes))
	}
	for _, n := range names {
		if !allowedNames[n] {
			t.Fatalf("unexpected file %q in the store", n)
		}
		if got := fileSize(t, dir, n); got != sizes[n] {
			t.Fatalf("%s size = %d, want constant %d", n, got, sizes[n])
		}
	}
}

func fileSize(t *testing.T, dir, name string) int64 {
	t.Helper()
	info, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestStore_RecordsHoldOnlyEpochAndClient(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-VALUE"
	t.Setenv("ANTHROPIC_API_KEY", sentinel)
	t.Setenv("GH_TOKEN", sentinel)
	dir := isolate(t)

	for _, c := range []Category{MissingBinary, BootstrapFailure, MalformedInput, SocketUnavailable, DeliveryFailure} {
		RecordFailure(c, "codex")
	}
	RecordOK("claude")

	names := listNames(t, dir)
	if len(names) != 6 {
		t.Fatalf("store holds %v, want 6 records", names)
	}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if !wantRecordRE.Match(b) || len(b) > 32 {
			t.Fatalf("%s holds %q, want only `<epoch> <client>\\n`", n, b)
		}
	}
}

func TestStore_ParallelWritersLeaveParseableRecords(t *testing.T) {
	dir := isolate(t)
	failures := []Category{MissingBinary, BootstrapFailure, MalformedInput, SocketUnavailable, DeliveryFailure}
	old := time.Now().Add(-5 * time.Minute)

	for round := 0; round < 20; round++ {
		for _, c := range failures {
			seed(t, dir, string(c), "claude", old)
		}
		seed(t, dir, "last-ok", "claude", old)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				client := "claude"
				if i%2 == 0 {
					client = "codex"
				}
				RecordFailure(failures[i%len(failures)], client)
				RecordOK(client)
				Read(dir, time.Now())
			}(i)
		}
		close(start)
		wg.Wait()

		for _, n := range listNames(t, dir) {
			if !allowedNames[n] {
				t.Fatalf("round %d: unexpected file %q", round, n)
			}
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatal(err)
			}
			if !wantRecordRE.Match(b) {
				t.Fatalf("round %d: %s holds %q", round, n, b)
			}
		}
	}
}

func TestRender(t *testing.T) {
	now := time.Now()
	at := time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)

	t.Run("current failure", func(t *testing.T) {
		out := Render(Report{State: StateCurrentFailure, Failures: []Record{
			{Category: SocketUnavailable, Client: "codex", At: at},
		}})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if lines[0] != "hook health: current-failure" {
			t.Fatalf("first line = %q", lines[0])
		}
		want := "  socket-unavailable (codex, 2026-11-05T10:30:00Z): " + SocketUnavailable.Hint()
		if len(lines) != 2 || lines[1] != want {
			t.Fatalf("lines = %q, want detail %q", lines, want)
		}
	})
	t.Run("no evidence is explicitly not proof of health", func(t *testing.T) {
		out := Render(Report{State: StateNoEvidence})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if lines[0] != "hook health: no-evidence" {
			t.Fatalf("first line = %q", lines[0])
		}
		if !strings.Contains(out, "not proof of health") {
			t.Fatalf("output lacks the not-proof-of-health line: %q", out)
		}
	})
	t.Run("plain no evidence output is pinned byte for byte", func(t *testing.T) {
		const want = "hook health: no-evidence\n" +
			"  no recent records: this is not proof of health (hooks may not have run, or records are older than 24h)\n"
		if got := Render(Report{State: StateNoEvidence}); got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
	t.Run("untrusted store prints exactly the fixed reason line", func(t *testing.T) {
		const reason = "  untrusted store: the hook-health directory or its cenci state directory is a symlink, " +
			"not a directory, foreign-owned or group/other-writable (for example left root-owned by a sudo run), " +
			"so hooks cannot record; fix its owner and permissions — this reporter never repairs it"
		out := Render(Report{State: StateNoEvidence, Untrusted: true})
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		if len(lines) != 2 || lines[0] != "hook health: no-evidence" || lines[1] != reason {
			t.Fatalf("lines = %q, want the token line then %q", lines, reason)
		}
		if strings.Contains(out, "no recent records") {
			t.Fatalf("untrusted output still blames missing records: %q", out)
		}
	})
	t.Run("every state has its token as the first line", func(t *testing.T) {
		for _, s := range []State{StateHealthy, StateRecovered, StateNoEvidence, StateCurrentFailure} {
			out := Render(Report{State: s})
			if first, _, _ := strings.Cut(out, "\n"); first != "hook health: "+string(s) {
				t.Fatalf("state %q rendered first line %q", s, first)
			}
		}
	})
	t.Run("every category carries a hint", func(t *testing.T) {
		for _, c := range Categories() {
			if c.Hint() == "" {
				t.Fatalf("category %q has no hint", c)
			}
		}
	})
	t.Run("end to end from a seeded store", func(t *testing.T) {
		dir := isolate(t)
		seed(t, dir, "delivery-failure", "claude", now.Add(-time.Minute))
		out := Render(Read(dir, now))
		if !strings.HasPrefix(out, "hook health: current-failure\n  delivery-failure (claude, ") {
			t.Fatalf("output = %q", out)
		}
	})
}
