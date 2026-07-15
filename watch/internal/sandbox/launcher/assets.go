package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ResolveAssetDir locates the cenci-sandbox plugin's asset directory — the
// one holding Dockerfile.base, entrypoint.sh, lib/ (and Dockerfile,
// fragments/ for the full builds). The bash launcher used its own script
// location (SCRIPT_DIR); the native launcher ships inside the cenci-watch
// binary while the assets ship with the cenci-sandbox plugin, so resolution
// is layered, mirroring install.sh's find_plugin_path:
//
//  1. CENCI_SANDBOX_ASSETS env override (dev checkouts, tests, version-skew
//     escape hatch). An override that doesn't hold the assets is a hard
//     error, never a silent fallthrough.
//  2. Marketplace checkouts (stable across plugin updates):
//     ~/.claude/plugins/marketplaces/*/sandbox, then ~/.codex/... .
//  3. Versioned plugin cache, newest by modtime:
//     ~/.claude/plugins/cache/*/cenci-sandbox/*, then ~/.codex/... .
func ResolveAssetDir() (string, error) {
	if dir := os.Getenv("CENCI_SANDBOX_ASSETS"); dir != "" {
		if isAssetDir(dir) {
			return dir, nil
		}
		return "", fmt.Errorf("CENCI_SANDBOX_ASSETS=%s does not contain the sandbox assets (Dockerfile.base, entrypoint.sh, lib/)", dir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate sandbox assets: %w (set CENCI_SANDBOX_ASSETS to the cenci-sandbox asset directory)", err)
	}

	for _, pattern := range []string{
		filepath.Join(home, ".claude", "plugins", "marketplaces", "*", "sandbox"),
		filepath.Join(home, ".codex", "plugins", "marketplaces", "*", "sandbox"),
	} {
		matches, _ := filepath.Glob(pattern)
		sort.Strings(matches)
		for _, m := range matches {
			if isAssetDir(m) {
				return m, nil
			}
		}
	}

	var newest string
	var newestMod time.Time
	for _, pattern := range []string{
		filepath.Join(home, ".claude", "plugins", "cache", "*", "cenci-sandbox", "*"),
		filepath.Join(home, ".codex", "plugins", "cache", "*", "cenci-sandbox", "*"),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if !isAssetDir(m) {
				continue
			}
			info, err := os.Stat(m)
			if err != nil {
				continue
			}
			if newest == "" || info.ModTime().After(newestMod) {
				newest = m
				newestMod = info.ModTime()
			}
		}
	}
	if newest != "" {
		return newest, nil
	}

	return "", fmt.Errorf("sandbox assets not found (no cenci-sandbox marketplace checkout or plugin cache under %s); install the cenci-sandbox plugin or set CENCI_SANDBOX_ASSETS to the asset directory", home)
}

// isAssetDir reports whether dir holds the sandbox build inputs the launcher
// needs: regular files Dockerfile.base and entrypoint.sh plus the lib/
// directory (the exact BASE_TAG inputs).
func isAssetDir(dir string) bool {
	for _, f := range []string{"Dockerfile.base", "entrypoint.sh"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	info, err := os.Stat(filepath.Join(dir, "lib"))
	return err == nil && info.IsDir()
}
