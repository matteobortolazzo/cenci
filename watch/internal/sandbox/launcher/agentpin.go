package launcher

import (
	"errors"
	"fmt"
	"os/exec"
)

// agentUnpinRunArgs builds the hardened `agent-cli.sh unpin <agent>`
// invocation: network-isolated (--network none, since clearing a pin never
// needs to contact the registry — unlike UpdateAgent's install/upgrade),
// otherwise hardened identically to agentUpdateRunArgs (root, --cap-drop=ALL,
// --security-opt=no-new-privileges, the read-write shared agent-CLI volume
// mount). It always runs against MonolithImage for the same reason
// agentUpdateRunArgs does (see that function's doc comment).
func (e *Engine) agentUnpinRunArgs(agent string) []string {
	return []string{"run", "--rm", "--user", "root",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--network", "none",
		"--entrypoint", "/bin/bash",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent",
		MonolithImage, "/usr/local/bin/lib/agent-cli.sh", "unpin", agent}
}

// UnpinAgent clears agent's version pin recorded by the shared agent-CLI
// volume's updater lib (#708's pin/unpin/skip-if-pinned shell contract), so a
// following UpdateAgent call no longer refuses. Callers must not run
// UpdateAgent for the same owner after a failed UnpinAgent — an uncleared pin
// would make that following call re-refuse.
func (e *Engine) UnpinAgent(agent string) error {
	if err := ValidateAgent(agent); err != nil {
		return err
	}
	if err := e.EnsureMonolithImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Clearing version pin for %s in shared volume '%s'...\n", agent, AgentCLIVolumeName(agent))
	if err := e.command(e.agentUnpinRunArgs(agent)...).Run(); err != nil {
		return fmt.Errorf("%s isolated agent unpin failed: %w", e.Runtime, err)
	}
	return nil
}

// UpdateAgentSkipIfPinned updates the host-global agent volume like
// UpdateAgent, but passes --skip-if-pinned so a host-wide sweep (`cenci
// sandbox update-agent --all`) never refuses — and never re-triggers a fresh
// pin — against a volume some other invocation deliberately pinned to an
// exact version; agent-cli.sh's --skip-if-pinned contract (#708) leaves a
// pinned volume untouched and exits 0 instead.
func (e *Engine) UpdateAgentSkipIfPinned(agent string) error {
	if err := ValidateAgent(agent); err != nil {
		return err
	}
	if err := e.EnsureMonolithImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Updating %s in shared volume '%s' (used by every sandbox on this host)...\n", agent, AgentCLIVolumeName(agent))
	args := append(e.agentUpdateRunArgs(agent, ""), "--skip-if-pinned")
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s isolated agent updater failed: %w", e.Runtime, err)
	}
	return nil
}

// IsAgentPinRefusal reports whether err is the isolated agent-cli.sh
// updater's exit-2 refusal to update a version-pinned volume (#708's
// pin/unpin contract), mirroring agentVolumePopulated's *exec.ExitError
// classification pattern (engine.go).
func IsAgentPinRefusal(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode() == 2
	}
	return false
}
