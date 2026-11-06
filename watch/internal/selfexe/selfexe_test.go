package selfexe

// Ticket #1372 Phase 3 (red): the resolver's candidate order and edge cases.
//
// Every test builds its own Resolver over temp-dir fixtures and injected
// seams, so none of them reads the developer's ambient HOME, PATH or
// executable (watch/docs/test-isolation.md). The two tests that exercise the
// real exec.LookPath and the package-level Resolve() set PATH/HOME with
// t.Setenv and therefore never call t.Parallel().
//
// Filename note (watch/docs/go-gotchas.md #1094): deliberately no
// *_linux_test.go-shaped name; platform behavior is gated on the Resolver's
// GOOS field and runtime.GOOS checks.

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const procSelfExe = "/proc/self/exe"

// writeExecutable creates an executable regular file named name in dir.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// installLinkPath is where install_on_path maintains the launcher under home.
func installLinkPath(home string) string {
	return filepath.Join(home, ".local", "bin", "cenci")
}

// symlinkInstall creates <home>/.local/bin/cenci as a symlink to target.
func symlinkInstall(t *testing.T, home, target string) string {
	t.Helper()
	link := installLinkPath(home)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// deletedExecutable returns a path that existed as an executable and was then
// removed, the shape os.Executable() reports after a plugin refresh.
func deletedExecutable(t *testing.T) string {
	t.Helper()
	path := writeExecutable(t, t.TempDir(), "cenci-deleted")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// failingResolver is a Resolver whose every candidate fails and whose GOOS is
// not linux; tests override only the seams they exercise.
func failingResolver(home string) Resolver {
	return Resolver{
		HomeDir:    func() string { return home },
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		Executable: func() (string, error) { return "", errors.New("executable unavailable") },
		GOOS:       "darwin",
	}
}

func mustResolve(t *testing.T, r Resolver) string {
	t.Helper()
	got, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want a resolved path", err)
	}
	return got
}

func mustFail(t *testing.T, r Resolver) error {
	t.Helper()
	got, err := r.Resolve()
	if err == nil {
		t.Fatalf("Resolve() = %q, want an error", got)
	}
	if got != "" {
		t.Errorf("Resolve() path = %q alongside an error, want empty", got)
	}
	return err
}

// -- AC1: install link first ---------------------------------------------------

func TestResolve_InstallLinkWinsEvenWhenOtherCandidatesExist(t *testing.T) {
	home := t.TempDir()
	fixture := writeExecutable(t, t.TempDir(), "cenci-installed")
	link := symlinkInstall(t, home, fixture)
	otherExe := writeExecutable(t, t.TempDir(), "cenci-daemon")
	onPath := writeExecutable(t, t.TempDir(), "cenci")

	r := failingResolver(home)
	r.LookPath = func(string) (string, error) { return onPath, nil }
	r.Executable = func() (string, error) { return otherExe, nil }
	r.GOOS = "linux"

	if got := mustResolve(t, r); got != link {
		t.Errorf("Resolve() = %q, want the install link %q itself (not its target %q)", got, link, fixture)
	}
}

func TestResolve_InstallLinkAsRegularExecutableFile(t *testing.T) {
	home := t.TempDir()
	linkDir := filepath.Dir(installLinkPath(home))
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := writeExecutable(t, linkDir, "cenci")

	if got := mustResolve(t, failingResolver(home)); got != link {
		t.Errorf("Resolve() = %q, want %q", got, link)
	}
}

// -- AC2: absolute LookPath when the link is missing ---------------------------

func TestResolve_LookPathUsedWhenInstallLinkMissing(t *testing.T) {
	pathDir := t.TempDir()
	want := writeExecutable(t, pathDir, "cenci")
	t.Setenv("PATH", pathDir)
	otherExe := writeExecutable(t, t.TempDir(), "cenci-daemon")

	r := failingResolver(t.TempDir())
	r.LookPath = exec.LookPath
	r.Executable = func() (string, error) { return otherExe, nil }

	if got := mustResolve(t, r); got != want {
		t.Errorf("Resolve() = %q, want the PATH hit %q ahead of os.Executable %q", got, want, otherExe)
	}
}

// -- AC3: /proc/self/exe on Linux ----------------------------------------------

func TestResolve_ProcSelfExeWhenExecutableDeleted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/exe is only resolvable on Linux")
	}
	cases := []struct {
		name       string
		executable func() (string, error)
	}{
		{"executable path deleted", func() (string, error) { return deletedExecutable(t), nil }},
		{"executable errors", func() (string, error) { return "", errors.New("readlink failed") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := failingResolver(t.TempDir())
			r.Executable = tc.executable
			r.GOOS = "linux"

			if got := mustResolve(t, r); got != procSelfExe {
				t.Errorf("Resolve() = %q, want %q", got, procSelfExe)
			}
		})
	}
}

// -- AC4: the existing os.Executable path --------------------------------------

func TestResolve_ExecutableUsedWhenItExistsAndOthersFail(t *testing.T) {
	exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
	r := failingResolver(t.TempDir())
	r.Executable = func() (string, error) { return exe, nil }
	r.GOOS = "linux"

	if got := mustResolve(t, r); got != exe {
		t.Errorf("Resolve() = %q, want the existing os.Executable path %q (not %s)", got, exe, procSelfExe)
	}
}

// -- AC5: nothing usable -------------------------------------------------------

func TestResolve_NoUsableCandidateListsWhatWasTried(t *testing.T) {
	home := t.TempDir()
	r := failingResolver(home)
	r.Executable = func() (string, error) { return deletedExecutable(t), nil }

	err := mustFail(t, r)

	if !errors.Is(err, ErrNoUsableBinary) {
		t.Errorf("errors.Is(err, ErrNoUsableBinary) = false for %v", err)
	}
	if !strings.Contains(err.Error(), "no usable cenci binary") {
		t.Errorf("error %q, want the substring %q", err, "no usable cenci binary")
	}
	for _, label := range []string{installLinkPath(home), "LookPath", "os.Executable"} {
		if !strings.Contains(err.Error(), label) {
			t.Errorf("error %q does not list the tried candidate %q", err, label)
		}
	}
	if strings.Contains(err.Error(), procSelfExe) {
		t.Errorf("error %q lists %s although GOOS is not linux, so it was never tried", err, procSelfExe)
	}
}

// -- Edge cases ----------------------------------------------------------------

func TestResolve_DanglingInstallLinkIsSkipped(t *testing.T) {
	home := t.TempDir()
	symlinkInstall(t, home, filepath.Join(t.TempDir(), "deleted-cache", "bin", "cenci"))
	exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
	r := failingResolver(home)
	r.Executable = func() (string, error) { return exe, nil }

	if got := mustResolve(t, r); got != exe {
		t.Errorf("Resolve() = %q, want the next candidate %q", got, exe)
	}
}

func TestResolve_InstallLinkToDirectoryIsSkipped(t *testing.T) {
	home := t.TempDir()
	symlinkInstall(t, home, t.TempDir())
	exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
	r := failingResolver(home)
	r.Executable = func() (string, error) { return exe, nil }

	if got := mustResolve(t, r); got != exe {
		t.Errorf("Resolve() = %q, want the next candidate %q", got, exe)
	}
}

func TestResolve_InstallPathThatIsADirectoryIsSkipped(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(installLinkPath(home), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
	r := failingResolver(home)
	r.Executable = func() (string, error) { return exe, nil }

	if got := mustResolve(t, r); got != exe {
		t.Errorf("Resolve() = %q, want the next candidate %q", got, exe)
	}
}

func TestResolve_NonExecutableCandidatesAreSkipped(t *testing.T) {
	t.Run("install link to a non-executable file", func(t *testing.T) {
		home := t.TempDir()
		plain := filepath.Join(t.TempDir(), "cenci-plain")
		if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		symlinkInstall(t, home, plain)
		exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
		r := failingResolver(home)
		r.Executable = func() (string, error) { return exe, nil }

		if got := mustResolve(t, r); got != exe {
			t.Errorf("Resolve() = %q, want the next candidate %q", got, exe)
		}
	})

	t.Run("install path is a non-executable regular file", func(t *testing.T) {
		home := t.TempDir()
		link := installLinkPath(home)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(link, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
		r := failingResolver(home)
		r.Executable = func() (string, error) { return exe, nil }

		if got := mustResolve(t, r); got != exe {
			t.Errorf("Resolve() = %q, want the next candidate %q", got, exe)
		}
	})

	t.Run("os.Executable path is a non-executable file", func(t *testing.T) {
		plain := filepath.Join(t.TempDir(), "cenci-plain")
		if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := failingResolver(t.TempDir())
		r.Executable = func() (string, error) { return plain, nil }

		mustFail(t, r)
	})

	t.Run("os.Executable path is a directory", func(t *testing.T) {
		r := failingResolver(t.TempDir())
		dir := t.TempDir()
		r.Executable = func() (string, error) { return dir, nil }

		mustFail(t, r)
	})
}

func TestResolve_EmptyHomeSkipsInstallLinkEntirely(t *testing.T) {
	exe := writeExecutable(t, t.TempDir(), "cenci-daemon")

	t.Run("falls through to the next candidate", func(t *testing.T) {
		r := failingResolver("")
		r.Executable = func() (string, error) { return exe, nil }

		got := mustResolve(t, r)
		if got != exe {
			t.Errorf("Resolve() = %q, want %q", got, exe)
		}
		if strings.HasPrefix(got, "/.local/bin") {
			t.Errorf("Resolve() = %q, want no root-relative install link", got)
		}
	})

	t.Run("nothing else usable is an error, never a root-relative path", func(t *testing.T) {
		err := mustFail(t, failingResolver(""))
		if !errors.Is(err, ErrNoUsableBinary) {
			t.Errorf("errors.Is(err, ErrNoUsableBinary) = false for %v", err)
		}
		if !strings.Contains(err.Error(), "install link: skipped") {
			t.Errorf("error %q, want the install link recorded as skipped", err)
		}
		if strings.Contains(err.Error(), ".local/bin/cenci") {
			t.Errorf("error %q names an install-link path, want none probed when HOME is empty", err)
		}
	})

	t.Run("no install-relative path is probed from the working directory", func(t *testing.T) {
		// With the skip removed, filepath.Join("", ".local", "bin", "cenci")
		// collapses to the relative ".local/bin/cenci" and would be stat'ed
		// against the cwd, where this fixture is a valid executable.
		cwd := t.TempDir()
		t.Chdir(cwd)
		probeDir := filepath.Join(".local", "bin")
		if err := os.MkdirAll(probeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, probeDir, "cenci")
		r := failingResolver("")
		r.Executable = func() (string, error) { return exe, nil }

		if got := mustResolve(t, r); got != exe {
			t.Errorf("Resolve() = %q, want %q -- an empty HOME must not probe a cwd-relative install link", got, exe)
		}
	})
}

func TestResolve_RelativeHomeIsSkippedLikeEmpty(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	relLink := filepath.Join("home", ".local", "bin")
	if err := os.MkdirAll(relLink, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, relLink, "cenci")
	exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
	r := failingResolver("home")
	r.Executable = func() (string, error) { return exe, nil }

	got := mustResolve(t, r)

	if got != exe {
		t.Errorf("Resolve() = %q, want %q -- a relative HOME must not yield a relative install link", got, exe)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("Resolve() = %q, want an absolute path", got)
	}
}

func TestResolve_RejectsUntrustedLookPathResults(t *testing.T) {
	absolute := writeExecutable(t, t.TempDir(), "cenci")
	cases := []struct {
		name string
		path string
		err  error
	}{
		{"relative PATH entry (ErrDot)", "./cenci", exec.ErrDot},
		{"non-absolute result without error", "cenci", nil},
		{"absolute result alongside ErrDot", absolute, exec.ErrDot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe := writeExecutable(t, t.TempDir(), "cenci-daemon")
			r := failingResolver(t.TempDir())
			r.LookPath = func(string) (string, error) { return tc.path, tc.err }
			r.Executable = func() (string, error) { return exe, nil }

			if got := mustResolve(t, r); got != exe {
				t.Errorf("Resolve() = %q, want the LookPath result %q rejected in favor of %q", got, tc.path, exe)
			}
		})
	}
}

func TestResolve_NonLinuxNeverReturnsProcSelfExe(t *testing.T) {
	for _, goos := range []string{"darwin", "freebsd", "windows"} {
		t.Run(goos, func(t *testing.T) {
			r := failingResolver(t.TempDir())
			r.Executable = func() (string, error) { return deletedExecutable(t), nil }
			r.GOOS = goos

			err := mustFail(t, r)

			if !errors.Is(err, ErrNoUsableBinary) {
				t.Errorf("errors.Is(err, ErrNoUsableBinary) = false for %v", err)
			}
		})
	}
}

func TestResolve_LinuxWithoutProcfsFallsThroughToTheError(t *testing.T) {
	lstatErr := errors.New("no such file or directory")
	var lstatPaths []string
	r := failingResolver(t.TempDir())
	r.Executable = func() (string, error) { return deletedExecutable(t), nil }
	r.GOOS = "linux"
	r.Lstat = func(path string) (fs.FileInfo, error) {
		lstatPaths = append(lstatPaths, path)
		return nil, lstatErr
	}

	err := mustFail(t, r)

	if !errors.Is(err, ErrNoUsableBinary) {
		t.Errorf("errors.Is(err, ErrNoUsableBinary) = false for %v", err)
	}
	if len(lstatPaths) != 1 || lstatPaths[0] != procSelfExe {
		t.Errorf("Lstat calls = %q, want exactly one for %q", lstatPaths, procSelfExe)
	}
	if !strings.Contains(err.Error(), procSelfExe) || !strings.Contains(err.Error(), lstatErr.Error()) {
		t.Errorf("error %q, want it to list %s with the Lstat failure %q", err, procSelfExe, lstatErr)
	}
}

// -- Production wiring ---------------------------------------------------------

func TestResolve_PackageLevelUsesTheProcessHomeAndInstallLink(t *testing.T) {
	home := t.TempDir()
	fixture := writeExecutable(t, t.TempDir(), "cenci-installed")
	link := symlinkInstall(t, home, fixture)
	t.Setenv("HOME", home)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the install link under the process HOME", err)
	}
	if got != link {
		t.Errorf("Resolve() = %q, want %q", got, link)
	}
}

func TestDefault_WiresTheRealSeams(t *testing.T) {
	r := Default()

	if r.HomeDir == nil || r.LookPath == nil || r.Executable == nil {
		t.Fatalf("Default() = %+v, want every seam populated", r)
	}
	if r.GOOS != runtime.GOOS {
		t.Errorf("Default().GOOS = %q, want %q", r.GOOS, runtime.GOOS)
	}
}
