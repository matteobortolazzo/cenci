package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveAssetDir_EnvOverride(t *testing.T) {
	dir := writeAssetFixture(t)
	t.Setenv("CENCI_SANDBOX_ASSETS", dir)
	t.Setenv("HOME", t.TempDir())

	got, err := ResolveAssetDir()
	if err != nil {
		t.Fatalf("ResolveAssetDir: %v", err)
	}
	if got != dir {
		t.Errorf("ResolveAssetDir = %q, want %q", got, dir)
	}
}

func TestResolveAssetDir_InvalidEnvOverrideErrorsWithoutFallthrough(t *testing.T) {
	// A valid marketplace checkout exists, but the explicit override is
	// broken — that must be a hard error, never a silent fallthrough.
	home := t.TempDir()
	t.Setenv("HOME", home)
	marketDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "cenci", "sandbox")
	writeAssetFixtureAt(t, marketDir)

	t.Setenv("CENCI_SANDBOX_ASSETS", t.TempDir()) // empty dir, not assets

	if _, err := ResolveAssetDir(); err == nil {
		t.Fatal("ResolveAssetDir succeeded with an invalid CENCI_SANDBOX_ASSETS, want error")
	} else if !strings.Contains(err.Error(), "CENCI_SANDBOX_ASSETS") {
		t.Errorf("error %q does not mention CENCI_SANDBOX_ASSETS", err)
	}
}

func TestResolveAssetDir_MarketplaceHit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CENCI_SANDBOX_ASSETS", "")
	marketDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "cenci", "sandbox")
	writeAssetFixtureAt(t, marketDir)

	got, err := ResolveAssetDir()
	if err != nil {
		t.Fatalf("ResolveAssetDir: %v", err)
	}
	if got != marketDir {
		t.Errorf("ResolveAssetDir = %q, want marketplace dir %q", got, marketDir)
	}
}

func TestResolveAssetDir_CacheFallbackPicksNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CENCI_SANDBOX_ASSETS", "")

	oldDir := filepath.Join(home, ".claude", "plugins", "cache", "cenci", "cenci-sandbox", "1.0.0")
	newDir := filepath.Join(home, ".claude", "plugins", "cache", "cenci", "cenci-sandbox", "1.2.0")
	writeAssetFixtureAt(t, oldDir)
	writeAssetFixtureAt(t, newDir)
	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(oldDir, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := ResolveAssetDir()
	if err != nil {
		t.Fatalf("ResolveAssetDir: %v", err)
	}
	if got != newDir {
		t.Errorf("ResolveAssetDir = %q, want newest cache dir %q", got, newDir)
	}
}

func TestResolveAssetDir_NothingFoundErrorsActionably(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CENCI_SANDBOX_ASSETS", "")

	_, err := ResolveAssetDir()
	if err == nil {
		t.Fatal("ResolveAssetDir succeeded with no assets anywhere, want error")
	}
	if !strings.Contains(err.Error(), "CENCI_SANDBOX_ASSETS") {
		t.Errorf("error %q does not point at the CENCI_SANDBOX_ASSETS escape hatch", err)
	}
}

// writeAssetFixtureAt writes the standard fixture at an exact path.
func writeAssetFixtureAt(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Dockerfile.base"), "FROM ubuntu:24.04\n")
	writeFile(t, filepath.Join(dir, "entrypoint.sh"), "#!/bin/sh\nexec \"$@\"\n")
	writeFile(t, filepath.Join(dir, "lib", "repo-scope.sh"), "slugify() { :; }\n")
}
