package launcher

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// toolchainTag opts in only asset versions that separate their system packages
// from startup scripts. The full BaseTag still governs the final image's
// freshness; this tag deliberately excludes entrypoint.sh and lib/ so changing
// those scripts cannot invalidate every language installation above the base.
func toolchainTag(assetDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(assetDir, "Dockerfile.base"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read toolchain base recipe: %w", err)
	}
	var toolchain, runtime bool
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[0] == "FROM" && fields[2] == "AS" && fields[3] == "toolchain" {
			toolchain = true
		}
		if strings.TrimSpace(line) == "FROM toolchain AS runtime" {
			runtime = true
		}
	}
	if !toolchain || !runtime {
		return "", nil
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("toolchain-%x", sum[:6]), nil
}

// managedDependencyRecipe recognizes the generated, single-stage recipe shape
// without rewriting it. Custom sections may execute or replace startup scripts
// during their build, so leave those recipes on the original complete-base path.
// Complex Dockerfile syntax is deliberately not parsed here: an uncertain shape
// loses an optimization, never its existing build semantics.
func managedDependencyRecipe(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read dependency recipe: %w", err)
	}
	inside, seenBegin, seenEnd := false, false, false
	froms := 0
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		switch line {
		case "# cenci:managed-begin":
			if seenBegin || seenEnd {
				return false, nil
			}
			inside, seenBegin = true, true
			continue
		case "# cenci:managed-end":
			if !inside || seenEnd {
				return false, nil
			}
			inside, seenEnd = false, true
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "# escape=") {
			return false, nil
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !inside || strings.Contains(line, "<<") || strings.Contains(line, "entrypoint.sh") || strings.Contains(line, "/usr/local/bin/lib") {
			return false, nil
		}
		fields := strings.Fields(line)
		// A second FROM would execute deferred triggers immediately, with the
		// asset overlay's context instead of the eventual consumer's context.
		if strings.EqualFold(fields[0], "ONBUILD") {
			return false, nil
		}
		if strings.EqualFold(fields[0], "FROM") {
			if line != "FROM cenci-sandbox-base:${BASE_VERSION}" {
				return false, nil
			}
			froms++
		}
	}
	return seenBegin && seenEnd && froms == 1, nil
}

// buildDerivedImage preserves the legacy build for older installed assets and
// custom repo recipes. Both paths retain the original -f and context, including
// the builder's Dockerfile-specific ignore-file handling.
func (e *Engine) buildDerivedImage(recipe, contextDir, image string, repo bool) error {
	tag, err := toolchainTag(e.AssetDir)
	if err != nil {
		return err
	}
	if tag != "" && repo {
		managed, err := managedDependencyRecipe(recipe)
		if err != nil {
			return err
		}
		if !managed {
			tag = ""
		}
	}
	if tag != "" {
		return e.buildWithRuntimeOverlay(recipe, contextDir, image, tag)
	}
	if err := e.ensureBaseImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", image, e.Runtime)
	args := []string{"build", "--build-arg", "BASE_VERSION=" + e.BaseTag,
		"--label", imageAgentLifecycleLabel + "=" + imageAgentLifecycleValue,
		"--label", imageBaseVersionLabel + "=" + e.BaseTag,
		"-t", image, "-f", recipe, contextDir}
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s build %s: %w", e.Runtime, image, err)
	}
	return nil
}

var buildImageIDPattern = regexp.MustCompile(`^(sha256:)?[0-9a-f]{64}$`)

func (e *Engine) buildWithRuntimeOverlay(recipe, contextDir, image, tag string) error {
	base := baseImageRepo + ":" + tag
	exists, err := e.imageExists(base)
	if err != nil {
		return err
	}
	if !exists {
		_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", base, e.Runtime)
		if err := e.command("build", "--target", "toolchain", "-f", filepath.Join(e.AssetDir, "Dockerfile.base"), "-t", base, e.AssetDir).Run(); err != nil {
			return fmt.Errorf("%s build toolchain base %s: %w", e.Runtime, base, err)
		}
	}

	dir, err := os.MkdirTemp("", "cenci-build-")
	if err != nil {
		return fmt.Errorf("prepare image build: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			_, _ = fmt.Fprintf(e.Stderr, "Warning: could not remove temporary build directory %s: %v\n", dir, err)
		}
	}()
	iidFile := filepath.Join(dir, "stack.iid")
	_, _ = fmt.Fprintf(e.Stdout, "Building %s dependencies with %s (reusing unchanged toolchain layers)...\n", image, e.Runtime)
	if err := e.command("build", "--build-arg", "BASE_VERSION="+tag, "--iidfile", iidFile, "-f", recipe, contextDir).Run(); err != nil {
		return fmt.Errorf("%s build dependency stack for %s: %w", e.Runtime, image, err)
	}
	idBytes, err := os.ReadFile(iidFile)
	if err != nil {
		return fmt.Errorf("read dependency image ID: %w", err)
	}
	id := strings.TrimSpace(string(idBytes))
	if !buildImageIDPattern.MatchString(id) {
		return fmt.Errorf("invalid dependency image ID %q", id)
	}

	// BuildKit resolves a bare sha256:<ID> in FROM as a registry name. Bind the
	// immutable ID to a unique local tag for this build; a shared intermediate
	// tag would let concurrent builds pick up one another's dependencies.
	stackRef := "cenci-sandbox-build:" + filepath.Base(dir)
	if err := e.command("tag", id, stackRef).Run(); err != nil {
		return fmt.Errorf("%s tag dependency image for %s: %w", e.Runtime, image, err)
	}
	defer func() {
		// Remove only the tag created above, never the ID or unrelated caches.
		if err := e.command("rmi", "--no-prune", stackRef).Run(); err != nil {
			_, _ = fmt.Fprintf(e.Stderr, "Warning: could not remove temporary build tag %s: %v\n", stackRef, err)
		}
	}()

	// COPY alone inherits USER, ENTRYPOINT, CMD, ENV, and other image metadata.
	overlay := "FROM " + stackRef + "\n" +
		"COPY --chown=dev:dev entrypoint.sh /usr/local/bin/entrypoint.sh\n" +
		"COPY --chown=dev:dev lib/ /usr/local/bin/lib/\n"
	overlayFile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(overlayFile, []byte(overlay), 0o600); err != nil {
		return fmt.Errorf("write runtime overlay recipe: %w", err)
	}
	_, _ = fmt.Fprintf(e.Stdout, "Adding current sandbox startup scripts to %s...\n", image)
	args := []string{"build",
		"--label", imageAgentLifecycleLabel + "=" + imageAgentLifecycleValue,
		"--label", imageBaseVersionLabel + "=" + e.BaseTag,
		"-t", image, "-f", overlayFile, e.AssetDir}
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s build runtime overlay for %s: %w", e.Runtime, image, err)
	}
	return nil
}
