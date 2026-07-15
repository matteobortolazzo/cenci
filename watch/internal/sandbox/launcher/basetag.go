package launcher

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// BaseTag computes the content-hash tag of the base image build inputs
// (Dockerfile.base + entrypoint.sh + every file under lib/), byte-exact with
// the bash pipeline in sandbox/cenci-sand — which is independently re-derived
// by sandbox/tests/smoke.test.sh, so any drift here breaks that suite:
//
//	cd <assetDir> && find Dockerfile.base entrypoint.sh lib -type f |
//	    LC_ALL=C sort | xargs -r sha256sum | sha256sum | cut -c1-12
//
// The byte-exact details: paths are RELATIVE to assetDir (so the digest is
// identical regardless of the absolute checkout path), sorted bytewise
// (LC_ALL=C), each manifest line is sha256sum's two-space text format
// ("<hex>  <path>\n"), and the tag is the first 12 hex chars of the sha256
// over those manifest bytes.
func BaseTag(assetDir string) (string, error) {
	// `find A B C -type f` hard-fails when any argument is missing; mirror
	// that with explicit stats (never inferred from a downstream error).
	for _, p := range []string{"Dockerfile.base", "entrypoint.sh", "lib"} {
		if _, err := os.Stat(filepath.Join(assetDir, p)); err != nil {
			return "", fmt.Errorf("failed to enumerate base image build inputs (Dockerfile.base, entrypoint.sh, lib/): %w", err)
		}
	}

	var files []string
	for _, p := range []string{"Dockerfile.base", "entrypoint.sh"} {
		info, err := os.Stat(filepath.Join(assetDir, p))
		if err != nil {
			return "", fmt.Errorf("failed to enumerate base image build inputs (Dockerfile.base, entrypoint.sh, lib/): %w", err)
		}
		if info.Mode().IsRegular() {
			files = append(files, p)
		}
	}
	err := filepath.WalkDir(filepath.Join(assetDir, "lib"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(assetDir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to enumerate base image build inputs (Dockerfile.base, entrypoint.sh, lib/): %w", err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no base image build inputs found (Dockerfile.base, entrypoint.sh, lib/)")
	}

	sort.Strings(files) // bytewise, matching LC_ALL=C sort

	var manifest bytes.Buffer
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(assetDir, filepath.FromSlash(f)))
		if err != nil {
			return "", fmt.Errorf("failed to hash base image build input %s: %w", f, err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&manifest, "%x  %s\n", sum, f)
	}

	sum := sha256.Sum256(manifest.Bytes())
	return fmt.Sprintf("%x", sum)[:12], nil
}
