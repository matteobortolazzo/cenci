package autostart

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// launchd is the macOS backend: a launchd agent in ~/Library/LaunchAgents,
// bootstrapped into the user's gui/<uid> domain.
type launchd struct{ opts Options }

var errNoLaunchdSession = errors.New("no launchd user session available")

func (l *launchd) domain() string { return "gui/" + strconv.Itoa(l.opts.UID) }

func (l *launchd) target() string { return l.domain() + "/" + LaunchdLabel }

func (l *launchd) plistPath() string {
	return filepath.Join(l.opts.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

func (l *launchd) ctl(ctx context.Context, args ...string) (string, error) {
	return run(ctx, "launchctl", args...)
}

// requireSession fails unless launchctl exists and the gui/<uid> domain does,
// which an SSH-only session lacks.
func (l *launchd) requireSession(ctx context.Context) error {
	if _, err := l.ctl(ctx, "print", l.domain()); err != nil {
		return errNoLaunchdSession
	}
	return nil
}

func (l *launchd) loaded(ctx context.Context) bool {
	_, err := l.ctl(ctx, "print", l.target())
	return err == nil
}

// Status reports a plist at the cenci path as enabled when launchd has it
// loaded and disabled when it does not.
func (l *launchd) Status(ctx context.Context) State {
	if l.requireSession(ctx) != nil {
		return StateUnsupported
	}
	if !fileExists(l.plistPath()) {
		return StateNotInstalled
	}
	if !l.loaded(ctx) {
		return StateDisabled
	}
	return StateEnabled
}

func (l *launchd) Install(ctx context.Context, force bool) (Outcome, error) {
	if err := l.requireSession(ctx); err != nil {
		return "", err
	}
	if err := checkLauncher(l.opts.Home); err != nil {
		return "", err
	}
	vars, err := captureEnvironment(l.opts.LookupEnv)
	if err != nil {
		return "", err
	}
	content, err := l.render(vars)
	if err != nil {
		return "", err
	}
	changed, err := prepareFile(l.plistPath(), content, launchdMarker, force)
	if err != nil {
		return "", err
	}
	loaded := l.loaded(ctx)
	if loaded && changed {
		// A loaded agent keeps running its old definition; reload it.
		if _, err := l.ctl(ctx, "bootout", l.target()); err != nil {
			return "", err
		}
		loaded = false
	}
	if !loaded {
		if err := l.opts.handover(func() bool { return false }); err != nil {
			return "", err
		}
		if _, err := l.ctl(ctx, "bootstrap", l.domain(), l.plistPath()); err != nil {
			return "", err
		}
	}
	if err := l.opts.waitReady(); err != nil {
		return "", err
	}
	if !changed {
		return OutcomeAlreadyInstalled, nil
	}
	return OutcomeInstalled, nil
}

func (l *launchd) Uninstall(ctx context.Context, force bool) (Outcome, error) {
	path := l.plistPath()
	exists, err := checkRemovable(path, launchdMarker, force)
	if err != nil {
		return "", err
	}
	if !exists {
		return OutcomeNotInstalled, nil
	}
	if err := l.requireSession(ctx); err != nil {
		return "", err
	}
	if l.loaded(ctx) {
		if _, err := l.ctl(ctx, "bootout", l.target()); err != nil {
			return "", err
		}
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return OutcomeRemoved, nil
}

// running reports whether the loaded agent's job is running. A loaded agent
// that is not running does not own a live daemon: a daemon that exited 0 through
// the already-running path is not restarted by KeepAlive.
func (l *launchd) running(ctx context.Context) bool {
	out, err := l.ctl(ctx, "print", l.target())
	return err == nil && strings.Contains(out, "state = running")
}

// Restart hands over from a daemon the agent does not own before kickstarting
// it; otherwise the agent's start would hit the already-running path and exit 0.
func (l *launchd) Restart(ctx context.Context) error {
	if err := l.opts.handover(func() bool { return l.running(ctx) }); err != nil {
		return err
	}
	_, err := l.ctl(ctx, "kickstart", "-k", l.target())
	return err
}

// render fills the template with XML-escaped values in a single pass, so a value
// that happens to contain a placeholder is never expanded again.
func (l *launchd) render(vars []envVar) (string, error) {
	home, err := xmlEscape(l.opts.Home)
	if err != nil {
		return "", err
	}
	entries := make([]string, 0, len(vars))
	for _, v := range vars {
		value, err := xmlEscape(v.value)
		if err != nil {
			return "", err
		}
		entries = append(entries, "\t\t<key>"+v.name+"</key>\n\t\t<string>"+value+"</string>")
	}
	return strings.NewReplacer(
		"@CENCI_HOME@", home,
		"@CENCI_ENVIRONMENT@", strings.Join(entries, "\n"),
	).Replace(l.opts.Templates.Launchd), nil
}

func xmlEscape(s string) (string, error) {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return "", err
	}
	return buf.String(), nil
}
