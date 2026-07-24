package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// RepoDindConfig reads <repoRoot>/.cenci/config.json's "sandbox.dind" key,
// mirroring internal/run/template.go's Load pattern (stdlib-only JSON read),
// and returns the resolved boolean plus an error. Only an absent config
// file, an absent "sandbox"/"dind" key, and an explicit `"dind":false`
// resolve to a safe (false, nil) off state. Every other failure class — an
// unreadable file, unparsable JSON, a non-object top level, or a
// wrong-typed "sandbox"/"dind" field — resolves to (false, error): a plain,
// path-bearing, non-usage error (IsUsage(err) is false), so corrupt or
// malformed config hard-fails the caller (exit 1) instead of silently
// launching with dind off (#632). A two-stage decode
// (map[string]any/json.RawMessage) is used instead of a typed struct so a
// wrong-typed field is distinguishable from an absent one.
func RepoDindConfig(repoRoot string) (bool, error) {
	configPath := filepath.Join(repoRoot, ".cenci", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", configPath, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return false, fmt.Errorf("%s: top level must be an object: %w", configPath, err)
		}
		return false, fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if top == nil {
		return false, fmt.Errorf("%s: top level must be an object", configPath)
	}

	sandboxRaw, ok := top["sandbox"]
	if !ok {
		return false, nil
	}
	var sandbox map[string]json.RawMessage
	if err := json.Unmarshal(sandboxRaw, &sandbox); err != nil {
		return false, fmt.Errorf("%s: \"sandbox\" must be an object: %w", configPath, err)
	}

	dindRaw, ok := sandbox["dind"]
	if !ok {
		return false, nil
	}
	var dind bool
	if err := json.Unmarshal(dindRaw, &dind); err != nil {
		return false, fmt.Errorf("%s: \"dind\" must be a boolean: %w", configPath, err)
	}
	return dind, nil
}

// ResolveDind resolves whether dind mode is on for this launch, in
// precedence order: --dind and --no-dind together is a usage error;
// --no-dind always wins, never errors on scope, and never reads repo config
// (an escape hatch that must always work even with corrupt config on disk);
// --dind turns dind on; otherwise the repo config (RepoDindConfig) decides.
// A RepoDindConfig read/parse error is propagated unchanged — a plain,
// non-usage hard error (exit 1) — because corrupt or wrong-typed stored
// config must never silently resolve to dind-off (#632). Requesting dind-on
// (via --dind or config) outside repo scope is a usage error — dind is only
// ever available in repo scope (#585).
func ResolveDind(opts Options, scope Scope) (bool, error) {
	if opts.Dind && opts.NoDind {
		return false, usageErrorf("--dind and --no-dind cannot be combined")
	}
	if opts.NoDind {
		return false, nil
	}

	on := opts.Dind
	if !on && scope.WorkspaceScope == "repo" {
		var err error
		on, err = RepoDindConfig(scope.RepoRoot)
		if err != nil {
			return false, err
		}
	}
	if on && scope.WorkspaceScope != "repo" {
		return false, usageErrorf("--dind requires repo scope (a git repository); it is not supported in legacy/default scope")
	}
	return on, nil
}

// dindPreflight rejects a dind launch when the resolved outer runtime isn't
// Docker (sysbox-runc is only ever registered with Docker, never Podman), and
// when sysbox-runc isn't registered with it — returning a hard error with
// install pointers for the two supported hosts (#585).
func (e *Engine) dindPreflight() error {
	if e.Runtime != "docker" {
		return fmt.Errorf("dind mode requires Docker as the outer container runtime (got %q); Podman does not support the sysbox-runc OCI runtime dind relies on", e.Runtime)
	}
	registered, err := sandbox.SysboxRegistered(e.Runtime)
	if err != nil {
		return fmt.Errorf("checking sysbox-runc registration with %s: %w", e.Runtime, err)
	}
	if !registered {
		return fmt.Errorf("dind mode requires the sysbox-runc OCI runtime to be registered with Docker, but it isn't.\n" +
			"Install it: on Arch Linux, the AUR package is sysbox-ce (e.g. 'yay -S sysbox-ce'); on Ubuntu, download the nestybox sysbox-ce .deb from https://github.com/nestybox/sysbox/releases and install it with 'sudo apt install ./sysbox-ce_*.deb'.\n" +
			"Docs: https://github.com/nestybox/sysbox")
	}
	return nil
}
