package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "cenci-test-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	binaryPath = filepath.Join(tmp, "cenci")

	// Build the binary from the current module root.
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("failed to build binary: " + err.Error())
	}

	os.Exit(m.Run())
}

func TestUnknownSubcommand_ExitCode2(t *testing.T) {
	cmd := exec.Command(binaryPath, "garbage")
	output, err := cmd.CombinedOutput()

	// Expect a non-zero exit.
	if err == nil {
		t.Fatal("expected non-zero exit code for unknown subcommand, got 0")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	if exitErr.ExitCode() != 2 {
		t.Errorf("expected exit code 2, got %d", exitErr.ExitCode())
	}

	if !strings.Contains(string(output), `unknown subcommand "garbage"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`unknown subcommand "garbage"`, output)
	}
}

func TestUnknownSubcommand_ErrorMessageFormat(t *testing.T) {
	cmd := exec.Command(binaryPath, "frobnicate")
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected non-zero exit for unknown subcommand, got 0")
	}

	if !strings.Contains(string(output), `cenci: unknown subcommand "frobnicate"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`cenci: unknown subcommand "frobnicate"`, output)
	}
}

// TestFlagRouting_DashV_NoLongerRoutesToDaemon locks in the BREAKING change:
// a bare top-level flag like -v used to fall through to running the daemon
// in the foreground. It no longer does — it's an unrecognized top-level
// argument like any other, so it exits 2 immediately (no daemon spawn, no
// need for a context timeout).
func TestFlagRouting_DashV_NoLongerRoutesToDaemon(t *testing.T) {
	cmd := exec.Command(binaryPath, "-v")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if strings.Contains(string(output), "cenci starting") {
		t.Errorf("-v must not route to the daemon anymore, got:\n%s", output)
	}
}

// TestBareInvocation_PrintsUsageAndExits2 locks in the BREAKING change: bare
// `cenci` used to run the daemon in the foreground. It now prints usage
// and exits 2 instead — the daemon only starts via the explicit `daemon`
// subcommand group.
func TestBareInvocation_PrintsUsageAndExits2(t *testing.T) {
	cmd := exec.Command(binaryPath)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "Usage:") {
		t.Errorf("expected usage text in output, got:\n%s", output)
	}
	if strings.Contains(string(output), "cenci starting") {
		t.Errorf("bare invocation must not route to the daemon anymore, got:\n%s", output)
	}
}

// TestHelpFlag_PrintsUsageAndExits0 covers the `help`/`-h`/`--help`
// convenience added alongside the bare-invocation BREAKING change above.
func TestHelpFlag_PrintsUsageAndExits0(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		cmd := exec.Command(binaryPath, arg)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", arg, err, output)
		}
		if !strings.Contains(string(output), "Usage:") {
			t.Errorf("%s: expected usage text, got:\n%s", arg, output)
		}
	}
}

func TestVersionSubcommandPrintsVersion(t *testing.T) {
	cmd := exec.Command(binaryPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci dev") {
		t.Errorf("version output = %q, want to contain %q", output, "cenci dev")
	}
}

func TestVersionFlagsRouteToVersion(t *testing.T) {
	for _, flagArg := range []string{"--version", "-version"} {
		cmd := exec.Command(binaryPath, flagArg)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", flagArg, err, output)
		}
		if !strings.Contains(string(output), "cenci dev") {
			t.Errorf("%s output = %q, want to contain %q", flagArg, output, "cenci dev")
		}
		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s output = %q, want NOT to contain %q", flagArg, output, "unknown subcommand")
		}
	}

	// Bare -v is now just an unrecognized top-level argument (see
	// TestFlagRouting_DashV_NoLongerRoutesToDaemon) — it must not print the
	// version banner either.
	cmd := exec.Command(binaryPath, "-v")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "cenci dev") {
		t.Errorf("-v output = %q, want NOT to contain %q", output, "cenci dev")
	}
}

func TestVersionStampedViaLdflags(t *testing.T) {
	stampedPath := filepath.Join(t.TempDir(), "cenci-stamped")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.9-test", "-o", stampedPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build stamped binary: %v", err)
	}

	cmd := exec.Command(stampedPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "9.9.9-test") {
		t.Errorf("version output = %q, want to contain %q (catches a typo'd -X path)", output, "9.9.9-test")
	}
}

// TestSocketDirSubcommandPrintsResolvedDir covers the new `cenci
// socket-dir` CLI command (#217): it must print the resolved SocketDir() path
// to stdout and exit 0, so shell consumers (widget scripts, tests) don't
// reimplement the XDG-vs-fallback logic themselves.
func TestSocketDirSubcommandPrintsResolvedDir(t *testing.T) {
	xdgDir := t.TempDir()
	cmd := exec.Command(binaryPath, "socket-dir")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+xdgDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("socket-dir: %v\n%s", err, output)
	}

	want := filepath.Join(xdgDir, "cenci")
	got := strings.TrimSpace(string(output))
	if got != want {
		t.Errorf("socket-dir output = %q, want %q", got, want)
	}
}

// TestSocketDirSubcommandRoutes locks in that "socket-dir" is recognized as a
// real subcommand (never falls through to the "unknown subcommand" error
// path), independent of what directory it resolves to.
func TestSocketDirSubcommandRoutes(t *testing.T) {
	cmd := exec.Command(binaryPath, "socket-dir")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("socket-dir must route to its own handler, got:\n%s", output)
	}
}
