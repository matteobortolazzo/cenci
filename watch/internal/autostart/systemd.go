package autostart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// systemd is the Linux backend: a systemd user unit under
// $XDG_CONFIG_HOME/systemd/user (default ~/.config/systemd/user).
type systemd struct{ opts Options }

var errNoSystemdManager = errors.New("no systemd user manager available")

func (s *systemd) unitPath() string {
	base := filepath.Join(s.opts.Home, ".config")
	if xdg, ok := s.opts.LookupEnv("XDG_CONFIG_HOME"); ok && filepath.IsAbs(xdg) {
		base = xdg
	}
	return filepath.Join(base, "systemd", "user", SystemdUnit)
}

func (s *systemd) ctl(ctx context.Context, args ...string) (string, error) {
	return run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// requireManager fails unless `systemctl --user show-environment` succeeds,
// which rules out a missing systemctl, WSL without systemd and bare containers.
func (s *systemd) requireManager(ctx context.Context) error {
	if _, err := s.ctl(ctx, "show-environment"); err != nil {
		return errNoSystemdManager
	}
	return nil
}

// Status asks the manager first only whether it exists; the file at the cenci
// path then decides installed or not, so a unit elsewhere on the manager's
// search path is never mistaken for ours.
func (s *systemd) Status(ctx context.Context) State {
	if s.requireManager(ctx) != nil {
		return StateUnsupported
	}
	if !fileExists(s.unitPath()) {
		return StateNotInstalled
	}
	if _, err := s.ctl(ctx, "is-enabled", SystemdUnit); err != nil {
		return StateDisabled
	}
	return StateEnabled
}

func (s *systemd) Install(ctx context.Context, force bool) (Outcome, error) {
	if err := s.requireManager(ctx); err != nil {
		return "", err
	}
	if err := checkLauncher(s.opts.Home); err != nil {
		return "", err
	}
	vars, err := captureEnvironment(s.opts.LookupEnv)
	if err != nil {
		return "", err
	}
	changed, err := prepareFile(s.unitPath(), s.render(vars), systemdMarker, force)
	if err != nil {
		return "", err
	}
	if _, err := s.ctl(ctx, "daemon-reload"); err != nil {
		return "", err
	}
	if err := s.opts.handover(func() bool { return s.active(ctx) }); err != nil {
		return "", err
	}
	if _, err := s.ctl(ctx, "enable", "--now", SystemdUnit); err != nil {
		return "", err
	}
	if err := s.opts.waitReady(); err != nil {
		return "", err
	}
	if !changed {
		return OutcomeAlreadyInstalled, nil
	}
	return OutcomeInstalled, nil
}

func (s *systemd) Uninstall(ctx context.Context, force bool) (Outcome, error) {
	path := s.unitPath()
	exists, err := checkRemovable(path, systemdMarker, force)
	if err != nil {
		return "", err
	}
	if !exists {
		return OutcomeNotInstalled, nil
	}
	if err := s.requireManager(ctx); err != nil {
		return "", err
	}
	if _, err := s.ctl(ctx, "disable", "--now", SystemdUnit); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	if _, err := s.ctl(ctx, "daemon-reload"); err != nil {
		return "", err
	}
	return OutcomeRemoved, nil
}

func (s *systemd) active(ctx context.Context) bool {
	_, err := s.ctl(ctx, "is-active", SystemdUnit)
	return err == nil
}

// Restart hands over from a daemon the unit does not own (a hook spawned one in
// the gap after a crash, so the unit is inactive) before restarting the unit;
// otherwise the unit's start would hit the already-running path and go inactive.
func (s *systemd) Restart(ctx context.Context) error {
	if err := s.opts.handover(func() bool { return s.active(ctx) }); err != nil {
		return err
	}
	_, err := s.ctl(ctx, "restart", SystemdUnit)
	return err
}

// render fills the template. The replacer runs in a single pass so a value that
// happens to contain a placeholder is never expanded again. A % in a value is
// written as %% because systemd expands specifiers in Environment=.
func (s *systemd) render(vars []envVar) string {
	lines := make([]string, 0, len(vars))
	for _, v := range vars {
		lines = append(lines, `Environment="`+v.name+"="+strings.ReplaceAll(v.value, "%", "%%")+`"`)
	}
	return strings.NewReplacer("@CENCI_ENVIRONMENT@", strings.Join(lines, "\n")).Replace(s.opts.Templates.Systemd)
}
