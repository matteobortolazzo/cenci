package capacity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const quotaFile = "daily-quota.json"

var (
	day1Evening = time.Date(2026, 11, 19, 23, 59, 0, 0, time.Local)
	day2Start   = time.Date(2026, 11, 20, 0, 0, 0, 0, time.Local)
	day1Noon    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)
)

func seedQuota(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, quotaFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readQuotaBytes(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, quotaFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustRecord(t *testing.T, q *DailyQuota, now time.Time, times int) {
	t.Helper()
	for i := 0; i < times; i++ {
		if err := q.Record(now); err != nil {
			t.Fatalf("Record #%d: %v", i+1, err)
		}
	}
}

func mustUsed(t *testing.T, q *DailyQuota, now time.Time) int {
	t.Helper()
	n, err := q.Used(now)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	return n
}

func mustPeek(t *testing.T, q *DailyQuota, now time.Time) int {
	t.Helper()
	n, err := q.Peek(now)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	return n
}

func TestDailyQuotaAbsentStoreReadsZeroAndCreatesNothing(t *testing.T) {
	root, parent, dir := storeDirs(t)
	q := NewDailyQuota(dir)

	if n := mustUsed(t, q, day1Noon); n != 0 {
		t.Errorf("Used = %d, want 0", n)
	}
	if n := mustPeek(t, q, day1Noon); n != 0 {
		t.Errorf("Peek = %d, want 0", n)
	}
	if names := listDir(t, root); len(names) != 0 {
		t.Errorf("reads created %v under the state root (parent %s)", names, filepath.Base(parent))
	}
}

func TestDailyQuotaExistingDirWithoutFileReadsZeroAndCreatesNothing(t *testing.T) {
	dir := trustedDir(t)
	q := NewDailyQuota(dir)

	if n := mustUsed(t, q, day1Noon); n != 0 {
		t.Errorf("Used = %d, want 0", n)
	}
	if n := mustPeek(t, q, day1Noon); n != 0 {
		t.Errorf("Peek = %d, want 0", n)
	}
	if names := listDir(t, dir); len(names) != 0 {
		t.Errorf("reads created %v in the store", names)
	}
}

func TestDailyQuotaRecordCountsLaunches(t *testing.T) {
	_, _, dir := storeDirs(t)
	q := NewDailyQuota(dir)

	mustRecord(t, q, day1Noon, 3)

	if n := mustUsed(t, q, day1Noon); n != 3 {
		t.Errorf("Used = %d, want 3", n)
	}
	if n := mustPeek(t, q, day1Noon); n != 3 {
		t.Errorf("Peek = %d, want 3", n)
	}
	want := `{"schema":1,"date":"2026-11-19","used":3}`
	if got := strings.TrimSpace(readQuotaBytes(t, dir)); got != want {
		t.Errorf("persisted file = %s, want %s", got, want)
	}
}

func TestDailyQuotaPersistsAcrossHandles(t *testing.T) {
	_, _, dir := storeDirs(t)
	mustRecord(t, NewDailyQuota(dir), day1Noon, 2)

	restarted := NewDailyQuota(dir)
	if n := mustUsed(t, restarted, day1Noon); n != 2 {
		t.Fatalf("Used after restart = %d, want 2", n)
	}
	mustRecord(t, restarted, day1Noon, 1)
	if n := mustPeek(t, NewDailyQuota(dir), day1Noon); n != 3 {
		t.Fatalf("Peek after restart and one more launch = %d, want 3", n)
	}
}

func TestDailyQuotaRollsOverAtLocalMidnight(t *testing.T) {
	_, _, dir := storeDirs(t)
	q := NewDailyQuota(dir)
	mustRecord(t, q, day1Evening, 2)

	if n := mustUsed(t, q, day1Evening); n != 2 {
		t.Errorf("Used at 23:59 = %d, want 2", n)
	}
	if n := mustUsed(t, q, day2Start); n != 0 {
		t.Errorf("Used at 00:00 next day = %d, want 0", n)
	}
	if n := mustPeek(t, q, day2Start); n != 0 {
		t.Errorf("Peek at 00:00 next day = %d, want 0", n)
	}

	mustRecord(t, q, day2Start, 1)
	if n := mustUsed(t, q, day2Start); n != 1 {
		t.Errorf("Used after first launch of the new day = %d, want 1", n)
	}
	if n := mustUsed(t, q, day1Evening); n != 0 {
		t.Errorf("Used for the earlier day after rollover = %d, want 0 (stored date is no longer that day)", n)
	}
}

func TestDailyQuotaUsesTheLocalCalendarDay(t *testing.T) {
	_, _, dir := storeDirs(t)
	q := NewDailyQuota(dir)
	// Two instants that are the same local day, expressed in different zones.
	morning := time.Date(2026, 11, 19, 0, 30, 0, 0, time.Local)
	night := time.Date(2026, 11, 19, 23, 30, 0, 0, time.Local).UTC()
	mustRecord(t, q, morning.UTC(), 1)

	if n := mustUsed(t, q, night); n != 1 {
		t.Fatalf("Used = %d, want 1 (same local day)", n)
	}
}

func TestDailyQuotaStoredDateOtherThanTodayReadsZero(t *testing.T) {
	for name, date := range map[string]string{
		"earlier date": "2026-11-18",
		"future date":  "2027-01-01",
	} {
		t.Run(name, func(t *testing.T) {
			dir := trustedDir(t)
			seedQuota(t, dir, `{"schema":1,"date":"`+date+`","used":7}`)
			q := NewDailyQuota(dir)

			if n := mustUsed(t, q, day1Noon); n != 0 {
				t.Errorf("Used = %d, want 0", n)
			}
			if n := mustPeek(t, q, day1Noon); n != 0 {
				t.Errorf("Peek = %d, want 0", n)
			}
			mustRecord(t, q, day1Noon, 1)
			if n := mustUsed(t, q, day1Noon); n != 1 {
				t.Errorf("Used after Record = %d, want 1 (the stale count is not carried)", n)
			}
		})
	}
}

func TestDailyQuotaConcurrentHandlesLoseNoLaunch(t *testing.T) {
	_, _, dir := storeDirs(t)
	handles := []*DailyQuota{NewDailyQuota(dir), NewDailyQuota(dir)}

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for _, h := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if err := h.Record(day1Noon); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Record: %v", err)
	}

	if n := mustPeek(t, NewDailyQuota(dir), day1Noon); n != 20 {
		t.Fatalf("persisted used = %d, want 20", n)
	}
}

func TestDailyQuotaBrokenStateIsAnErrorAndNeverRewritten(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    error
	}{
		{"not json", "garbage", ErrCorrupt},
		{"empty file", "", ErrCorrupt},
		{"unknown field", `{"schema":1,"date":"2026-11-19","used":1,"extra":true}`, ErrCorrupt},
		{"trailing data", `{"schema":1,"date":"2026-11-19","used":1}{"schema":1}`, ErrCorrupt},
		{"missing schema", `{"date":"2026-11-19","used":1}`, ErrCorrupt},
		{"schema of the wrong type", `{"schema":"1","date":"2026-11-19","used":1}`, ErrCorrupt},
		{"malformed date", `{"schema":1,"date":"19/11/2026","used":1}`, ErrCorrupt},
		{"negative used", `{"schema":1,"date":"2026-11-19","used":-1}`, ErrCorrupt},
		{"fractional used", `{"schema":1,"date":"2026-11-19","used":1.5}`, ErrCorrupt},
		{"truncated", `{"schema":1,"date":"2026-11-1`, ErrCorrupt},
		{"null used", `{"schema":1,"date":"2026-11-19","used":null}`, ErrCorrupt},
		{"null date", `{"schema":1,"date":null,"used":1}`, ErrCorrupt},
		{"null schema", `{"schema":null,"date":"2026-11-19","used":1}`, ErrCorrupt},
		{"newer schema", `{"schema":2,"date":"2026-11-19","used":1}`, ErrUnknownSchema},
		{"newer schema with other fields", `{"schema":2,"day":"2026-11-19","count":1}`, ErrUnknownSchema},
		{"schema zero", `{"schema":0,"date":"2026-11-19","used":1}`, ErrUnknownSchema},
		{"oversized", `{"schema":1,"date":"2026-11-19","used":1,"pad":"` + strings.Repeat("x", 4096) + `"}`, ErrNotRegular},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := trustedDir(t)
			seedQuota(t, dir, tc.content)
			q := NewDailyQuota(dir)

			if _, err := q.Used(day1Noon); !errors.Is(err, tc.want) {
				t.Errorf("Used err = %v, want %v", err, tc.want)
			}
			if _, err := q.Peek(day1Noon); !errors.Is(err, tc.want) {
				t.Errorf("Peek err = %v, want %v", err, tc.want)
			}
			if err := q.Record(day1Noon); !errors.Is(err, tc.want) {
				t.Errorf("Record err = %v, want %v", err, tc.want)
			}
			if got := readQuotaBytes(t, dir); got != tc.content {
				t.Errorf("file was rewritten: %q, want %q", got, tc.content)
			}
		})
	}
}

func TestDailyQuotaBrokenStateOnAnotherDayIsStillAnError(t *testing.T) {
	// A corrupt file is never auto-reset, even when a new day would otherwise
	// start from zero.
	dir := trustedDir(t)
	seedQuota(t, dir, "garbage")
	q := NewDailyQuota(dir)
	if err := q.Record(day2Start); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Record err = %v, want ErrCorrupt", err)
	}
	if got := readQuotaBytes(t, dir); got != "garbage" {
		t.Fatalf("file was rewritten: %q", got)
	}
}

func TestDailyQuotaSymlinkedDirectoryIsUnreadableAndNothingIsWrittenThrough(t *testing.T) {
	_, parent, dir := storeDirs(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	for _, p := range []string{parent, target} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	q := NewDailyQuota(dir)

	if _, err := q.Used(day1Noon); !errors.Is(err, ErrUntrusted) {
		t.Errorf("Used err = %v, want ErrUntrusted", err)
	}
	if _, err := q.Peek(day1Noon); !errors.Is(err, ErrUntrusted) {
		t.Errorf("Peek err = %v, want ErrUntrusted", err)
	}
	if err := q.Record(day1Noon); !errors.Is(err, ErrUntrusted) {
		t.Errorf("Record err = %v, want ErrUntrusted", err)
	}
	if names := listDir(t, target); len(names) != 0 {
		t.Errorf("files written through the symlinked directory: %v", names)
	}
}

func TestDailyQuotaSymlinkedFileIsUnreadableAndNothingIsWrittenThrough(t *testing.T) {
	dir := trustedDir(t)
	outside := filepath.Join(t.TempDir(), "outside.json")
	const original = `{"schema":1,"date":"2026-11-19","used":1}`
	if err := os.WriteFile(outside, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, quotaFile)); err != nil {
		t.Fatal(err)
	}
	q := NewDailyQuota(dir)

	if _, err := q.Used(day1Noon); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Used err = %v, want ErrNotRegular", err)
	}
	if _, err := q.Peek(day1Noon); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Peek err = %v, want ErrNotRegular", err)
	}
	if err := q.Record(day1Noon); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Record err = %v, want ErrNotRegular", err)
	}
	if got, _ := os.ReadFile(outside); string(got) != original {
		t.Errorf("symlink target changed to %q", got)
	}
	if info, err := os.Lstat(filepath.Join(dir, quotaFile)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v, %v", info, err)
	}
}

func TestDailyQuotaUnresolvedDirectoryIsAnError(t *testing.T) {
	q := NewDailyQuota("")
	if _, err := q.Used(day1Noon); !errors.Is(err, ErrUnresolved) {
		t.Errorf("Used err = %v, want ErrUnresolved", err)
	}
	if _, err := q.Peek(day1Noon); !errors.Is(err, ErrUnresolved) {
		t.Errorf("Peek err = %v, want ErrUnresolved", err)
	}
	if err := q.Record(day1Noon); !errors.Is(err, ErrUnresolved) {
		t.Errorf("Record err = %v, want ErrUnresolved", err)
	}
}

func TestDailyQuotaPeekTakesNoLock(t *testing.T) {
	dir := trustedDir(t)
	q := NewDailyQuota(dir)
	mustRecord(t, q, day1Noon, 2)
	q.store.lockTimeout = 100 * time.Millisecond

	var peeked int
	var peekErr error
	err := NewStore(dir).WithLock(func() error {
		peeked, peekErr = q.Peek(day1Noon)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if peekErr != nil || peeked != 2 {
		t.Fatalf("Peek under a held lock = %d, %v; want 2, nil", peeked, peekErr)
	}
}

func TestDailyQuotaUsedAndRecordWaitForTheLock(t *testing.T) {
	dir := trustedDir(t)
	q := NewDailyQuota(dir)
	mustRecord(t, q, day1Noon, 1)
	q.store.lockTimeout = 100 * time.Millisecond

	var usedErr, recordErr error
	err := NewStore(dir).WithLock(func() error {
		_, usedErr = q.Used(day1Noon)
		recordErr = q.Record(day1Noon)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !errors.Is(usedErr, ErrLockUnavailable) {
		t.Errorf("Used under a held lock err = %v, want ErrLockUnavailable", usedErr)
	}
	if !errors.Is(recordErr, ErrLockUnavailable) {
		t.Errorf("Record under a held lock err = %v, want ErrLockUnavailable", recordErr)
	}
	if n := mustPeek(t, q, day1Noon); n != 1 {
		t.Errorf("count after the refused Record = %d, want 1", n)
	}
}

// Every unreadable-state error becomes part of a skip or log line, so none may
// carry a path or the words the lazyboards contract matches on.
func TestDailyQuotaErrorMessagesArePathFreeAndContractSafe(t *testing.T) {
	_, parent, symlinked := storeDirs(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	for _, p := range []string{parent, target} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, symlinked); err != nil {
		t.Fatal(err)
	}

	corrupt := trustedDir(t)
	seedQuota(t, corrupt, "garbage")
	newer := trustedDir(t)
	seedQuota(t, newer, `{"schema":2}`)
	oversized := trustedDir(t)
	seedQuota(t, oversized, strings.Repeat("x", 5000))
	locked := trustedDir(t)
	lockedQuota := NewDailyQuota(locked)
	lockedQuota.store.lockTimeout = 50 * time.Millisecond

	var lockErr error
	if err := NewStore(locked).WithLock(func() error {
		lockErr = lockedQuota.Record(day1Noon)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	errs := map[string]struct {
		err  error
		dirs []string
	}{
		"unresolved":     {func() error { _, e := NewDailyQuota("").Used(day1Noon); return e }(), nil},
		"untrusted":      {func() error { _, e := NewDailyQuota(symlinked).Used(day1Noon); return e }(), []string{symlinked, target}},
		"corrupt":        {func() error { _, e := NewDailyQuota(corrupt).Used(day1Noon); return e }(), []string{corrupt}},
		"unknown schema": {func() error { _, e := NewDailyQuota(newer).Used(day1Noon); return e }(), []string{newer}},
		"not regular":    {func() error { _, e := NewDailyQuota(oversized).Used(day1Noon); return e }(), []string{oversized}},
		"lock":           {lockErr, []string{locked}},
	}
	for name, tc := range errs {
		t.Run(name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("expected an error")
			}
			msg := tc.err.Error()
			for _, banned := range []string{" dispatch ", " skip:"} {
				if strings.Contains(msg, banned) {
					t.Errorf("message %q contains %q", msg, banned)
				}
			}
			for _, leak := range append([]string{os.TempDir(), quotaFile}, tc.dirs...) {
				if strings.Contains(msg, leak) {
					t.Errorf("message %q carries a path (%s)", msg, leak)
				}
			}
		})
	}

	// Each failure class reads differently, so a log line says which one it was.
	seen := map[string]string{}
	for name, tc := range errs {
		if prev, dup := seen[tc.err.Error()]; dup {
			t.Errorf("%q and %q share the message %q", name, prev, tc.err.Error())
		}
		seen[tc.err.Error()] = name
	}
}
