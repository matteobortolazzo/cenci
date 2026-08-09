package main_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// -- diagnose --verify ---------------------------------------------------
//
// `cenci diagnose [--name <session>] --verify` re-runs the read-only diagnostic
// probes behind the recovery commands `cenci diagnose` already surfaces
// (daemon reachability today) and prints a pass/fail line per check, so an
// operator can confirm a suggested recovery command actually worked.
// --verify must never launch or attach — it stays within diagnose's
// existing read-only contract. These black-box tests drive the real built
// `cenci` binary as a subprocess via diagEnv/writeScriptedRuntimes/
// writeAssetFixture, shared with diagnose_test.go and sandbox_open_test.go
// (same package).

// TestDiagnoseVerify_MissingDaemonSocket_ReportsFail covers verify case (a):
// the daemon's event socket still doesn't exist -> exits 0 (a report, not a
// gate) with a "[fail]" line carrying the event-socket-missing code.
func TestDiagnoseVerify_MissingDaemonSocket_ReportsFail(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, false) // no live events socket

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession", "--verify")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose --verify (missing socket): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "[fail]") {
		t.Errorf("expected a [fail] line, got:\n%s", out)
	}
	if !strings.Contains(out, "CENCI-DAEMON-SOCKET-001") {
		t.Errorf("expected the event-socket-missing code in the verify output, got:\n%s", out)
	}
}

// TestDiagnoseVerify_DaemonReachable_ReportsPass covers verify case (b): the
// daemon is now reachable (the recovery command worked) -> a "[pass]" line,
// and no lingering event-socket-missing code.
func TestDiagnoseVerify_DaemonReachable_ReportsPass(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true) // live events socket

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession", "--verify")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose --verify (reachable): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "[pass]") {
		t.Errorf("expected a [pass] line, got:\n%s", out)
	}
	if strings.Contains(out, "CENCI-DAEMON-SOCKET-001") {
		t.Errorf("did not expect the event-socket-missing code when the daemon is reachable, got:\n%s", out)
	}
}

// TestDiagnoseVerify_MalformedFlagCombination_Exits2 is the usage-error
// case: --verify combined with an unrecognized flag must exit 2 with a
// "cenci diagnose:"-prefixed message naming the actual offending flag
// (-nonsense-flag), not a generic/wrong error — so this genuinely proves
// --verify itself was recognized and parsed before the unrelated flag broke
// parsing, rather than merely reusing whatever "unrecognized flag" message
// an entirely-unregistered --verify would already produce today.
func TestDiagnoseVerify_MalformedFlagCombination_Exits2(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession", "--verify", "--nonsense-flag")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected a usage error exit 2, got %T %v\n%s", err, err, output)
	}
	out := string(output)
	if !strings.Contains(out, "cenci diagnose:") {
		t.Errorf("expected a \"cenci diagnose:\"-prefixed usage error, not the generic unknown-subcommand fallback, got:\n%s", out)
	}
	if !strings.Contains(out, "-nonsense-flag") {
		t.Errorf("expected the usage error to name the actual offending flag -nonsense-flag (proving --verify itself parsed successfully), got:\n%s", out)
	}
}

// TestDiagnoseVerify_NeverLaunchesOrAttaches pins the read-only contract:
// --verify must never invoke the container runtime for launch/attach
// side effects (`run` to create a container, or interactive `exec -it` to
// attach) — only the existing read-only probe helpers. Asserted directly
// against the fake runtime's call log (writeScriptedRuntimes), the
// strongest available evidence no such invocation happened.
func TestDiagnoseVerify_NeverLaunchesOrAttaches(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession", "--verify")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose --verify: %v\n%s", err, output)
	}

	calls, rerr := os.ReadFile(callLog)
	if rerr != nil {
		t.Fatalf("read call log: %v", rerr)
	}
	callsStr := string(calls)
	if strings.Contains(callsStr, "exec -it") {
		t.Errorf("diagnose --verify must never attach interactively (exec -it); calls:\n%s\ndiagnose output:\n%s", callsStr, output)
	}
	for _, line := range strings.Split(strings.TrimRight(callsStr, "\n"), "\n") {
		if strings.HasPrefix(line, "run ") && !strings.Contains(line, "/bin/cat") {
			t.Errorf("diagnose --verify must never launch a workload container (only the short-lived home-volume /bin/cat reads diagnose already performs), got call:\n%s\nfull call log:\n%s", line, callsStr)
		}
	}
}
