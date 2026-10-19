package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const remoteUsage = `Usage:
  cenci remote claude [--name NAME] [--dir PATH] [--dry-run] [-- agent-args...]
  cenci remote codex [start|pair|stop] [--dir PATH] [--dry-run] [-- agent-args...]
  cn remote ...                     same commands through the cn alias

Runs the installed agent directly on this machine, without a cenci container.
Claude --name sets the title visible on your phone. Codex does not expose an
equivalent name option; name chats in its client. Existing agent permissions apply.
For Codex pairing: run 'cn remote codex start', then 'cn remote codex pair'.
The native CLI handles authentication, eligibility, and pairing output.
`

// runRemote replaces this process so the native CLI owns terminal IO, signal
// handling, and exit status. It does not start a shell or change agent permissions.
func runRemote(args []string) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = fmt.Fprint(os.Stdout, remoteUsage)
		return
	}
	if len(args) == 0 || (args[0] != "claude" && args[0] != "codex") {
		fmt.Fprintln(os.Stderr, "cenci remote: expected claude or codex; see 'cenci remote --help'")
		os.Exit(2)
	}
	agent := args[0]
	args = args[1:]
	argv := []string{agent, "remote-control"}
	if agent == "codex" && len(args) > 0 {
		switch args[0] {
		case "start", "pair", "stop":
			argv = append(argv, args[0])
			args = args[1:]
		}
	}

	var passthrough []string
	for i, arg := range args {
		if arg == "--" {
			passthrough, args = args[i+1:], args[:i]
			break
		}
	}
	fs := flag.NewFlagSet("remote "+agent, flag.ExitOnError)
	name := fs.String("name", "", "Claude remote session title")
	dir := fs.String("dir", ".", "working directory")
	dryRun := fs.Bool("dry-run", false, "print directory and argv without launching")
	fs.Usage = func() { _, _ = fmt.Fprint(fs.Output(), remoteUsage) }
	_ = fs.Parse(args)
	rejectExtra("cenci remote", fs.Args())
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "name" {
			if agent != "claude" {
				fmt.Fprintln(os.Stderr, "cenci remote: --name is supported only for Claude; name Codex chats in its client")
				os.Exit(2)
			}
			if strings.TrimSpace(*name) == "" {
				fmt.Fprintln(os.Stderr, "cenci remote: --name must not be empty")
				os.Exit(2)
			}
			argv = append(argv, "--name", *name)
		}
	})
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "cenci remote: --dir must not be empty")
		os.Exit(2)
	}
	argv = append(argv, passthrough...)
	cwd, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci remote: resolve directory: %v\n", err)
		os.Exit(1)
	}
	info, err := os.Stat(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci remote: directory: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "cenci remote: not a directory: %s\n", cwd)
		os.Exit(1)
	}
	if *dryRun {
		fmt.Printf("directory: %q\nargv:", cwd)
		for _, arg := range argv {
			fmt.Printf(" %q", arg)
		}
		fmt.Println()
		return
	}
	if err := os.Chdir(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "cenci remote: change directory: %v\n", err)
		os.Exit(1)
	}
	path, err := exec.LookPath(agent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci remote: find %s on PATH: %v; install a version supporting remote-control\n", agent, err)
		os.Exit(1)
	}
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "cenci remote: launch %s: %v\n", agent, err)
		os.Exit(1)
	}
}
