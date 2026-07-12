package dispatch

import (
	"errors"
	"strings"
	"testing"
)

// TestCollectTicketsAttemptsEveryRepoOnFailure locks in CollectTickets' best-
// effort contract (ticket #122): a failure on one repo must not stop
// collection from being attempted on the rest, and every per-repo failure
// must be joined into the returned error so the caller's log names every
// failing repo, not just the first. collectRepoTickets shells out to gh with
// no injection seam (by design, out of scope here), so this drives it with
// repos gh cannot resolve (nonexistent owner/repo, and no gh auth in the test
// environment either way) to get a deterministic per-repo error from each.
func TestCollectTicketsAttemptsEveryRepoOnFailure(t *testing.T) {
	repos := []RepoConfig{
		{Repo: "o/nonexistent-a", Dir: t.TempDir()},
		{Repo: "o/nonexistent-b", Dir: t.TempDir()},
		{Repo: "o/nonexistent-c", Dir: t.TempDir()},
	}

	tickets, err := CollectTickets(repos)

	if err == nil {
		t.Fatal("expected a joined error from three failing repos, got nil")
	}
	if len(tickets) != 0 {
		t.Errorf("expected no tickets from all-failing repos, got %+v", tickets)
	}

	msg := err.Error()
	for _, rc := range repos {
		if !strings.Contains(msg, rc.Repo) {
			t.Errorf("joined error %q must name every failing repo, missing %q", msg, rc.Repo)
		}
	}
}

// TestCollectTicketsJoinsMultipleErrors is a basic sanity check that the
// errors.Join composition used by CollectTickets behaves as errors.Join
// contracts: unwrapping the joined error yields every individual per-repo
// error, and errors.Is finds each of them.
func TestCollectTicketsJoinsMultipleErrors(t *testing.T) {
	repos := []RepoConfig{
		{Repo: "o/nonexistent-x", Dir: t.TempDir()},
		{Repo: "o/nonexistent-y", Dir: t.TempDir()},
	}

	_, err := CollectTickets(repos)
	if err == nil {
		t.Fatal("expected a non-nil joined error")
	}

	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("expected an errors.Join-composed error implementing Unwrap() []error, got %T", err)
	}
	unwrapped := joined.Unwrap()
	if len(unwrapped) != len(repos) {
		t.Fatalf("expected %d unwrapped errors, got %d: %v", len(repos), len(unwrapped), unwrapped)
	}
	for i, rc := range repos {
		if !strings.Contains(unwrapped[i].Error(), rc.Repo) {
			t.Errorf("unwrapped error[%d] = %q, want it to name %q", i, unwrapped[i].Error(), rc.Repo)
		}
		if !errors.Is(err, unwrapped[i]) {
			t.Errorf("errors.Is must find the per-repo error for %q in the joined error", rc.Repo)
		}
	}
}
