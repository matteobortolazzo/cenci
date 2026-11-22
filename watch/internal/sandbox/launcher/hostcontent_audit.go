package launcher

import (
	"fmt"
	"io"
)

// HostContentPosture distinguishes requested source paths from the binds of a
// running container. Inspect can verify bind topology and identity, but cannot
// verify whether a client has read a skill or its discovery links yet.
type HostContentPosture struct {
	Configured []MountPosture `json:"configured"`
	Observed   []MountPosture `json:"observed,omitempty"`
	Status     string         `json:"status"`
	Warning    string         `json:"warning,omitempty"`
}

func (e *Engine) observedHostContent(name string, profile hostProfile, mounts []MountPosture) *HostContentPosture {
	actual := []MountPosture{}
	for _, mount := range mounts {
		if mount.Kind == MountKindHostContent {
			actual = append(actual, mount)
		}
	}
	if len(profile.Mounts) == 0 && len(actual) == 0 {
		return nil
	}
	h := &HostContentPosture{Configured: profile.Mounts, Observed: actual, Status: "recreate"}
	if !sameHostContent(profile.Mounts, actual) {
		return h
	}
	identity, err := e.hostContentIdentity(name)
	if err != nil {
		h.Status = "unverified"
		h.Warning = fmt.Sprintf("could not verify personal content mount identity: %v", err)
	} else if identity == profile.ContentIdentity {
		h.Status = "compatible"
	}
	return h
}

func (h *HostContentPosture) writeText(w io.Writer) {
	if h == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "\nPersonal host content (%s)\n", h.Status)
	if len(h.Configured) == 0 {
		_, _ = fmt.Fprintln(w, "  No sources selected for this repository and agent.")
	}
	for _, m := range h.Configured {
		_, _ = fmt.Fprintf(w, "  configured  %s -> %s\n", m.Source, m.Destination)
	}
	if h.Status == "recreate" {
		_, _ = fmt.Fprintln(w, "  Stop the scoped container and reopen it to apply the selection.")
	}
	if h.Warning != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", h.Warning)
	}
}

func hostContentWeakenings(mounts []MountPosture) []BoundaryWeakening {
	var result []BoundaryWeakening
	for _, m := range mounts {
		if m.Kind != MountKindHostContent {
			continue
		}
		effect := "the agent can read this host content and act on its instructions"
		if !m.ReadOnly {
			effect = "this host content is writable from the running container"
		}
		result = append(result, BoundaryWeakening{
			Option: "sandboxHostProfiles", Active: true, Effect: effect,
			HostPath: m.Source, ContainerPath: m.Destination,
		})
	}
	return result
}
