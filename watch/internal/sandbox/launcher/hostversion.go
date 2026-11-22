package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type hostVersionPolicy struct {
	Mode       string `json:"mode"`
	Executable string `json:"executable"`
	configPath string
}

var versionAccountHome = func() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot resolve OS account home: %w", err)
	}
	return account.HomeDir, nil
}

func globalHostVersionPolicy(agent string) (hostVersionPolicy, error) {
	// An agent store is shared by all repos on a runtime. Repo-level opt-outs
	// apply only to per-session profile features and cannot select store versions.
	home, err := os.UserHomeDir()
	if err != nil {
		return hostVersionPolicy{}, err
	}
	profile, err := resolveHostProfile(agent, Scope{}, home)
	if err != nil {
		return hostVersionPolicy{}, err
	}
	return profile.versionPolicy()
}

func (p hostProfile) versionPolicy() (hostVersionPolicy, error) {
	var policy hostVersionPolicy
	if len(p.Version) == 0 {
		return policy, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(p.Version))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("sandboxHostProfiles version: %w", err)
	}
	switch policy.Mode {
	case "managed":
		if policy.Executable != "" {
			return policy, errors.New("managed version policy does not accept an executable")
		}
	case "follow-host":
		if !filepath.IsAbs(policy.Executable) || filepath.Clean(policy.Executable) != policy.Executable || !addDirPathIsPlain(policy.Executable) {
			return policy, errors.New("follow-host requires a canonical absolute executable path in host settings")
		}
	default:
		return policy, errors.New("sandboxHostProfiles version.mode must be managed or follow-host")
	}
	policy.configPath = p.versionConfigPath
	return policy, nil
}

// Check all ancestors, including those above HOME. A checkout may supply an
// apparent HOME/PATH; neither may turn checkout-owned code into a host probe.
func checkHostVersionPath(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
				return fmt.Errorf("host probe refuses repository path %q", current)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		// Temp homes used by sandboxes/test runners can have a system-owned sticky
		// ancestor; a same-uid attacker already controls that account.
		st := info.Sys().(*syscall.Stat_t)
		stickyAncestor := info.IsDir() && info.Mode()&os.ModeSticky != 0 && st.Uid == 0 && pathContains(current, home) && current != home
		if int(st.Uid) != os.Getuid() && st.Uid != 0 || info.Mode().Perm()&0022 != 0 && !stickyAncestor {
			return fmt.Errorf("untrusted owner or writable permissions at %q", current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

// Error on overflow rather than retaining or printing arbitrary wrapper output.
type versionOutput struct {
	buf bytes.Buffer
	max int
}

func (b *versionOutput) Write(data []byte) (int, error) {
	if len(data) > b.max-b.buf.Len() {
		return 0, errors.New("version probe output too large")
	}
	return b.buf.Write(data)
}

func (p hostVersionPolicy) probe(agent string, e *Engine) (string, error) {
	if filepath.Base(p.Executable) != agent {
		return "", fmt.Errorf("configured host executable must be named %q", agent)
	}
	for _, candidate := range []string{p.configPath, p.Executable} {
		if err := checkHostVersionPath(candidate); err != nil {
			return "", err
		}
	}
	path, err := hostNativeClient(agent, p.Executable)
	if err != nil {
		return "", err
	}
	// The active engine must confirm that running workloads cannot replace the
	// policy or executable. Read-only planning without a runtime never executes it.
	if e.Runtime == "" {
		return "", errors.New("runtime unavailable to check host program exposure")
	}
	if err := e.checkWritableSandboxBinds(func(source, name string) error {
		if identityContains(source, path) {
			return fmt.Errorf("native host client %q is writable by %s", path, name)
		}
		return p.rejectWritableBind(source, name)
	}); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = "/"
	cmd.Env = []string{"HOME=/", "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "LC_ALL=C"}
	cmd.WaitDelay = 100 * time.Millisecond
	out := versionOutput{max: 1024}
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("host --version timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("host --version failed: %w", err)
	}
	line := strings.TrimSpace(out.buf.String())
	var version string
	switch agent {
	case "codex":
		version, _ = strings.CutPrefix(line, "codex-cli ")
		if version == line {
			version = ""
		}
	case "claude":
		version, _ = strings.CutSuffix(line, " (Claude Code)")
		if version == line {
			version = ""
		}
	}
	if !IsExactSemver(version) {
		return "", fmt.Errorf("unsupported %s --version output; expected the official CLI exact version", agent)
	}
	return version, nil
}

func (p hostVersionPolicy) rejectWritableBind(source, name string) error {
	paths := []string{p.configPath, p.Executable}
	if path, err := filepath.EvalSymlinks(p.Executable); err == nil {
		paths = append(paths, path)
		// For the npm dispatcher, protect the platform package as well. This
		// also rejects new writable mounts before its payload is installed.
		if strings.HasSuffix(path, "/node_modules/@openai/codex/bin/codex.js") {
			root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
			if identityContains(root, source) || identityContains(source, root) {
				return fmt.Errorf("host Codex installation %q overlaps a writable bind by %s", root, name)
			}
		}
	}
	for _, path := range paths {
		if identityContains(source, path) {
			return fmt.Errorf("follow-host path %q is writable by %s", path, name)
		}
	}
	return nil
}

// Returns handled=false only for the ordinary managed lifecycle.
func (e *Engine) followHostIfConfigured(agent string, launch, skipPinned bool) (bool, error) {
	policy, err := globalHostVersionPolicy(agent)
	if err != nil {
		return true, err
	}
	if policy.Mode != "follow-host" {
		return false, nil
	}
	exists, err := e.volumeExists(AgentCLIVolumeName(agent))
	if err != nil {
		return true, err
	}
	var status agentVolumeStatus
	if exists {
		status, _, err = e.agentVolumePopulated(agent)
		if err != nil {
			return true, err
		}
	}
	if status.Pin != "" && status.Populated {
		if !skipPinned {
			return true, fmt.Errorf("%w: %s pinned to %s", errHostVolumePinned, agent, status.Pin)
		}
		_, _ = fmt.Fprintf(e.Stdout, "Shared %s is pinned to %s; skipping host match.\n", agent, status.Pin)
		return true, nil
	}
	version, err := policy.probe(agent, e)
	if err == nil && status.Populated && status.Version == version {
		return true, nil
	}
	if err == nil {
		if err = e.EnsureMonolithImage(); err == nil {
			args := append(e.agentUpdateRunArgs(agent, version), "--follow-host")
			err = e.command(args...).Run()
			if err != nil {
				err = fmt.Errorf("could not install verified Linux %s %s: %w", agent, version, err)
			}
		}
	}
	if err != nil {
		mismatch := fmt.Errorf("cannot follow host %s: %w; check version.executable and registry access, then retry", agent, err)
		if launch && status.Populated {
			_, _ = fmt.Fprintf(e.Stderr, "Warning: %v. Continuing with existing %s %s.\n", mismatch, agent, status.Version)
			return true, nil
		}
		return true, mismatch
	}
	return true, nil
}

var errHostVolumePinned = errors.New("shared agent volume has an explicit pin")
