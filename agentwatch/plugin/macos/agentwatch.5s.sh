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
# The `.5s.` in the filename is SwiftBar's refresh interval. `agentwatch status`
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
# Empty stdout / non-zero exit covers both "daemon down" and "no sessions"
# (class=none, which status.go already renders as exit 1 / no output).
JSON="$("$BIN" status 2>/dev/null || true)"
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
  if (cls === 'none') return ''

  var text = data.text || ''
  var tooltip = data.tooltip || ''

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

  var lines = []

  // Menu bar line: the counts (text, as-is) tinted by the highest-priority class.
  lines.push((text || 'agents') + ' |' + paramsFor(cls))
  lines.push('---')

  // One dropdown row per session, colored by its OWN status so a need-input row
  // stays loud even when other sessions are merely running.
  var tips = tooltip.split('\n').filter(function (l) { return l.length > 0 })
  tips.forEach(function (line) {
    var m = line.match(/\(([a-z-]+)\)\s*$/)
    var status = m ? m[1] : 'none'
    var body = line.replace(/\s*\([a-z-]+\)\s*$/, '')
    lines.push(body + ' |' + paramsFor(status))
  })

  lines.push('---')
  lines.push('Refresh | refresh=true')

  return lines.join('\n')
}
JXA
)" || true

[ -z "$OUTPUT" ] && exit 0
printf '%s\n' "$OUTPUT"
