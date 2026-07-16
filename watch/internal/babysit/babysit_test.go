package babysit

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func withCommands(t *testing.T, responses []string, calls *[][]string) {
	t.Helper()
	original := command
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if name != "gh" {
			return []byte(""), nil
		}
		if i >= len(responses) {
			return nil, fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
		out := responses[i]
		i++
		return []byte(out), nil
	}
	t.Cleanup(func() { command = original })
}

func openPR() string {
	return `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[]}`
}

func TestTickQuietBacksOff(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{openPR(), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, delay, err := tick(&s)
	if err != nil || terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	if delay != 120*time.Second || s.CurrentDelaySeconds != 120 {
		t.Fatalf("delay = %v, state = %d", delay, s.CurrentDelaySeconds)
	}
	if s.LastHeadSHA != "abc" || s.FixAttempts != 0 {
		t.Fatalf("unexpected CI state: %#v", s)
	}
}

func TestTickLaunchesAddressReviewForNewFeedback(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{openPR(), `[]`, `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900}
	_, delay, err := tick(&s)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 300*time.Second || s.LastCommentAt == "" || !reflect.DeepEqual(s.AddressedIDs, []int64{7}) {
		t.Fatalf("unexpected state: %#v", s)
	}
	found := false
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == "address-review" && c[3] == "42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("address-review was not launched: %#v", calls)
	}
}

func TestTickMergedRelabelsClosingIssues(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	withCommands(t, []string{merged, `{}`, `{}`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	if err != nil || !terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	want := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("issue relabel missing: %#v", calls)
	}
}

func TestStatePathDoesNotExposeRepositoryName(t *testing.T) {
	p := statePath("/state", "secret-owner/private-repo", "42")
	if strings.Contains(p, "secret") || !strings.HasSuffix(p, "-42.json") {
		t.Fatalf("unsafe state path: %s", p)
	}
}
