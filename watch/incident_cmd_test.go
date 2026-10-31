package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/incident"
)

const (
	cliTenantID = "11111111-1111-4111-8111-111111111111"
	cliClientID = "22222222-2222-4222-8222-222222222222"
)

// cliWorld is a hermetic host for the built binary: a fake container runtime and
// a fake az first on PATH, both recording every invocation, and a child
// environment with every ambient Azure identity variable removed.
type cliWorld struct {
	bin          string
	runtimeTrace string
	azTrace      string
	stateDir     string
}

func newCLIWorld(t *testing.T) *cliWorld {
	t.Helper()
	w := &cliWorld{bin: t.TempDir(), stateDir: t.TempDir()}
	w.runtimeTrace = filepath.Join(w.bin, "runtime-calls")
	w.azTrace = filepath.Join(w.bin, "az-calls")
	// The runtime answers every call with success and no output; az fails every call.
	writeCLIFake(t, filepath.Join(w.bin, "podman"), w.runtimeTrace, 0)
	writeCLIFake(t, filepath.Join(w.bin, "az"), w.azTrace, 1)
	return w
}

func writeCLIFake(t *testing.T, path, trace string, exit int) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + trace + "'\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

// env returns the child environment: the ambient one without any Azure identity
// variable, and with the fakes first on PATH.
func (w *cliWorld) env() []string {
	ambient := map[string]bool{"IDENTITY_ENDPOINT": true, "IDENTITY_HEADER": true, "MSI_ENDPOINT": true, "MSI_SECRET": true, "IMDS_ENDPOINT": true, "PATH": true}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "AZURE_") || ambient[name] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PATH="+w.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// run executes the built binary and returns its exit code and stderr.
func (w *cliWorld) run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = w.env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0, stderr.String()
		}
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return exit.ExitCode(), stderr.String()
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("cenci %v did not exit; stderr so far: %s", args, stderr.String())
		return -1, ""
	}
}

func readCLITrace(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// config writes a valid incident worker config whose auth member is the raw JSON
// given, or absent when raw is empty.
func (w *cliWorld) config(t *testing.T, rawAuth string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := incident.Config{Enabled: true, StateDir: w.stateDir, Namespace: "contoso.servicebus.windows.net", Queue: "alerts", Concurrency: 1, TimeoutSeconds: 30, MaxAttempts: 2, BudgetUSD: 1,
		Agent: incident.ContainerConfig{Runtime: "podman", Image: "tools:reviewed", APIKeyEnv: "ANTHROPIC_API_KEY", Network: "incident-internal", ProxyURL: "http://proxy:3128"},
		Resources: []incident.Resource{{ID: "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app", Repo: "owner/repo", Dir: repo, Base: "main", Environment: "production",
			Deployments: []incident.Deployment{{Commit: strings.Repeat("a", 40), From: time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)}},
			Workspace:   "00000000-0000-0000-0000-000000000000", Query: "AppExceptions | take 10", Workflow: "Use cenci stages.",
			Runbooks: []string{"RUNBOOK.md"}, Regression: []string{"go", "test", "./..."}, Checks: [][]string{{"go", "test", "./..."}}, TestPaths: []string{"*_test.go"}}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "auth")
	if rawAuth != "" {
		doc["auth"] = json.RawMessage(rawAuth)
	}
	if b, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func certificateAuth(path string) string {
	return `{"mode":"servicePrincipalCertificate","tenantId":"` + cliTenantID + `","clientId":"` + cliClientID + `","certificatePath":"` + path + `"}`
}

// requireRefusedBeforeStartup asserts the run exited 1 with every marker in
// stderr, and that nothing was started: no container runtime call, no az call,
// and no owner identity written to the state directory.
func (w *cliWorld) requireRefusedBeforeStartup(t *testing.T, code int, stderr string, markers ...string) {
	t.Helper()
	if code != 1 {
		t.Fatalf("exit code %d, want 1; stderr: %s", code, stderr)
	}
	if !strings.HasPrefix(stderr, "cenci incident: ") {
		t.Fatalf("stderr must carry the cenci incident prefix: %s", stderr)
	}
	for _, m := range markers {
		if !strings.Contains(stderr, m) {
			t.Fatalf("stderr missing %q: %s", m, stderr)
		}
	}
	if got := readCLITrace(t, w.runtimeTrace); got != "" {
		t.Fatalf("container runtime was invoked before the refusal: %s", got)
	}
	if got := readCLITrace(t, w.azTrace); got != "" {
		t.Fatalf("az was invoked: %s", got)
	}
	if _, err := os.Stat(filepath.Join(w.stateDir, "owner-id")); !os.IsNotExist(err) {
		t.Fatalf("ledger recovery or cleanup started (owner-id present): %v", err)
	}
}

func TestIncidentRunWithoutAuthIsRefusedBeforeAnyStartupWork(t *testing.T) {
	w := newCLIWorld(t)
	code, stderr := w.run(t, "incident", "run", "--config", w.config(t, ""))
	w.requireRefusedBeforeStartup(t, code, stderr, "no explicit worker identity is configured", "Worker identity", "cenci incident setup --config")
}

func TestIncidentRunRefusesInvalidAuthNamingTheField(t *testing.T) {
	cases := []struct{ name, auth, field string }{
		{"client-secret-field", `{"mode":"servicePrincipalCertificate","tenantId":"` + cliTenantID + `","clientId":"` + cliClientID + `","certificatePath":"/etc/w.pem","clientSecret":"hunter2"}`, "auth.clientSecret"},
		{"secret-mode", `{"mode":"servicePrincipalSecret","tenantId":"` + cliTenantID + `","clientId":"` + cliClientID + `"}`, "auth.mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCLIWorld(t)
			code, stderr := w.run(t, "incident", "run", "--config", w.config(t, tc.auth))
			w.requireRefusedBeforeStartup(t, code, stderr, tc.field)
			if strings.Contains(stderr, "hunter2") {
				t.Fatalf("a rejected field's value reached stderr: %s", stderr)
			}
		})
	}
}

func TestIncidentRunRefusesACertificateWithLooseModeBeforeAnyStartupWork(t *testing.T) {
	w := newCLIWorld(t)
	cert := filepath.Join(t.TempDir(), "worker.pem")
	if err := os.WriteFile(cert, []byte("not parsed: the mode is judged first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cert, 0644); err != nil {
		t.Fatal(err)
	}
	code, stderr := w.run(t, "incident", "run", "--config", w.config(t, certificateAuth(cert)))
	w.requireRefusedBeforeStartup(t, code, stderr, cert, "0644", "mode 0600 or 0400")
}

func TestIncidentStatusAndCancelNeedNoAzureIdentity(t *testing.T) {
	w := newCLIWorld(t)
	key := "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"
	ledger := `{"version":1,"incidents":{"` + key + `":{"key":"` + key + `","alertId":"a","status":"queued"}}}`
	if err := os.WriteFile(filepath.Join(w.stateDir, "incidents.json"), []byte(ledger), 0600); err != nil {
		t.Fatal(err)
	}
	if code, stderr := w.run(t, "incident", "status", "--state-dir", w.stateDir); code != 0 {
		t.Fatalf("status exit %d: %s", code, stderr)
	}
	if code, stderr := w.run(t, "incident", "cancel", "--state-dir", w.stateDir, "--id", key); code != 0 {
		t.Fatalf("cancel exit %d: %s", code, stderr)
	}
	if got := readCLITrace(t, w.azTrace); got != "" {
		t.Fatalf("status/cancel invoked az: %s", got)
	}
	if got := readCLITrace(t, w.runtimeTrace); got != "" {
		t.Fatalf("status/cancel invoked the container runtime: %s", got)
	}
}

// Every case exits before any config is read or any endpoint is reached, so no
// verify test here touches the network.
func TestIncidentVerifyUsageErrorsExitTwo(t *testing.T) {
	w := newCLIWorld(t)
	cfg := filepath.Join(t.TempDir(), "missing-config.json")
	cases := []struct {
		name string
		args []string
	}{
		{"no-config", []string{"incident", "verify"}},
		{"state-dir", []string{"incident", "verify", "--config", cfg, "--state-dir", w.stateDir}},
		{"id", []string{"incident", "verify", "--config", cfg, "--id", "k"}},
		{"extra-argument", []string{"incident", "verify", "--config", cfg, "extra"}},
		{"wait-below-range", []string{"incident", "verify", "--config", cfg, "--propagation-wait", "29s"}},
		{"wait-above-range", []string{"incident", "verify", "--config", cfg, "--propagation-wait", "15m1s"}},
		{"wait-zero", []string{"incident", "verify", "--config", cfg, "--propagation-wait", "0s"}},
		{"wait-garbage", []string{"incident", "verify", "--config", cfg, "--propagation-wait", "soon"}},
		{"wait-on-run", []string{"incident", "run", "--config", cfg, "--propagation-wait", "1m"}},
		{"wait-on-status", []string{"incident", "status", "--state-dir", w.stateDir, "--propagation-wait", "1m"}},
		{"wait-on-cancel", []string{"incident", "cancel", "--state-dir", w.stateDir, "--id", "k", "--propagation-wait", "1m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, stderr := w.run(t, tc.args...); code != 2 {
				t.Fatalf("exit %d, want 2; stderr: %s", code, stderr)
			}
		})
	}
	if got := readCLITrace(t, w.azTrace); got != "" {
		t.Fatalf("az was invoked: %s", got)
	}
}

// The bounds are inclusive: 30s and 15m pass the usage gate, so the run reaches
// the config load and fails there (exit 1, not 2).
func TestIncidentVerifyAcceptsTheInclusivePropagationWaitBounds(t *testing.T) {
	w := newCLIWorld(t)
	cfg := filepath.Join(t.TempDir(), "missing-config.json")
	for _, wait := range []string{"30s", "15m"} {
		code, stderr := w.run(t, "incident", "verify", "--config", cfg, "--propagation-wait", wait)
		if code != 1 || !strings.HasPrefix(stderr, "cenci incident: ") {
			t.Fatalf("--propagation-wait %s: exit %d, want 1 from the config load; stderr: %s", wait, code, stderr)
		}
	}
}

func TestIncidentVerifyWithoutAuthIsRefusedLikeRun(t *testing.T) {
	w := newCLIWorld(t)
	config := w.config(t, "")
	runCode, runStderr := w.run(t, "incident", "run", "--config", config)
	verifyCode, verifyStderr := w.run(t, "incident", "verify", "--config", config)
	w.requireRefusedBeforeStartup(t, verifyCode, verifyStderr, "no explicit worker identity is configured", "Worker identity")
	if runCode != verifyCode || runStderr != verifyStderr {
		t.Fatalf("verify and run disagree about a missing identity:\nrun (%d): %s\nverify (%d): %s", runCode, runStderr, verifyCode, verifyStderr)
	}
}

func TestIncidentHelpDocumentsVerify(t *testing.T) {
	out, err := exec.Command(binaryPath, "help").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "incident verify --config PATH [--propagation-wait D]") {
		t.Fatalf("help does not document incident verify and its flags:\n%s", out)
	}
	_, stderr := newCLIWorld(t).run(t, "incident", "unknown")
	if !strings.Contains(stderr, "use run, verify, status, cancel, or setup") {
		t.Fatalf("the unknown-verb hint must list verify: %q", stderr)
	}
}

func TestIncidentCLIOptInAndUsage(t *testing.T) {
	for _, args := range [][]string{{"incident"}, {"incident", "unknown"}, {"incident", "run"}, {"incident", "status"}, {"incident", "setup"}, {"incident", "cancel", "--state-dir", "/tmp", "--id", "x", "extra"}} {
		out, err := exec.Command(binaryPath, args...).CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	// The unknown-verb hint lists every verb, setup included.
	for _, args := range [][]string{{"incident"}, {"incident", "unknown"}} {
		if out, _ := exec.Command(binaryPath, args...).CombinedOutput(); !strings.Contains(string(out), "use run, verify, status, cancel, or setup") {
			t.Fatalf("%v: usage hint lacks the setup verb: %s", args, out)
		}
	}
	// A missing --config for setup is a usage error that names the flag, not the
	// unknown-verb hint.
	if out, _ := exec.Command(binaryPath, "incident", "setup").CombinedOutput(); !strings.Contains(string(out), "--config") {
		t.Fatalf("incident setup without --config must name the missing flag: %s", out)
	}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(binaryPath, "incident", "run", "--config", config).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "disabled") {
		t.Fatalf("opt-in: %v %s", err, out)
	}
}

func TestIncidentVerifyOnDisabledConfigIsRefusedLikeRun(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := newCLIWorld(t)
	runCode, runStderr := w.run(t, "incident", "run", "--config", config)
	verifyCode, verifyStderr := w.run(t, "incident", "verify", "--config", config)
	if verifyCode != 1 {
		t.Fatalf("verify exit %d, want 1; stderr: %s", verifyCode, verifyStderr)
	}
	if !strings.Contains(verifyStderr, "disabled") {
		t.Fatalf("verify stderr must name the disabled config: %s", verifyStderr)
	}
	if runCode != verifyCode || runStderr != verifyStderr {
		t.Fatalf("verify and run disagree about a disabled config:\nrun (%d): %s\nverify (%d): %s", runCode, runStderr, verifyCode, verifyStderr)
	}
	if got := readCLITrace(t, w.azTrace); got != "" {
		t.Fatalf("az was invoked: %s", got)
	}
}

func TestIncidentCLIStatusAndCancelPersist(t *testing.T) {
	dir := t.TempDir()
	out, err := exec.Command(binaryPath, "incident", "status", "--state-dir", dir).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("empty status %v %s", err, out)
	}
	// A durable record written by intake can be managed with no Azure credentials
	// and without starting any agent or receiver.
	key := "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"
	ledger := `{"version":1,"incidents":{"` + key + `":{"key":"` + key + `","alertId":"a","status":"queued"}}}`
	if err := os.WriteFile(filepath.Join(dir, "incidents.json"), []byte(ledger), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command(binaryPath, "incident", "cancel", "--state-dir", dir, "--id", key).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel %v %s", err, out)
	}
	out, err = exec.Command(binaryPath, "incident", "status", "--state-dir", dir).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "cancelled" {
		t.Fatalf("status %s", out)
	}
}
