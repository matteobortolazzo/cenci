package tmux

import (
	"log"
	"math"
	"strconv"
)

// RenderHeadroom sets/clears session-scoped @cenci-headroom-<agent>
// global tmux user variables from the reconciler's per-agent-type headroom
// map (#171). Values are rounded to the nearest integer percent and clamped
// to 0..100. Only changed values trigger a SetOption call. Agents previously
// tracked but absent from headroom have their variable cleared.
//
// The default server always gets the variables; so does every non-empty socket
// that hosts a tracked window (#1429). A socket that no longer hosts a tracked
// window has its variables cleared and is no longer tracked.
func (f *Frontend) RenderHeadroom(headroom map[string]float64) {
	values := make(map[string]string, len(headroom))
	for agent, frac := range headroom {
		values[agent] = strconv.Itoa(clampPercent(agent, frac))
	}
	f.lastHeadroom = values

	hosting := map[string]bool{"": true}
	for wk := range f.windows {
		hosting[wk.socket] = true
	}
	for socket := range hosting {
		f.renderHeadroomOn(socket, values)
	}
	for socket := range f.headroomVars {
		if !hosting[socket] {
			f.renderHeadroomOn(socket, nil)
			delete(f.headroomVars, socket)
		}
	}
}

// syncHeadroom pushes the last rendered headroom to a non-empty socket as soon
// as a window is first tracked on it, instead of waiting for the next
// reconciler tick. The default server is RenderHeadroom's own.
func (f *Frontend) syncHeadroom(socket string) {
	if socket == "" {
		return
	}
	f.renderHeadroomOn(socket, f.lastHeadroom)
}

// renderHeadroomOn sets each value that differs from what was last set on
// socket, and clears the variable of every agent previously set there but
// absent from values.
func (f *Frontend) renderHeadroomOn(socket string, values map[string]string) {
	for agent, value := range values {
		if f.headroomVars[socket][agent] == value {
			continue
		}
		f.setOpt(socket, headroomVarName(agent), value, "error setting @cenci-headroom-"+agent)
		if f.headroomVars[socket] == nil {
			f.headroomVars[socket] = make(map[string]string)
		}
		f.headroomVars[socket][agent] = value
	}

	for agent := range f.headroomVars[socket] {
		if _, ok := values[agent]; ok {
			continue
		}
		f.setOpt(socket, headroomVarName(agent), "", "error clearing @cenci-headroom-"+agent)
		delete(f.headroomVars[socket], agent)
	}
}

// headroomVarName returns the tmux global user variable name for an agent
// type's headroom.
func headroomVarName(agent string) string {
	return "@cenci-headroom-" + agent
}

// clampPercent rounds a 0.0-1.0 fraction to the nearest integer percent,
// clamped to the 0..100 range. frac is contractually 0.0-1.0 per
// UsageProvider.Headroom(); if an upstream bug produces NaN, Inf, or a
// value outside that range, this logs the anomaly (unconditionally, as a
// data-integrity signal) before still returning a valid clamped percent.
func clampPercent(agent string, frac float64) int {
	switch {
	case math.IsNaN(frac):
		log.Printf("headroom for %s out of expected [0,1] range: %v", agent, frac)
		frac = 0
	case math.IsInf(frac, 1):
		log.Printf("headroom for %s out of expected [0,1] range: %v", agent, frac)
		frac = 1
	case math.IsInf(frac, -1):
		log.Printf("headroom for %s out of expected [0,1] range: %v", agent, frac)
		frac = 0
	case frac < 0 || frac > 1:
		log.Printf("headroom for %s out of expected [0,1] range: %v", agent, frac)
	}
	pct := int(math.Round(frac * 100))
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// setOpt sets a session-wide (global) tmux option on the given server and
// always logs on failure (unlike setWindowOpt's verbose-gated fire-and-forget
// pattern): this call has no natural backstop like a confirmed-existing
// window, so a failure here (e.g. no tmux server, unreachable tmux binary) can
// silently stop headroom reporting for the life of the process.
func (f *Frontend) setOpt(socket, key, value, errMsg string) {
	if err := f.client.SetOption(socket, key, value); err != nil {
		log.Printf("%s%s: %v", errMsg, socketLogSuffix(socket), err)
	}
}
