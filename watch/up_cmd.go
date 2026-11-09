package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/up"
)

// upTmuxConfigName is the shipped tmux config `cenci-installer --tmux-defaults`
// writes next to config.json.
const upTmuxConfigName = "tmux.conf"

const upUsage = `Usage: cenci up

Start or enter the cenci tmux server and its "cenci" session, and make sure
the daemon is running. Outside tmux it attaches; inside the cenci server it
switches the client; inside any other tmux server it refuses (exit 1) and tells
you how to attach on the cenci socket. Needs the config that
"cenci-installer --tmux-defaults" installs.
`

// runUp implements `cenci up`: flag parsing, wiring and exit codes (0 success,
// 1 failure or refusal, 2 usage error) around the internal/up engine.
func runUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprint(os.Stdout, upUsage)
			return
		}
		fmt.Fprintf(os.Stderr, "cenci up: %v\n", err)
		os.Exit(2)
	}
	rejectExtra("cenci up", fs.Args())

	configFile := run.DefaultConfigPath()
	if configFile == "" {
		fmt.Fprintln(os.Stderr, "cenci up: cannot resolve the config directory: no home directory is known")
		os.Exit(1)
	}

	err := up.Run(context.Background(), up.Opts{
		ConfigPath:   filepath.Join(filepath.Dir(configFile), upTmuxConfigName),
		EnsureDaemon: ensureDaemonReachable,
		LookPath:     exec.LookPath,
		Exec:         syscall.Exec,
		Env:          os.Environ,
		Stderr:       os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci up: %v\n", err)
		os.Exit(1)
	}
}

// ensureDaemonReachable starts the daemon when it is not running and reports
// whether it ended up reachable: EnsureRunning is silent and bounded, so the
// probe afterwards is what tells a failed start apart.
func ensureDaemonReachable() error {
	daemon.EnsureRunning()
	if !daemon.Alive(ipc.DefaultEventSocketPath()) {
		return errors.New("it did not become reachable")
	}
	return nil
}
