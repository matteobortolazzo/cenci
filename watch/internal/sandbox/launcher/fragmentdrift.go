package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fragment-drift detection (#1048): a repo's committed .cenci/Dockerfile is
// generated once, by hand, via the /cenci:configure skill — never by this
// binary. When the installed cenci-sandbox plugin's sandbox/fragments/*.dockerfile
// change (a docker-group fix, a CVE bump, ...), nothing propagates that change
// into an already-committed per-repo Dockerfile, and nothing says so. This
// file implements a label-free, read-only comparison of the repo's committed
// managed block against the installed fragments at freshness-check time (see
// the plan's decision: Option 1 — no cenci.fragments-version label, no
// build-argv change, so the check works day-one on already-built images and
// survives a rebuild by construction, since BuildRepoImage never writes
// .cenci/Dockerfile).
//
// Constraints (ticket #1048, owner comment 3):
//  1. Bounded to the "# cenci:managed-begin" .. "# cenci:managed-end" region;
//     absent or malformed markers is a silent skip (unmanaged/legacy file,
//     not drift) — never a warning.
//  2. Fragment selection is derived from the repo's own managed block, never
//     a Go re-port of SKILL.md's stack->fragment mapping table.
//  3. Matching is true multi-line fixed-string containment (strings.Contains
//     over the whole normalized block), never grep -F/line-wise alternation.
//  4. Identity when content has drifted: per-fragment "# cenci:fragment-begin
//     <name>" / "-end <name>" markers are preferred when present; otherwise
//     the installed fragment's "# -- <title> --.." banner line anchors
//     identity (banner present + mismatch -> drift; banner absent -> not
//     selected). Detection is never gated on the markers existing.
//  5. The dotnet fragment's "ARG DOTNET_SDK_VERSION=" value (and the optional
//     auto-detect-failure comment SKILL.md inserts right after it) is
//     normalized before comparing — the one false positive that would sink
//     the feature. No other ARG line is touched.

const (
	managedBeginMarker = "# cenci:managed-begin"
	managedEndMarker   = "# cenci:managed-end"

	fragmentMarkerBeginPrefix = "# cenci:fragment-begin "
	fragmentMarkerEndPrefix   = "# cenci:fragment-end "

	bannerLinePrefix = "# ── " // "# ── " — sandbox/fragments/*.dockerfile's title banner

	dotnetArgPrefix         = "ARG DOTNET_SDK_VERSION="
	dotnetNormalizedArgLine = "ARG DOTNET_SDK_VERSION=<normalized>"
	dotnetAutoDetectComment = "# .NET version could not be auto-detected from the stack token — using fragment default. See sandbox/README.md to pin manually."

	// maxManagedDockerfileSize bounds the repo's .cenci/Dockerfile read
	// (security Low, #1048): a legitimate generated file is a few KiB. A
	// file exceeding this, or one that isn't a regular file, is treated as
	// "not a cenci-managed block" — the same silent-skip contract as a
	// missing file — rather than reading arbitrary or non-regular content
	// into memory.
	maxManagedDockerfileSize = 1 << 20 // 1 MiB
)

// fragment is one installed sandbox/fragments/*.dockerfile: its selection
// name (basename without ".dockerfile") and raw content.
type fragment struct {
	name    string
	content string
}

// normalizeLineEndings collapses CRLF to LF. Installed fragment files under
// <assetDir>/fragments are LF-only on disk, but a repo's committed
// .cenci/Dockerfile is hand-authored content and may carry CRLF line endings
// (e.g. a Windows-checkout core.autocrlf setting) despite being otherwise
// byte-identical to the installed fragment. Without this, extractManagedBlock
// / extractMarkedRegion strip "\r" only when matching marker lines — the
// returned block/region content keeps every line's "\r" — so
// strings.Contains in containsNormalizedFragment never matches an LF-only
// fragment against a CRLF-normalized-block, and a byte-for-byte-equivalent
// repo file is falsely reported as drifted. Applied at the read boundary so
// every downstream function (marker/banner matching, containment) operates
// on LF-only content uniformly.
func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// loadFragments reads every *.dockerfile under <assetDir>/fragments. A
// missing or unreadable fragments/ directory is an infrastructure failure,
// distinct from the repo-side silent-skip cases below — the caller
// (Engine.warnFragmentDrift) turns it into a single non-fatal stderr warning
// naming the failing probe.
func loadFragments(assetDir string) ([]fragment, error) {
	fragDir := filepath.Join(assetDir, "fragments")
	entries, err := os.ReadDir(fragDir)
	if err != nil {
		return nil, fmt.Errorf("reading sandbox fragments directory %s: %w", fragDir, err)
	}
	var fragments []fragment
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".dockerfile") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fragDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading sandbox fragment %s: %w", entry.Name(), err)
		}
		fragments = append(fragments, fragment{
			name:    strings.TrimSuffix(entry.Name(), ".dockerfile"),
			content: normalizeLineEndings(string(data)),
		})
	}
	return fragments, nil
}

// extractManagedBlock returns the content strictly between a single
// "# cenci:managed-begin" line and a single "# cenci:managed-end" line
// (begin before end), or ok=false for anything else — no markers, only one
// of the pair, a duplicated marker, or the end appearing before the begin.
// All of those are a silent skip (constraint 1): a marker-less or malformed
// file is an unmanaged/legacy Dockerfile, not drift.
func extractManagedBlock(content string) (block string, ok bool) {
	lines := strings.Split(content, "\n")
	beginIdx, endIdx := -1, -1
	beginCount, endCount := 0, 0
	for i, line := range lines {
		switch strings.TrimRight(line, " \t\r") {
		case managedBeginMarker:
			beginCount++
			beginIdx = i
		case managedEndMarker:
			endCount++
			endIdx = i
		}
	}
	if beginCount != 1 || endCount != 1 || beginIdx >= endIdx {
		return "", false
	}
	return joinTrailingNewline(lines[beginIdx+1 : endIdx]), true
}

// joinTrailingNewline joins lines with "\n", appending a trailing "\n" when
// non-empty so an extracted region preserves the trailing newline the source
// content had before its closing marker line — otherwise a fragment whose
// own content (correctly) ends in "\n" could never be found as a suffix
// match of an extracted region that dropped it, turning an exact match into
// a false drift report.
func joinTrailingNewline(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// extractMarkedRegion returns the content strictly between a
// "# cenci:fragment-begin <name>" line and a "# cenci:fragment-end <name>"
// line inside block, or ok=false when the pair isn't found in order. This is
// the exact-identification path /cenci:configure emits going forward
// (constraint 4).
func extractMarkedRegion(block, name string) (region string, ok bool) {
	beginLine := fragmentMarkerBeginPrefix + name
	endLine := fragmentMarkerEndPrefix + name
	lines := strings.Split(block, "\n")
	beginIdx, endIdx := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == beginLine && beginIdx == -1 {
			beginIdx = i
		}
		if trimmed == endLine {
			endIdx = i
		}
	}
	if beginIdx == -1 || endIdx == -1 || beginIdx >= endIdx {
		return "", false
	}
	return joinTrailingNewline(lines[beginIdx+1 : endIdx]), true
}

// fragmentBanner returns the fragment's "# ── <title> ──…" title line (the
// legacy identity anchor, constraint 4/9), or "" when the fragment carries
// none.
func fragmentBanner(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.HasPrefix(trimmed, bannerLinePrefix) {
			return trimmed
		}
	}
	return ""
}

// selectedRegion decides whether the repo's managed block selects f, and if
// so, which region of the block to compare it against: the marked region
// when a per-fragment marker names f (marker-preferred, constraint 9),
// otherwise the whole managed block when f's banner line appears anywhere in
// it (the legacy fallback — never gated on the markers existing, constraint
// 4's hard requirement). Neither found means the block never selects f.
func selectedRegion(block string, f fragment) (region string, selected bool) {
	if region, ok := extractMarkedRegion(block, f.name); ok {
		return region, true
	}
	banner := fragmentBanner(f.content)
	if banner == "" {
		return "", false
	}
	if strings.Contains(block, banner) {
		return block, true
	}
	return "", false
}

// normalizeManagedContent narrowly normalizes exactly the dotnet fragment's
// substituted line (constraint 5): the "ARG DOTNET_SDK_VERSION=" value is a
// per-repo substitution made by /cenci:configure (SKILL.md's ".NET version
// substitution"), never verbatim in a per-repo block, so it must not be
// compared literally. The auto-detect-failure comment SKILL.md:499 inserts
// immediately after that ARG line is likewise stripped. Both rules are
// anchored to their exact literal text — no other ARG or comment line is
// ever touched (watch/AGENTS.md's narrow-exclusion rule).
func normalizeManagedContent(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == dotnetAutoDetectComment {
			continue
		}
		if strings.HasPrefix(line, dotnetArgPrefix) {
			out = append(out, dotnetNormalizedArgLine)
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// containsNormalizedFragment reports whether region verbatim-contains
// fragmentContent, after normalization, as a true multi-line fixed-string
// substring (strings.Contains over the whole normalized text) — never
// grep -F / line-wise alternation, which "matches" as soon as any single
// line coincidentally appears anywhere (constraint 3; the lesson
// sandbox/tests/fragments-drift.test.sh's header documents, #831).
func containsNormalizedFragment(region, fragmentContent string) bool {
	return strings.Contains(normalizeManagedContent(region), normalizeManagedContent(fragmentContent))
}

// detectFragmentDrift compares repoRoot's committed <repoRoot>/.cenci/Dockerfile
// managed block against every fragment installed under <assetDir>/fragments,
// returning the installed fragment names (file basename without
// ".dockerfile") that the block selects but no longer matches. A repo
// Dockerfile that is missing, non-regular (e.g. a directory or symlink), or
// larger than maxManagedDockerfileSize, or whose managed markers are
// absent/malformed, is a silent skip: ([]string(nil), nil), never an error —
// none of those are drift, they're an unmanaged/legacy/non-file case. Any
// other read failure (permission denied, a corrupt/unreadable file, ...) is
// an infrastructure error, propagated to the caller exactly like a missing or
// unreadable fragments/ directory below, so warnFragmentDrift prints the same
// one-line stderr probe-failure warning for either.
func detectFragmentDrift(assetDir, repoRoot string) ([]string, error) {
	dockerfilePath := filepath.Join(repoRoot, ".cenci", "Dockerfile")

	info, err := os.Lstat(dockerfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("checking repo Dockerfile %s: %w", dockerfilePath, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxManagedDockerfileSize {
		return nil, nil
	}

	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading repo Dockerfile %s: %w", dockerfilePath, err)
	}

	block, ok := extractManagedBlock(normalizeLineEndings(string(data)))
	if !ok {
		return nil, nil
	}

	fragments, err := loadFragments(assetDir)
	if err != nil {
		return nil, err
	}

	var drifted []string
	for _, f := range fragments {
		region, selected := selectedRegion(block, f)
		if !selected {
			continue
		}
		if !containsNormalizedFragment(region, f.content) {
			drifted = append(drifted, f.name)
		}
	}
	sort.Strings(drifted)
	return drifted, nil
}

// warnFragmentDrift prints a single non-fatal stderr warning naming any
// drifted fragments and the /cenci:configure remedy, or a probe-failure
// warning when either the installed fragments/ directory or the repo's own
// .cenci/Dockerfile could not be read (permission denied, a corrupt file,
// ... — anything other than the documented silent-skip cases: a missing,
// non-regular, or oversized Dockerfile, or absent/malformed managed
// markers). Scoped strictly to scope.UsingRepoImage (constraint 7) — the monolith
// image builds from this plugin's own pre-composed sandbox/Dockerfile, whose
// fragment identity is already guarded by fragments-drift.test.sh. Never
// touches scope.Image or any freshness state: this is a separate, read-only
// signal that must never flip imageCurrent's "current" boolean (constraint
// 6), called from EnsureImage's no-rebuild path and from the end of
// BuildRepoImage (AC #5: a rebuild alone must not clear the drift state,
// which holds here for free since this never writes .cenci/Dockerfile).
func (e *Engine) warnFragmentDrift(scope Scope) {
	if !scope.UsingRepoImage {
		return
	}
	drifted, err := detectFragmentDrift(e.AssetDir, scope.RepoRoot)
	if err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "Warning: could not check %s's committed .cenci/Dockerfile for fragment drift (%v); skipping the fragment-drift check.\n", scope.RepoRoot, err)
		return
	}
	if len(drifted) == 0 {
		return
	}
	_, _ = fmt.Fprintf(e.Stderr, "Warning: %s's committed .cenci/Dockerfile is out of date with these installed sandbox fragments: %s. Re-run /cenci:configure to regenerate it, then `cenci sandbox build` to rebuild the image.\n", scope.RepoRoot, strings.Join(drifted, ", "))
}
