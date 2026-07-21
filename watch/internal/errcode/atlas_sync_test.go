package errcode

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// tokenPattern extracts every CENCI-* token embedded in the atlas's
// free-form markdown text. It mirrors codeFormat's character classes
// (CENCI-<AREA>-<SUBAREA>-<NNN>) but without the ^/$ anchors codeFormat
// carries: codeFormat is defined (in errcode_test.go, same package) to
// MatchString a whole code string in isolation, so it can never match a
// token embedded inside a larger line of prose. Every token this pattern
// finds is still cross-checked against codeFormat.MatchString below, so the
// two stay provably in sync.
var tokenPattern = regexp.MustCompile(`CENCI-[A-Z]+-[A-Z]+-[0-9]{3}`)

// atlasPath resolves docs/failure-atlas.md at repo root via runtime.Caller,
// climbing out of the watch/ Go module (docs/ lives outside it — see the
// plan's Architectural Context: a CWD-relative path or go:embed cannot
// reach it, since go:embed cannot cross the module root upward). This
// resolves at compile time from this source file's own absolute path, so it
// is independent of the test binary's working directory.
func atlasPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	// file: <repo-root>/watch/internal/errcode/atlas_sync_test.go
	// climb: errcode -> internal -> watch -> <repo-root>
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "failure-atlas.md")
}

// readAtlas reads the failure atlas, failing loudly (t.Fatalf, not a skip)
// when it cannot — a moved/renamed atlas file must break CI, not silently
// pass with zero coverage.
func readAtlas(t *testing.T) string {
	t.Helper()
	path := atlasPath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read failure atlas at %s: %v (a missing/renamed atlas must fail loudly, not be silently skipped)", path, err)
	}
	return string(content)
}

// TestAtlasSync_EveryRegisteredCodeHasAnAtlasEntry is the forward half of
// the bidirectional registry<->atlas sync invariant: every code
// errcode.AllCodes() returns must appear (ideally as a heading/anchor) in
// docs/failure-atlas.md, so a newly registered code can never silently ship
// without operator-facing recovery documentation.
func TestAtlasSync_EveryRegisteredCodeHasAnAtlasEntry(t *testing.T) {
	codes := AllCodes()
	if len(codes) == 0 {
		t.Fatal("errcode.AllCodes() returned no codes; the sync test cannot verify atlas coverage (guard against a silently-vacuous pass)")
	}

	atlas := readAtlas(t)
	for _, code := range codes {
		if !strings.Contains(atlas, string(code)) {
			t.Errorf("docs/failure-atlas.md has no entry for registered code %s", code)
		}
	}
}

// TestAtlasSync_EveryAtlasCodeIsRegistered is the backward half: every
// CENCI-*-shaped token found in the atlas must map to a currently
// registered errcode.Code, catching a stale doc entry left behind after a
// code was renamed or removed from the registry.
func TestAtlasSync_EveryAtlasCodeIsRegistered(t *testing.T) {
	atlas := readAtlas(t)

	tokens := tokenPattern.FindAllString(atlas, -1)
	if len(tokens) == 0 {
		t.Fatal("no CENCI-* tokens found in docs/failure-atlas.md; the backward sync check cannot verify anything (guard against a silently-vacuous pass)")
	}

	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if seen[token] {
			continue
		}
		seen[token] = true

		if !codeFormat.MatchString(token) {
			t.Errorf("extracted atlas token %q does not satisfy codeFormat %s; tokenPattern and codeFormat have drifted apart", token, codeFormat.String())
			continue
		}
		if _, ok := Lookup(Code(token)); !ok {
			t.Errorf("docs/failure-atlas.md references %s, which is not a registered errcode.Code (stale entry for a renamed/removed code?)", token)
		}
	}
}
