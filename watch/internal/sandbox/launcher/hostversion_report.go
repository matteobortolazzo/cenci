package launcher

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// AgentVersionPosture contains version facts only. Untrusted CLI or container
// output must not be copied into audit reports verbatim.
type AgentVersionPosture struct {
	Policy    string `json:"policy"`
	Requested string `json:"requested,omitempty"`
	Effective string `json:"effective,omitempty"`
	Pin       string `json:"pin,omitempty"`
	Note      string `json:"note,omitempty"`
}

func (v *AgentVersionPosture) writeText(w io.Writer) {
	if v == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "Agent version policy: %s", v.Policy)
	for _, field := range [][2]string{{"host", v.Requested}, {"effective", v.Effective}, {"pin", v.Pin}} {
		if field[1] != "" {
			_, _ = fmt.Fprintf(w, "  %s=%s", field[0], field[1])
		}
	}
	if v.Note != "" {
		_, _ = fmt.Fprintf(w, " (%s)", v.Note)
	}
	_, _ = fmt.Fprintln(w)
}

// Audit can read version files from an existing workload. Diagnose can additionally mount an
// existing store read-only with the locally cached monolith. Neither starts an
// updater, creates a missing volume, pulls or builds an image.
func (e *Engine) describeHostVersion(agent, name string, p hostVersionPolicy, diagnose bool) *AgentVersionPosture {
	if p.Mode == "" {
		return nil
	}
	v := &AgentVersionPosture{Policy: p.Mode}
	if p.Mode == "follow-host" {
		var err error
		v.Requested, err = p.probe(agent, e)
		if err != nil {
			v.Note = fmt.Sprintf("host version unavailable: %v", err)
		}
	}
	var args []string
	if e.Runtime != "" {
		if diagnose {
			if exists, err := e.volumeExists(AgentCLIVolumeName(agent)); err == nil && exists {
				args = slices.Insert(e.agentVolumeCheckRunArgs(agent), 1, "--pull=never")
			}
		} else if name != "" {
			v.Effective = e.readVersionFact(name, "current/VERSION")
			v.Pin = e.readVersionFact(name, "PIN")
		}
	}
	if len(args) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, e.Runtime, args...)
		cmd.WaitDelay = 100 * time.Millisecond
		out := versionOutput{max: 1024}
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			status, ok := parseAgentVolumeStatus(out.buf.Bytes())
			if ok && status.Populated && IsExactSemver(status.Version) {
				v.Effective = status.Version
			}
			if ok && IsExactSemver(status.Pin) {
				v.Pin = status.Pin
			}
		}
	}
	if v.Pin != "" {
		v.Note = "explicit sandbox pin takes precedence"
	} else if v.Note == "" {
		switch {
		case v.Effective == "":
			v.Note = "effective version unavailable"
		case v.Requested != "" && v.Requested != v.Effective:
			v.Note = "host and sandbox differ; the next launch will retry synchronization"
		}
	}
	return v
}

// cp returns a tar stream. Read only a bounded regular file with the expected
// basename and exact-semver content; never execute code from a workload.
func (e *Engine) readVersionFact(container, path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Runtime, "cp", container+":/opt/cenci-agent/"+path, "-")
	cmd.WaitDelay = 100 * time.Millisecond
	out := versionOutput{max: 32 << 10}
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	tr := tar.NewReader(&out.buf)
	h, err := tr.Next()
	if err != nil || h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != filepath.Base(path) || h.Size > 256 {
		return ""
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(body))
	if !IsExactSemver(value) {
		return ""
	}
	return value
}
