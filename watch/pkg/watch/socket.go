package watch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// EventSocketBasename is the file name of the daemon's inbound event socket,
// nested under SocketDir(). It is exported so internal/ipc can build
// DefaultEventSocketPath from the same literal used by the sun_path length
// check in resolveSocketDir, keeping the two from drifting apart.
const EventSocketBasename = "cenci-events.sock"

// sunPathMax is the platform's Unix domain socket path length bound
// (sockaddr_un.sun_path, including the NUL terminator budget accounted for
// by the "< sunPathMax" comparisons below), selected via runtime.GOOS behind
// a package var so tests can pin either platform's bound on any runner —
// mirroring internal/sandbox/launcher/dind.go's dindHostOS precedent. No
// resolveSocketDir code path ever reassigns it.
var sunPathMax = func() int {
	if runtime.GOOS == "darwin" {
		return 104
	}
	return 108
}()

// SocketDirTier identifies which link of the resolution chain produced a
// SocketDirResolution.
type SocketDirTier string

const (
	// TierOverride is the top of the chain: $CENCI_SOCKET_DIR, used verbatim.
	TierOverride SocketDirTier = "override"
	// TierState is the middle tier: $XDG_STATE_HOME/cenci/run (default
	// ~/.local/state/cenci/run).
	TierState SocketDirTier = "state"
	// TierTmp is the final, always-available fallback: /tmp/cenci-<uid>/cenci.
	TierTmp SocketDirTier = "tmp"
)

// SocketDirResolution is the result of walking the three-tier socket
// directory chain: which directory won, which tier produced it, and — for
// the tmp tier — why the higher tiers were skipped or failed.
type SocketDirResolution struct {
	Dir    string
	Tier   SocketDirTier
	Reason string
}

// hardenLeaf applies the same leaf-hardening shape to whichever tier's
// candidate directory is being finalized: create fresh at 0700, reject a
// symlink outright (never follow it), reject a non-directory, and warn
// (without chmod) on a pre-existing directory with loose permissions. This
// is the single home of that shape; every tier's resolution funnels through
// it so the "per behavior per tier" hardening matrix is genuinely shared
// code.
func hardenLeaf(dir string) error {
	info, statErr := os.Lstat(dir)
	switch {
	case os.IsNotExist(statErr):
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	case statErr != nil:
		return statErr
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("socket dir path %q is a symlink; refusing for security", dir)
	case !info.IsDir():
		return fmt.Errorf("socket dir path %q exists but is not a directory", dir)
	default:
		if info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("socket dir %q has insecure permissions %04o (group/other writable); refusing", dir, info.Mode().Perm())
		}
		if info.Mode().Perm()&0077 != 0 {
			log.Printf("warning: socket dir %q has loose permissions %04o (group/other access); expected 0700", dir, info.Mode().Perm())
		}
	}
	return nil
}

// sunPathTooLong reports whether dir, joined with EventSocketBasename, would
// exceed the platform's sun_path bound, and returns the computed length for
// logging/error purposes.
func sunPathTooLong(dir string) (tooLong bool, computedLen int) {
	computedLen = len(dir) + 1 + len(EventSocketBasename)
	return computedLen >= sunPathMax, computedLen
}

// resolveSocketDir walks the three-tier chain and returns the winning
// directory, already hardened at its leaf.
//
// Tier 1 ($CENCI_SOCKET_DIR, used verbatim — no appended segment): if set,
// it must win or the whole resolution hard-errors. A relative path, an
// over-bound path, a symlink, a non-directory, or an uncreatable directory
// all return immediately with a content-specific error — never a silent
// fall-through to tier 2 or tier 3.
//
// Tier 2 ($XDG_STATE_HOME/cenci/run, default ~/.local/state/cenci/run): any
// failure (unresolvable $HOME/$XDG_STATE_HOME, uncreatable state dir,
// over-bound path) is logged with the specific reason and falls through to
// tier 3.
//
// Tier 3 (/tmp/cenci-<uid>/cenci): the final, always-available fallback. Its
// base (/tmp/cenci-<uid>) keeps the pre-existing MkdirAll(0700) +
// unconditional Chmod(0700); only its leaf goes through hardenLeaf.
func resolveSocketDir() (SocketDirResolution, error) {
	if override := os.Getenv("CENCI_SOCKET_DIR"); override != "" {
		if !filepath.IsAbs(override) {
			return SocketDirResolution{}, fmt.Errorf("CENCI_SOCKET_DIR %q must be an absolute path", override)
		}
		if tooLong, n := sunPathTooLong(override); tooLong {
			return SocketDirResolution{}, fmt.Errorf("CENCI_SOCKET_DIR %q is too long for a Unix socket path: %d bytes, want < %d", override, n, sunPathMax)
		}
		if err := hardenLeaf(override); err != nil {
			return SocketDirResolution{}, fmt.Errorf("CENCI_SOCKET_DIR %q is unusable: %w", override, err)
		}
		return SocketDirResolution{Dir: override, Tier: TierOverride}, nil
	}

	dir, reason, err := resolveStateTier()
	if err != nil {
		return SocketDirResolution{}, err
	}
	if dir != "" {
		return SocketDirResolution{Dir: dir, Tier: TierState}, nil
	}

	tmpDir, err := resolveTmpTier()
	if err != nil {
		return SocketDirResolution{}, err
	}
	return SocketDirResolution{Dir: tmpDir, Tier: TierTmp, Reason: reason}, nil
}

// resolveStateTier attempts tier 2 ($XDG_STATE_HOME/cenci/run). A failure to
// resolve or create the state root (unresolvable $HOME/$XDG_STATE_HOME, an
// uncreatable state directory, or an over-bound path) is logged with its
// specific reason (never memoized, so it fires on every call, matching
// today's per-call loose-permission warning) and reported back as
// dir=="", err==nil so the caller falls through to tier 3, carrying the
// reason along for AC #6's daemon warning. A hardenLeaf failure at the leaf
// itself (symlink, non-directory) is a hard error instead — once the state
// root is genuinely reachable, an unexpected object sitting at the socket
// leaf is a security-relevant condition, not a "try somewhere else" one.
func resolveStateTier() (dir, reason string, err error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			reason = "HOME and XDG_STATE_HOME are both unresolvable"
			log.Printf("could not resolve a state directory (%s); falling back to /tmp for the socket dir", reason)
			return "", reason, nil
		}
		base = filepath.Join(home, ".local", "state")
	}

	stateRoot := filepath.Join(base, "cenci")
	leaf := filepath.Join(stateRoot, "run")

	if tooLong, n := sunPathTooLong(leaf); tooLong {
		reason = fmt.Sprintf("state-tier socket dir %q is too long for a Unix socket path: %d bytes, want < %d", leaf, n, sunPathMax)
		log.Printf("warning: %s; falling back to /tmp for the socket dir", reason)
		return "", reason, nil
	}

	if mkErr := os.MkdirAll(stateRoot, 0700); mkErr != nil {
		reason = fmt.Sprintf("could not create state directory %q: %v", stateRoot, mkErr)
		log.Printf("warning: %s; falling back to /tmp for the socket dir", reason)
		return "", reason, nil
	}
	if hErr := hardenLeaf(leaf); hErr != nil {
		return "", "", fmt.Errorf("state-tier socket dir %q is unusable: %w", leaf, hErr)
	}
	return leaf, "", nil
}

// resolveTmpTier is the final, always-available fallback:
// /tmp/cenci-<uid>/cenci. Its base keeps the pre-existing MkdirAll(0700) +
// unconditional Chmod(0700); only the leaf goes through hardenLeaf. As the
// last tier, any hardenLeaf failure here is necessarily a hard error — there
// is nowhere left to fall back to.
func resolveTmpTier() (string, error) {
	base := filepath.Join(os.TempDir(), fmt.Sprintf("cenci-%d", os.Getuid()))
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("could not create tmp socket base %q: %w", base, err)
	}
	if err := os.Chmod(base, 0700); err != nil {
		return "", fmt.Errorf("could not chmod tmp socket base %q: %w", base, err)
	}

	leaf := filepath.Join(base, "cenci")
	if err := hardenLeaf(leaf); err != nil {
		return "", fmt.Errorf("tmp-tier socket dir %q is unusable: %w", leaf, err)
	}
	return leaf, nil
}

// ResolveSocketDir walks the three-tier $CENCI_SOCKET_DIR ->
// $XDG_STATE_HOME/cenci/run -> /tmp/cenci-<uid>/cenci chain and returns the
// winning directory along with which tier produced it and, for the tmp
// tier, why. This is the single home of socket-directory resolution; other
// cenci packages build their socket paths from it (via SocketDir()).
func ResolveSocketDir() (SocketDirResolution, error) {
	return resolveSocketDir()
}

// SocketDir returns the resolved cenci socket directory: the winning tier of
// $CENCI_SOCKET_DIR -> $XDG_STATE_HOME/cenci/run -> /tmp/cenci-<uid>/cenci,
// with 0700 permissions enforced at creation and preserved thereafter. See
// ResolveSocketDir for tier semantics and hardenLeaf for the leaf-hardening
// shape (symlink rejection, non-directory rejection, loose-permission
// warning without chmod on a pre-existing directory). Creation is idempotent
// against a directory that already exists (e.g. a container bind-mount
// pre-created by the host) — permissions are only forced on fresh creation
// so this never fights an already-mounted directory's existing
// ownership/perms.
func SocketDir() (string, error) {
	res, err := resolveSocketDir()
	if err != nil {
		return "", err
	}
	return res.Dir, nil
}

// defaultSocketPath returns a socket path for the given name, nested under
// SocketDir(), with a flat fallback under os.TempDir() if the directory
// cannot be resolved/created.
func defaultSocketPath(name string) string {
	dir, err := SocketDir()
	if err != nil {
		log.Printf("warning: could not create secure socket dir: %v; using fallback path", err)
		return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d.sock", name, os.Getuid()))
	}
	return filepath.Join(dir, name+".sock")
}

// DefaultSocketPath returns the broadcast socket path that subscribers connect
// to with Dial. It resolves to <SocketDir>/cenci.sock, falling back to
// /tmp/cenci-<uid>.sock if the secure directory cannot be created.
func DefaultSocketPath() string { return defaultSocketPath("cenci") }

// DefaultPIDPath returns the path of the daemon's PID file, nested under
// SocketDir() alongside the broadcast/event sockets: <SocketDir>/cenci.pid.
// Falls back to /tmp/cenci-<uid>.pid if the secure directory cannot be
// created, mirroring the other Default*Path fallbacks in this file. The PID
// file records the process ID of the running `cenci daemon start`
// process so `daemon stop`/`daemon status` can locate it without scanning the
// process table.
func DefaultPIDPath() string {
	dir, err := SocketDir()
	if err != nil {
		log.Printf("warning: could not create secure socket dir: %v; using fallback pid path", err)
		return filepath.Join(os.TempDir(), fmt.Sprintf("cenci-%d.pid", os.Getuid()))
	}
	return filepath.Join(dir, "cenci.pid")
}
