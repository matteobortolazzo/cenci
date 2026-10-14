package ticketstate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Identity is a recorded directory resolved to its git toplevel and origin repo.
type Identity struct {
	Toplevel string
	Repo     Repo
}

// Resolver maps a recorded directory to its Identity. Only successes are
// cached, so a failed resolution is retried on the next call. The cache is
// never invalidated: if a repo's origin is repointed later, the cached identity
// goes stale, and a stale Identity.Repo is not guaranteed to fail or come back
// Unknown -- it can name a different repository that has a ticket with the
// same number and so yield a real, wrong verdict. Cross-checking Identity.Repo
// against the repo the ticket actually belongs to is the caller's
// responsibility; the Resolver does not do it.
type Resolver struct {
	mu    sync.Mutex
	cache map[string]Identity
}

// Resolve validates dir, then resolves its git toplevel and origin owner/name.
// dir comes from a tmux window option, so it is trusted only as an absolute
// path to an existing directory; anything else is rejected before git runs.
func (r *Resolver) Resolve(ctx context.Context, dir string) (Identity, error) {
	if err := validateDir(dir); err != nil {
		return Identity{}, err
	}
	key := filepath.Clean(dir)

	r.mu.Lock()
	id, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return id, nil
	}

	stdout, stderr, err := execGit(ctx, key, "rev-parse", "--show-toplevel")
	if err != nil {
		return Identity{}, fmt.Errorf("resolving git toplevel of %s: %w", key, withStderr(err, stderr))
	}
	toplevel := strings.TrimSpace(stdout)
	if !filepath.IsAbs(toplevel) {
		return Identity{}, fmt.Errorf("resolving git toplevel of %s: git reported %q, want an absolute path", key, toplevel)
	}

	// The remote URL is never echoed into an error: it may embed credentials.
	stdout, _, err = execGit(ctx, toplevel, "remote", "get-url", "origin")
	if err != nil {
		return Identity{}, fmt.Errorf("reading the origin remote of %s: %w", toplevel, err)
	}
	owner, name, err := parseRemoteURL(strings.TrimSpace(stdout))
	if err != nil {
		return Identity{}, fmt.Errorf("parsing the origin remote of %s: %w", toplevel, err)
	}

	id = Identity{Toplevel: toplevel, Repo: Repo{Owner: owner, Name: name}}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]Identity)
	}
	r.cache[key] = id
	r.mu.Unlock()
	return id, nil
}

// validateDir accepts only a non-empty absolute path, free of NUL and newline,
// that names an existing directory.
func validateDir(dir string) error {
	switch {
	case dir == "":
		return fmt.Errorf("repo dir is empty")
	case strings.ContainsAny(dir, "\x00\n"):
		return fmt.Errorf("repo dir %q contains a NUL or newline", dir)
	case !filepath.IsAbs(dir):
		return fmt.Errorf("repo dir %q must be an absolute path", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("repo dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo dir %s is not a directory", dir)
	}
	return nil
}

// parseRemoteURL extracts owner/name from a git remote URL. It dispatches by
// prefix (ssh://, https://, http://, else scp-style [user@]host:path), is
// host-agnostic, tolerates userinfo, a .git suffix and a trailing slash, and
// takes the last two path segments as owner/name, each of which must pass
// validRepoPart. Errors carry only a redacted form of the URL.
func parseRemoteURL(url string) (owner, name string, err error) {
	var path string
	switch {
	case strings.HasPrefix(url, "ssh://"), strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"):
		_, rest, _ := strings.Cut(url, "://")
		_, p, ok := strings.Cut(rest, "/")
		if !ok {
			return "", "", fmt.Errorf("no path in remote url %q", redactURL(url))
		}
		path = p
	default:
		_, p, ok := strings.Cut(url, ":")
		if !ok {
			return "", "", fmt.Errorf("unrecognized remote url %q", redactURL(url))
		}
		path = p
	}

	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return "", "", fmt.Errorf("remote url %q has no owner/name path", redactURL(url))
	}
	owner, name = segments[len(segments)-2], segments[len(segments)-1]
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("remote url %q has an empty owner or name", redactURL(url))
	}
	// A query string, fragment, stray userinfo or whitespace left in a segment
	// is not part of a repo name; rejecting it here keeps a secret that rode in
	// on such text out of every Identity (and the error says nothing of it).
	if !validRepoPart(owner) || !validRepoPart(name) {
		return "", "", fmt.Errorf("remote url %q has an invalid owner or name", redactURL(url))
	}
	return owner, name, nil
}

// redactURL masks credentials before a remote URL appears in error text:
// userinfo is everything before the last "@" of the authority (the text after
// an optional "://" up to the first "/", "?" or "#"; for scp-style
// "user:pass@host:path" the same rule applies), and a query string or fragment
// is masked wholesale. An "@" later in the path is not userinfo.
func redactURL(url string) string {
	scheme, rest := "", url
	if i := strings.Index(url, "://"); i != -1 {
		scheme, rest = url[:i+3], url[i+3:]
	}
	authEnd := strings.IndexAny(rest, "/?#")
	if authEnd == -1 {
		authEnd = len(rest)
	}
	if at := strings.LastIndex(rest[:authEnd], "@"); at != -1 {
		rest = "***" + rest[at:]
	}
	if i := strings.IndexAny(rest, "?#"); i != -1 {
		rest = rest[:i+1] + "***"
	}
	return scheme + rest
}
