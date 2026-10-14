package ticketstate

import (
	"os"
	"path/filepath"
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
