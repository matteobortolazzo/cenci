package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// -- fakes ---------------------------------------------------------------
//
// These black-box tests exercise the real built `cenci` binary (see
// sandbox_open_test.go for the shared itoa helper and internal/exectest for
// the shared WriteExecutable/ShellQuote helpers) with PATH overridden to a
// temp dir containing a fake `cenci-installer`
// wrapper script that records the argv it receives and exits with a given
// code, mirroring writeFakeCenciSand's pattern for the "sandbox"/"open"
// verbs.

// writeFakeCenciInstaller writes a fake `cenci-installer` to dir that records the
// argv it receives (one arg per line) to captureFile, echoes a marker to
// stdout/stderr so inherited-stdio forwarding can be asserted, and exits with
// exitCode.
func writeFakeCenciInstaller(t *testing.T, home, captureFile string, exitCode int) {
	t.Helper()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir managed bin: %v", err)
	}
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + exectest.ShellQuote(captureFile) + "\n" +
		"printf 'cenci-installer stdout marker\\n'\n" +
		"printf 'cenci-installer stderr marker\\n' >&2\n" +
		"exit " + itoa(exitCode) + "\n"
	exectest.WriteExecutable(t, filepath.Join(dir, "cenci-installer"), body)
}

func wrapperEnv(home, path string) []string {
	return append(os.Environ(), "HOME="+home, "PATH="+path)
}

// -- doctor ---------------------------------------------------------------

func TestDoctor_ExecsWrapperWithDoctorMode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "doctor")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "doctor" {
		t.Errorf("captured argv = %v, want [doctor]", got)
	}
	if !strings.Contains(string(output), "cenci-installer stdout marker") {
		t.Errorf("expected wrapper stdout forwarded, got:\n%s", output)
	}
}

func TestUpdate_ExecsWrapperWithUpdateMode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "update")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "update" {
		t.Errorf("captured argv = %v, want [update]", got)
	}
	if !strings.Contains(string(output), "cenci-installer stdout marker") {
		t.Errorf("expected wrapper stdout forwarded, got:\n%s", output)
	}
}

func TestDoctor_PropagatesWrapperExitCode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 3)

	cmd := exec.Command(binaryPath, "doctor")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
	}
}

func TestUpdate_PropagatesWrapperExitCode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 5)

	cmd := exec.Command(binaryPath, "update")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", exitErr.ExitCode())
	}
}

func TestDoctor_MissingManagedWrapper_Exits1WithClearError(t *testing.T) {
	dir := t.TempDir() // no cenci-installer fake

	cmd := exec.Command(binaryPath, "doctor")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci") {
		t.Errorf("expected error to mention the missing wrapper, got:\n%s", output)
	}
}

func TestUpdate_MissingManagedWrapper_Exits1WithClearError(t *testing.T) {
	dir := t.TempDir() // no cenci-installer fake

	cmd := exec.Command(binaryPath, "update")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci") {
		t.Errorf("expected error to mention the missing wrapper, got:\n%s", output)
	}
}

func TestDoctor_TrailingArgument_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "doctor", "extra")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-installer to never be invoked for a trailing positional")
	}
}

func TestUpdate_TrailingArgument_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "update", "extra")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-installer to never be invoked for a trailing positional")
	}
}

func TestDoctor_UnknownFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "doctor", "--bogus")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-installer to never be invoked for an unknown flag")
	}
}

func TestUpdate_UnknownFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "update", "--bogus")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-installer to never be invoked for an unknown flag")
	}
}

func TestUpdate_ForwardsSupportedInstallerFlags(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(home, "argv.txt")
	writeFakeCenciInstaller(t, home, capture, 0)

	cmd := exec.Command(binaryPath, "update", "--yes", "--build", "--lazyboards")
	cmd.Env = wrapperEnv(home, t.TempDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "update --yes --build --lazyboards" {
		t.Errorf("captured argv = %v, want supported flags forwarded", got)
	}
}

func TestUpdate_RejectsConflictingInstallerFlags(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(home, "argv.txt")
	writeFakeCenciInstaller(t, home, capture, 0)

	cmd := exec.Command(binaryPath, "update", "--build", "--no-build")
	cmd.Env = wrapperEnv(home, t.TempDir())
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("update error = %v, want exit 2; output:\n%s", err, output)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("managed wrapper was invoked for conflicting flags: %v", err)
	}
}

func TestUpdate_UsesManagedWrapperInsteadOfPATHShadow(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(home, "managed-argv.txt")
	writeFakeCenciInstaller(t, home, capture, 0)
	shadowDir := t.TempDir()
	shadowCapture := filepath.Join(home, "shadow-ran")
	exectest.WriteExecutable(t, filepath.Join(shadowDir, "cenci-installer"),
		"#!/bin/sh\ntouch "+exectest.ShellQuote(shadowCapture)+"\nexit 0\n")

	cmd := exec.Command(binaryPath, "update")
	cmd.Env = wrapperEnv(home, shadowDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update: %v\n%s", err, output)
	}
	if _, err := os.Stat(shadowCapture); !os.IsNotExist(err) {
		t.Fatalf("PATH-shadow wrapper ran: %v", err)
	}
	if got := joinArgv(readCapturedArgv(t, capture)); got != "update" {
		t.Errorf("managed wrapper argv = %q, want update", got)
	}
}

// -- uninstall -------------------------------------------------------------

func TestUninstall_ExecsWrapperWithUninstallMode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "uninstall")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "uninstall" {
		t.Errorf("captured argv = %v, want [uninstall]", got)
	}
	if !strings.Contains(string(output), "cenci-installer stdout marker") {
		t.Errorf("expected wrapper stdout forwarded, got:\n%s", output)
	}
}

func TestUninstall_PropagatesWrapperExitCode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 4)

	cmd := exec.Command(binaryPath, "uninstall")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 4 {
		t.Errorf("exit code = %d, want 4", exitErr.ExitCode())
	}
}

func TestUninstall_MissingManagedWrapper_Exits1WithClearError(t *testing.T) {
	dir := t.TempDir() // no cenci-installer fake

	cmd := exec.Command(binaryPath, "uninstall")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci") {
		t.Errorf("expected error to mention the missing wrapper, got:\n%s", output)
	}
}

func TestUninstall_TrailingArgument_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "uninstall", "extra")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-installer to never be invoked for a trailing positional")
	}
}

func TestUninstall_UnknownFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "uninstall", "--bogus")
	cmd.Env = wrapperEnv(dir, dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-installer to never be invoked for an unknown flag")
	}
}

func TestUninstall_UsesManagedWrapperInsteadOfPATHShadow(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(home, "managed-argv.txt")
	writeFakeCenciInstaller(t, home, capture, 0)
	shadowDir := t.TempDir()
	shadowCapture := filepath.Join(home, "shadow-ran")
	exectest.WriteExecutable(t, filepath.Join(shadowDir, "cenci-installer"),
		"#!/bin/sh\ntouch "+exectest.ShellQuote(shadowCapture)+"\nexit 0\n")

	cmd := exec.Command(binaryPath, "uninstall")
	cmd.Env = wrapperEnv(home, shadowDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output)
	}
	if _, err := os.Stat(shadowCapture); !os.IsNotExist(err) {
		t.Fatalf("PATH-shadow wrapper ran: %v", err)
	}
	if got := joinArgv(readCapturedArgv(t, capture)); got != "uninstall" {
		t.Errorf("managed wrapper argv = %q, want uninstall", got)
	}
}
