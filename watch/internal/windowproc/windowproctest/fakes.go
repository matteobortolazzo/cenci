// Package windowproctest provides the shared fake tmux, docker and podman
// binaries the close-path kill tests (internal/sandbox, internal/windowproc,
// internal/closecmd, internal/daemon) drive end to end, in the same spirit as
// internal/tmux/tmuxtest. It is a non-test package so every one of those test
// packages can import it; it imports nothing from internal/sandbox so the
// sandbox package's own tests can use it too.
//
// The fakes are builtin-only shell scripts (tests set PATH to the fake
// directory alone), configured at write time from a Spec, and append one line
// per invocation to a single call log so cross-binary ordering can be asserted.
// The two long in-container scripts are logged as tokens (LaunchProbeToken,
// LivenessToken) so the call-log lines stay readable; every other argument is
// logged verbatim.
package windowproctest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

const (
	// LaunchProbeToken stands for the in-container launch-ID probe script in a
	// logged `exec ... sh -c <script> _ <id>` line.
	LaunchProbeToken = "<launch-probe-script>"
	// LivenessToken stands for the in-container liveness probe script in a
	// logged `exec ... sh -c <script> _ <pid>` line.
	LivenessToken = "<liveness-script>"
	// PaneFormatToken stands for the list-panes -F format in a logged line. The
	// fake substitutes it only when the argument equals PaneFormat exactly, so a
	// regression in the format shows up as a different logged line.
	PaneFormatToken = "<pane-format>"
	// PaneFormat is the exact `list-panes -F` format the killer must request for
	// the user-writable options.
	PaneFormat = "#{pane_id}\t#{pane_pid}\t#{@cenci-launch-id}\t#{@cenci-container}\t#{@cenci-runtime}"
	// OwnedFormatToken stands for the tmux-owned-fields-only list-panes -F format
	// in a logged line (substituted only on an exact match, like PaneFormatToken).
	OwnedFormatToken = "<owned-format>"
	// OwnedFormat is the exact `list-panes -F` format of tmux's own fields
	// (pane_id, pane_pid), which no pane option can forge.
	OwnedFormat = "#{pane_id}\t#{pane_pid}"

	// TransportMarker is the stderr text of a failing fake runtime exec (the
	// container is gone), so a test can assert the cause reached the error.
	TransportMarker = "No such container"
	// TmuxGoneText is the stderr text real tmux prints for a missing window.
	TmuxGoneText = "can't find window"
	// TmuxUnrelatedErrorText is the stderr text of a tmux failure that is not a
	// missing target.
	TmuxUnrelatedErrorText = "protocol version mismatch (client 8, server 7)"
)

// Proc is one fake in-container process.
type Proc struct {
	PID   string
	Start string
	// IgnoreTerm: SIGTERM leaves the process alive; only SIGKILL ends it.
	IgnoreTerm bool
	// ExitsBeforeTerm: the process is listed, but exits before SIGTERM lands --
	// `kill -TERM` fails and the liveness probe then reports it gone.
	ExitsBeforeTerm bool
	// TermFails: `kill -TERM` fails (exit 1) and the process stays alive.
	TermFails bool
	// ReusedStart: the PID now belongs to an unrelated process, so the liveness
	// probe always reports this (different) start time.
	ReusedStart string
	// LateFork: the process only appears in a listing taken after the first
	// SIGKILL (a fork that raced the first listing).
	LateFork bool
	// Dead: already gone before the test starts.
	Dead bool
	// SurvivesKill: SIGKILL does not end the process (a process stuck in
	// uninterruptible sleep), so it is still listed and alive afterwards.
	SurvivesKill bool
	// KillFails: `kill -KILL` fails (exit 1) and the process stays alive.
	KillFails bool
	// ExitsBeforeKill: the process exits between the liveness probe and the
	// SIGKILL -- `kill -KILL` fails and the next liveness probe reports it gone.
	ExitsBeforeKill bool
}

// Pane is one row `tmux list-panes -F PaneFormat` prints.
type Pane struct {
	ID, PanePID, LaunchID, Container, Runtime string
}

// Spec configures the fake binaries.
type Spec struct {
	// Launches maps a launch ID to the in-container processes carrying it. PIDs
	// must be unique across the whole Spec.
	Launches map[string][]Proc
	// ProbeFails makes the launch-ID probe exec fail (the container is gone).
	ProbeFails bool
	// RelistFails makes the launch-ID probe exec fail once the first SIGKILL has
	// been sent -- the bounded re-list after the KILL pass -- while the first
	// listing still works.
	RelistFails bool
	// Unreadable maps a launch ID to host pids the launch-ID probe reports as
	// "__UNREADABLE__<TAB>pid" rows: processes whose environ it could not read.
	Unreadable map[string][]string
	// ProbeOutputs maps a launch ID to the raw stdout the launch-ID probe prints
	// for it, overriding Launches for that ID (to emit malformed rows).
	ProbeOutputs map[string]string
	// LivenessFails makes every liveness probe exec fail.
	LivenessFails bool

	// Panes is what `list-panes` prints, one TAB-separated row each.
	Panes []Pane
	// ListOutput, when non-nil, replaces the formatted Panes verbatim as the
	// option listing (PaneFormat). The tmux-owned listing (OwnedFormat) is derived
	// from it -- the first two TAB fields of every line, empty when missing --
	// unless OwnedOutput is set.
	ListOutput *string
	// OwnedOutput, when non-nil, replaces the derived tmux-owned listing verbatim:
	// what real tmux prints for the pane_id and pane_pid it owns.
	OwnedOutput *string
	// ListGone: list-panes fails with tmux's missing-window message.
	ListGone bool
	// ListFails: list-panes fails with an unrelated tmux error.
	ListFails bool
	// KillWindowGone: kill-window fails with tmux's missing-window message.
	KillWindowGone bool
	// KillWindowFails: kill-window fails with an unrelated tmux error.
	KillWindowFails bool
	// ProbePIDsAtKillWindow lists host pids whose liveness (`kill -0`) the fake
	// kill-window records as "tmux kill-window-check <pid> alive|gone".
	ProbePIDsAtKillWindow []string
}

// Flavor is one runtime x agent combination of a sandbox pane. Every close-path
// test that must hold for Claude and Codex panes (AC15) ranges over Flavors.
type Flavor struct {
	Name, Runtime, Container string
}

// Flavors covers both runtimes and both agent container prefixes.
var Flavors = []Flavor{
	{"docker claude", "docker", "claude-cenci-close"},
	{"docker codex", "docker", "codex-cenci-close"},
	{"podman claude", "podman", "claude-cenci-close"},
	{"podman codex", "podman", "codex-cenci-close"},
}

// SandboxSpec is a spec with one sandbox pane (pane %1) of the flavor, whose
// launch holds procs. panePID should come from HarmlessPanePID: a sandbox pane
// is never signaled on the host, and if a regression did, only that sleeper
// would be hit.
func (f Flavor) SandboxSpec(launchID, panePID string, procs ...Proc) Spec {
	return Spec{
		Launches: map[string][]Proc{launchID: procs},
		Panes:    []Pane{{ID: "%1", PanePID: panePID, LaunchID: launchID, Container: f.Container, Runtime: f.Runtime}},
	}
}

// Fakes is an installed set of fake binaries.
type Fakes struct {
	Dir     string
	CallLog string
}

// Install writes the fakes into a fresh temp directory, makes that directory
// the only PATH entry for the test, and verifies the fakes are what PATH
// resolves to.
func Install(t *testing.T, spec Spec) *Fakes {
	t.Helper()
	dir := t.TempDir()
	f := &Fakes{Dir: dir, CallLog: filepath.Join(dir, "calls.txt")}
	WriteFakes(t, dir, f.CallLog, spec)
	t.Setenv("PATH", dir)
	RequireFakesOnPath(t, dir)
	return f
}

// Calls returns the call log lines in order (nil when nothing was invoked).
func (f *Fakes) Calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.CallLog)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read call log: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// RequireFakesOnPath fails the test unless tmux, docker and podman all resolve
// to the fakes in dir -- a test that silently ran the ambient binaries would
// prove nothing.
func RequireFakesOnPath(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"tmux", "docker", "podman"} {
		got, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("LookPath(%s): %v", name, err)
		}
		if want := filepath.Join(dir, name); got != want {
			t.Fatalf("%s resolves to %s, want the fake at %s", name, got, want)
		}
	}
}

// WriteFakes writes fake tmux, docker and podman executables into dir. They
// append to callLog and keep per-pid state under dir/state.
func WriteFakes(t *testing.T, dir, callLog string, spec Spec) {
	t.Helper()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatalf("create fake state dir: %v", err)
	}
	for _, procs := range spec.Launches {
		for _, p := range procs {
			if p.Dead {
				if err := os.WriteFile(filepath.Join(state, "dead."+p.PID), nil, 0o644); err != nil {
					t.Fatalf("seed dead state: %v", err)
				}
			}
		}
	}
	runtime := runtimeScript(callLog, state, spec)
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), runtime)
	exectest.WriteExecutable(t, filepath.Join(dir, "podman"), runtime)
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), tmuxScript(callLog, spec))
}

func q(s string) string { return exectest.ShellQuote(s) }

func tmuxScript(callLog string, spec Spec) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("line=tmux\n")
	b.WriteString("for a in \"$@\"; do\n  if [ \"$a\" = " + q(PaneFormat) + " ]; then a=" + q(PaneFormatToken) + "; fi\n  if [ \"$a\" = " + q(OwnedFormat) + " ]; then a=" + q(OwnedFormatToken) + "; fi\n  line=\"$line $a\"\ndone\n")
	b.WriteString("printf '%s\\n' \"$line\" >> " + q(callLog) + "\n")

	var out, owned string
	if spec.ListOutput != nil {
		out = *spec.ListOutput
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			f := strings.SplitN(line, "\t", 3)
			for len(f) < 2 {
				f = append(f, "")
			}
			owned += f[0] + "\t" + f[1] + "\n"
		}
	} else {
		for _, p := range spec.Panes {
			out += strings.Join([]string{p.ID, p.PanePID, p.LaunchID, p.Container, p.Runtime}, "\t") + "\n"
			owned += p.ID + "\t" + p.PanePID + "\n"
		}
	}
	if spec.OwnedOutput != nil {
		owned = *spec.OwnedOutput
	}
	b.WriteString("case \"$1\" in\nlist-panes)\n")
	switch {
	case spec.ListGone:
		b.WriteString("  printf '%s\\n' " + q(TmuxGoneText+": 0") + " >&2\n  exit 1\n")
	case spec.ListFails:
		b.WriteString("  printf '%s\\n' " + q(TmuxUnrelatedErrorText) + " >&2\n  exit 1\n")
	default:
		b.WriteString("  if [ \"$5\" = " + q(OwnedFormat) + " ]; then printf '%s' " + q(owned) + "; else printf '%s' " + q(out) + "; fi\n  exit 0\n")
	}
	b.WriteString("  ;;\nkill-window)\n")
	for _, pid := range spec.ProbePIDsAtKillWindow {
		b.WriteString("  if kill -0 " + q(pid) + " 2>/dev/null; then verdict=alive; else verdict=gone; fi\n")
		b.WriteString("  printf 'tmux kill-window-check %s %s\\n' " + q(pid) + " \"$verdict\" >> " + q(callLog) + "\n")
	}
	switch {
	case spec.KillWindowGone:
		b.WriteString("  printf '%s\\n' " + q(TmuxGoneText+": 0") + " >&2\n  exit 1\n")
	case spec.KillWindowFails:
		b.WriteString("  printf '%s\\n' " + q(TmuxUnrelatedErrorText) + " >&2\n  exit 1\n")
	default:
		b.WriteString("  exit 0\n")
	}
	b.WriteString("  ;;\nesac\nexit 0\n")
	return b.String()
}

func runtimeScript(callLog, state string, spec Spec) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("S=" + q(state) + "\n")
	b.WriteString("line=\"${0##*/}\"\n")
	b.WriteString("for a in \"$@\"; do\n  case \"$a\" in\n  *CENCI_LAUNCH_ID*) a=" + q(LaunchProbeToken) + " ;;\n  *__GONE__*) a=" + q(LivenessToken) + " ;;\n  esac\n  line=\"$line $a\"\ndone\n")
	b.WriteString("printf '%s\\n' \"$line\" >> " + q(callLog) + "\n")
	b.WriteString("[ \"$1\" = exec ] || exit 0\n")
	b.WriteString("case \"$5\" in\nsh)\n  case \"$7\" in\n")

	// Launch-ID probe.
	b.WriteString("  *CENCI_LAUNCH_ID*)\n")
	switch {
	case spec.ProbeFails:
		b.WriteString("    printf 'Error response from daemon: " + TransportMarker + ": %s\\n' \"$4\" >&2\n    exit 1\n")
	default:
		if spec.RelistFails {
			b.WriteString("    if [ -f \"$S/killed\" ]; then\n      printf 'Error response from daemon: " + TransportMarker + ": %s\\n' \"$4\" >&2\n      exit 1\n    fi\n")
		}
		b.WriteString("    case \"$9\" in\n")
		for id, out := range spec.ProbeOutputs {
			b.WriteString("    " + q(id) + ") printf '%s' " + q(out) + " ;;\n")
		}
		for id, procs := range spec.Launches {
			b.WriteString("    " + q(id) + ")\n")
			for _, pid := range spec.Unreadable[id] {
				b.WriteString("      printf '__UNREADABLE__\\t%s\\n' " + q(pid) + "\n")
			}
			for _, p := range procs {
				cond := "[ ! -f \"$S/dead." + p.PID + "\" ]"
				if p.LateFork {
					cond = "[ -f \"$S/killed\" ] && " + cond
				}
				b.WriteString("      if " + cond + "; then printf '%s\\t%s\\n' " + q(p.PID) + " " + q(p.Start) + "; fi\n")
			}
			b.WriteString("      ;;\n")
		}
		b.WriteString("    esac\n    exit 0\n")
	}
	b.WriteString("    ;;\n")

	// Liveness probe.
	b.WriteString("  *__GONE__*)\n")
	if spec.LivenessFails {
		b.WriteString("    printf 'Error response from daemon: " + TransportMarker + ": %s\\n' \"$4\" >&2\n    exit 1\n")
	} else {
		b.WriteString("    case \"$9\" in\n")
		for _, procs := range spec.Launches {
			for _, p := range procs {
				switch {
				case p.ReusedStart != "":
					b.WriteString("    " + q(p.PID) + ") printf '%s\\n' " + q(p.ReusedStart) + " ;;\n")
				default:
					b.WriteString("    " + q(p.PID) + ") if [ -f \"$S/dead." + p.PID + "\" ]; then printf '__GONE__\\n'; else printf '%s\\n' " + q(p.Start) + "; fi ;;\n")
				}
			}
		}
		b.WriteString("    *) printf '__GONE__\\n' ;;\n    esac\n    exit 0\n")
	}
	b.WriteString("    ;;\n  esac\n  ;;\n")

	// Signals.
	b.WriteString("kill)\n  case \"$6\" in\n  -TERM)\n    case \"$7\" in\n")
	for _, procs := range spec.Launches {
		for _, p := range procs {
			dead := ": > \"$S/dead." + p.PID + "\""
			switch {
			case p.ExitsBeforeTerm:
				b.WriteString("    " + q(p.PID) + ") " + dead + "; printf 'OCI runtime exec failed: no such process\\n' >&2; exit 1 ;;\n")
			case p.TermFails:
				b.WriteString("    " + q(p.PID) + ") printf 'Error response from daemon: container is not running\\n' >&2; exit 1 ;;\n")
			case p.IgnoreTerm:
				b.WriteString("    " + q(p.PID) + ") exit 0 ;;\n")
			default:
				b.WriteString("    " + q(p.PID) + ") " + dead + "; exit 0 ;;\n")
			}
		}
	}
	b.WriteString("    esac\n    ;;\n  -KILL)\n    : > \"$S/killed\"\n    case \"$7\" in\n")
	for _, procs := range spec.Launches {
		for _, p := range procs {
			switch {
			case p.SurvivesKill:
				b.WriteString("    " + q(p.PID) + ") ;;\n")
				continue
			case p.KillFails:
				b.WriteString("    " + q(p.PID) + ") printf 'Error response from daemon: container is not running\\n' >&2; exit 1 ;;\n")
				continue
			case p.ExitsBeforeKill:
				b.WriteString("    " + q(p.PID) + ") : > \"$S/dead." + p.PID + "\"; printf 'OCI runtime exec failed: no such process\\n' >&2; exit 1 ;;\n")
				continue
			}
			b.WriteString("    " + q(p.PID) + ") : > \"$S/dead." + p.PID + "\" ;;\n")
		}
	}
	b.WriteString("    esac\n    ;;\n  esac\n  ;;\nesac\nexit 0\n")
	return b.String()
}

// ListCalls is the call-log prefix of every window kill: the listing of tmux's
// own fields, then the listing of the user-writable options, both for target.
func ListCalls(target string) []string {
	return []string{
		"tmux list-panes -t " + target + " -F " + OwnedFormatToken,
		"tmux list-panes -t " + target + " -F " + PaneFormatToken,
	}
}

// SandboxCloseCalls builds the call log a successful sandbox close must
// produce when every pid ignores SIGTERM and dies on SIGKILL, with the grace
// window at 0: list the window's panes, probe the launch, TERM every pid, then
// per pid a liveness probe and a KILL, the bounded re-list, and finally
// kill-window. rt is the runtime binary name and target the exact tmux target.
func SandboxCloseCalls(rt, container, launchID, target string, pids ...string) []string {
	calls := ListCalls(target)
	probe := rt + " exec -u dev " + container + " sh -c " + LaunchProbeToken + " _ " + launchID
	calls = append(calls, probe)
	for _, pid := range pids {
		calls = append(calls, rt+" exec -u root "+container+" kill -TERM "+pid)
	}
	for _, pid := range pids {
		calls = append(calls,
			rt+" exec -u root "+container+" sh -c "+LivenessToken+" _ "+pid,
			rt+" exec -u root "+container+" kill -KILL "+pid,
		)
	}
	calls = append(calls, probe, fmt.Sprintf("tmux kill-window -t %s", target))
	return calls
}
