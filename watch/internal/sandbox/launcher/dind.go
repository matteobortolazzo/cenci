package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// RepoDindConfig reads <repoRoot>/.cenci/config.json's "sandbox.dind" key,
// mirroring internal/run/template.go's Load pattern (stdlib-only JSON read).
// Every "not explicitly true" input — an absent file, a non-object top
// level, a wrong-typed "sandbox" or "dind" field, unparsable JSON, or a
// missing "sandbox"/"dind" key — resolves to false, silently: a
// config-parsing hiccup must not block every future launch in the repo
// (#585).
func RepoDindConfig(repoRoot string) bool {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".cenci", "config.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Sandbox struct {
			Dind bool `json:"dind"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.Sandbox.Dind
}

// ResolveDind resolves whether dind mode is on for this launch, in
// precedence order: --dind and --no-dind together is a usage error;
// --no-dind always wins and never errors on scope (an escape hatch that must
// always work); --dind turns dind on; otherwise the repo config
// (RepoDindConfig) decides. Requesting dind-on (via --dind or config) outside
// repo scope is a usage error — dind is only ever available in repo scope
// (#585).
func ResolveDind(opts Options, scope Scope) (bool, error) {
	if opts.Dind && opts.NoDind {
		return false, usageErrorf("--dind and --no-dind cannot be combined")
	}
	if opts.NoDind {
		return false, nil
	}

	on := opts.Dind
	if !on && scope.WorkspaceScope == "repo" {
		on = RepoDindConfig(scope.RepoRoot)
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
