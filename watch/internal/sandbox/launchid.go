package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

// LaunchIDEnvName is the per-exec env var carrying a sandbox launch's unique
// ID into the container (#1252). The launcher creates the ID, records it on the
// launching tmux pane under PaneOptionLaunchID, and passes it here only when
// every pane option was set; the orphan scan reads it back from
// /proc/<pid>/environ and judges liveness by whether the ID is recorded on a
// live pane of the process's socket. Never create-time env (it would land in
// the container-lifetime PID-1 environ and go stale, #356).
const LaunchIDEnvName = "CENCI_LAUNCH_ID"

// SandboxContainerEnvName and SandboxRuntimeEnvName are the per-exec env vars
// that carry the container a launch lives in and the runtime that runs it,
// next to LaunchIDEnvName (#1292). They are exported only when the pane tag
// succeeded (the same moment as the ID) and, like the ID, are never
// create-time env. `cenci pipeline label --transition working` reads all three
// to record the launch on the ticket's pipeline state; the CENCI_SANDBOX_
// prefix is the launcher-variable convention (docs/cli-conventions.md).
const (
	SandboxContainerEnvName = "CENCI_SANDBOX_CONTAINER"
	SandboxRuntimeEnvName   = "CENCI_SANDBOX_RUNTIME"
)

// ValidLaunchRuntime reports whether rt is a runtime a launch may record: the
// closed set docker/podman. Anything else (including "") is rejected, so a
// recorded runtime can never reach an exec argv as a command name of the
// writer's choosing.
func ValidLaunchRuntime(rt string) bool {
	return rt == "docker" || rt == "podman"
}

// The tmux pane user options a launch records on its own pane (#1252). The
// launch ID is the identity the reaper matches; the container and runtime
// let a later consumer (#1253) find the container a pane's launch lives in.
const (
	PaneOptionLaunchID  = "@cenci-launch-id"
	PaneOptionContainer = "@cenci-container"
	PaneOptionRuntime   = "@cenci-runtime"
)

// launchIDBytes is the entropy of one launch ID (hex-encoded to twice as many
// characters).
const launchIDBytes = 16

// launchIDPattern is the launch-ID contract: 32 lowercase hex characters.
var launchIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NewLaunchID returns a fresh launch ID: launchIDBytes from crypto/rand,
// lowercase hex. It always satisfies ValidLaunchID.
func NewLaunchID() (string, error) {
	b := make([]byte, launchIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate launch id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ValidLaunchID reports whether s is a well-formed launch ID. Anything else
// (including the scan's malformedFieldMarker) is malformed and never signaled.
func ValidLaunchID(s string) bool {
	return launchIDPattern.MatchString(s)
}

// ValidPaneID reports whether s is a real tmux pane id (`%<digits>`).
func ValidPaneID(s string) bool {
	return panePattern.MatchString(s)
}

// ValidSocketPath reports whether s is a usable tmux socket path: absolute,
// no tab or newline, length-bounded (see socketPattern).
func ValidSocketPath(s string) bool {
	return socketPattern.MatchString(s)
}

// malformedFieldMarker is the fixed value the in-container scan prints in place
// of a TMUX_PANE, CENCI_TMUX_SOCKET or CENCI_LAUNCH_ID value that is not a
// well-formed field (so it can neither forge nor shift scan rows). It matches
// none of panePattern, socketPattern or the launch-ID shape, so a sanitized
// field always fails open host-side. tests/reap-orphans.test.sh hard-codes the
// same string.
const malformedFieldMarker = "__MALFORMED__"
