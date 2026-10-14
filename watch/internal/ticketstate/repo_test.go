package ticketstate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

const leakToken = "s3cr3tTOKEN"

// isolateGit keeps real-git fixtures independent of the developer's gitconfig.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func runRealGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// newGitRepo creates a real repository in a temp dir, with an origin remote
// when origin is non-empty, and returns its (not symlink-resolved) path.
func newGitRepo(t *testing.T, origin string) string {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	runRealGit(t, dir, "init", "-q")
	if origin != "" {
		runRealGit(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// installRecordingGit puts a git shim first on PATH that logs each call's argv
// and then execs the real git, and returns a func reporting the call count.
func installRecordingGit(t *testing.T) (calls func() int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required for the repo-resolver tests: %v", err)
	}
	dir := installShim(t, "git", `dir=$(dirname "$0")
printf '%s\n' "$*" >> "$dir/calls.log"
exec `+exectest.ShellQuote(realGit)+` "$@"`)
	return func() int {
		data, err := os.ReadFile(filepath.Join(dir, "calls.log"))
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
}

func TestResolveMapsSubdirectoryToToplevelAndOriginRepo(t *testing.T) {
	top := newGitRepo(t, "https://github.com/acme/widgets.git")
	sub := filepath.Join(top, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := (&Resolver{}).Resolve(context.Background(), sub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := Identity{Toplevel: realPath(t, top), Repo: Repo{Owner: "acme", Name: "widgets"}}
	if got != want {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
}

func TestResolveOriginShapes(t *testing.T) {
	cases := []struct {
		name   string
		origin string
	}{
		{"https with .git", "https://github.com/acme/widgets.git"},
		{"https without .git", "https://github.com/acme/widgets"},
		{"https with trailing slash", "https://github.com/acme/widgets/"},
		{"https with embedded credentials", "https://user:" + leakToken + "@github.com/acme/widgets.git"},
		{"ssh url", "ssh://git@github.com/acme/widgets.git"},
		{"scp style", "git@github.com:acme/widgets.git"},
		{"scp style via an SSH host alias", "git@github-work:acme/widgets.git"},
		{"other host (host-agnostic)", "https://git.example.org/acme/widgets.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top := newGitRepo(t, tc.origin)
			got, err := (&Resolver{}).Resolve(context.Background(), top)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if want := (Repo{Owner: "acme", Name: "widgets"}); got.Repo != want {
				t.Errorf("Repo = %+v, want %+v", got.Repo, want)
			}
			if got.Toplevel != realPath(t, top) {
				t.Errorf("Toplevel = %q, want %q", got.Toplevel, realPath(t, top))
			}
		})
	}
}

// TestResolveRejectsInvalidDirWithoutAnyGitCall: the recorded dir is trusted
// only as an absolute, existing directory. Every other shape is rejected
// before git ever runs.
func TestResolveRejectsInvalidDirWithoutAnyGitCall(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	cases := []struct {
		name  string
		dir   string
		check func(t *testing.T, err error)
	}{
		{"empty", "", nil},
		{"relative", "some/relative/dir", func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), "absolute") {
				t.Errorf("err = %q, want it to say the path must be absolute", err)
			}
		}},
		{"dot", ".", nil},
		{"missing", missing, func(t *testing.T, err error) {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("err = %v, want errors.Is(_, fs.ErrNotExist)", err)
			}
		}},
		{"a file, not a directory", file, func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), "directory") {
				t.Errorf("err = %q, want it to say the path is not a directory", err)
			}
		}},
		{"embedded newline", t.TempDir() + "\n/etc", nil},
		{"embedded NUL", t.TempDir() + "\x00/etc", nil},
		{"leading dash", "-C", nil},
	}
	calls := installRecordingGit(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&Resolver{}).Resolve(context.Background(), tc.dir)
			if err == nil {
				t.Fatalf("Resolve(%q) = %+v, nil; want an error", tc.dir, got)
			}
			if got != (Identity{}) {
				t.Errorf("Resolve returned a non-zero Identity %+v alongside an error", got)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
			if n := calls(); n != 0 {
				t.Errorf("git was invoked %d time(s) for an invalid dir", n)
			}
		})
	}
}

func TestResolveNoOriginIsAnError(t *testing.T) {
	top := newGitRepo(t, "")

	got, err := (&Resolver{}).Resolve(context.Background(), top)
	if err == nil {
		t.Fatalf("Resolve = %+v, nil; want an error for a repo without origin", got)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("err = %q, want it to name the missing origin remote", err)
	}
}

func TestResolveUnparseableOriginIsAnError(t *testing.T) {
	top := newGitRepo(t, "https://github.com/onlyone")

	got, err := (&Resolver{}).Resolve(context.Background(), top)
	if err == nil {
		t.Fatalf("Resolve = %+v, nil; want an error for an origin without owner/name", got)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("err = %q, want it to name the origin remote", err)
	}
}

func TestResolveNonRepositoryIsAnError(t *testing.T) {
	isolateGit(t)
	got, err := (&Resolver{}).Resolve(context.Background(), t.TempDir())
	if err == nil {
		t.Fatalf("Resolve = %+v, nil; want an error outside any repository", got)
	}
}

// TestResolveCacheHitMakesNoSecondGitCall: a success is cached by the cleaned
// dir, so a repeat lookup (even spelled with a trailing slash) is served
// without touching git -- and survives the repo vanishing.
func TestResolveCacheHitMakesNoSecondGitCall(t *testing.T) {
	top := newGitRepo(t, "git@github.com:acme/widgets.git")
	calls := installRecordingGit(t)
	r := &Resolver{}

	first, err := r.Resolve(context.Background(), top)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	afterFirst := calls()
	if afterFirst == 0 {
		t.Fatal("the first Resolve made no git call; the shim is not in use")
	}

	second, err := r.Resolve(context.Background(), top)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	spelled, err := r.Resolve(context.Background(), top+"/")
	if err != nil {
		t.Fatalf("Resolve with trailing slash: %v", err)
	}
	if second != first || spelled != first {
		t.Errorf("cached results %+v / %+v differ from the first %+v", second, spelled, first)
	}
	if n := calls(); n != afterFirst {
		t.Errorf("git calls = %d after cache hits, want still %d", n, afterFirst)
	}

	if err := os.RemoveAll(filepath.Join(top, ".git")); err != nil {
		t.Fatal(err)
	}
	if again, err := r.Resolve(context.Background(), top); err != nil || again != first {
		t.Errorf("Resolve after removing .git = %+v, %v; want the cached %+v", again, err, first)
	}
}

// TestResolveFailureIsNotCached: a failed resolution is retried on the next
// call and succeeds once the cause is fixed.
func TestResolveFailureIsNotCached(t *testing.T) {
	top := newGitRepo(t, "")
	calls := installRecordingGit(t)
	r := &Resolver{}

	if got, err := r.Resolve(context.Background(), top); err == nil {
		t.Fatalf("first Resolve = %+v, nil; want an error (no origin yet)", got)
	}
	afterFailure := calls()

	runRealGit(t, top, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	got, err := r.Resolve(context.Background(), top)
	if err != nil {
		t.Fatalf("retry Resolve: %v (a failure must not be cached)", err)
	}
	if want := (Repo{Owner: "acme", Name: "widgets"}); got.Repo != want {
		t.Errorf("retry Repo = %+v, want %+v", got.Repo, want)
	}
	if n := calls(); n <= afterFailure {
		t.Errorf("git calls = %d, want more than %d: the retry must re-run git", n, afterFailure)
	}
}

// TestResolveNeverEchoesCredentialsInErrors: an https origin may carry a
// token; no error may contain it.
func TestResolveNeverEchoesCredentialsInErrors(t *testing.T) {
	cases := []struct {
		name   string
		origin string
	}{
		{"https user:token, no owner/name", "https://user:" + leakToken + "@github.com/onlyone"},
		{"https token-only userinfo", "https://" + leakToken + "@github.com/onlyone"},
		{"scp style token user", leakToken + "@github.com:onlyone"},
		// These once parsed "successfully" into a garbage owner/name that
		// carried the secret into Identity (and any error built from it).
		{"https query-string token", "https://github.com/acme/widgets.git?access_token=" + leakToken},
		{"https fragment token", "https://github.com/acme/widgets.git#" + leakToken},
		{"https userinfo plus query-string token", "https://user@github.com/acme/widgets?token=" + leakToken},
		{"scp style user:password", "user:" + leakToken + "@github.com:acme/widgets"},
		{"scp style user:password, no owner/name", "user:" + leakToken + "@github.com:onlyone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top := newGitRepo(t, tc.origin)
			got, err := (&Resolver{}).Resolve(context.Background(), top)
			if err == nil {
				t.Fatalf("Resolve = %+v, nil; want an error", got)
			}
			if strings.Contains(err.Error(), leakToken) {
				t.Errorf("err = %q leaks the credential", err)
			}
			if !strings.Contains(err.Error(), "origin") {
				t.Errorf("err = %q, want it to name the origin remote", err)
			}
		})
	}
}

// TestRedactURL pins what survives redaction: userinfo is the text before the
// last `@` of the authority only (an `@` later in the path is not userinfo),
// and a query string or fragment is masked wholesale.
func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://user:" + leakToken + "@github.com/acme/w", "https://***@github.com/acme/w"},
		{"https://" + leakToken + "@github.com/acme/w", "https://***@github.com/acme/w"},
		{"https://user:p@ss" + leakToken + "@host/x", "https://***@host/x"},
		{"https://github.com/acme/w@x", "https://github.com/acme/w@x"},
		{"ssh://git@github.com/acme/w.git", "ssh://***@github.com/acme/w.git"},
		{"user:" + leakToken + "@host:acme/w", "***@host:acme/w"},
		{"git@github.com:acme/w.git", "***@github.com:acme/w.git"},
		{"github.com:acme/w.git", "github.com:acme/w.git"},
		{"https://github.com/a?access_token=" + leakToken, "https://github.com/a?***"},
		{"https://github.com/a#" + leakToken, "https://github.com/a#***"},
		{"https://u:" + leakToken + "@github.com/a?x@y", "https://***@github.com/a?***"},
	}
	for _, tc := range cases {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// parseRemoteURL is the one pure unit under test (plan Test Strategy): a table
// over URL shapes and credential redaction that no integration path can
// enumerate cheaply.
func TestParseRemoteURL(t *testing.T) {
	good := []struct {
		url, owner, name string
	}{
		{"https://github.com/acme/widgets.git", "acme", "widgets"},
		{"https://github.com/acme/widgets", "acme", "widgets"},
		{"https://github.com/acme/widgets/", "acme", "widgets"},
		{"http://github.com/acme/widgets.git", "acme", "widgets"},
		{"https://user:" + leakToken + "@github.com/acme/widgets.git", "acme", "widgets"},
		{"https://ghe.corp.example/acme/widgets.git", "acme", "widgets"},
		{"https://host.example/group/acme/widgets.git", "acme", "widgets"},
		{"ssh://git@github.com/acme/widgets.git", "acme", "widgets"},
		{"ssh://git@github.com:22/acme/widgets.git", "acme", "widgets"},
		{"git@github.com:acme/widgets.git", "acme", "widgets"},
		{"git@github.com:acme/widgets", "acme", "widgets"},
		{"git@github-work:acme/widgets.git", "acme", "widgets"},
		{"github.com:acme/widgets.git", "acme", "widgets"},
		{"https://github.com/acme/my.repo-name_2.git", "acme", "my.repo-name_2"},
	}
	for _, tc := range good {
		t.Run("ok/"+strings.ReplaceAll(tc.url, leakToken, "TOKEN"), func(t *testing.T) {
			owner, name, err := parseRemoteURL(tc.url)
			if err != nil {
				t.Fatalf("parseRemoteURL(%q): %v", tc.url, err)
			}
			if owner != tc.owner || name != tc.name {
				t.Errorf("parseRemoteURL(%q) = %q/%q, want %q/%q", tc.url, owner, name, tc.owner, tc.name)
			}
		})
	}

	bad := []string{
		"",
		"not a url",
		"https://github.com",
		"https://github.com/",
		"https://github.com/onlyone",
		"https://github.com//widgets",
		"git@github.com:onlyone",
		"git@github.com:",
		"https://user:" + leakToken + "@github.com/onlyone",
		leakToken + "@github.com:onlyone",
		// Owner and name must pass the same validator as the reader's repo check.
		"https://github.com/acme/widgets.git?access_token=" + leakToken,
		"https://github.com/acme/widgets.git#" + leakToken,
		"https://github.com/acme/widgets?x=1",
		"user:" + leakToken + "@github.com:acme/widgets",
		"https://github.com/-evil/widgets",
		"https://github.com/acme/-widgets",
		"git@github.com:acme/wid gets",
		"https://github.com/ac me/widgets",
	}
	for _, url := range bad {
		t.Run("err/"+strings.ReplaceAll(url, leakToken, "TOKEN"), func(t *testing.T) {
			owner, name, err := parseRemoteURL(url)
			if err == nil {
				t.Fatalf("parseRemoteURL(%q) = %q/%q, nil; want an error", url, owner, name)
			}
			if strings.Contains(err.Error(), leakToken) {
				t.Errorf("err = %q leaks the credential", err)
			}
			if owner != "" || name != "" {
				t.Errorf("parseRemoteURL(%q) returned %q/%q alongside an error", url, owner, name)
			}
		})
	}
}
