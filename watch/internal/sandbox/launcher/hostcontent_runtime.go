package launcher

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func (p hostProfile) createArgs(runtime string) []string {
	if len(p.Mounts) == 0 {
		return nil
	}
	args := []string{"--label", "cenci-sand.host-content-id=" + p.ContentIdentity}
	// Both engines include nested mounts by default. Exclude those explicitly:
	// a selected skill never grants the container access to another filesystem.
	nonrecursive := "bind-recursive=disabled"
	if filepath.Base(runtime) == "podman" {
		nonrecursive = "bind-nonrecursive"
	}
	for _, m := range p.Mounts {
		args = append(args, "--mount", "type=bind,src="+m.Source+",dst="+m.Destination+",readonly,"+nonrecursive)
	}
	return args
}

func (p hostProfile) rejectWritableBind(source, name string) error {
	if !p.Authorized {
		return nil
	}
	// Check in both directions. A nested writable bind could expose one
	// file in a read-only skill at a second, writable location.
	paths := []string{p.configPath}
	if p.preferences.Source != "" {
		paths = append(paths, p.preferences.Source)
	}
	for _, m := range p.Mounts {
		paths = append(paths, m.Source)
	}
	for _, path := range paths {
		if identityContains(source, path) || identityContains(path, source) {
			return fmt.Errorf("host profile %q overlaps %q, mounted writable by %s", path, source, name)
		}
	}
	return nil
}

func (p hostProfile) rejectWritableOverlaps(dirs []addDirMount) error {
	for _, d := range dirs {
		if err := p.rejectWritableBind(d.Host, "--add-dir"); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) checkProfileWriters(profile hostProfile) error {
	if !profile.Authorized {
		return nil
	}
	return e.checkWritableSandboxBinds(profile.rejectWritableBind)
}

func (e *Engine) hostContentIdentity(name string) (string, error) {
	out, err := e.inspectFormat(name, `{{with index .Config.Labels "cenci-sand.host-content-id"}}{{.}}{{end}}`, "host content identity")
	return strings.TrimSpace(string(out)), err
}

func (e *Engine) checkHostContentReuse(ctx launchCtx, mounts []reuseMount) error {
	hasContent := false
	for _, m := range mounts {
		if mountKindForDestination(m.Destination) == MountKindHostContent {
			hasContent = true
			break
		}
	}
	if !hasContent && len(ctx.Profile.Mounts) == 0 {
		return nil
	}
	recreate := fmt.Errorf("running container %q has different host content; run 'cenci sandbox stop %s', then relaunch", ctx.Scope.ContainerName, ctx.Scope.ContainerName)
	if !hasContent || len(ctx.Profile.Mounts) == 0 {
		return recreate
	}
	observed, err := e.inspectObservedPosture(ctx.Scope.ContainerName)
	if err != nil {
		return err
	}
	var active []MountPosture
	for _, m := range observed.Mounts {
		if mountKindForDestination(m.Destination) == MountKindHostContent {
			active = append(active, MountPosture{Source: m.Source, Destination: m.Destination, ReadOnly: m.ReadOnly, Kind: MountKindHostContent})
		}
	}
	if !sameHostContent(ctx.Profile.Mounts, active) {
		return recreate
	}
	id, err := e.hostContentIdentity(ctx.Scope.ContainerName)
	if err != nil {
		return err
	}
	if id != ctx.Profile.ContentIdentity {
		return recreate
	}
	return nil
}

func (e *Engine) activateHostProfile(ctx launchCtx) error {
	if err := e.activatePreferences(ctx); err != nil {
		return err
	}
	if len(ctx.Profile.Mounts) == 0 {
		return nil
	}
	cmd := exec.Command(e.Runtime, "exec", "-u", "dev", ctx.Scope.ContainerName, "bash", "/usr/local/bin/lib/host-content.sh", ctx.Agent, "/home/dev")
	cmd.Stderr = e.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not activate host content in %s: %w; check the reported collision or rebuild the sandbox image", ctx.Scope.ContainerName, err)
	}
	return nil
}
