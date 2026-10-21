package main_test

// Black-box CLI tests for `cenci pipeline followup` (ticket #1339): the exit-2
// usage surface (P1) and the JSON wire contract (P2) of the real built binary
// with a fake `gh` on PATH. The engine's behavior (lineage, gates, append,
// create, retry) is pinned in internal/pipeline/followup_test.go; this file
// only covers what the CLI layer owns. The fake gh logs every call so a usage
// error can be shown to precede any gh call.

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

const (
	followupUsageMarker = "cenci pipeline followup: usage:"
	followupPRURL       = "https://github.com/o/r/pull/7"
	followupItemsJSON   = `[{"context":"extract the retry helper","area":"watch"}]`
)

// followupFixture is a temp dir holding an items file, a title file and a fake
// gh whose every invocation is appended to calls.log.
type followupFixture struct {
	dir       string
	itemsFile string
	titleFile string
	callsLog  string
}

// newFollowupFixture installs a fake gh running ghScript (a /bin/sh body that
// may use "$@") after logging the call.
func newFollowupFixture(t *testing.T, items, ghScript string) followupFixture {
	t.Helper()
	dir := t.TempDir()
	f := followupFixture{
		dir:       dir,
		itemsFile: filepath.Join(dir, "items.json"),
		titleFile: filepath.Join(dir, "title.txt"),
		callsLog:  filepath.Join(dir, "calls.log"),
	}
	if err := os.WriteFile(f.itemsFile, []byte(items), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.titleFile, []byte("watch: deferred follow-ups\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exectest.WriteExecutable(t, filepath.Join(binDir, "gh"),
		"#!/bin/sh\necho \"$@\" >> '"+f.callsLog+"'\n"+ghScript+"\n")
	return f
}

func (f followupFixture) env() []string { return pipelineEnv(filepath.Join(f.dir, "bin")) }

// calls returns the logged gh invocations, one per line.
func (f followupFixture) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.callsLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// run executes `cenci pipeline followup args...`, returning stdout, stderr and
// the exit code.
func (f followupFixture) run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"pipeline", "followup"}, args...)...)
	cmd.Env = f.env()
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return so.String(), se.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("followup %v: %v", args, err)
	}
	return so.String(), se.String(), 0
}

// followupRaw decodes the result without importing the engine types so the
// test sees the wire format only.
type followupRaw struct {
	Followup map[string]json.RawMessage `json:"followup"`
	Warnings json.RawMessage            `json:"warnings"`
	Errors   json.RawMessage            `json:"errors"`
}

func decodeFollowup(t *testing.T, stdout string) followupRaw {
	t.Helper()
	var r followupRaw
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not the followup JSON: %v\n%s", err, stdout)
	}
	return r
}

func (r followupRaw) str(t *testing.T, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(r.Followup[key], &s); err != nil {
		t.Fatalf("followup.%s = %s: %v", key, r.Followup[key], err)
	}
	return s
}

func assertRawJSON(t *testing.T, name string, got json.RawMessage, want string) {
	t.Helper()
	if string(got) != want {
		t.Errorf("%s = %s, want %s", name, got, want)
	}
}

// -- P1: usage errors --------------------------------------------------------

func TestPipelineFollowup_UsageErrors_Exit2(t *testing.T) {
	// A gh that would record any call: a usage error must precede every call.
	f := newFollowupFixture(t, followupItemsJSON, "exit 1")

	// Each case differs from a valid invocation in exactly the named way.
	valid := func(extra ...string) []string {
		return append([]string{"42", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL}, extra...)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"missing id without ticketless", []string{"--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"no arguments at all", nil},
		{"id together with ticketless (id first)", valid("--ticketless")},
		{"id together with ticketless (id after flag)", []string{"--ticketless", "42", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"non-numeric id", []string{"abc", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"zero id", []string{"0", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"leading-zero id", []string{"042", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"unknown source", []string{"42", "--source", "bogus", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"missing source", []string{"42", "--items-file", f.itemsFile, "--pr-url", followupPRURL}},
		{"missing items-file", []string{"42", "--source", "implement", "--pr-url", followupPRURL}},
		{"empty items-file", []string{"42", "--source", "implement", "--items-file", "", "--pr-url", followupPRURL}},
		{"missing pr-url", []string{"42", "--source", "implement", "--items-file", f.itemsFile}},
		{"pr-url not a pull url", valid("--pr-url", "https://github.com/o/r/issues/7")},
		{"pr-url with whitespace", valid("--pr-url", "https://github.com/o/r/pull/7 x")},
		{"pr-url non-numeric pull", valid("--pr-url", "https://github.com/o/r/pull/abc")},
		{"repo-slug without owner", valid("--repo-slug", "repo")},
		{"repo-slug with three segments", valid("--repo-slug", "a/b/c")},
		{"repo-slug with illegal char", valid("--repo-slug", "o/r;rm")},
		{"possibly-related without create", valid("--possibly-related", "12,15")},
		{"possibly-related malformed", valid("--create", "--title-file", f.titleFile, "--possibly-related", "12,,15")},
		{"possibly-related non-numeric", valid("--create", "--title-file", f.titleFile, "--possibly-related", "12,x")},
		{"append-to with create", valid("--area", "watch", "--append-to", "9", "--create", "--title-file", f.titleFile)},
		{"append-to without area", valid("--append-to", "9")},
		{"append-to zero", valid("--area", "watch", "--append-to", "0")},
		{"append-to negative", valid("--area", "watch", "--append-to", "-3")},
		{"append-to non-numeric", valid("--area", "watch", "--append-to", "x")},
		{"create without title-file", valid("--create")},
		{"empty area", valid("--area", "")},
		{"unknown flag", valid("--bogus")},
		{"trailing positional", valid("extra")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := f.run(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, followupUsageMarker) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, followupUsageMarker)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("stdout = %q, want empty (usage errors never emit the JSON result)", stdout)
			}
			if strings.Count(strings.TrimRight(stderr, "\n"), "\n") != 0 {
				t.Errorf("stderr = %q, want a one-line hint", stderr)
			}
		})
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("gh was called for usage errors: %q", calls)
	}
}

// -- P2: wire contract -------------------------------------------------------

// A Followup origin under --source implement is the cheapest successful run:
// action skipped, exit 0, and every optional array still serialized as [].
func TestPipelineFollowup_Contract_ArraysNeverNull(t *testing.T) {
	f := newFollowupFixture(t, followupItemsJSON, `case "$*" in
  "issue view 42 --json milestone,labels") echo '{"milestone":null,"labels":[{"name":"Followup"}]}' ;;
  *) echo "unexpected gh call: $*" >&2; exit 1 ;;
esac`)

	stdout, stderr, code := f.run(t, "42", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	r := decodeFollowup(t, stdout)

	if got := r.str(t, "action"); got != "skipped" {
		t.Errorf("followup.action = %q, want skipped", got)
	}
	if got := r.str(t, "reason"); got != "origin-is-followup" {
		t.Errorf("followup.reason = %q, want origin-is-followup", got)
	}
	if _, ok := r.Followup["number"]; ok {
		t.Errorf("followup.number present (%s), want it absent for a skipped run", r.Followup["number"])
	}
	assertRawJSON(t, "followup.items", r.Followup["items"], `[{"context":"extract the retry helper","area":"watch"}]`)
	assertRawJSON(t, "followup.candidates", r.Followup["candidates"], `[]`)
	assertRawJSON(t, "followup.also_matched", r.Followup["also_matched"], `[]`)
	assertRawJSON(t, "warnings", r.Warnings, `[]`)
	assertRawJSON(t, "errors", r.Errors, `[]`)

	gotKeys := slices.Sorted(maps.Keys(r.Followup))
	wantKeys := []string{"action", "also_matched", "candidates", "items", "reason"}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("followup keys = %v, want exactly %v", gotKeys, wantKeys)
	}
	if want := []string{"issue view 42 --json milestone,labels"}; !reflect.DeepEqual(f.calls(t), want) {
		t.Errorf("gh calls = %q, want %q", f.calls(t), want)
	}
}

// An unreadable origin under --source implement fails closed: exit 1 with the
// JSON result on stdout, a non-empty errors array, items echoed, no number.
func TestPipelineFollowup_GhReadFailure_Exit1(t *testing.T) {
	f := newFollowupFixture(t, followupItemsJSON, `echo "GraphQL: Could not resolve to an Issue with the number of 42." >&2
exit 1`)

	stdout, stderr, code := f.run(t, "42", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	r := decodeFollowup(t, stdout)
	if got := r.str(t, "action"); got != "skipped" {
		t.Errorf("followup.action = %q, want skipped", got)
	}
	if got := r.str(t, "reason"); got != "origin-unreadable" {
		t.Errorf("followup.reason = %q, want origin-unreadable", got)
	}
	if _, ok := r.Followup["number"]; ok {
		t.Errorf("followup.number present (%s), want it absent", r.Followup["number"])
	}
	assertRawJSON(t, "followup.items", r.Followup["items"], `[{"context":"extract the retry helper","area":"watch"}]`)
	var errs []string
	if err := json.Unmarshal(r.Errors, &errs); err != nil {
		t.Fatalf("errors = %s: %v", r.Errors, err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "origin issue unreadable") || !strings.Contains(errs[0], "Could not resolve") {
		t.Errorf("errors = %q, want one entry naming the unreadable origin and gh's text", errs)
	}
}

// --ticketless with no open Followup issue creates a bare Followup: exit 0,
// number from the POST, no origin read, and the exact create call sequence.
func TestPipelineFollowup_TicketlessCreate_Exit0(t *testing.T) {
	f := newFollowupFixture(t, followupItemsJSON, `case "$*" in
  "issue list --label Followup --state open --limit 200 --json number,title,body,labels,assignees") echo '[]' ;;
  "label create Followup --color C5DEF5 --description "*) exit 0 ;;
  "api repos/{owner}/{repo}/issues -X POST --input "*) echo '{"number":99}' ;;
  "issue view 99 --json title") echo '{"title":"watch: deferred follow-ups"}' ;;
  *) echo "unexpected gh call: $*" >&2; exit 1 ;;
esac`)

	stdout, stderr, code := f.run(t, "--ticketless", "--source", "implement", "--items-file", f.itemsFile,
		"--pr-url", followupPRURL, "--title-file", f.titleFile)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	r := decodeFollowup(t, stdout)
	if got := r.str(t, "action"); got != "created" {
		t.Errorf("followup.action = %q, want created", got)
	}
	if got := r.str(t, "reason"); got != "no-lineage-match" {
		t.Errorf("followup.reason = %q, want no-lineage-match", got)
	}
	assertRawJSON(t, "followup.number", r.Followup["number"], `99`)
	assertRawJSON(t, "followup.candidates", r.Followup["candidates"], `[]`)
	assertRawJSON(t, "followup.also_matched", r.Followup["also_matched"], `[]`)
	assertRawJSON(t, "warnings", r.Warnings, `["cross-ticket candidates skipped: no --area"]`)
	assertRawJSON(t, "errors", r.Errors, `[]`)

	calls := f.calls(t)
	if len(calls) != 4 {
		t.Fatalf("gh calls = %q, want list, label create, POST, title re-fetch", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "issue view 42") {
			t.Errorf("ticketless run read an origin: %q", c)
		}
	}
}

// An empty items list exits 1 with "no deferred items" and zero gh calls.
func TestPipelineFollowup_EmptyItems_Exit1(t *testing.T) {
	f := newFollowupFixture(t, `[]`, "exit 1")

	stdout, stderr, code := f.run(t, "42", "--source", "implement", "--items-file", f.itemsFile, "--pr-url", followupPRURL)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	r := decodeFollowup(t, stdout)
	if got := r.str(t, "action"); got != "skipped" {
		t.Errorf("followup.action = %q, want skipped", got)
	}
	if got := r.str(t, "reason"); got != "no-deferred-items" {
		t.Errorf("followup.reason = %q, want no-deferred-items", got)
	}
	if _, ok := r.Followup["number"]; ok {
		t.Errorf("followup.number present (%s), want it absent", r.Followup["number"])
	}
	assertRawJSON(t, "followup.items", r.Followup["items"], `[]`)
	assertRawJSON(t, "followup.candidates", r.Followup["candidates"], `[]`)
	assertRawJSON(t, "followup.also_matched", r.Followup["also_matched"], `[]`)
	assertRawJSON(t, "warnings", r.Warnings, `[]`)
	var errs []string
	if err := json.Unmarshal(r.Errors, &errs); err != nil {
		t.Fatalf("errors = %s: %v", r.Errors, err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "no deferred items") {
		t.Errorf("errors = %q, want one entry containing %q", errs, "no deferred items")
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("gh was called for an empty items file: %q", calls)
	}
}
