package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncidentCLIOptInAndUsage(t *testing.T) {
	for _, args := range [][]string{{"incident"}, {"incident", "unknown"}, {"incident", "run"}, {"incident", "status"}, {"incident", "cancel", "--state-dir", "/tmp", "--id", "x", "extra"}} {
		out, err := exec.Command(binaryPath, args...).CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 {
			t.Fatalf("%v: %v %s", args, err, out)
		}
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
