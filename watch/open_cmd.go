package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// This file implements `cenci open` (plus the "cn" argv[0] alias). It
// launches natively via the internal/sandbox/launcher engine — nothing
// shells out to the sandbox/cenci-sand bash launcher anymore. See
// sandbox_cmd.go for the `cenci sandbox <verb>` group.

// runOpen implements `cenci open [shortcut] [flags] [-- passthrough]`
// (and the "cn" argv[0] alias, which prepends no extra token — args here is
// already everything after the binary name). It launches natively via the
// internal/sandbox/launcher engine, whose final attach execs the container
// runtime in place of this process so the interactive session owns the TTY
// and its exit code propagates.
//
// Grammar: an optional one-token shortcut (ch/cs/co/cf, xl/xt/xs — the
// internal/sandbox shortcut tables) may appear first; after that, only the
// recognized flags below and an optional "--" passthrough sentinel are
// accepted. Any other leading positional is a usage error, matching the
// strict-parsing convention used by the sandbox verbs in sandbox_cmd.go.
func runOpen(args []string) {
	var shortcutToken, shortcutAgent, shortcutModel string
	hasShortcut := false
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		agent, model, ok := sandbox.ResolveShortcut(args[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "cenci open: unrecognized shortcut %q (expected one of ch, cs, co, cf, xl, xt, xs)\n", args[0])
			os.Exit(2)
		}
		shortcutToken, shortcutAgent, shortcutModel = args[0], agent, model
		hasShortcut = true
		args = args[1:]
	}

	// Split off a "--" passthrough sentinel ourselves (rather than relying on
	// the flag package's own "--" handling) so anything after it is forwarded
	// verbatim, while anything else left over after flag parsing is still
	// treated as an unexpected argument below.
	var passthrough []string
	for i, a := range args {
		if a == "--" {
			passthrough = args[i+1:]
			args = args[:i]
			break
		}
	}

	fs := flag.NewFlagSet("open", flag.ExitOnError)
	agentFlag := fs.String("agent", "", "agent to launch (claude, codex, or opencode)")
	modelFlag := fs.String("model", "", "model override")
	nameFlag := fs.String("name", "", "sandbox instance name")
	shellFlag := fs.Bool("shell", false, "attach a shell instead of launching the agent")
	dockerFlag := fs.Bool("docker", false, "mount the host docker/podman socket (opt-in DooD)")
	hostNetworkFlag := fs.Bool("host-network", false, "use host network mode")
	reseedFlag := fs.Bool("reseed-creds", false, "force a credential reseed from the host on the next container create")
	_ = fs.Parse(args)
	rejectExtra("cenci open", fs.Args())

	// A shortcut implies a specific agent; a later explicit --agent that
	// disagrees would silently pair the wrong agent with the shortcut's
	// model, so reject the conflicting combination instead (mirrors
	// cenci-sand's own shortcut/--agent consistency check).
	if hasShortcut && *agentFlag != "" && *agentFlag != shortcutAgent {
		fmt.Fprintf(os.Stderr, "cenci open: shortcut %q selects the %s agent, but --agent %s was also given. Drop the shortcut or the --agent flag so they agree.\n", shortcutToken, shortcutAgent, *agentFlag)
		os.Exit(2)
	}

	finalAgent := shortcutAgent
	if *agentFlag != "" {
		finalAgent = *agentFlag
	}

	finalModel := shortcutModel
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			finalModel = *modelFlag
		}
	})

	// Empty agent/model stay empty here — Launch applies the claude default
	// and the per-agent model default.
	eng, err := launcher.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
		os.Exit(1)
	}
	err = eng.Launch(launcher.Options{
		Agent:       finalAgent,
		Model:       finalModel,
		Name:        *nameFlag,
		Shell:       *shellFlag,
		Docker:      *dockerFlag,
		HostNetwork: *hostNetworkFlag,
		ReseedCreds: *reseedFlag,
		AgentArgs:   passthrough,
	})
	if err == nil {
		return // unreachable in practice: a successful Launch never returns
	}
	if launcher.IsUsage(err) {
		fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
	os.Exit(1)
}
