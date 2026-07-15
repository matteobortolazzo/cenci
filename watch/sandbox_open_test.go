package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- fakes -------------------------------------------------------------
//
// These black-box tests exercise the real built `cenci` binary as a
// subprocess (binaryPath, built once in TestMain in main_test.go) with PATH
// overridden to a temp dir containing fake `cenci-sand`/`docker`/`podman`
// scripts. Fakes are plain POSIX `/bin/sh` scripts (not `#!/usr/bin/env ...`)
// so they resolve without depending on the overridden PATH containing a
// shell.

// writeFakeCenciSand writes a fake `cenci-sand` to dir that records the argv
// it receives (one arg per line) to captureFile and exits with exitCode.
func writeFakeCenciSand(t *testing.T, dir, captureFile string, exitCode int) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(captureFile) + "\nexit " + itoa(exitCode) + "\n"
	writeExecutable(t, filepath.Join(dir, "cenci-sand"), body)
}

// writeFakeDocker writes a fake `docker` (or `podman`) to dir that appends
// each invocation's argv (space-joined) as a line to callLog, and — when
// invoked as `<name> ps ...` — prints psOutput to stdout.
func writeFakeDocker(t *testing.T, dir, name, callLog, psOutput string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(callLog) + "\n" +
		"if [ \"$1\" = \"ps\" ]; then printf '%s' " + shellQuote(psOutput) + "; fi\n" +
		"exit 0\n"
	writeExecutable(t, filepath.Join(dir, name), body)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// shellQuote single-quotes s for embedding in a generated shell script body.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// readCapturedArgv reads a fake-cenci-sand capture file into a []string,
// one element per line (trailing blank line from the final \n dropped).
func readCapturedArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", path, err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func joinArgv(argv []string) string {
	return strings.Join(argv, " ")
}

// -- sandbox <batch verb> ------------------------------------------------

func TestSandboxBuild_ForwardsBuildFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--build" {
		t.Errorf("captured argv = %v, want [--build]", got)
	}
}

func TestSandboxBuildBase_ForwardsBuildBaseFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "build-base")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox build-base: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--build-base" {
		t.Errorf("captured argv = %v, want [--build-base]", got)
	}
}

func TestSandboxUpdatePlugins_ForwardsUpdatePluginsFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox update-plugins: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--update-plugins" {
		t.Errorf("captured argv = %v, want [--update-plugins]", got)
	}
}

func TestSandboxReseedCreds_ForwardsReseedCredsFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "reseed-creds")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox reseed-creds: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--reseed-creds" {
		t.Errorf("captured argv = %v, want [--reseed-creds]", got)
	}
}

func TestSandboxReapOrphans_ForwardsReapOrphansFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "reap-orphans")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox reap-orphans: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--reap-orphans" {
		t.Errorf("captured argv = %v, want [--reap-orphans]", got)
	}
}

func TestSandboxPrune_WithoutVolumes_ForwardsOnlyPruneFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "prune")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox prune: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--prune" {
		t.Errorf("captured argv = %v, want [--prune]", got)
	}
}

func TestSandboxPrune_WithVolumes_ForwardsPruneAndVolumesFlags(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "prune", "--volumes")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox prune --volumes: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--prune --volumes" {
		t.Errorf("captured argv = %v, want [--prune --volumes]", got)
	}
}

func TestSandboxBatchVerb_PropagatesChildExitCode(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 7)

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 7 {
		t.Errorf("exit code = %d, want 7", exitErr.ExitCode())
	}
}

func TestSandboxUnknownFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "build", "--bogus")
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
		t.Error("expected cenci-sand to never be invoked for an unknown flag")
	}
}

func TestSandboxUnknownFlag_OnPrune_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "prune", "--bogus")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestSandboxTrailingPositional_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "build", "extra")
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
		t.Error("expected cenci-sand to never be invoked for a trailing positional")
	}
}

func TestSandboxUnknownSubcommand_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "sandbox", "bogus")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestSandboxNoSubcommand_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "sandbox")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

// -- sandbox ls / stop (native Go against docker/podman) -----------------

const canonPSAllOutput = "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
	"codex-cenci-agentstack\tExited (0) 5 minutes ago\tcenci-sandbox:latest\n" +
	"unrelated-container\tUp 1 hour\tnginx:latest\n"

func TestSandboxLs_ListsMatchingContainersFromFakeDocker(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeFakeDocker(t, dir, "docker", callLog, canonPSAllOutput)

	cmd := exec.Command(binaryPath, "sandbox", "ls")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox ls: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "claude-cenci-agentstack") || !strings.Contains(out, "codex-cenci-agentstack") {
		t.Errorf("expected both sandbox containers listed, got:\n%s", out)
	}
	if strings.Contains(out, "unrelated-container") {
		t.Errorf("expected non-sandbox container to be filtered out, got:\n%s", out)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	if !strings.Contains(string(calls), "ps -a") {
		t.Errorf("expected a 'ps -a' invocation, got call log:\n%s", calls)
	}
}

func TestSandboxStop_StopsMatchingContainers(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	// Only running containers are relevant to `stop`.
	psRunning := "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
		"unrelated-container\tUp 1 hour\tnginx:latest\n"
	writeFakeDocker(t, dir, "docker", callLog, psRunning)

	cmd := exec.Command(binaryPath, "sandbox", "stop")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox stop: %v\n%s", err, output)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	callsStr := string(calls)
	if !strings.Contains(callsStr, "stop claude-cenci-agentstack") {
		t.Errorf("expected a 'stop claude-cenci-agentstack' invocation, got call log:\n%s", callsStr)
	}
	if strings.Contains(callsStr, "stop unrelated-container") {
		t.Errorf("expected non-sandbox container to never be stopped, got call log:\n%s", callsStr)
	}
}

func TestSandboxStop_WithFilterArg_OnlyStopsMatchingName(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	psRunning := "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
		"codex-cenci-otherrepo\tUp 1 hour\tcenci-sandbox:latest\n"
	writeFakeDocker(t, dir, "docker", callLog, psRunning)

	cmd := exec.Command(binaryPath, "sandbox", "stop", "agentstack")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox stop agentstack: %v\n%s", err, output)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	callsStr := string(calls)
	if !strings.Contains(callsStr, "stop claude-cenci-agentstack") {
		t.Errorf("expected the matching container to be stopped, got call log:\n%s", callsStr)
	}
	if strings.Contains(callsStr, "stop codex-cenci-otherrepo") {
		t.Errorf("expected the non-matching container to be left alone, got call log:\n%s", callsStr)
	}
}

// -- open ------------------------------------------------------------------

func TestOpenCh_ResolvesClaudeAndHaikuModel(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--agent claude --model haiku" {
		t.Errorf("captured argv = %v, want [--agent claude --model haiku]", got)
	}
}

func TestOpenXs_ResolvesCodexAndSolModel(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "xs")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open xs: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--agent codex --model gpt-5.6-sol" {
		t.Errorf("captured argv = %v, want [--agent codex --model gpt-5.6-sol]", got)
	}
}

func TestOpenAllShortcuts_ResolveExactlyAsCenciSandWould(t *testing.T) {
	cases := []struct {
		token, agent, model string
	}{
		{"ch", "claude", "haiku"},
		{"cs", "claude", "sonnet"},
		{"co", "claude", "opus"},
		{"cf", "claude", "fable"},
		{"xl", "codex", "gpt-5.6-luna"},
		{"xt", "codex", "gpt-5.6-terra"},
		{"xs", "codex", "gpt-5.6-sol"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "argv.txt")
			writeFakeCenciSand(t, dir, capture, 0)

			cmd := exec.Command(binaryPath, "open", tc.token)
			cmd.Env = append(os.Environ(), "PATH="+dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("open %s: %v\n%s", tc.token, err, output)
			}

			want := "--agent " + tc.agent + " --model " + tc.model
			got := readCapturedArgv(t, capture)
			if joinArgv(got) != want {
				t.Errorf("captured argv = %v, want [%s]", got, want)
			}
		})
	}
}

func TestOpenShortcutConflictsWithAgentFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "ch", "--agent", "codex")
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
		t.Error("expected cenci-sand to never be exec'd on a shortcut/--agent conflict")
	}
}

func TestOpenUnknownFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "--bogus")
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
		t.Error("expected cenci-sand to never be exec'd for an unknown flag")
	}
}

func TestOpenUnrecognizedLeadingPositional_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "not-a-shortcut")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestOpen_ForwardsNameShellDockerHostNetworkFlags(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "--name", "mybox", "--shell", "--docker", "--host-network")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	want := "--name mybox --shell --docker --host-network"
	if joinArgv(got) != want {
		t.Errorf("captured argv = %v, want [%s]", got, want)
	}
}

func TestOpen_PassthroughAfterDoubleDash(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "ch", "--", "--resume", "--custom-flag")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	want := "--agent claude --model haiku -- --resume --custom-flag"
	if joinArgv(got) != want {
		t.Errorf("captured argv = %v, want [%s]", got, want)
	}
}

func TestOpen_MissingCenciSandBinary_Exits1(t *testing.T) {
	dir := t.TempDir() // no cenci-sand fake

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
}

// -- argv[0] == "cn" dispatch ------------------------------------------

// buildCnAlias copies the built cenci binary to <dir>/cn so
// filepath.Base(os.Args[0]) == "cn" inside the copy.
func buildCnAlias(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	cnPath := filepath.Join(dir, "cn")
	if err := os.WriteFile(cnPath, data, 0o755); err != nil {
		t.Fatalf("write cn alias: %v", err)
	}
	return cnPath
}

func TestCnArgv0_RoutesToOpen(t *testing.T) {
	binDir := t.TempDir()
	cnPath := buildCnAlias(t, binDir)

	fakeDir := t.TempDir()
	capture := filepath.Join(fakeDir, "argv.txt")
	writeFakeCenciSand(t, fakeDir, capture, 0)

	cmd := exec.Command(cnPath, "xs")
	cmd.Env = append(os.Environ(), "PATH="+fakeDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn xs: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--agent codex --model gpt-5.6-sol" {
		t.Errorf("captured argv = %v, want [--agent codex --model gpt-5.6-sol]", got)
	}
}

func TestCnArgv0_BareInvocationDoesNotErrorLikeCenci(t *testing.T) {
	// A bare `cenci` (no subcommand) exits 2. `cn` with no args is a
	// bare `open` with no shortcut/flags -- valid, not an error.
	binDir := t.TempDir()
	cnPath := buildCnAlias(t, binDir)

	fakeDir := t.TempDir()
	capture := filepath.Join(fakeDir, "argv.txt")
	writeFakeCenciSand(t, fakeDir, capture, 0)

	cmd := exec.Command(cnPath)
	cmd.Env = append(os.Environ(), "PATH="+fakeDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn (bare): %v\n%s", err, output)
	}
}
