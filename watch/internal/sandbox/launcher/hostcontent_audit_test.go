package launcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostContentAuditPlannedAndObserved(t *testing.T) {
	repo, home := newAuditTestRepo(t), t.TempDir()
	t.Chdir(repo)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, ".claude", "skills", "notes")
	writeFile(t, filepath.Join(dir, "SKILL.md"), "personal-text-canary")
	config := filepath.Join(home, ".config", "cenci", "config.json")
	writeFile(t, config, fmt.Sprintf(`{"sandboxHostProfiles":{"claude":{"repositories":[%q],"content":{"skillDirectories":[%q]}}}}`, repo, dir))
	if err := os.Chmod(config, 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Agent: "claude"}
	planned, err := auditEngine().Audit(opts)
	if err != nil {
		t.Fatal(err)
	}
	dest := "/opt/cenci-host-content/skills/notes"
	if planned.HostContent == nil || len(planned.HostContent.Configured) != 1 || planned.HostContent.Configured[0].Source != dir ||
		planned.HostContent.Configured[0].Destination != dest || !planned.HostContent.Configured[0].ReadOnly ||
		len(planned.HostContent.Observed) != 0 || planned.HostContent.Status != "planned" {
		t.Fatalf("planned personal content = %+v", planned.HostContent)
	}
	eng, calls := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", ComputeScope("claude", "", repo, home).ContainerName+"\n")
	// An unexpected writable mount remains visible even when the intended
	// source has changed or the operator has disabled content entirely.
	t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|bridge|runc||0\n/old/source::"+dest+"::true\n")
	for _, disabled := range []bool{false, true} {
		if disabled {
			writeFile(t, config, "{}")
		}
		p, err := eng.Audit(opts)
		if err != nil {
			t.Fatal(err)
		}
		h := p.HostContent
		if p.Basis != PostureBasisRunning || h == nil || h.Status != "recreate" ||
			len(h.Observed) != 1 || h.Observed[0].Source != "/old/source" || h.Observed[0].ReadOnly ||
			(len(h.Configured) == 0) != disabled {
			t.Fatalf("disabled=%v posture=%+v content=%+v", disabled, p, h)
		}
		var report bytes.Buffer
		if err := p.WriteText(&report); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(report.String(), "recreate") || !strings.Contains(report.String(), "/old/source") ||
			strings.Contains(report.String()+string(encoded), "personal-text-canary") {
			t.Fatalf("unexpected report: %s", report.String())
		}
	}
	for _, line := range readCallLog(t, calls) {
		if !strings.HasPrefix(line, "ps ") && !strings.HasPrefix(line, "inspect ") {
			t.Fatalf("audit runtime call: %s", line)
		}
	}
}
