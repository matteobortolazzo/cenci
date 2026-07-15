package launcher

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// homeVolumePattern matches the per-agent home volume names cenci-sand
// creates (VOLUME_NAME="${CONTAINER_PREFIX}-home-...").
var homeVolumePattern = regexp.MustCompile(`^(claude|codex)-cenci-home-`)

// Prune cleans up superseded cenci-sandbox-base tags, dangling images, and
// stopped sandbox containers. Volumes are left alone unless volumes is true,
// and even then only after an interactive y/Y confirmation — they hold
// copied credentials and full session history. The confirmation reads one
// line from e.Stdin with no TTY detection, so a piped run default-denies
// exactly like an interactive "just press enter" does.
func (e *Engine) Prune(volumes bool) error {
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

	if !volumes {
		return nil
	}

	out, err = exec.Command(e.Runtime, "volume", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		_, _ = fmt.Fprintln(e.Stderr, "Error: failed to list sandbox volumes.")
		return fmt.Errorf("%s volume ls: %w", e.Runtime, err)
	}
	var stale []string
	for _, name := range splitLines(string(out)) {
		if homeVolumePattern.MatchString(name) {
			stale = append(stale, name)
		}
	}
	if len(stale) == 0 {
		_, _ = fmt.Fprintln(e.Stdout, "No sandbox volumes found.")
		return nil
	}

	_, _ = fmt.Fprintln(e.Stderr, "The following volumes hold copied credentials and full session history:")
	_, _ = fmt.Fprintln(e.Stderr, strings.Join(stale, "\n"))
	_, _ = fmt.Fprint(e.Stderr, "Remove these volumes? [y/N] ")
	// Only a complete line can confirm: bash's `read ... || CONFIRM=""`
	// resets the variable on EOF, so a truncated "y" without a newline
	// default-denies there too.
	confirm := ""
	if line, err := bufio.NewReader(e.Stdin).ReadString('\n'); err == nil {
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
