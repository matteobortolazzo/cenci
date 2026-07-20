package launcher

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// repoImagePattern matches per-repo derived images (cenci-sandbox-<slug>:latest,
// built from <repo-root>/.cenci/Dockerfile — see HasRepoImage/SelectImage/
// Slugify in scope.go). It requires a non-empty slug segment after
// "cenci-sandbox-", so it never matches the monolith (cenci-sandbox:latest,
// no "-<slug>" suffix). It deliberately does NOT exclude the base image by
// itself — "base" is a valid slug character sequence, so
// cenci-sandbox-base:latest matches this pattern too and callers must skip
// baseImageRepo+":latest" explicitly, the same way the base-tag loop below
// skips baseImageRepo+":latest" and e.BaseImage().
var repoImagePattern = regexp.MustCompile(`^cenci-sandbox-[a-z0-9_.-]+:latest$`)

// Prune cleans up superseded cenci-sandbox-base tags, dangling images, and
// stopped sandbox containers. With images true, it also prompts (default-deny)
// to remove per-repo derived images (cenci-sandbox-<slug>:latest). With
// volumes true, it also prompts (default-deny) to remove stale home and
// agent-CLI volumes — they hold copied credentials and full session history.
// images and volumes are independent: either, both, or neither may be set.
// Each confirmation reads one line from e.Stdin with no TTY detection, so a
// piped run default-denies exactly like an interactive "just press enter"
// does; when both prompts fire, they share a single bufio.Reader over
// e.Stdin so the second prompt's answer isn't lost to the first reader's
// internal read-ahead buffer.
func (e *Engine) Prune(images, volumes bool) error {
	_, _ = fmt.Fprintln(e.Stdout, "Removing superseded cenci-sandbox-base tags...")
	out, err := exec.Command(e.Runtime, "images", "--format", "{{.Repository}}:{{.Tag}}", baseImageRepo).Output()
	if err != nil {
		_, _ = fmt.Fprintln(e.Stderr, "Error: failed to list cenci-sandbox-base images.")
		return fmt.Errorf("%s images: %w", e.Runtime, err)
	}
	for _, tag := range splitLines(string(out)) {
		if tag == "" || tag == e.BaseImage() || tag == baseImageRepo+":latest" {
			continue
		}
		_, _ = fmt.Fprintf(e.Stdout, "  removing %s\n", tag)
		_ = e.command("rmi", tag).Run() // best-effort, matching `|| true`
	}

	_, _ = fmt.Fprintln(e.Stdout, "Removing stopped sandbox containers...")
	out, err = exec.Command(e.Runtime, "ps", "-a",
		"--filter", "status=exited", "--filter", "status=created",
		"--format", "{{.Names}}").Output()
	if err != nil {
		_, _ = fmt.Fprintln(e.Stderr, "Error: failed to list sandbox containers.")
		return fmt.Errorf("%s ps: %w", e.Runtime, err)
	}
	for _, name := range splitLines(string(out)) {
		if !sandbox.IsSandboxContainerName(name) {
			continue
		}
		_, _ = fmt.Fprintf(e.Stdout, "  removing %s\n", name)
		_ = e.command("rm", name).Run() // best-effort, matching `|| true`
	}

	_, _ = fmt.Fprintln(e.Stdout, "Pruning dangling images...")
	if err := e.command("image", "prune", "-f").Run(); err != nil {
		return fmt.Errorf("%s image prune: %w", e.Runtime, err)
	}

	if !images && !volumes {
		return nil
	}

	// One shared reader for both prompts below: two independent
	// bufio.NewReader(e.Stdin) instances would each buffer ahead from the
	// same underlying stream, so the first reader could swallow bytes meant
	// for the second prompt's confirmation.
	reader := bufio.NewReader(e.Stdin)

	if images {
		if err := e.pruneRepoImages(reader); err != nil {
			return err
		}
	}

	if !volumes {
		return nil
	}
	return e.pruneVolumes(reader)
}

// pruneRepoImages lists every image on the host, filters for per-repo
// derived images (cenci-sandbox-<slug>:latest), and after an interactive
// y/Y confirmation removes each one individually. prune has no repo/cwd
// scope of its own, so there's no reliable way to detect "repo deleted from
// disk" — this always lists every image and lets the user confirm, exactly
// like the volumes tier already does.
//
// Removal here is per-image best-effort (rmi one tag at a time), NOT a
// single batch rmi call like the volumes tier's batch rm. This is
// intentional: a running sandbox holding one repo's image open shouldn't
// block cleanup of every other repo's image. Do not "fix" this to match the
// volumes tier's batch-and-hard-fail approach.
func (e *Engine) pruneRepoImages(reader *bufio.Reader) error {
	out, err := exec.Command(e.Runtime, "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		_, _ = fmt.Fprintln(e.Stderr, "Error: failed to list sandbox images.")
		return fmt.Errorf("%s images: %w", e.Runtime, err)
	}
	var candidates []string
	for _, tag := range splitLines(string(out)) {
		if tag == "" || tag == baseImageRepo+":latest" || !repoImagePattern.MatchString(tag) {
			continue
		}
		candidates = append(candidates, tag)
	}
	if len(candidates) == 0 {
		_, _ = fmt.Fprintln(e.Stdout, "No sandbox images found.")
		return nil
	}

	_, _ = fmt.Fprintln(e.Stderr, "Sandbox images (built from <repo>/.cenci/Dockerfile; safe to rebuild):")
	_, _ = fmt.Fprintln(e.Stderr, strings.Join(candidates, "\n"))
	_, _ = fmt.Fprint(e.Stderr, "Remove these images? [y/N] ")
	// Only a complete line can confirm: bash's `read ... || CONFIRM=""`
	// resets the variable on EOF, so a truncated "y" without a newline
	// default-denies there too.
	confirm := ""
	if line, err := reader.ReadString('\n'); err == nil {
		confirm = strings.TrimRight(line, "\n")
	}
	if confirm == "y" || confirm == "Y" {
		for _, tag := range candidates {
			_, _ = fmt.Fprintf(e.Stdout, "  removing %s\n", tag)
			_ = e.command("rmi", tag).Run() // best-effort, per-image so one busy repo doesn't block the rest
		}
		return nil
	}
	_, _ = fmt.Fprintln(e.Stdout, "Skipping image removal.")
	return nil
}

// pruneVolumes lists home and agent-CLI volumes for every supported agent
// (claude/codex/opencode, per sandbox.SupportedAgents) and, after an
// interactive y/Y confirmation, removes every stale one in a single batch
// call.
func (e *Engine) pruneVolumes(reader *bufio.Reader) error {
	out, err := exec.Command(e.Runtime, "volume", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		_, _ = fmt.Fprintln(e.Stderr, "Error: failed to list sandbox volumes.")
		return fmt.Errorf("%s volume ls: %w", e.Runtime, err)
	}
	var homes, agents, stale []string
	for _, name := range splitLines(string(out)) {
		if sandbox.IsHomeVolumeName(name) {
			homes = append(homes, name)
		} else if sandbox.IsAgentCLIVolumeName(name) {
			agents = append(agents, name)
		}
	}
	stale = append(stale, homes...)
	stale = append(stale, agents...)
	if len(stale) == 0 {
		_, _ = fmt.Fprintln(e.Stdout, "No sandbox volumes found.")
		return nil
	}

	if len(homes) > 0 {
		_, _ = fmt.Fprintln(e.Stderr, "Credential-bearing home volumes (credentials, config, and session history):")
		_, _ = fmt.Fprintln(e.Stderr, strings.Join(homes, "\n"))
	}
	if len(agents) > 0 {
		_, _ = fmt.Fprintln(e.Stderr, "Shared agent CLI volumes (global executables; no credentials):")
		_, _ = fmt.Fprintln(e.Stderr, strings.Join(agents, "\n"))
	}
	_, _ = fmt.Fprint(e.Stderr, "Remove these volumes? [y/N] ")
	// Only a complete line can confirm: bash's `read ... || CONFIRM=""`
	// resets the variable on EOF, so a truncated "y" without a newline
	// default-denies there too.
	confirm := ""
	if line, err := reader.ReadString('\n'); err == nil {
		confirm = strings.TrimRight(line, "\n")
	}
	if confirm == "y" || confirm == "Y" {
		if err := e.command(append([]string{"volume", "rm"}, stale...)...).Run(); err != nil {
			return fmt.Errorf("%s volume rm: %w", e.Runtime, err)
		}
		return nil
	}
	_, _ = fmt.Fprintln(e.Stdout, "Skipping volume removal.")
	return nil
}
