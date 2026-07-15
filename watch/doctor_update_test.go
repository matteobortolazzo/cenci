package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
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
func writeFakeCenciInstaller(t *testing.T, dir, captureFile string, exitCode int) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + exectest.ShellQuote(captureFile) + "\n" +
		"printf 'cenci-installer stdout marker\\n'\n" +
		"printf 'cenci-installer stderr marker\\n' >&2\n" +
		"exit " + itoa(exitCode) + "\n"
	exectest.WriteExecutable(t, filepath.Join(dir, "cenci-installer"), body)
}

// -- doctor ---------------------------------------------------------------

func TestDoctor_ExecsWrapperWithDoctorMode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciInstaller(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "doctor")
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", exitErr.ExitCode())
	}
}

func TestDoctor_MissingWrapperOnPath_Exits1WithClearError(t *testing.T) {
	dir := t.TempDir() // no cenci-installer fake

	cmd := exec.Command(binaryPath, "doctor")
	cmd.Env = append(os.Environ(), "PATH="+dir)
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

func TestUpdate_MissingWrapperOnPath_Exits1WithClearError(t *testing.T) {
	dir := t.TempDir() // no cenci-installer fake

	cmd := exec.Command(binaryPath, "update")
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
	cmd.Env = append(os.Environ(), "PATH="+dir)
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
