package launcher

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const preferenceHelper = "/usr/local/bin/lib/host-preferences.py"

type appliedPreferences struct {
	Imported   []string `json:"imported"`
	Overridden []string `json:"overridden"`
}

func (r appliedPreferences) valid(agent string) bool {
	for _, keys := range [][]string{r.Imported, r.Overridden} {
		for _, key := range keys {
			if ok, _ := allowedPreference(agent, key, nil); !ok {
				return false
			}
		}
	}
	return true
}

// The helper sees only filtered fields on stdin. On opt-out, probe for the
// ledger first so images predating inheritance keep working without a rebuild.
func (e *Engine) activatePreferences(launch launchCtx) error {
	p := launch.Profile.preferences
	args := []string{"exec", "-i", "-u", "dev", launch.Scope.ContainerName}
	if p.Source == "" {
		args = append(args, "/bin/sh", "-c", `if [ ! -e "/home/dev/.$1/.cenci-preferences.json" ]; then exit 0; fi; exec /usr/bin/python3 -I "$2" "$1" /home/dev apply`, "sh", launch.Agent, preferenceHelper)
	} else {
		args = append(args, "/usr/bin/python3", "-I", preferenceHelper, launch.Agent, "/home/dev", "apply")
	}
	payload := []byte("{}")
	if p.Source != "" {
		payload, _ = json.Marshal(p.values)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Runtime, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(payload)
	out := newCapturedStderr(io.Discard)
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot apply %s sandbox preferences in %s: %w; verify the local config and rebuild the sandbox image if the helper is missing", launch.Agent, launch.Scope.ContainerName, err)
	}
	if p.Source == "" {
		return nil
	}
	var report appliedPreferences
	if err := json.Unmarshal([]byte(out.retained.String()), &report); err != nil || !report.valid(launch.Agent) {
		return fmt.Errorf("could not verify that %s sandbox preferences were applied", launch.Agent)
	}
	keys := append(slices.Clone(report.Imported), report.Overridden...)
	slices.Sort(keys)
	if !slices.Equal(keys, p.Selected) {
		return fmt.Errorf("incomplete %s sandbox preference report", launch.Agent)
	}
	if len(report.Overridden) > 0 {
		fmt.Fprintf(e.Stderr, "Sandbox-local preferences take precedence for %s\n", strings.Join(report.Overridden, ", "))
	}
	if len(p.Skipped) > 0 {
		fmt.Fprintf(e.Stderr, "Skipped unsupported host preferences in %s: %s\n", p.Source, strings.Join(p.Skipped, ", "))
	}
	return nil
}

// Copy a bounded ledger as tar to stdout. Audit does not start a process in
// the container, and never extracts members or renders workload-supplied text.
func (e *Engine) readPreferenceReport(name, agent string, p *PreferencePosture) {
	p.Status = "last activation unavailable"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Runtime, "cp", name+":/home/dev/."+agent+"/.cenci-preferences.json", "-")
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if cmd.Start() != nil {
		return
	}
	reader := tar.NewReader(io.LimitReader(out, 16<<10))
	header, err := reader.Next()
	var ledger struct {
		Report *appliedPreferences `json:"report"`
	}
	if err == nil && header.Typeflag == tar.TypeReg && header.Size <= 4096 {
		err = json.NewDecoder(reader).Decode(&ledger)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(out, 16<<10))
	_ = out.Close()
	if waitErr := cmd.Wait(); waitErr != nil || err != nil || ledger.Report == nil || !ledger.Report.valid(agent) {
		return
	}
	p.Status = "last activation"
	p.Imported = ledger.Report.Imported
	p.Overridden = ledger.Report.Overridden
}
