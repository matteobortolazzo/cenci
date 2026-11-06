// Package selfexe resolves the cenci binary a long-lived process should
// self-exec (#1372). A daemon started from a plugin cache directory keeps
// running after a plugin refresh deletes that directory, and os.Executable()
// then returns a path that no longer exists. Resolve therefore prefers the
// installed launcher over the process's own path.
//
// Leaf package: it imports only the standard library so internal/daemon and
// internal/reap (which daemon already imports) can both depend on it.
package selfexe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoUsableBinary is wrapped by Resolve's error when no candidate yields an
// absolute, existing, executable path.
var ErrNoUsableBinary = errors.New("no usable cenci binary")

// procSelfLink is the Linux magic link the kernel lets a process exec even
// after its own image file has been deleted.
const procSelfLink = "/proc/self/exe"

// Resolver holds the seams Resolve consults, so tests can inject a home
// directory, PATH lookup, os.Executable result and GOOS without touching the
// ambient process state.
type Resolver struct {
	HomeDir    func() string
	LookPath   func(string) (string, error)
	Executable func() (string, error)
	GOOS       string
	// Lstat is optional; nil means os.Lstat. It gates the /proc/self/exe fallback.
	Lstat func(string) (fs.FileInfo, error)
}

// Default returns a Resolver wired to the real process: $HOME, exec.LookPath,
// os.Executable and runtime.GOOS.
func Default() Resolver {
	return Resolver{
		HomeDir:    func() string { return os.Getenv("HOME") },
		LookPath:   exec.LookPath,
		Executable: os.Executable,
		GOOS:       runtime.GOOS,
	}
}

// Resolve returns the cenci binary the running process should self-exec,
// using the production seams.
func Resolve() (string, error) {
	return Default().Resolve()
}

// Resolve returns an absolute path to the cenci binary to self-exec, trying in
// order: the install link $HOME/.local/bin/cenci (returned as the link path
// itself, which install_on_path re-points on version bumps), an absolute PATH
// lookup, the os.Executable path, and finally /proc/self/exe on Linux. When no
// candidate is usable the error wraps ErrNoUsableBinary and lists every
// candidate tried with the reason it was rejected.
func (r Resolver) Resolve() (string, error) {
	var tried []string

	if home := r.HomeDir(); home == "" || !filepath.IsAbs(home) {
		tried = append(tried, fmt.Sprintf("install link: skipped (HOME %q is empty or not absolute)", home))
	} else {
		link := filepath.Join(home, ".local", "bin", "cenci")
		if err := checkExecRegular(link); err != nil {
			tried = append(tried, fmt.Sprintf("install link %s: %v", link, err))
		} else {
			return link, nil
		}
	}

	if found, err := r.LookPath("cenci"); err != nil {
		tried = append(tried, fmt.Sprintf("LookPath: %v", err))
	} else if !filepath.IsAbs(found) {
		tried = append(tried, fmt.Sprintf("LookPath: %q is not absolute", found))
	} else {
		return found, nil
	}

	if exe, err := r.Executable(); err != nil {
		tried = append(tried, fmt.Sprintf("os.Executable: %v", err))
	} else if err := checkExecRegular(exe); err != nil {
		tried = append(tried, fmt.Sprintf("os.Executable %s: %v", exe, err))
	} else {
		return exe, nil
	}

	if r.GOOS == "linux" {
		lstat := r.Lstat
		if lstat == nil {
			lstat = os.Lstat
		}
		if _, err := lstat(procSelfLink); err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", procSelfLink, err))
		} else {
			return procSelfLink, nil
		}
	}

	return "", fmt.Errorf("%w: tried %s", ErrNoUsableBinary, strings.Join(tried, "; "))
}

// checkExecRegular reports why path is not an executable regular file. os.Stat
// follows a symlink, so a dangling link or a link to a directory is rejected.
func checkExecRegular(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("not executable")
	}
	return nil
}
