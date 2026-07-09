package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentwatch-test-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	binaryPath = filepath.Join(tmp, "agentwatch")

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

	if !strings.Contains(string(output), `agentwatch: unknown subcommand "frobnicate"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`agentwatch: unknown subcommand "frobnicate"`, output)
	}
}

func TestFlagRouting_DashV_NotUnknownSubcommand(t *testing.T) {
	// -v should route to daemon, not be treated as unknown subcommand.
	// The daemon will likely fail (no tmux), but should NOT print
	// "unknown subcommand". Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "-v")
	output, _ := cmd.CombinedOutput()

	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected -v flag NOT to trigger unknown subcommand error, got:\n%s", output)
	}
}

func TestNoArgs_NotUnknownSubcommand(t *testing.T) {
	// No arguments should route to daemon, not unknown subcommand.
	// Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath)
	output, _ := cmd.CombinedOutput()

	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected no-args NOT to trigger unknown subcommand error, got:\n%s", output)
	}
}
