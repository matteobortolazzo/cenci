package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig(t *testing.T) Config {
	t.Helper()
	c := testConfig(t)
	if err := os.MkdirAll(c.Resources[0].Dir, 0700); err != nil {
		t.Fatal(err)
	}
	c.Namespace = "contoso.servicebus.windows.net"
	c.Queue = "alerts"
	c.Agent = ContainerConfig{Runtime: "podman", Image: "tools:reviewed", APIKeyEnv: "ANTHROPIC_API_KEY", Network: "incident-internal", ProxyURL: "http://proxy:3128"}
	c.Resources[0].Workspace = "00000000-0000-0000-0000-000000000000"
	c.Resources[0].Query = "AppExceptions | take 10"
	c.Resources[0].Workflow = "Use cenci stages and configured project gates."
	return c
}

func TestConfigRequiresExplicitOptInAndCompleteTrustedPolicy(t *testing.T) {
	for _, kind := range []string{"valid", "disabled", "zero-concurrency", "zero-budget", "rounded-zero-budget", "wrong-runtime", "production-key", "unsafe-namespace", "ambiguous-broker", "missing-workflow", "missing-checks", "path-traversal", "overlapping-deployments"} {
		t.Run(kind, func(t *testing.T) {
			c := validConfig(t)
			switch kind {
			case "disabled":
				c.Enabled = false
			case "zero-concurrency":
				c.Concurrency = 0
			case "zero-budget":
				c.BudgetUSD = 0
			case "rounded-zero-budget":
				c.BudgetUSD = 0.0000001
			case "wrong-runtime":
				c.Agent.Runtime = "sh"
			case "production-key":
				c.Agent.APIKeyEnv = "AZURE_CLIENT_SECRET"
			case "unsafe-namespace":
				c.Namespace = "evil.test/path.servicebus.windows.net"
			case "ambiguous-broker":
				c.Topic = "topic"
			case "missing-workflow":
				c.Resources[0].Workflow = ""
			case "missing-checks":
				c.Resources[0].Checks = nil
			case "path-traversal":
				c.Resources[0].Runbooks = []string{"../secret"}
			case "overlapping-deployments":
				c.Resources[0].Deployments = append(c.Resources[0].Deployments, c.Resources[0].Deployments[0])
			}
			b, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			put(t, path, string(b))
			_, err = LoadConfig(path)
			if kind == "valid" && err != nil {
				t.Fatal(err)
			}
			if kind != "valid" && err == nil {
				t.Fatal("incomplete policy accepted")
			}
		})
	}
}

func TestDeploymentUsesFiredTimeAndHistoricalBoundary(t *testing.T) {
	c := testConfig(t)
	r := c.Resources[0]
	boundary := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	r.Deployments[0].Until = boundary
	r.Deployments = append(r.Deployments, Deployment{Commit: strings.Repeat("b", 40), From: boundary})
	if got := r.deployed(boundary.Add(-time.Nanosecond)); got != strings.Repeat("a", 40) {
		t.Fatal("historical alert attributed to latest release")
	}
	if got := r.deployed(boundary); got != strings.Repeat("b", 40) {
		t.Fatal("exclusive deployment boundary incorrect")
	}
	if got := r.deployed(boundary.Add(-2 * time.Hour)); got != "" {
		t.Fatal("missing history guessed a release")
	}
}

func TestWorkerOwnershipAndCorruptLedgerFailClosed(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	lock, err := s.WorkerLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	other, err := NewStore(c.StateDir).WorkerLock()
	if other != nil {
		_ = other.Close()
	}
	if err == nil {
		t.Fatal("second worker claimed same state directory")
	}
	put(t, filepath.Join(c.StateDir, "incidents.json"), `{"version":99,"incidents":{}}`)
	if _, err := s.List(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("corrupt schema: %v", err)
	}
}
