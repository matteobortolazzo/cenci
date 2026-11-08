package launcher

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// This file implements `cenci open --add-dir` / `cenci audit --add-dir`
// (ticket #737): additional host directories bind-mounted read-write at
// /repos/<slug> next to the primary /workspace repo. resolveAddDirs is the one
// resolution and validation site: resolveLaunchContext (Launch and DryRun) and
// auditWithSecrets (Audit) both call it, so a real launch, its dry-run preview
// and the audit report can never disagree about which directories are mounted
// or which are refused.

// addDirContainerRoot is the container-side parent of every added directory.
// It is deliberately not "/workspace-<slug>": that would share a string prefix
// with /workspace, the sibling-prefix confusion scope_test.go's Repos-evil case
// guards against.
const addDirContainerRoot = "/repos"

// addDirMount is one resolved added directory: the absolute, symlink-resolved
// host path and its /repos/<slug> mount point.
type addDirMount struct {
	Host      string
	Container string
}

// addDirForbidden is how a sensitive-location guard matches a target.
type addDirForbidden int

const (
	// forbidExact refuses a target equal to the guard path.
	forbidExact addDirForbidden = iota
	// forbidCovering refuses a target equal to, or an ancestor of, the guard
	// path (mounting it would expose the guard path).
	forbidCovering
	// forbidOverlap refuses a target equal to, an ancestor of, or inside the
	// guard path.
	forbidOverlap
)

// addDirGuard is one sensitive host location the denylist protects.
type addDirGuard struct {
	path string
	mode addDirForbidden
	why  string
}

// resolveAddDirs validates and resolves raw (the --add-dir values, in command
// line order) against the launch scope and home. Every failure is a usage error
// raised before any runtime call, so a rejected path creates and attaches
// nothing. The result is deduplicated by resolved host path and keeps the
// first-seen order; nil when raw is empty (home and the socket directory are
// then never consulted). When raw is not empty, a non-absolute home or an
// unresolvable daemon socket directory is a usage error: the denylist is never
// evaluated incompletely.
//
// Per path, in this order: empty input; ':' or a non-printable character (it
// would break the -v src:dst syntax or the line-based reuse/observed inspect
// parsing) in the raw and the resolved spelling; existence and directory-ness;
// the sensitive-location denylist; a linked git worktree; overlap with the
// primary workspace; then, across paths, dedupe and /repos/<slug> collision.
// The denylist and the primary-overlap check run on both the as-typed absolute
// path and the symlink-resolved one, and on every spelling of a directory (a
// repeated directory is deduplicated only after it passed); the input-shape,
// existence and worktree checks run on the resolved target. Path comparison is
// always component-wise, never a string prefix.
func resolveAddDirs(raw []string, scope Scope, home string) ([]addDirMount, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// A denylist built from a relative $HOME would compare nothing against the
	// absolute targets, so it fails closed instead of validating against it.
	if !filepath.IsAbs(home) {
		return nil, usageErrorf("--add-dir needs an absolute $HOME to evaluate the sensitive-location denylist, got %q", home)
	}
	guards, err := addDirSensitiveGuards(home)
	if err != nil {
		return nil, err
	}
	primary := addDirGuard{path: scope.WorkspaceBindHost, mode: forbidOverlap}

	var mounts []addDirMount
	owner := map[string]string{}  // /repos/<slug> -> the --add-dir value that claimed it
	hostSeen := map[string]bool{} // resolved host path
	for _, in := range raw {
		resolved, forms, err := resolveAddDirPath(in)
		if err != nil {
			return nil, err
		}
		// Every validation runs on every spelling, before the already-mounted
		// skip below: a second spelling of an accepted directory is checked on
		// its own lexical form too, so acceptance never depends on the order the
		// spellings were passed in.
		for _, g := range guards {
			if g.refuses(forms) {
				return nil, usageErrorf("--add-dir %q is a sensitive host location (%s); it cannot be mounted into the sandbox", in, g.why)
			}
		}
		if err := rejectLinkedWorktree(in, resolved); err != nil {
			return nil, err
		}
		if primary.refuses(forms) {
			return nil, usageErrorf("--add-dir %q overlaps the primary workspace %q, which is already mounted at %s; pass a directory outside it", in, scope.WorkspaceBindHost, workspaceContainer)
		}
		if hostSeen[resolved] {
			continue
		}
		container := addDirContainerRoot + "/" + Slugify(filepath.Base(resolved))
		if first, clash := owner[container]; clash {
			return nil, usageErrorf("--add-dir %q and %q would both mount at %q; rename one directory or pass only one", first, in, container)
		}
		owner[container] = in
		hostSeen[resolved] = true
		mounts = append(mounts, addDirMount{Host: resolved, Container: container})
	}
	return mounts, nil
}

// resolveAddDirPath turns one --add-dir value into its symlink-resolved
// absolute host path (a relative value resolves against the host cwd) plus both
// spellings the denylist compares: the lexical absolute path and the resolved
// one. It enforces the input-shape, existence and directory checks.
func resolveAddDirPath(in string) (resolved string, forms []string, err error) {
	if in == "" {
		return "", nil, usageErrorf("--add-dir %q requires a non-empty path", in)
	}
	if !addDirPathIsPlain(in) {
		return "", nil, addDirCharsError(in)
	}
	abs, err := filepath.Abs(in)
	if err != nil {
		return "", nil, usageErrorf("--add-dir %q cannot be made absolute: %v", in, err)
	}
	resolved, err = filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, usageErrorf("--add-dir %q does not exist", in)
		}
		return "", nil, usageErrorf("--add-dir %q cannot be resolved: %v", in, err)
	}
	if !addDirPathIsPlain(resolved) {
		return "", nil, addDirCharsError(in)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, usageErrorf("--add-dir %q cannot be read: %v", in, err)
	}
	if !info.IsDir() {
		return "", nil, usageErrorf("--add-dir %q is not a directory", in)
	}
	return resolved, uniqueStrings(abs, resolved), nil
}

func addDirCharsError(in string) error {
	return usageErrorf("--add-dir %q contains ':' or a non-printable character, which would break the -v syntax or the container-state parsing", in)
}

// addDirPathIsPlain reports whether p is valid UTF-8 free of ':' and of every
// non-printable rune (control characters, newlines, bidi and format
// characters). Spaces are fine: the path is passed as one argv element.
func addDirPathIsPlain(p string) bool {
	if !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r == ':' || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// rejectLinkedWorktree refuses a directory whose .git entry is a regular file
// (a linked worktree or a submodule): that file holds an absolute host gitdir
// path that does not exist at the translated container path. When git can prove
// the directory is a linked worktree of a normal main checkout, the hint names
// that checkout; otherwise the hint is generic.
func rejectLinkedWorktree(in, resolved string) error {
	info, err := os.Lstat(filepath.Join(resolved, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		// Only "absent" means "no .git": an unreadable entry could be exactly
		// the linked-worktree file this check exists to catch.
		return usageErrorf("--add-dir %q: its .git entry cannot be inspected: %v", in, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if main := mainCheckoutOf(resolved); main != "" {
		return usageErrorf("--add-dir %q is a linked git worktree (its .git file points at a host path that does not exist in the container); pass its main checkout %q instead", in, main)
	}
	return usageErrorf("--add-dir %q is a linked git worktree or submodule (its .git file points at a host path that does not exist in the container); pass the main checkout instead", in)
}

// mainCheckoutOf returns the main checkout of the linked worktree at dir, or ""
// when git cannot prove one: git failed, dir is not a linked worktree (the
// git-dir equals the common dir, as for a submodule), or the common dir is not a
// plain <checkout>/.git.
func mainCheckoutOf(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	lines := splitLines(string(out))
	if len(lines) != 2 || lines[0] == lines[1] || filepath.Base(lines[1]) != ".git" {
		return ""
	}
	return filepath.Dir(lines[1])
}

// addDirSystemDirs are the host system directories no added directory may
// overlap. The agent runs as root with passwordless sudo inside the container,
// so a writable bind of any of them is host-root or host-code-execution reach.
// A guard for a path absent on this host is harmless. /root joins this list
// as a whole only for a home outside /root; for a home inside it /root gets the
// per-home guards instead (see addDirSensitiveGuards). /srv, /mnt and
// /home are deliberately absent: repositories legitimately live there; /var,
// /opt and the tmp directories are guarded only against being mounted whole
// (addDirCoveringDirs) and against their root-run state subdirectories
// (addDirHostStateDirs).
var addDirSystemDirs = []string{
	"/etc", "/boot", "/usr", "/bin", "/sbin", "/lib", "/lib64",
	"/proc", "/sys", "/dev", "/run",
	"/private/etc", "/Library", "/System", // macOS
}

// addDirRootHome is the root user's home; see addDirSensitiveGuards.
const addDirRootHome = "/root"

// addDirCoveringDirs are shared parent directories: mounting one whole (or one
// of its parents) would expose everything in it, but a project directory inside
// one is fine, so each is refused itself and as a parent only. The tmp
// directories hold the host's tmux server sockets, ssh-agent sockets and X11
// sockets, and /var and /opt hold root-run state such as /var/spool and
// /opt/homebrew (guarded below).
var addDirCoveringDirs = []string{"/tmp", "/private/tmp", "/var/tmp", "/var", "/private/var", "/opt"}

// addDirHostStateDirs are the root-run host state directories under the
// covering directories above, refused with everything inside them.
var addDirHostStateDirs = []string{
	"/var/spool", "/var/lib", "/var/db", "/nix", "/opt/homebrew", "/snap",
	"/tmp/.X11-unix", // the X server's sockets
}

// addDirHomeGuards are the guards derived from one home directory: the
// directory itself and its parents, its credential, control and login-run
// directories. addDirSensitiveGuards builds them for $HOME and, when $HOME is
// strictly inside /root, for /root as well.
func addDirHomeGuards(home string) []addDirGuard {
	guards := []addDirGuard{
		{path: home, mode: forbidCovering, why: "your home directory or a parent of it"},
		{path: filepath.Join(home, ".ssh"), mode: forbidOverlap, why: "SSH keys"},
		{path: filepath.Join(home, ".gnupg"), mode: forbidOverlap, why: "GnuPG keys"},
		{path: filepath.Join(home, ".config", "cenci"), mode: forbidOverlap, why: "cenci's fleet config"},
		{path: filepath.Join(home, ".local", "state", "cenci"), mode: forbidOverlap, why: "cenci's state"},
		{path: filepath.Join(home, ".local", "bin"), mode: forbidOverlap, why: "the cenci install link directory"},
		{path: filepath.Join(home, ".docker"), mode: forbidOverlap, why: "the Docker client config and sockets"},
		{path: filepath.Join(home, ".colima"), mode: forbidOverlap, why: "the Colima runtime sockets"},
		{path: filepath.Join(home, ".orbstack"), mode: forbidOverlap, why: "the OrbStack runtime sockets"},
		// The autostart locations below duplicate internal/autostart's path
		// derivation (systemd.go unitPath, launchd.go plistPath) instead of
		// importing it: this package must not depend on the service backends.
		{path: filepath.Join(home, ".config", "systemd"), mode: forbidOverlap, why: "the systemd user units that run at login"},
		{path: filepath.Join(home, ".local", "share", "systemd"), mode: forbidOverlap, why: "the systemd user units that run at login"},
		{path: filepath.Join(home, ".config", "autostart"), mode: forbidOverlap, why: "the autostart entries that run at login"},
		{path: filepath.Join(home, "Library", "LaunchAgents"), mode: forbidOverlap, why: "the launchd agents that run at login"},
	}
	for _, spec := range credentialSourceSpecs {
		guards = append(guards, addDirGuard{
			path: filepath.Dir(spec.pathFrom(home)),
			mode: forbidOverlap,
			why:  "it holds " + spec.typ + " credentials",
		})
	}
	return guards
}

// addDirEnvPath returns the absolute path named by the environment variable
// name, or "" when it is unset or empty. A set-but-relative value is resolved
// against the current directory: ssh and tmux resolve their own relative
// $SSH_AUTH_SOCK and $TMUX_TMPDIR against their working directory, so the guard
// follows the path they would use instead of silently ignoring it.
func addDirEnvPath(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", usageErrorf("--add-dir cannot be validated: $%s=%q cannot be made absolute: %v", name, value, err)
	}
	return abs, nil
}

// addDirSensitiveGuards is the no-override denylist, deliberately not
// exhaustive: the filesystem root and the system directories, $HOME and its
// parents, every directory holding a credential file cenci stages
// (credentialSourceSpecs), ~/.ssh and ~/.gnupg, the container-runtime and
// session-bus socket directories, the host tmux server and ssh-agent socket
// directories (and the shared tmp directories holding them), the directories
// whose contents run at login (cenci's own autostart unit, XDG autostart,
// launchd agents), and cenci's own host control paths (fleet config, state, the
// daemon socket directory and the install link directory). Mounting any of them
// read-write would walk around the narrow, read-only credential staging in
// assembleVolumeMounts, hand the agent a runtime, tmux or agent socket (the
// agent is root with sudo, so a tmux socket is host code execution), let it
// plant code the host runs at the next login, or let it rewrite cenci's own
// configuration and sockets.
//
// It fails closed: when the daemon socket directory cannot be resolved the
// denylist cannot be complete, so the result is a usage error naming the cause.
func addDirSensitiveGuards(home string) ([]addDirGuard, error) {
	uid := strconv.Itoa(os.Getuid())
	guards := []addDirGuard{{path: "/", mode: forbidExact, why: "the filesystem root"}}
	guards = append(guards, addDirHomeGuards(home)...)
	guards = append(guards,
		// The host tmux server's default socket directory; the directory tmux
		// creates under $TMUX_TMPDIR and the directory of the socket named by
		// $TMUX are added below. The guard is kept even when TMUX_TMPDIR
		// relocates the server, since another tmux started without it still uses
		// this one.
		addDirGuard{path: "/tmp/tmux-" + uid, mode: forbidOverlap, why: "the host tmux server's sockets"},
		// emacsclient --eval runs code in the host's emacs server, which listens
		// in /tmp/emacs<uid>.
		addDirGuard{path: "/tmp/emacs" + uid, mode: forbidOverlap, why: "the host emacs server's socket"},
	)
	for _, dir := range addDirSystemDirs {
		guards = append(guards, addDirGuard{path: dir, mode: forbidOverlap, why: "the system directory " + dir})
	}
	// /root is a system directory unless it is the user's own home, or holds it:
	// then every project directory under it would be refused. The rule:
	//   - $HOME outside /root: /root is refused as a whole.
	//   - $HOME is /root: the $HOME guards above protect /root itself and its
	//     credential and control directories; nothing more is needed.
	//   - $HOME strictly inside /root (such as /root/me): the home guards are
	//     built for /root too, so /root, /root/.ssh, /root/.gnupg,
	//     /root/.config/systemd and the rest stay refused while /root/me/code/x
	//     and other non-sensitive /root directories are accepted.
	switch {
	case filepath.Clean(home) == addDirRootHome:
	case pathContains(addDirRootHome, home):
		guards = append(guards, addDirHomeGuards(addDirRootHome)...)
	default:
		guards = append(guards, addDirGuard{path: addDirRootHome, mode: forbidOverlap, why: "the system directory " + addDirRootHome})
	}
	for _, dir := range addDirCoveringDirs {
		guards = append(guards, addDirGuard{path: dir, mode: forbidCovering, why: "the shared directory " + dir + " or a parent of it holds host sockets and root-run state; mount a directory inside it instead"})
	}
	for _, dir := range addDirHostStateDirs {
		guards = append(guards, addDirGuard{path: dir, mode: forbidOverlap, why: "the host state directory " + dir})
	}
	if base := os.Getenv("TMPDIR"); filepath.IsAbs(base) {
		guards = append(guards, addDirGuard{path: base, mode: forbidCovering, why: "your temp directory $TMPDIR or a parent of it; mount a directory inside it instead"})
	}
	// A socket is exposed by mounting its directory or an ancestor of it, so the
	// directory of a socket is guarded as a covering directory: a socket directly
	// in $HOME or /tmp then refuses only that directory and its parents, never
	// every project below. $TMUX_TMPDIR names a base, not a socket: tmux creates
	// its socket directory <base>/tmux-<uid>, which is guarded with everything
	// inside it. $TMUX is set by the tmux server itself with an absolute path, so
	// a relative one is ignored.
	base, err := addDirEnvPath("TMUX_TMPDIR")
	if err != nil {
		return nil, err
	}
	if base != "" {
		guards = append(guards, addDirGuard{path: filepath.Join(base, "tmux-"+uid), mode: forbidOverlap, why: "the host tmux server's socket directory under $TMUX_TMPDIR"})
	}
	if socket, _, _ := strings.Cut(os.Getenv("TMUX"), ","); filepath.IsAbs(socket) {
		guards = append(guards, addDirGuard{path: filepath.Dir(socket), mode: forbidCovering, why: "the socket directory of your running tmux server ($TMUX) or a parent of it; mount a directory inside it instead"})
	}
	socket, err := addDirEnvPath("SSH_AUTH_SOCK")
	if err != nil {
		return nil, err
	}
	if socket != "" {
		guards = append(guards, addDirGuard{path: filepath.Dir(socket), mode: forbidCovering, why: "the directory of your ssh-agent socket ($SSH_AUTH_SOCK) or a parent of it; mount a directory inside it instead"})
	}
	// A relative XDG_* value is ignored on purpose, not by oversight: the XDG
	// Base Directory specification says a relative path is invalid and must be
	// ignored, so the tools that honor these variables never use one. $TMPDIR
	// above follows the same rule.
	if base := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(base) {
		guards = append(guards, addDirGuard{path: base, mode: forbidOverlap, why: "your session runtime directory (agent and bus sockets)"})
	}
	if base := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(base) {
		guards = append(guards,
			addDirGuard{path: filepath.Join(base, "cenci"), mode: forbidOverlap, why: "cenci's fleet config"},
			addDirGuard{path: filepath.Join(base, "systemd"), mode: forbidOverlap, why: "the systemd user units that run at login"},
			addDirGuard{path: filepath.Join(base, "autostart"), mode: forbidOverlap, why: "the autostart entries that run at login"},
		)
	}
	if base := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(base) {
		guards = append(guards, addDirGuard{path: filepath.Join(base, "systemd"), mode: forbidOverlap, why: "the systemd user units that run at login"})
	}
	if base := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(base) {
		guards = append(guards, addDirGuard{path: filepath.Join(base, "cenci"), mode: forbidOverlap, why: "cenci's state"})
	}
	socketDir, err := ipc.DefaultSocketDir()
	if err != nil {
		return nil, usageErrorf("--add-dir cannot be validated: the cenci daemon socket directory cannot be resolved: %v", err)
	}
	guards = append(guards, addDirGuard{path: socketDir, mode: forbidOverlap, why: "cenci's daemon socket directory"})
	return guards, nil
}

// refuses reports whether the guard matches the target: by path string (any
// spelling of the guard, lexical and symlink-resolved, the latter resolved
// through the deepest existing ancestor so an absent ~/.ssh under a symlinked
// $HOME is still found, against any spelling of the target), or by file
// identity, which also catches a spelling no string comparison equates, such as
// ~/.SSH on a case-insensitive filesystem.
func (g addDirGuard) refuses(targetForms []string) bool {
	return g.refusesByPath(targetForms) || g.refusesByIdentity(targetForms)
}

// refusesByIdentity is refuses' file-identity half: the same modes as the
// string comparison, with "equal to" and "inside" decided by os.SameFile.
func (g addDirGuard) refusesByIdentity(targetForms []string) bool {
	for _, tp := range targetForms {
		switch g.mode {
		case forbidExact:
			if sameFile(tp, g.path) {
				return true
			}
		case forbidCovering:
			if identityContains(tp, g.path) {
				return true
			}
		default:
			if identityContains(tp, g.path) || identityContains(g.path, tp) {
				return true
			}
		}
	}
	return false
}

// statIsAbsent reports whether a Stat error means "there is nothing at this
// path": the path or a component does not exist, or a component is not a
// directory. Any other Stat error (permission denied, a symlink loop, an I/O
// error) says nothing about identity, so for a denylist it must not read as "no
// match".
func statIsAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// sameFile reports whether a and b are the same file. A path that is absent
// matches nothing; one whose Stat fails for any other reason counts as a match,
// like pathContains' unknowable comparisons.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return !statIsAbsent(err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		return !statIsAbsent(err)
	}
	return os.SameFile(ai, bi)
}

// identityContains reports whether child, or one of its ancestors, is the same
// file as parent: parent equals or contains child by file identity rather than
// by path string. Absent ancestors (see statIsAbsent) are skipped, so a
// not-yet-created child still matches through its existing parents, and an
// absent parent matches nothing. A Stat error of any other kind on the parent
// or on an ancestor counts as a match: the unreadable path could be exactly the
// one being compared, and a denylist refuses what it cannot compare.
func identityContains(parent, child string) bool {
	pi, err := os.Stat(parent)
	if err != nil {
		return !statIsAbsent(err)
	}
	for cur := filepath.Clean(child); ; cur = filepath.Dir(cur) {
		ci, err := os.Stat(cur)
		switch {
		case err == nil:
			if os.SameFile(pi, ci) {
				return true
			}
		case !statIsAbsent(err):
			return true
		}
		if filepath.Dir(cur) == cur {
			return false
		}
	}
}

func (g addDirGuard) refusesByPath(targetForms []string) bool {
	for _, gp := range uniqueStrings(filepath.Clean(g.path), resolveExistingPrefix(g.path)) {
		for _, tp := range targetForms {
			switch g.mode {
			case forbidExact:
				if tp == gp {
					return true
				}
			case forbidCovering:
				if pathContains(tp, gp) {
					return true
				}
			default:
				if pathContains(tp, gp) || pathContains(gp, tp) {
					return true
				}
			}
		}
	}
	return false
}

// pathContains reports whether child equals parent or sits inside it, compared
// component by component (never as a string prefix, so /a/repo-evil is not
// inside /a/repo). Paths that cannot be related (filepath.Rel fails, as for a
// relative parent against an absolute child) count as contained: for a denylist
// an unknowable comparison must refuse, never pass.
func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err != nil || filepath.IsLocal(rel)
}

// resolveExistingPrefix resolves symlinks in p as far as p exists: the deepest
// existing ancestor is resolved and the not-yet-existing remainder re-joined.
func resolveExistingPrefix(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func uniqueStrings(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// addDirAgentTokens builds the agent flags that make the agent aware of each
// mounted directory: one single token "--add-dir=/repos/<slug>" per directory.
// The single-token form matters: Claude Code's --add-dir takes several values,
// so a two-token "--add-dir /repos/x" would swallow a positional passthrough
// prompt that follows as one more directory.
func addDirAgentTokens(mounts []addDirMount) []string {
	tokens := make([]string, 0, len(mounts))
	for _, m := range mounts {
		tokens = append(tokens, "--add-dir="+m.Container)
	}
	return tokens
}

// observedAddDirs extracts the added-directory mounts of a running container
// from its inspected mounts: every one whose destination is under /repos/.
func observedAddDirs(mounts []reuseMount) []addDirMount {
	var out []addDirMount
	for _, m := range mounts {
		if strings.HasPrefix(m.Destination, addDirContainerRoot+"/") {
			out = append(out, addDirMount{Host: m.Source, Container: m.Destination})
		}
	}
	return out
}

// sameAddDirSet reports whether a running container's /repos/ mounts are
// exactly the requested set, comparing (host source, container destination)
// pairs order-insensitively. A container with no /repos/ mounts has the empty
// set, so a legacy container matches a launch without --add-dir.
func sameAddDirSet(requested []addDirMount, mounts []reuseMount) bool {
	order := func(a, b addDirMount) int {
		return cmp.Or(strings.Compare(a.Host, b.Host), strings.Compare(a.Container, b.Container))
	}
	got := observedAddDirs(mounts)
	want := slices.Clone(requested)
	slices.SortFunc(got, order)
	slices.SortFunc(want, order)
	return slices.Equal(got, want)
}
