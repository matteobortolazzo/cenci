#!/usr/bin/env bash
#
# AgentWatch — SwiftBar menu bar plugin for macOS.
#
# <swiftbar.title>AgentWatch</swiftbar.title>
# <swiftbar.version>1.0</swiftbar.version>
# <swiftbar.author>Matteo Bortolazzo</swiftbar.author>
# <swiftbar.author.github>matteobortolazzo</swiftbar.author.github>
# <swiftbar.desc>Live Claude Code / Codex session status in the macOS menu bar, via the agentwatch daemon.</swiftbar.desc>
# <swiftbar.dependencies>agentwatch</swiftbar.dependencies>
# <swiftbar.abouturl>https://github.com/matteobortolazzo/agent-stack/tree/main/agentwatch</swiftbar.abouturl>
# <swiftbar.hideAbout>false</swiftbar.hideAbout>
# <swiftbar.hideRunInTerminal>true</swiftbar.hideRunInTerminal>
# <swiftbar.hideLastUpdated>true</swiftbar.hideLastUpdated>
# <swiftbar.hideDisablePlugin>false</swiftbar.hideDisablePlugin>
#
# The `.5s.` in the filename is SwiftBar's refresh interval. `agentwatch widget-json`
# is a cheap one-shot socket read, so a short interval is fine. Rename the segment
# (e.g. `.2s.`, `.10s.`) to change the polling cadence.
#
# This is a read-only frontend over the same Waybar JSON contract consumed by the
# noctalia and dms widgets — it makes no daemon or Go changes.

set -uo pipefail

# --- 1. Resolve the agentwatch binary -------------------------------------
#
# SwiftBar is a GUI app and runs plugins with a minimal PATH that excludes
# /opt/homebrew/bin, /usr/local/bin, and the plugin bin/ dir. Resolve the binary
# explicitly. Override with the AGENTWATCH_BIN env var / SwiftBar variable.
resolve_bin() {
  if [ -n "${AGENTWATCH_BIN:-}" ] && [ -x "${AGENTWATCH_BIN}" ]; then
    printf '%s\n' "${AGENTWATCH_BIN}"
    return 0
  fi

  local candidates=(
    /opt/homebrew/bin/agentwatch
    /usr/local/bin/agentwatch
  )
  # Plugin bootstrap installs the binary at <plugin-root>/bin/agentwatch. An
  # installed plugin lives at ~/.claude/plugins/cache/<marketplace>/agentwatch/<version>/,
  # and the marketplace checkout keeps the repo's plugin/ layout — cover both.
  local g
  for g in \
    "$HOME"/.claude/plugins/cache/*/agentwatch/*/bin/agentwatch \
    "$HOME"/.claude/plugins/marketplaces/*/agentwatch/plugin/bin/agentwatch; do
    [ -x "$g" ] && candidates+=("$g")
  done

  local c
  for c in "${candidates[@]}"; do
    if [ -x "$c" ]; then
      printf '%s\n' "$c"
      return 0
    fi
  done

  if command -v agentwatch >/dev/null 2>&1; then
    command -v agentwatch
    return 0
  fi

  return 1
}

BIN="$(resolve_bin || true)"
[ -z "$BIN" ] && exit 0   # binary not found — hide the menu bar item

# --- 2. Poll the daemon ---------------------------------------------------
#
# Empty stdout / non-zero exit covers both "daemon down" and "no sessions with
# dispatch off" (alt=none, which status.go already renders as exit 1 / no
# output).
JSON="$("$BIN" widget-json 2>/dev/null || true)"
[ -z "$JSON" ] && exit 0   # nothing live — hide the item

# --- 3. Format as SwiftBar output via JXA ---------------------------------
#
# JavaScript for Automation ships with every macOS (native JSON.parse, no
# jq/python/brew). The JSON is passed via env var to dodge quoting issues and the
# multi-arg-shebang pitfall (macOS collapses `#!/usr/bin/osascript -l JavaScript`
# args), so osascript is invoked from bash via a heredoc rather than a shebang.
OUTPUT="$(AGENTWATCH_JSON="$JSON" /usr/bin/osascript -l JavaScript <<'JXA'
function run() {
  ObjC.import('Foundation')
  var raw = $.NSProcessInfo.processInfo.environment.objectForKey('AGENTWATCH_JSON')
  var json = raw ? ObjC.unwrap(raw) : ''
  if (!json) return ''

  var data
  try { data = JSON.parse(json) } catch (e) { return '' }

  var cls = data['class'] || 'none'
  var alt = data['alt'] || 'none'
  if (alt === 'none') return ''

  var text = data.text || ''
  var tooltip = data.tooltip || ''
  var headroom = data.headroom || {}

  // class -> [SF Symbol, color]. Colors mirror colorForClass in the QML widgets;
  // icons use SF Symbols, which has no literal robot glyph, so `running` uses
  // brain.head.profile.fill instead of the QML widgets' robot/smart_toy. A null
  // symbol means no attention treatment (idle / unknown). need-input is the
  // loudest treatment — that's the attention layer's whole job.
  function styleFor(c) {
    switch (c) {
      case 'need-input': return ['exclamationmark.triangle.fill', 'red']
      case 'running':    return ['brain.head.profile.fill', 'blue']
      case 'done':       return ['checkmark.circle.fill', 'green']
      case 'stopped':    return ['pause.circle.fill', 'orange']
      default:           return [null, null] // idle / none — hidden icon
    }
  }

  function paramsFor(c) {
    var s = styleFor(c)
    var p = ''
    if (s[0]) p += ' sfimage=' + s[0]
    if (s[1]) p += ' sfcolor=' + s[1]
    return p
  }

  // Same thresholds as headroomClass in status.go: >25% normal, 10-25%
  // (inclusive both ends) warning, <10% critical.
  function colorForHeadroom(pct) {
    if (pct > 25) return 'green'
    if (pct >= 10) return 'orange'
    return 'red'
  }

  var lines = []

  // Menu bar line: the counts (text, as-is) tinted by the highest-priority class.
  lines.push((text || 'agents') + ' |' + paramsFor(cls))
  lines.push('---')

  // Agent keys with budget headroom data, sorted for deterministic order
  // (object key order is otherwise unreliable). Also used below to compute
  // the exact compact headroom summary line status.go appends to tooltip
  // (e.g. "claude 73%  codex 15%"), mirroring formatHeadroom in status.go, so
  // that specific line can be recognized and skipped when parsing session
  // rows below (it's rendered separately from the numeric `headroom` field
  // instead). Empty string when there's no headroom data — never matches a
  // real tooltip line, since tips are pre-filtered to non-empty lines.
  var hKeys = Object.keys(headroom).sort()
  var headroomLine = hKeys.map(function (agent) {
    var pct = Math.floor(headroom[agent] * 100 + 0.5)
    return agent + ' ' + pct + '%'
  }).join('  ')

  // Fleet dispatch loop summary. `dispatch` is passed through the status JSON
  // verbatim from status.go (only enabled/interval/last_run_at/
  // last_dispatched/last_skipped/last_error are used here — pass_running/
  // daemon_running are intentionally not surfaced, per the ticket's scope
  // note). dispatchLine mirrors the exact text formatDispatch/
  // formatLastRunTime append to the Go tooltip, so it can be recognized and
  // skipped by exact match in the tooltip-line loop below and rendered
  // instead as its own dedicated row (no SF Symbol/color, per the accepted
  // default treatment).
  var dispatch = data.dispatch || null
  var dispatchLine = ''
  if (dispatch && dispatch.enabled) {
    dispatchLine = dispatch.interval ? 'dispatch: on (' + dispatch.interval + ')' : 'dispatch: on'
    if (dispatch.last_error) {
      dispatchLine += ' — last run failed: ' + dispatch.last_error
    } else if (dispatch.last_run_at) {
      // Guard against an unparseable/malformed last_run_at: new Date() never
      // throws, it silently yields an Invalid Date whose getHours()/
      // getMinutes() are NaN. Fall back to the raw string, mirroring
      // formatLastRunTime's fallback to the raw RFC3339 string in status.go,
      // so a bad timestamp stays visible instead of rendering "NaN:NaN".
      var runDate = new Date(dispatch.last_run_at)
      var lastRun
      if (isNaN(runDate.getTime())) {
        lastRun = dispatch.last_run_at
      } else {
        var hh = ('0' + runDate.getHours()).slice(-2)
        var mm = ('0' + runDate.getMinutes()).slice(-2)
        lastRun = hh + ':' + mm
      }
      dispatchLine += ' — last run ' + lastRun + ', ' +
        dispatch.last_dispatched + ' dispatched / ' + dispatch.last_skipped + ' skipped'
    }
  }

  // One dropdown row per session, colored by its OWN status so a need-input row
  // stays loud even when other sessions are merely running. Lines ending in
  // "(status)" are session rows. The known headroom summary line and the known
  // dispatch summary line (both computed above) are each skipped by exact
  // match. Any OTHER non-matching line (unexpected status.go output, encoding
  // glitch, future format change, etc.) still renders with the 'none' status
  // fallback instead of being silently dropped, so a real regression stays
  // visible rather than vanishing.
  var tips = tooltip.split('\n').filter(function (l) { return l.length > 0 })
  tips.forEach(function (line) {
    if (headroomLine !== '' && line === headroomLine) return
    if (dispatchLine !== '' && line === dispatchLine) return
    var m = line.match(/\(([a-z-]+)\)\s*$/)
    var status = m ? m[1] : 'none'
    var body = m ? line.replace(/\s*\([a-z-]+\)\s*$/, '') : line
    lines.push(body + ' |' + paramsFor(status))
  })

  // One dropdown row per agent with budget headroom data. Omitted entirely
  // when the field is absent/empty — no headroom leaked into the menu.
  // Invariant: `agent` keys are currently a fixed claude/codex whitelist from
  // local config, not attacker/session-controllable — if that ever becomes
  // dynamic/user-derived, re-check this for injection into the sfcolor= param
  // string or Pango markup.
  hKeys.forEach(function (agent) {
    var pct = Math.floor(headroom[agent] * 100 + 0.5)
    lines.push(agent + ' ' + pct + '% | sfcolor=' + colorForHeadroom(pct))
  })

  // Dedicated dispatch summary row, rendered from the structured `dispatch`
  // field rather than the generic tooltip-line loop above. Omitted entirely
  // when the loop is disabled/absent — no dispatch noise leaked into the menu.
  if (dispatchLine !== '') {
    lines.push(dispatchLine)
  }

  lines.push('---')
  lines.push('Refresh | refresh=true')

  return lines.join('\n')
}
JXA
)" || true

[ -z "$OUTPUT" ] && exit 0
printf '%s\n' "$OUTPUT"
