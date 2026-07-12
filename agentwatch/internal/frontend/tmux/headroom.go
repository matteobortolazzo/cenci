package tmux

import (
	"log"
	"math"
	"strconv"
)

// RenderHeadroom sets/clears session-scoped @agentwatch-headroom-<agent>
// global tmux user variables from the reconciler's per-agent-type headroom
// map (#171). Values are rounded to the nearest integer percent and clamped
// to 0..100. Only changed values trigger a SetOption call. Agents previously
// tracked but absent from headroom have their variable cleared.
func (f *Frontend) RenderHeadroom(headroom map[string]float64) {
	for agent, frac := range headroom {
		value := strconv.Itoa(clampPercent(agent, frac))
		if f.headroomVars[agent] == value {
			continue
		}
		f.setOpt(headroomVarName(agent), value, "error setting @agentwatch-headroom-"+agent)
		f.headroomVars[agent] = value
	}

	for agent := range f.headroomVars {
		if _, ok := headroom[agent]; ok {
			continue
		}
		f.setOpt(headroomVarName(agent), "", "error clearing @agentwatch-headroom-"+agent)
		delete(f.headroomVars, agent)
	}
}

// headroomVarName returns the tmux global user variable name for an agent
// type's headroom.
func headroomVarName(agent string) string {
	return "@agentwatch-headroom-" + agent
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

// setOpt sets a session-wide (global) tmux option and always logs on
// failure (unlike setWindowOpt's verbose-gated fire-and-forget pattern):
// this call has no natural backstop like a confirmed-existing window, so a
// failure here (e.g. no tmux server, unreachable tmux binary) can silently
// stop headroom reporting for the life of the process.
func (f *Frontend) setOpt(key, value, errMsg string) {
	if err := f.client.SetOption(key, value); err != nil {
		log.Printf("%s: %v", errMsg, err)
	}
}
