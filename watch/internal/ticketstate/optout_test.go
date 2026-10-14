package ticketstate

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeCleanupConfig writes body to <top>/.cenci/config.json and returns top.
func writeCleanupConfig(t *testing.T, body string) string {
	t.Helper()
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, ".cenci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, ".cenci", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return top
}

// TestCleanupEnabledEnabledRows covers every row of the plan's table whose
// result is a clean "enabled" or "disabled" answer, plus the exact-key
// requirement (go-gotchas rule 15): a differently-cased key is not the key.
func TestCleanupEnabledEnabledRows(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"windowCleanup absent", `{"dispatch":{"repos":[]}}`, true},
		{"empty object", `{}`, true},
		{"windowCleanup is an empty object", `{"windowCleanup":{}}`, true},
		{"enabled true", `{"windowCleanup":{"enabled":true}}`, true},
		{"enabled false", `{"windowCleanup":{"enabled":false}}`, false},
		{"enabled false among other keys", `{"planning":{"attended":true},"windowCleanup":{"note":"x","enabled":false}}`, false},
		{"differently-cased block name is not the key", `{"WindowCleanup":{"enabled":false}}`, true},
		{"differently-cased enabled is not the key", `{"windowCleanup":{"Enabled":false}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CleanupEnabled(writeCleanupConfig(t, tc.body))
			if err != nil {
				t.Fatalf("CleanupEnabled: %v", err)
			}
			if got != tc.want {
				t.Errorf("CleanupEnabled(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestCleanupEnabledMissingFileIsEnabled(t *testing.T) {
	t.Run("no .cenci directory", func(t *testing.T) {
		got, err := CleanupEnabled(t.TempDir())
		if err != nil || !got {
			t.Errorf("CleanupEnabled = %v, %v; want true, nil", got, err)
		}
	})
	t.Run(".cenci directory without config.json", func(t *testing.T) {
		top := t.TempDir()
		if err := os.MkdirAll(filepath.Join(top, ".cenci"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := CleanupEnabled(top)
		if err != nil || !got {
			t.Errorf("CleanupEnabled = %v, %v; want true, nil", got, err)
		}
	})
}

// TestCleanupEnabledErrorRows: every unreadable or malformed shape is an error
// (never silently enabled or disabled), and the error names the failing
// element so the classes stay distinguishable (error-handling rule 8).
func TestCleanupEnabledErrorRows(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string // substring the error must carry
	}{
		{"malformed JSON", `{"windowCleanup":`, "config.json"},
		{"empty file", ``, "config.json"},
		{"top level is an array", `[]`, "config.json"},
		{"top level is a string", `"windowCleanup"`, "config.json"},
		{"top level is null", `null`, "config.json"},
		{"windowCleanup is a string", `{"windowCleanup":"off"}`, "windowCleanup"},
		{"windowCleanup is an array", `{"windowCleanup":[]}`, "windowCleanup"},
		{"windowCleanup is a bool", `{"windowCleanup":false}`, "windowCleanup"},
		{"enabled is null", `{"windowCleanup":{"enabled":null}}`, "enabled"},
		{"enabled is a string", `{"windowCleanup":{"enabled":"false"}}`, "enabled"},
		{"enabled is a number", `{"windowCleanup":{"enabled":0}}`, "enabled"},
		{"enabled is an object", `{"windowCleanup":{"enabled":{}}}`, "enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CleanupEnabled(writeCleanupConfig(t, tc.body))
			if err == nil {
				t.Fatalf("CleanupEnabled(%q) = %v, nil; want an error", tc.body, got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

// cleanupEnabledWithin runs CleanupEnabled and fails the test instead of
// hanging when it does not return: a blocking read of a hostile config path is
// the very failure these tests pin.
func cleanupEnabledWithin(t *testing.T, top string) (bool, error) {
	t.Helper()
	type outcome struct {
		enabled bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		enabled, err := CleanupEnabled(top)
		done <- outcome{enabled, err}
	}()
	select {
	case o := <-done:
		return o.enabled, o.err
	case <-time.After(5 * time.Second):
		t.Fatal("CleanupEnabled did not return: it is blocking on the config path")
		return false, nil
	}
}

// TestCleanupEnabledRejectsNonRegularConfig: a committed `.cenci/config.json`
// that is a FIFO, a device symlink or a directory must not be read; it is an
// error so the caller fails open without hanging or exhausting memory.
func TestCleanupEnabledRejectsNonRegularConfig(t *testing.T) {
	newTop := func(t *testing.T) (top, cfg string) {
		t.Helper()
		top = t.TempDir()
		if err := os.MkdirAll(filepath.Join(top, ".cenci"), 0o755); err != nil {
			t.Fatal(err)
		}
		return top, filepath.Join(top, ".cenci", "config.json")
	}

	t.Run("FIFO", func(t *testing.T) {
		top, cfg := newTop(t)
		if err := syscall.Mkfifo(cfg, 0o644); err != nil {
			t.Skipf("cannot create a FIFO here: %v", err)
		}
		got, err := cleanupEnabledWithin(t, top)
		if err == nil {
			t.Fatalf("CleanupEnabled = %v, nil; want an error for a FIFO config", got)
		}
		if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("err = %q, want it to name the config file", err)
		}
	})

	t.Run("symlink to /dev/zero", func(t *testing.T) {
		if _, err := os.Stat("/dev/zero"); err != nil {
			t.Skipf("/dev/zero is unavailable: %v", err)
		}
		top, cfg := newTop(t)
		if err := os.Symlink("/dev/zero", cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cleanupEnabledWithin(t, top)
		if err == nil {
			t.Fatalf("CleanupEnabled = %v, nil; want an error for a device config", got)
		}
		if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("err = %q, want it to name the config file", err)
		}
	})

	t.Run("dangling symlink is a missing file", func(t *testing.T) {
		top, cfg := newTop(t)
		if err := os.Symlink(filepath.Join(top, "nowhere"), cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cleanupEnabledWithin(t, top)
		if err != nil || !got {
			t.Errorf("CleanupEnabled = %v, %v; want true, nil", got, err)
		}
	})
}

// TestCleanupEnabledBoundsConfigSize: a regular file over maxConfigBytes is an
// error even when its content is otherwise a valid config, so the failure is
// provably about size and not parsing.
func TestCleanupEnabledBoundsConfigSize(t *testing.T) {
	padded := func(size int) string {
		const body = `{"windowCleanup":{"enabled":false}}`
		return body + strings.Repeat(" ", size-len(body))
	}

	t.Run("over the cap", func(t *testing.T) {
		got, err := cleanupEnabledWithin(t, writeCleanupConfig(t, padded(maxConfigBytes+1)))
		if err == nil {
			t.Fatalf("CleanupEnabled = %v, nil; want an error for an oversize config", got)
		}
		if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("err = %q, want it to name the config file", err)
		}
	})

	t.Run("exactly at the cap is still read", func(t *testing.T) {
		got, err := cleanupEnabledWithin(t, writeCleanupConfig(t, padded(maxConfigBytes)))
		if err != nil || got {
			t.Errorf("CleanupEnabled = %v, %v; want false, nil", got, err)
		}
	})
}

func TestCleanupEnabledUnreadableFileIsAnError(t *testing.T) {
	// A directory where config.json should be is unreadable on every platform
	// and, unlike a chmod, behaves the same for root and non-root runs.
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, ".cenci", "config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := CleanupEnabled(top)
	if err == nil {
		t.Fatalf("CleanupEnabled = %v, nil; want an error for an unreadable config", got)
	}
	if !strings.Contains(err.Error(), "config.json") {
		t.Errorf("err = %q, want it to name the config file", err)
	}
}

// --- worktreeCleanup block (#1263) ---

var wantDefaultWorktreeCleanup = WorktreeCleanup{Enabled: true, StaleAfter: DefaultStaleAfter}

func TestDefaultStaleAfterIsSevenDays(t *testing.T) {
	if want := 7 * 24 * time.Hour; DefaultStaleAfter != want {
		t.Errorf("DefaultStaleAfter = %v, want %v", DefaultStaleAfter, want)
	}
}

// TestWorktreeCleanupConfigEnabledRows covers every clean answer: absent file,
// block or key means enabled with the default; enabled:false disables; staleAfter
// accepts Go durations and whole days; keys match exactly (go-gotchas rule 15).
func TestWorktreeCleanupConfigEnabledRows(t *testing.T) {
	cases := []struct {
		name string
		body string
		want WorktreeCleanup
	}{
		{"empty object", `{}`, wantDefaultWorktreeCleanup},
		{"block absent", `{"dispatch":{"repos":[]}}`, wantDefaultWorktreeCleanup},
		{"empty block", `{"worktreeCleanup":{}}`, wantDefaultWorktreeCleanup},
		{"enabled true", `{"worktreeCleanup":{"enabled":true}}`, wantDefaultWorktreeCleanup},
		{"enabled false", `{"worktreeCleanup":{"enabled":false}}`, WorktreeCleanup{Enabled: false, StaleAfter: DefaultStaleAfter}},
		{"enabled false among other keys", `{"planning":{"attended":true},"worktreeCleanup":{"note":"x","enabled":false}}`, WorktreeCleanup{Enabled: false, StaleAfter: DefaultStaleAfter}},
		{"staleAfter 7d", `{"worktreeCleanup":{"staleAfter":"7d"}}`, WorktreeCleanup{Enabled: true, StaleAfter: 7 * 24 * time.Hour}},
		{"staleAfter 1d", `{"worktreeCleanup":{"staleAfter":"1d"}}`, WorktreeCleanup{Enabled: true, StaleAfter: 24 * time.Hour}},
		{"staleAfter 30d", `{"worktreeCleanup":{"staleAfter":"30d"}}`, WorktreeCleanup{Enabled: true, StaleAfter: 30 * 24 * time.Hour}},
		{"staleAfter 36h", `{"worktreeCleanup":{"staleAfter":"36h"}}`, WorktreeCleanup{Enabled: true, StaleAfter: 36 * time.Hour}},
		{"staleAfter 90m", `{"worktreeCleanup":{"staleAfter":"90m"}}`, WorktreeCleanup{Enabled: true, StaleAfter: 90 * time.Minute}},
		{"staleAfter 1h30m", `{"worktreeCleanup":{"staleAfter":"1h30m"}}`, WorktreeCleanup{Enabled: true, StaleAfter: 90 * time.Minute}},
		{"enabled false with a staleAfter", `{"worktreeCleanup":{"enabled":false,"staleAfter":"36h"}}`, WorktreeCleanup{Enabled: false, StaleAfter: 36 * time.Hour}},
		{"differently-cased block name is not the key", `{"WorktreeCleanup":{"enabled":false,"staleAfter":"1d"}}`, wantDefaultWorktreeCleanup},
		{"differently-cased enabled is not the key", `{"worktreeCleanup":{"Enabled":false}}`, wantDefaultWorktreeCleanup},
		{"differently-cased staleAfter is not the key", `{"worktreeCleanup":{"StaleAfter":"1d","staleafter":"2d"}}`, wantDefaultWorktreeCleanup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WorktreeCleanupConfig(writeCleanupConfig(t, tc.body))
			if err != nil {
				t.Fatalf("WorktreeCleanupConfig: %v", err)
			}
			if got != tc.want {
				t.Errorf("WorktreeCleanupConfig(%s) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

func TestWorktreeCleanupConfigMissingFileIsEnabledWithDefault(t *testing.T) {
	t.Run("no .cenci directory", func(t *testing.T) {
		got, err := WorktreeCleanupConfig(t.TempDir())
		if err != nil || got != wantDefaultWorktreeCleanup {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", got, err, wantDefaultWorktreeCleanup)
		}
	})
	t.Run(".cenci directory without config.json", func(t *testing.T) {
		top := t.TempDir()
		if err := os.MkdirAll(filepath.Join(top, ".cenci"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := WorktreeCleanupConfig(top)
		if err != nil || got != wantDefaultWorktreeCleanup {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", got, err, wantDefaultWorktreeCleanup)
		}
	})
}

// TestWorktreeCleanupConfigStaleAfterDayCountBoundary: the whole-day form is
// multiplied into a Duration, so the largest count that fits is accepted exactly
// and one more day, or a count too big to parse, is an error rather than a
// wrapped-around (possibly negative or tiny) duration (error-handling rule 15).
func TestWorktreeCleanupConfigStaleAfterDayCountBoundary(t *testing.T) {
	const day = 24 * time.Hour
	maxDays := int64(math.MaxInt64) / int64(day)
	body := func(days string) string { return `{"worktreeCleanup":{"staleAfter":"` + days + `d"}}` }

	t.Run("largest count that fits is accepted", func(t *testing.T) {
		got, err := WorktreeCleanupConfig(writeCleanupConfig(t, body(strconv.FormatInt(maxDays, 10))))
		if err != nil {
			t.Fatalf("WorktreeCleanupConfig: %v", err)
		}
		if want := time.Duration(maxDays) * day; got.StaleAfter != want {
			t.Errorf("StaleAfter = %v, want %v", got.StaleAfter, want)
		}
	})
	for name, days := range map[string]string{
		"one day past the largest count":          strconv.FormatInt(maxDays+1, 10),
		"a count that overflows int64 parsing":    "99999999999999999999",
		"a count that overflows only the product": strconv.FormatInt(math.MaxInt64/2, 10),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := WorktreeCleanupConfig(writeCleanupConfig(t, body(days)))
			if err == nil {
				t.Fatalf("WorktreeCleanupConfig = %+v, nil; want an error for %sd", got, days)
			}
			if !strings.Contains(err.Error(), "worktreeCleanup.staleAfter") {
				t.Errorf("err = %q, want it to name worktreeCleanup.staleAfter", err)
			}
		})
	}
}

// TestWorktreeCleanupConfigErrorRows: every unreadable, malformed, wrongly typed
// or non-positive shape is an error that names the failing element so the
// classes stay distinguishable (error-handling rule 8).
func TestWorktreeCleanupConfigErrorRows(t *testing.T) {
	const (
		block   = "worktreeCleanup"
		enabled = "worktreeCleanup.enabled"
		stale   = "worktreeCleanup.staleAfter"
	)
	cases := []struct {
		name    string
		body    string
		wantMsg string // substring the error must carry
	}{
		{"malformed JSON", `{"worktreeCleanup":`, "config.json"},
		{"empty file", ``, "config.json"},
		{"top level is an array", `[]`, "config.json"},
		{"top level is a string", `"worktreeCleanup"`, "config.json"},
		{"top level is null", `null`, "config.json"},

		{"block is null", `{"worktreeCleanup":null}`, block},
		{"block is a string", `{"worktreeCleanup":"off"}`, block},
		{"block is an array", `{"worktreeCleanup":[]}`, block},
		{"block is a bool", `{"worktreeCleanup":false}`, block},

		{"enabled is null", `{"worktreeCleanup":{"enabled":null}}`, enabled},
		{"enabled is a string", `{"worktreeCleanup":{"enabled":"false"}}`, enabled},
		{"enabled is a number", `{"worktreeCleanup":{"enabled":0}}`, enabled},
		{"enabled is an object", `{"worktreeCleanup":{"enabled":{}}}`, enabled},

		{"staleAfter is null", `{"worktreeCleanup":{"staleAfter":null}}`, stale},
		{"staleAfter is a number", `{"worktreeCleanup":{"staleAfter":7}}`, stale},
		{"staleAfter is the number zero", `{"worktreeCleanup":{"staleAfter":0}}`, stale},
		{"staleAfter is a bool", `{"worktreeCleanup":{"staleAfter":true}}`, stale},
		{"staleAfter is an object", `{"worktreeCleanup":{"staleAfter":{}}}`, stale},
		{"staleAfter is an array", `{"worktreeCleanup":{"staleAfter":["7d"]}}`, stale},
		{"staleAfter is the empty string", `{"worktreeCleanup":{"staleAfter":""}}`, stale},
		{"staleAfter is the string 0", `{"worktreeCleanup":{"staleAfter":"0"}}`, stale},
		{"staleAfter is 0s", `{"worktreeCleanup":{"staleAfter":"0s"}}`, stale},
		{"staleAfter is 0d", `{"worktreeCleanup":{"staleAfter":"0d"}}`, stale},
		{"staleAfter is 000d", `{"worktreeCleanup":{"staleAfter":"000d"}}`, stale},
		{"staleAfter is -1h", `{"worktreeCleanup":{"staleAfter":"-1h"}}`, stale},
		{"staleAfter is -1d", `{"worktreeCleanup":{"staleAfter":"-1d"}}`, stale},
		{"staleAfter is +7d", `{"worktreeCleanup":{"staleAfter":"+7d"}}`, stale},
		{"staleAfter is the string 7 (no unit)", `{"worktreeCleanup":{"staleAfter":"7"}}`, stale},
		{"staleAfter is 7D (wrong case)", `{"worktreeCleanup":{"staleAfter":"7D"}}`, stale},
		{"staleAfter is 1.5d (fractional days)", `{"worktreeCleanup":{"staleAfter":"1.5d"}}`, stale},
		{"staleAfter is 7d12h (days mixed with a duration)", `{"worktreeCleanup":{"staleAfter":"7d12h"}}`, stale},
		{"staleAfter has a leading space", `{"worktreeCleanup":{"staleAfter":" 7d"}}`, stale},
		{"staleAfter has a trailing space", `{"worktreeCleanup":{"staleAfter":"7d "}}`, stale},
		{"staleAfter is d alone", `{"worktreeCleanup":{"staleAfter":"d"}}`, stale},
		{"staleAfter is not a duration", `{"worktreeCleanup":{"staleAfter":"soon"}}`, stale},

		// Every known key is checked even when the block is disabled.
		{"enabled false with a bad staleAfter", `{"worktreeCleanup":{"enabled":false,"staleAfter":"x"}}`, stale},
		{"enabled false with a non-positive staleAfter", `{"worktreeCleanup":{"enabled":false,"staleAfter":"0d"}}`, stale},
		{"enabled false with a null staleAfter", `{"worktreeCleanup":{"enabled":false,"staleAfter":null}}`, stale},
		{"bad enabled beside a valid staleAfter", `{"worktreeCleanup":{"enabled":"no","staleAfter":"7d"}}`, enabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WorktreeCleanupConfig(writeCleanupConfig(t, tc.body))
			if err == nil {
				t.Fatalf("WorktreeCleanupConfig(%q) = %+v, nil; want an error", tc.body, got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestWorktreeCleanupConfigErrorQuotesAndBoundsTheBadValue: a bad staleAfter is
// echoed so the operator can find it, but quoted and length-limited, never raw
// config text spliced into a log line.
func TestWorktreeCleanupConfigErrorQuotesAndBoundsTheBadValue(t *testing.T) {
	t.Run("short value is quoted", func(t *testing.T) {
		_, err := WorktreeCleanupConfig(writeCleanupConfig(t, `{"worktreeCleanup":{"staleAfter":"soon"}}`))
		if err == nil || !strings.Contains(err.Error(), `"soon"`) {
			t.Errorf("err = %v, want it to quote the bad value %q", err, "soon")
		}
	})
	t.Run("newlines in the value do not reach the message", func(t *testing.T) {
		_, err := WorktreeCleanupConfig(writeCleanupConfig(t, `{"worktreeCleanup":{"staleAfter":"7d\nforged: line"}}`))
		if err == nil {
			t.Fatal("WorktreeCleanupConfig succeeded, want an error")
		}
		if strings.ContainsAny(err.Error(), "\r\n") {
			t.Errorf("err = %q, want it on one line", err)
		}
	})
	t.Run("a very long value is truncated", func(t *testing.T) {
		long := strings.Repeat("x", 4096)
		_, err := WorktreeCleanupConfig(writeCleanupConfig(t, `{"worktreeCleanup":{"staleAfter":"`+long+`"}}`))
		if err == nil {
			t.Fatal("WorktreeCleanupConfig succeeded, want an error")
		}
		if strings.Contains(err.Error(), long) {
			t.Errorf("err carries all %d bytes of the bad value, want it length-limited", len(long))
		}
		if !strings.Contains(err.Error(), "worktreeCleanup.staleAfter") {
			t.Errorf("err = %q, want it to name worktreeCleanup.staleAfter", err)
		}
	})
}

// TestConfigBlocksAreIndependent: windowCleanup and worktreeCleanup share one
// reader but never read each other's block, so a setting or a mistake in one does
// not change, or fail, the other.
func TestConfigBlocksAreIndependent(t *testing.T) {
	t.Run("windowCleanup disabled leaves the worktree config at its defaults", func(t *testing.T) {
		got, err := WorktreeCleanupConfig(writeCleanupConfig(t, `{"windowCleanup":{"enabled":false}}`))
		if err != nil || got != wantDefaultWorktreeCleanup {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", got, err, wantDefaultWorktreeCleanup)
		}
	})
	t.Run("worktreeCleanup disabled leaves window cleanup enabled", func(t *testing.T) {
		got, err := CleanupEnabled(writeCleanupConfig(t, `{"worktreeCleanup":{"enabled":false,"staleAfter":"1d"}}`))
		if err != nil || !got {
			t.Errorf("CleanupEnabled = %v, %v; want true, nil", got, err)
		}
	})
	t.Run("both blocks set independently", func(t *testing.T) {
		top := writeCleanupConfig(t, `{"windowCleanup":{"enabled":false},"worktreeCleanup":{"staleAfter":"36h"}}`)
		window, err := CleanupEnabled(top)
		if err != nil || window {
			t.Errorf("CleanupEnabled = %v, %v; want false, nil", window, err)
		}
		worktree, err := WorktreeCleanupConfig(top)
		want := WorktreeCleanup{Enabled: true, StaleAfter: 36 * time.Hour}
		if err != nil || worktree != want {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", worktree, err, want)
		}
	})
	t.Run("a malformed windowCleanup block does not fail the worktree reader", func(t *testing.T) {
		got, err := WorktreeCleanupConfig(writeCleanupConfig(t, `{"windowCleanup":"off","worktreeCleanup":{"staleAfter":"1d"}}`))
		want := WorktreeCleanup{Enabled: true, StaleAfter: 24 * time.Hour}
		if err != nil || got != want {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", got, err, want)
		}
	})
	t.Run("a malformed worktreeCleanup block does not fail the window reader", func(t *testing.T) {
		got, err := CleanupEnabled(writeCleanupConfig(t, `{"worktreeCleanup":{"staleAfter":"soon"},"windowCleanup":{"enabled":false}}`))
		if err != nil || got {
			t.Errorf("CleanupEnabled = %v, %v; want false, nil", got, err)
		}
	})
}

// worktreeCleanupConfigWithin runs WorktreeCleanupConfig and fails the test
// instead of hanging when it does not return: a blocking read of a hostile
// config path is the very failure these tests pin.
func worktreeCleanupConfigWithin(t *testing.T, top string) (WorktreeCleanup, error) {
	t.Helper()
	type outcome struct {
		cfg WorktreeCleanup
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		cfg, err := WorktreeCleanupConfig(top)
		done <- outcome{cfg, err}
	}()
	select {
	case o := <-done:
		return o.cfg, o.err
	case <-time.After(5 * time.Second):
		t.Fatal("WorktreeCleanupConfig did not return: it is blocking on the config path")
		return WorktreeCleanup{}, nil
	}
}

// TestWorktreeCleanupConfigRejectsNonRegularConfig: a committed
// `.cenci/config.json` that is a FIFO, a device symlink or a directory must not
// be read; it is an error so the caller fails open without hanging.
func TestWorktreeCleanupConfigRejectsNonRegularConfig(t *testing.T) {
	newTop := func(t *testing.T) (top, cfg string) {
		t.Helper()
		top = t.TempDir()
		if err := os.MkdirAll(filepath.Join(top, ".cenci"), 0o755); err != nil {
			t.Fatal(err)
		}
		return top, filepath.Join(top, ".cenci", "config.json")
	}
	requireConfigError := func(t *testing.T, got WorktreeCleanup, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("WorktreeCleanupConfig = %+v, nil; want an error", got)
		}
		if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("err = %q, want it to name the config file", err)
		}
	}

	t.Run("FIFO", func(t *testing.T) {
		top, cfg := newTop(t)
		if err := syscall.Mkfifo(cfg, 0o644); err != nil {
			t.Skipf("cannot create a FIFO here: %v", err)
		}
		got, err := worktreeCleanupConfigWithin(t, top)
		requireConfigError(t, got, err)
	})

	t.Run("symlink to /dev/zero", func(t *testing.T) {
		if _, err := os.Stat("/dev/zero"); err != nil {
			t.Skipf("/dev/zero is unavailable: %v", err)
		}
		top, cfg := newTop(t)
		if err := os.Symlink("/dev/zero", cfg); err != nil {
			t.Fatal(err)
		}
		got, err := worktreeCleanupConfigWithin(t, top)
		requireConfigError(t, got, err)
	})

	t.Run("directory at the config path", func(t *testing.T) {
		top, cfg := newTop(t)
		if err := os.Mkdir(cfg, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := worktreeCleanupConfigWithin(t, top)
		requireConfigError(t, got, err)
	})

	t.Run("dangling symlink is a missing file", func(t *testing.T) {
		top, cfg := newTop(t)
		if err := os.Symlink(filepath.Join(top, "nowhere"), cfg); err != nil {
			t.Fatal(err)
		}
		got, err := worktreeCleanupConfigWithin(t, top)
		if err != nil || got != wantDefaultWorktreeCleanup {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", got, err, wantDefaultWorktreeCleanup)
		}
	})
}

// TestWorktreeCleanupConfigBoundsConfigSize: a regular file over maxConfigBytes
// is an error even when its content is otherwise a valid config, so the failure
// is provably about size and not parsing.
func TestWorktreeCleanupConfigBoundsConfigSize(t *testing.T) {
	padded := func(size int) string {
		const body = `{"worktreeCleanup":{"enabled":false}}`
		return body + strings.Repeat(" ", size-len(body))
	}

	t.Run("over the cap", func(t *testing.T) {
		got, err := worktreeCleanupConfigWithin(t, writeCleanupConfig(t, padded(maxConfigBytes+1)))
		if err == nil {
			t.Fatalf("WorktreeCleanupConfig = %+v, nil; want an error for an oversize config", got)
		}
		if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("err = %q, want it to name the config file", err)
		}
	})

	t.Run("exactly at the cap is still read", func(t *testing.T) {
		got, err := worktreeCleanupConfigWithin(t, writeCleanupConfig(t, padded(maxConfigBytes)))
		want := WorktreeCleanup{Enabled: false, StaleAfter: DefaultStaleAfter}
		if err != nil || got != want {
			t.Errorf("WorktreeCleanupConfig = %+v, %v; want %+v, nil", got, err, want)
		}
	})
}
