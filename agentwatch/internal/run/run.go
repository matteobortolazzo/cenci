package run

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/daemon"
)

// Controller is the small consumer-side tmux interface the launcher needs.
// *tmux.ExecClient satisfies it structurally (SetWindowOption already exists;
// CurrentSession/IsGroupedSession/NewWindow are added alongside it). Tests use
// their own mock.
type Controller interface {
	CurrentSession() (string, error)
	IsGroupedSession(session string) (bool, error)
	NewWindow(session, name, shellCommand string) error
	SetWindowOption(target, key, value string) error
}

// Opts holds the resolved launcher inputs, mirroring the `run` flags.
type Opts struct {
	Workflow string
	// Ticket is the full skill argument: a ticket id or task description plus
	// any additional context (e.g. "42 focus on the API layer"). It is
	// substituted whole into {ticket}; only the first token drives gh lookup
	// and the numeric window-name prefix.
	Ticket     string
	Agent      string // --agent; empty falls back to config defaultAgent then "claude"
	Sandbox    bool   // resolved --sandbox value; only honored when SandboxSet
	SandboxSet bool   // whether --sandbox/--no-sandbox was passed (flag > config)
	Model      string // --model
	Session    string // --session; empty uses the current tmux session
	Slug       string // --slug
	ConfigPath string // --config
	DryRun     bool   // --dry-run
	// Dir, when set, is the working directory the window starts in: a
	// `cd <dir> &&` prefix is prepended to the built command so a dispatched
	// session lands in its repo (finding that repo's .plans/ and git tree).
	Dir string

	// Out receives dry-run output; nil defaults to os.Stdout.
	Out io.Writer

	// EnsureDaemon starts the agentwatch daemon if it isn't already running,
	// and waits briefly for it to come up; nil uses the real implementation
	// (daemon.EnsureRunning). Called right before the window is
	// spawned (never on dry-run or a refused grouped session): agent-sand
	// only mounts the event socket into the sandbox if it already exists at
	// container launch (#139), so a window spawned before the daemon is up
	// never gets status wired in for its whole lifetime.
	EnsureDaemon func()
}

// Run resolves the (agent, workflow) command and spawns a detached tmux window
// named <number>-<skill> running it. The window is set automatic-rename off so
// the daemon later preserves the join key instead of overwriting it with a
// detected task name. With DryRun it prints the resolution and spawns nothing.
func Run(opts Opts, ctrl Controller) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	cfg, err := Load(opts.ConfigPath)
	if err != nil {
		return err
	}

	// 1. Resolve agent: flag > config defaultAgent > "claude".
	agent := strings.TrimSpace(opts.Agent)
	if agent == "" {
		agent = cfg.DefaultAgent
	}
	if agent == "" {
		agent = "claude"
	}

	// Resolve sandbox: explicit flag wins, else config default, else sandbox
	// (the container is the mandatory runtime — #98). --no-sandbox opts out.
	sandbox := opts.Sandbox
	if !opts.SandboxSet {
		if cfg.Sandbox != nil {
			sandbox = *cfg.Sandbox
		} else {
			sandbox = true
		}
	}

	// 2 + 5. Resolve template and build the launcher command.
	argv, err := cfg.BuildCommand(agent, opts.Workflow, opts.Ticket, opts.Model, sandbox)
	if err != nil {
		return err
	}
	shellCommand := shellJoin(argv)

	// When a start directory is requested, cd into it first so the session
	// lands in its repo. The directory rides in the command string, so no
	// Controller change is needed.
	if dir := strings.TrimSpace(opts.Dir); dir != "" {
		shellCommand = "cd " + shellQuote(dir) + " && " + shellCommand
	}

	// 3. Resolve target session: flag > current tmux session.
	session := strings.TrimSpace(opts.Session)
	if session == "" {
		s, serr := ctrl.CurrentSession()
		s = strings.TrimSpace(s)
		switch {
		case serr == nil && s != "":
			session = s
		case opts.DryRun:
			session = "(current tmux session)"
		default:
			return fmt.Errorf("not inside a tmux session; pass --session <name> or run from within tmux")
		}
	}

	// 6. Compute the window name: `<number>-<skill>` for a numeric ticket, else
	// the free-text slug. opts.Workflow is always the running skill (refine /
	// design / implement).
	name := windowName(opts.Ticket, opts.Workflow, opts.Slug)
	if name == "" {
		name = slugify(opts.Workflow)
	}

	// 7. Dry-run: print the resolution and stop before touching tmux state.
	if opts.DryRun {
		_, err := fmt.Fprintf(out, "session: %s\nwindow:  %s\ncommand: %s\n", session, name, shellCommand)
		return err
	}

	// 4. Safety guard: refuse grouped sessions — a new window would propagate
	// to every session in the group.
	grouped, err := ctrl.IsGroupedSession(session)
	if err != nil {
		return fmt.Errorf("checking session %q: %w", session, err)
	}
	if grouped {
		return fmt.Errorf("refusing to spawn into grouped session %q: new windows propagate to all sessions in the group; target an ungrouped session with --session", session)
	}

	ensure := opts.EnsureDaemon
	if ensure == nil {
		ensure = daemon.EnsureRunning
	}
	ensure()

	if err := ctrl.NewWindow(session, name, shellCommand); err != nil {
		return fmt.Errorf("creating window %q in session %q: %w", name, session, err)
	}
	// Pin the name so the daemon flags the window ManuallyNamed and preserves
	// the join key instead of renaming it to the detected task.
	target := session + ":" + name
	if err := ctrl.SetWindowOption(target, "automatic-rename", "off"); err != nil {
		return fmt.Errorf("setting automatic-rename off on %q: %w", target, err)
	}
	return nil
}

// shellJoin quotes each arg and joins them into a single POSIX shell command
// line suitable for tmux new-window (which runs it via the shell).
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote single-quotes s when it contains anything outside a conservative
// safe set, escaping embedded single quotes. Safe words pass through unquoted.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !isShellSafe(r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// isShellSafe reports whether r may appear unquoted in a POSIX shell word.
func isShellSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("-_./:=@%+", r)
	}
}
