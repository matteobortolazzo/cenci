package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeAssetFixture builds a minimal asset dir: Dockerfile.base,
// entrypoint.sh, and lib/ with a nested subdirectory.
func writeAssetFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Dockerfile.base"), "FROM ubuntu:24.04\n")
	writeFile(t, filepath.Join(dir, "entrypoint.sh"), "#!/bin/sh\nexec \"$@\"\n")
	writeFile(t, filepath.Join(dir, "lib", "repo-scope.sh"), "slugify() { :; }\n")
	writeFile(t, filepath.Join(dir, "lib", "nested", "seed-auth.sh"), "seed() { :; }\n")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBaseTag_Is12LowercaseHexAndDeterministic(t *testing.T) {
	dir := writeAssetFixture(t)

	tag, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(tag) {
		t.Errorf("tag = %q, want 12 lowercase hex chars", tag)
	}

	again, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag (second run): %v", err)
	}
	if tag != again {
		t.Errorf("tag not deterministic: %q vs %q", tag, again)
	}
}

func TestBaseTag_ChangesWhenContentChanges(t *testing.T) {
	dir := writeAssetFixture(t)
	before, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	writeFile(t, filepath.Join(dir, "lib", "repo-scope.sh"), "slugify() { echo changed; }\n")
	after, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag after content change: %v", err)
	}
	if before == after {
		t.Error("tag unchanged after a lib/ file's content changed")
	}
}

func TestBaseTag_ChangesWhenFileNameChanges(t *testing.T) {
	dir := writeAssetFixture(t)
	before, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	// Same bytes under a different relative path must alter the digest: the
	// manifest hashes "<hex>  <path>" lines, not just contents.
	old := filepath.Join(dir, "lib", "repo-scope.sh")
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeFile(t, filepath.Join(dir, "lib", "renamed-scope.sh"), string(data))

	after, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag after rename: %v", err)
	}
	if before == after {
		t.Error("tag unchanged after a lib/ file was renamed (path must be part of the digest)")
	}
}

func TestBaseTag_MissingInputsError(t *testing.T) {
	for _, missing := range []string{"Dockerfile.base", "entrypoint.sh", "lib"} {
		t.Run(missing, func(t *testing.T) {
			dir := writeAssetFixture(t)
			if err := os.RemoveAll(filepath.Join(dir, missing)); err != nil {
				t.Fatalf("remove %s: %v", missing, err)
			}
			if _, err := BaseTag(dir); err == nil {
				t.Errorf("BaseTag succeeded with %s missing, want error", missing)
			}
		})
	}
}

// TestBaseTag_MatchesBashPipeline runs the real cenci-sand / smoke.test.sh
// shell pipeline over the same fixture and asserts byte-exact agreement —
// this is the guard against the Go port drifting from the digest
// sandbox/tests/smoke.test.sh independently re-derives.
func TestBaseTag_MatchesBashPipeline(t *testing.T) {
	for _, tool := range []string{"sh", "find", "sort", "xargs", "sha256sum", "cut"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH; skipping bash parity check", tool)
		}
	}

	dir := writeAssetFixture(t)
	goTag, err := BaseTag(dir)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	pipeline := `cd "$1" && find Dockerfile.base entrypoint.sh lib -type f | LC_ALL=C sort | xargs -r sha256sum | sha256sum | cut -c1-12`
	out, err := exec.Command("sh", "-c", pipeline, "sh", dir).Output()
	if err != nil {
		t.Fatalf("bash pipeline: %v", err)
	}
	bashTag := strings.TrimSpace(string(out))

	if goTag != bashTag {
		t.Errorf("Go tag %q != bash pipeline tag %q", goTag, bashTag)
	}
}
