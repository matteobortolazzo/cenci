package watch

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog redirects the standard logger's output into a buffer for the
// duration of the test and restores the original output in t.Cleanup. Not
// safe to run in parallel with other tests that also mutate log output (this
// package already forces serial execution via t.Setenv elsewhere).
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

func TestSecureSocketDir_UsesXDGWhenSet(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}
	if got != xdgDir {
		t.Errorf("expected %q, got %q", xdgDir, got)
	}
}

func TestSecureSocketDir_CreatesTmpDirWhenNoXDG(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}

	expectedSuffix := fmt.Sprintf("agentwatch-%d", os.Getuid())
	if !strings.HasSuffix(got, expectedSuffix) {
		t.Errorf("expected path ending with %q, got %q", expectedSuffix, got)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) error: %v", got, err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got file")
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected permissions 0700, got %04o", perm)
	}
}

func TestSecureSocketDir_ValidatesExistingDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	// Create the directory with wrong permissions before calling secureSocketDir.
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("agentwatch-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0777); err != nil {
		t.Fatal(err)
	}

	// Override TMP so secureSocketDir uses our test directory's parent.
	t.Setenv("TMPDIR", filepath.Dir(dir))

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}
	if got != dir {
		t.Errorf("expected %q, got %q", dir, got)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) error: %v", got, err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected permissions fixed to 0700, got %04o", perm)
	}
}

func TestSecureSocketDir_RejectsInsecureXDG(t *testing.T) {
	// Create a directory with world-writable permissions.
	insecureDir := filepath.Join(t.TempDir(), "insecure-xdg")
	if err := os.Mkdir(insecureDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Chmod bypasses umask, ensuring the directory is actually world-writable.
	if err := os.Chmod(insecureDir, 0777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", insecureDir)

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}
	// Should NOT return the insecure XDG dir; should fall through to /tmp.
	if got == insecureDir {
		t.Errorf("secureSocketDir() returned insecure XDG dir %q; expected /tmp fallback", insecureDir)
	}
}

// TestDefaultSocketPath_ResolvesUnderNestedSocketDir replaces the former
// TestDefaultSocketPath_UsesSecureDir: defaultSocketPath must route through the
// nested SocketDir() (not secureSocketDir() directly), so agentwatch.sock lands
// in the same dedicated agentwatch/ subdirectory as the events socket (#217).
func TestDefaultSocketPath_ResolvesUnderNestedSocketDir(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got := DefaultSocketPath()

	// Deliberately built from primitives (not from a live SocketDir() call):
	// once SocketDir() is nested but defaultSocketPath is NOT yet rerouted to
	// build on it, comparing against a live SocketDir() call would coincidentally
	// still pass. This pins the actual nested-path shape from the AC.
	want := filepath.Join(xdgDir, "agentwatch", "agentwatch.sock")
	if got != want {
		t.Errorf("DefaultSocketPath() = %q, want %q (must resolve under the nested SocketDir(), not the flat secureSocketDir())", got, want)
	}
}

// TestSocketDir_NestsAgentwatchSubdirUnderXDG covers the AC: when
// $XDG_RUNTIME_DIR is valid, SocketDir() must return a dedicated agentwatch/
// subdirectory nested under it (not the shared XDG dir itself, which also
// holds wayland/pulse/keyring sockets), created with 0700 if missing.
func TestSocketDir_NestsAgentwatchSubdirUnderXDG(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir() error: %v", err)
	}

	want := filepath.Join(xdgDir, "agentwatch")
	if got != want {
		t.Errorf("SocketDir() = %q, want %q (nested agentwatch/ subdir under XDG_RUNTIME_DIR)", got, want)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) error: %v", got, err)
	}
	if !info.IsDir() {
		t.Fatalf("SocketDir() %q is not a directory", got)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("SocketDir() dir permissions = %04o, want 0700", perm)
	}
}

// TestSocketDir_NestsAgentwatchSubdirUnderTmpFallback covers the AC: the /tmp
// fallback branch must nest agentwatch/ one level deeper than today's flat
// /tmp/agentwatch-<uid>/ directory, created with 0700 if missing.
func TestSocketDir_NestsAgentwatchSubdirUnderTmpFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	got, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir() error: %v", err)
	}

	want := filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()), "agentwatch")
	if got != want {
		t.Errorf("SocketDir() = %q, want %q (nested one level deeper than the /tmp fallback dir)", got, want)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) error: %v", got, err)
	}
	if !info.IsDir() {
		t.Fatalf("SocketDir() %q is not a directory", got)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("SocketDir() dir permissions = %04o, want 0700", perm)
	}
}

// TestSocketDir_IdempotentAgainstPreCreatedDir covers the container bind-mount
// case called out in the plan's Risks section: a container may pre-create the
// nested agentwatch/ mountpoint (with host ownership) before agentwatch itself
// ever runs. SocketDir() must not error against that, and repeated calls must
// keep resolving to the same path without error.
func TestSocketDir_IdempotentAgainstPreCreatedDir(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (parentOfNestedDir string)
	}{
		{
			name: "xdg branch",
			setup: func(t *testing.T) string {
				xdgDir := t.TempDir()
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
				return xdgDir
			},
		},
		{
			name: "tmp fallback branch",
			setup: func(t *testing.T) string {
				t.Setenv("XDG_RUNTIME_DIR", "")
				tmpRoot := t.TempDir()
				t.Setenv("TMPDIR", tmpRoot)
				return filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.setup(t)

			// Simulate a container bind-mounting the nested dir before
			// agentwatch ever runs (host pre-creates the mountpoint).
			preCreated := filepath.Join(parent, "agentwatch")
			if err := os.MkdirAll(preCreated, 0700); err != nil {
				t.Fatalf("pre-creating %q: %v", preCreated, err)
			}

			got, err := SocketDir()
			if err != nil {
				t.Fatalf("SocketDir() must not error against a pre-existing dir: %v", err)
			}
			if got != preCreated {
				t.Errorf("SocketDir() = %q, want %q", got, preCreated)
			}

			// A second call must be idempotent: same path, no error.
			second, err := SocketDir()
			if err != nil {
				t.Fatalf("second SocketDir() call error (must be idempotent): %v", err)
			}
			if second != preCreated {
				t.Errorf("second SocketDir() call = %q, want %q", second, preCreated)
			}
		})
	}
}

// TestSocketDir_ErrorsWhenNestedPathIsFile covers the review-flagged edge
// case: if a plain file (not a directory) already occupies the nested
// agentwatch/ path, SocketDir() must return a clear error itself rather than
// silently succeeding and letting socket creation fail later with a
// confusing "not a directory" error.
func TestSocketDir_ErrorsWhenNestedPathIsFile(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (parentOfNestedDir string)
	}{
		{
			name: "xdg branch",
			setup: func(t *testing.T) string {
				xdgDir := t.TempDir()
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
				return xdgDir
			},
		},
		{
			name: "tmp fallback branch",
			setup: func(t *testing.T) string {
				t.Setenv("XDG_RUNTIME_DIR", "")
				tmpRoot := t.TempDir()
				t.Setenv("TMPDIR", tmpRoot)
				return filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.setup(t)

			// Pre-create the /tmp-fallback branch's parent dir so writing the
			// plain file at the nested path succeeds even when the parent
			// itself doesn't exist yet.
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatalf("pre-creating parent %q: %v", parent, err)
			}

			// Simulate a plain file already occupying the nested agentwatch/
			// path (instead of a pre-created directory).
			preCreatedFile := filepath.Join(parent, "agentwatch")
			if err := os.WriteFile(preCreatedFile, []byte("not a directory"), 0600); err != nil {
				t.Fatalf("pre-creating file %q: %v", preCreatedFile, err)
			}

			_, err := SocketDir()
			if err == nil {
				t.Fatalf("SocketDir() error = nil, want error when a plain file occupies the nested path")
			}
		})
	}
}

// TestSocketDir_RejectsSymlinkAtNestedPath covers the symlink-hygiene gap
// (#224): if the nested agentwatch/ path is a symlink, SocketDir() must
// refuse to follow it and must return a clear error, leaving the symlink
// itself untouched.
func TestSocketDir_RejectsSymlinkAtNestedPath(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (parentOfNestedDir string)
	}{
		{
			name: "xdg branch",
			setup: func(t *testing.T) string {
				xdgDir := t.TempDir()
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
				return xdgDir
			},
		},
		{
			name: "tmp fallback branch",
			setup: func(t *testing.T) string {
				t.Setenv("XDG_RUNTIME_DIR", "")
				tmpRoot := t.TempDir()
				t.Setenv("TMPDIR", tmpRoot)
				return filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.setup(t)

			// Pre-create the /tmp-fallback branch's parent dir so planting the
			// symlink at the nested path succeeds even when the parent itself
			// doesn't exist yet.
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatalf("pre-creating parent %q: %v", parent, err)
			}

			targetDir := t.TempDir()
			nested := filepath.Join(parent, "agentwatch")
			if err := os.Symlink(targetDir, nested); err != nil {
				t.Fatalf("planting symlink %q -> %q: %v", nested, targetDir, err)
			}

			_, err := SocketDir()
			if err == nil {
				t.Fatalf("SocketDir() error = nil, want error when nested path is a symlink")
			}
			if !strings.Contains(err.Error(), "symlink") {
				t.Errorf("SocketDir() error = %q, want error mentioning %q", err.Error(), "symlink")
			}

			// The symlink itself must not have been followed or replaced.
			linkInfo, lerr := os.Lstat(nested)
			if lerr != nil {
				t.Fatalf("Lstat(%q) error: %v", nested, lerr)
			}
			if linkInfo.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("expected %q to still be a symlink, got mode %v", nested, linkInfo.Mode())
			}
			gotTarget, rerr := os.Readlink(nested)
			if rerr != nil {
				t.Fatalf("Readlink(%q) error: %v", nested, rerr)
			}
			if gotTarget != targetDir {
				t.Errorf("symlink target changed: got %q, want %q", gotTarget, targetDir)
			}
		})
	}
}

// TestSocketDir_WarnsOnLoosePermissions covers the silent loose-permission
// gap (#224): when a pre-existing nested agentwatch/ dir is group/world
// accessible, SocketDir() must still succeed (idempotent, no chmod fight)
// but must log a non-fatal warning.
func TestSocketDir_WarnsOnLoosePermissions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (parentOfNestedDir string)
	}{
		{
			name: "xdg branch",
			setup: func(t *testing.T) string {
				xdgDir := t.TempDir()
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
				return xdgDir
			},
		},
		{
			name: "tmp fallback branch",
			setup: func(t *testing.T) string {
				t.Setenv("XDG_RUNTIME_DIR", "")
				tmpRoot := t.TempDir()
				t.Setenv("TMPDIR", tmpRoot)
				return filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.setup(t)

			nested := filepath.Join(parent, "agentwatch")
			if err := os.MkdirAll(nested, 0700); err != nil {
				t.Fatalf("pre-creating nested dir %q: %v", nested, err)
			}
			// Chmod bypasses umask, ensuring the dir is actually group/world
			// accessible regardless of the process umask.
			if err := os.Chmod(nested, 0755); err != nil {
				t.Fatalf("chmod %q: %v", nested, err)
			}

			logBuf := captureLog(t)

			got, err := SocketDir()
			if err != nil {
				t.Fatalf("SocketDir() error: %v", err)
			}
			if got != nested {
				t.Errorf("SocketDir() = %q, want %q", got, nested)
			}

			if !strings.Contains(strings.ToLower(logBuf.String()), "warning") {
				t.Errorf("expected a warning to be logged for loose permissions on %q, got log output: %q", nested, logBuf.String())
			}
		})
	}
}

// TestSocketDir_NoWarnOnStrictPermissions covers the flip side of #224: a
// pre-existing nested agentwatch/ dir that is already 0700 or stricter must
// not trigger any warning.
func TestSocketDir_NoWarnOnStrictPermissions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (parentOfNestedDir string)
	}{
		{
			name: "xdg branch",
			setup: func(t *testing.T) string {
				xdgDir := t.TempDir()
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
				return xdgDir
			},
		},
		{
			name: "tmp fallback branch",
			setup: func(t *testing.T) string {
				t.Setenv("XDG_RUNTIME_DIR", "")
				tmpRoot := t.TempDir()
				t.Setenv("TMPDIR", tmpRoot)
				return filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.setup(t)

			nested := filepath.Join(parent, "agentwatch")
			if err := os.MkdirAll(nested, 0700); err != nil {
				t.Fatalf("pre-creating nested dir %q: %v", nested, err)
			}
			// Chmod explicitly (bypassing umask) so the dir is provably 0700
			// regardless of the process umask.
			if err := os.Chmod(nested, 0700); err != nil {
				t.Fatalf("chmod %q: %v", nested, err)
			}

			logBuf := captureLog(t)

			got, err := SocketDir()
			if err != nil {
				t.Fatalf("SocketDir() error: %v", err)
			}
			if got != nested {
				t.Errorf("SocketDir() = %q, want %q", got, nested)
			}

			if strings.Contains(strings.ToLower(logBuf.String()), "warning") {
				t.Errorf("expected no warning for strict permissions on %q, got log output: %q", nested, logBuf.String())
			}
		})
	}
}

// TestSocketNames_ShareSocketDirParent guards the risk called out in the
// plan: if defaultSocketPath isn't rerouted through SocketDir(), the
// broadcast and events sockets would split across two different
// directories. Both must resolve inside whichever directory SocketDir()
// returns, in both the XDG and /tmp-fallback branches.
func TestSocketNames_ShareSocketDirParent(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (wantParent string)
	}{
		{
			name: "xdg branch",
			setup: func(t *testing.T) string {
				xdgDir := t.TempDir()
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
				return filepath.Join(xdgDir, "agentwatch")
			},
		},
		{
			name: "tmp fallback branch",
			setup: func(t *testing.T) string {
				t.Setenv("XDG_RUNTIME_DIR", "")
				tmpRoot := t.TempDir()
				t.Setenv("TMPDIR", tmpRoot)
				return filepath.Join(tmpRoot, fmt.Sprintf("agentwatch-%d", os.Getuid()), "agentwatch")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// wantParent is built from primitives (not a live SocketDir()
			// call): pins the actual nested-path shape from the AC, so this
			// stays red until both SocketDir() nests AND defaultSocketPath is
			// rerouted through it (the exact split-sockets risk from the plan).
			wantParent := tt.setup(t)

			broadcast := DefaultSocketPath()
			events := defaultSocketPath("agentwatch-events")

			if filepath.Dir(broadcast) != wantParent {
				t.Errorf("agentwatch.sock parent = %q, want %q", filepath.Dir(broadcast), wantParent)
			}
			if filepath.Dir(events) != wantParent {
				t.Errorf("agentwatch-events.sock parent = %q, want %q", filepath.Dir(events), wantParent)
			}
			if filepath.Dir(broadcast) != filepath.Dir(events) {
				t.Errorf("agentwatch.sock and agentwatch-events.sock must share a parent dir; got %q vs %q", filepath.Dir(broadcast), filepath.Dir(events))
			}
		})
	}
}
