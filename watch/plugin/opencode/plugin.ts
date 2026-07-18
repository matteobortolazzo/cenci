// cenci OpenCode plugin (#488)
//
// Forwards OpenCode lifecycle hooks to the shared cenci daemon so it can
// update tmux window indicators and Waybar-compatible counts, mirroring the
// existing Claude Code (plugin/hooks) and Codex (plugin/codex) integrations.
//
// Transport: every event is reported via `cenci notify -agent opencode`
// (watch/notify_cmd.go), reusing that binary's existing socket resolution,
// daemon-start-on-demand, and retry — this plugin never writes directly to
// the daemon's Unix socket.
//
// Event vocabulary: OpenCode's own event stream (session/message/permission/
// tool) is mapped onto the EXISTING hook_event_name vocabulary the daemon
// already understands (SessionStart, UserPromptSubmit, PreToolUse,
// PostToolUse, PermissionRequest, Stop, StopFailure, SessionEnd) — no new
// daemon event types are introduced.
//
// Fail-open: every hook body below is wrapped in try/catch. A missing cenci
// binary, an unreachable daemon/socket, or a broken bootstrap must never
// throw into OpenCode or block tool execution.
//
// Privacy: tool arguments, provider credentials, and model output are never
// logged or sent. The raw user prompt text IS handed to the local `cenci
// notify` process (via its stdin, never argv), which reduces it in-process
// to a short task-name string (see PromptTaskName in notify_cmd.go) before
// anything crosses the daemon socket or gets logged — this plugin itself
// never logs or transmits the prompt anywhere else, exactly like the Claude
// Code/Codex integrations.
import type { Plugin } from "@opencode-ai/plugin"
import { spawn } from "node:child_process"
import { fileURLToPath } from "node:url"
import path from "node:path"

// A tool call fires tool.execute.before *and* tool.execute.after for every
// single tool invocation. Spawning a `cenci notify` process per call would be
// unbounded under a busy session, so tool activity is coalesced into a
// throttled "still running" heartbeat instead.
const TOOL_HEARTBEAT_THROTTLE_MS = 2000

// pluginDir resolves the directory this module lives in (watch/plugin/opencode),
// independent of OpenCode's working directory.
function pluginDir(): string {
  return path.dirname(fileURLToPath(import.meta.url))
}

// cenciBinPath resolves the plugin-local binary bootstrap.sh installs,
// shared with the Claude Code/Codex plugins (watch/plugin/bin/cenci).
function cenciBinPath(): string {
  return path.join(pluginDir(), "..", "bin", "cenci")
}

// sendEvent reports one hook event via `cenci notify -agent opencode`,
// writing the compact JSON payload notify_cmd.go expects to the child's
// stdin. Every failure (missing binary, spawn error, broken pipe) is
// swallowed: delivery is best-effort and must never surface into OpenCode.
function sendEvent(payload: Record<string, unknown>): void {
  try {
    const child = spawn(cenciBinPath(), "notify -agent opencode".split(" "), {
      stdio: ["pipe", "ignore", "ignore"],
      detached: true,
    })
    child.on("error", () => {
      // Missing/broken binary — nothing to report yet, and nothing to throw.
    })
    child.stdin?.on("error", () => {
      // Daemon-side pipe closed early; safe to ignore.
    })
    child.stdin?.end(JSON.stringify(payload))
    child.unref()
  } catch (_err) {
    // Fail open: a spawn failure must never interrupt OpenCode.
  }
}

// --- per-session bookkeeping -----------------------------------------------
//
// OpenCode subagents (Task-tool delegation) run as their own child session
// with a parentID pointing at the delegating session. To land subagent
// activity on the SAME tmux window as the parent (rather than creating a
// second, orphaned daemon session), every hookEvent's session_id is resolved
// to the root of that parent chain, while agent_id carries the subagent's own
// session id — the daemon's existing AgentID != "" suppression (see
// internal/daemon/event.go) then keeps a subagent's terminal events from
// flipping the main session to done/stopped.

const parentOf = new Map<string, string>()
const trackedSessions = new Set<string>()
const lastToolHeartbeat = new Map<string, number>()
// Keyed by sessionID -> the set of message keys already reported as
// UserPromptSubmit for that session, so a session's dedup state can be
// dropped in full when the session ends (see session.deleted below).
const sentUserMessage = new Map<string, Set<string>>()

function rememberParent(sessionID: string, parentID: string | undefined): void {
  if (parentID && parentID !== sessionID) {
    parentOf.set(sessionID, parentID)
  }
}

// rootSession walks the parentID chain to the top-level session so subagent
// events land on the same daemon session/tmux window as their parent.
function rootSession(sessionID: string): string {
  const seen = new Set<string>()
  let current = sessionID
  while (parentOf.has(current) && !seen.has(current)) {
    seen.add(current)
    current = parentOf.get(current) as string
  }
  return current
}

// hookEvent builds the notify_cmd.go stdin payload for sessionID, tagging
// agent_id when the event belongs to a subagent (see rootSession above).
function hookEvent(sessionID: string, hookEventName: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const root = rootSession(sessionID)
  const payload: Record<string, unknown> = {
    hook_event_name: hookEventName,
    session_id: root,
    ...extra,
  }
  if (root !== sessionID) {
    payload.agent_id = sessionID
  }
  return payload
}

export const CenciPlugin: Plugin = async ({ directory, worktree, project, client, $ }) => {
  // Provision the plugin-local binary and start the daemon on first load.
  // Runs detached and fails open (bootstrap.sh itself never exits non-zero);
  // the try/catch here only guards the spawn call itself.
  try {
    const bootstrap = path.join(pluginDir(), "bootstrap.sh")
    const child = spawn(bootstrap, [], { stdio: "ignore", detached: true })
    child.on("error", () => {
      // No shell available or bootstrap missing — nothing more to do here;
      // sendEvent() below degrades to a no-op if the binary never appears.
    })
    child.unref()
  } catch (_err) {
    // Fail open: bootstrap failure must never block plugin load.
  }

  // Silence unused-binding lint for context fields this plugin does not
  // currently need but that are part of the documented ctx shape (#488).
  void directory
  void worktree
  void project
  void client
  void $

  // handleSessionIdle reports the done transition and clears tool-heartbeat
  // throttle state for sessionID.
  function handleSessionIdle(sessionID: string): void {
    sendEvent(hookEvent(sessionID, "Stop"))
    lastToolHeartbeat.delete(sessionID)
  }

  // heartbeat reports tool activity as a throttled "still running" signal —
  // never a per-call spawn — and never logs the tool's arguments.
  function heartbeat(sessionID: string | undefined, hookEventName: string, toolName: string | undefined): void {
    if (!sessionID) return
    trackedSessions.add(rootSession(sessionID))
    const now = Date.now()
    const last = lastToolHeartbeat.get(sessionID) ?? 0
    if (now - last < TOOL_HEARTBEAT_THROTTLE_MS) return
    lastToolHeartbeat.set(sessionID, now)
    sendEvent(hookEvent(sessionID, hookEventName, toolName ? { tool_name: toolName } : {}))
  }

  // promptTextFrom extracts a short prompt string from a message.updated
  // event's properties for compact task naming (frontend.PromptTaskName on
  // the daemon side reduces it further and never round-trips the raw text
  // back out — see notify_cmd.go).
  function promptTextFrom(props: Record<string, unknown>): string {
    const parts = props.parts as Array<{ type?: string; text?: string }> | undefined
    const text = parts?.find((p) => p.type === "text")?.text
    return typeof text === "string" ? text : ""
  }

  return {
    async event({ event }) {
      try {
        const props = (event as { properties?: Record<string, unknown> }).properties ?? {}

        switch (event.type) {
          case "session.created": {
            const info = props.info as { id?: string; parentID?: string } | undefined
            const sessionID = info?.id
            if (!sessionID) return
            rememberParent(sessionID, info?.parentID)
            trackedSessions.add(rootSession(sessionID))
            sendEvent(hookEvent(sessionID, "SessionStart"))
            return
          }
          case "session.updated": {
            const info = props.info as { id?: string; parentID?: string } | undefined
            const sessionID = info?.id
            if (!sessionID) return
            rememberParent(sessionID, info?.parentID)
            return
          }
          case "session.status": {
            const sessionID = props.sessionID as string | undefined
            const status = (props.status as { type?: string } | undefined)?.type
            if (!sessionID) return
            if (status === "busy") {
              sendEvent(hookEvent(sessionID, "PostToolUse"))
            } else if (status === "idle") {
              handleSessionIdle(sessionID)
            }
            return
          }
          case "session.idle": {
            const sessionID = props.sessionID as string | undefined
            if (!sessionID) return
            handleSessionIdle(sessionID)
            return
          }
          case "session.error": {
            const sessionID = props.sessionID as string | undefined
            if (!sessionID) return
            // Conservative terminal detection: only report an error-driven
            // stop for a session we have actually seen start — an internal
            // session.error unrelated to a tracked user session must not
            // spuriously terminal-ize an unrelated/untracked window.
            if (!trackedSessions.has(rootSession(sessionID))) return
            sendEvent(hookEvent(sessionID, "StopFailure"))
            return
          }
          case "session.deleted": {
            const info = props.info as { id?: string } | undefined
            const sessionID = info?.id ?? (props.sessionID as string | undefined)
            if (!sessionID) return
            sendEvent(hookEvent(sessionID, "SessionEnd"))
            trackedSessions.delete(rootSession(sessionID))
            parentOf.delete(sessionID)
            lastToolHeartbeat.delete(sessionID)
            sentUserMessage.delete(sessionID)
            return
          }
          case "message.updated": {
            const info = props.info as
              | { id?: string; sessionID?: string; role?: string; parentID?: string }
              | undefined
            if (!info?.sessionID || info.role !== "user") return
            const msgKey = info.id ?? `${info.sessionID}:${Date.now()}`
            const sessionMessages = sentUserMessage.get(info.sessionID) ?? new Set<string>()
            if (sessionMessages.has(msgKey)) return
            sessionMessages.add(msgKey)
            sentUserMessage.set(info.sessionID, sessionMessages)
            sendEvent(hookEvent(info.sessionID, "UserPromptSubmit", { prompt: promptTextFrom(props) }))
            return
          }
          case "permission.asked": {
            const sessionID = props.sessionID as string | undefined
            if (!sessionID) return
            trackedSessions.add(rootSession(sessionID))
            sendEvent(hookEvent(sessionID, "PermissionRequest"))
            return
          }
          case "permission.replied": {
            const sessionID = props.sessionID as string | undefined
            if (!sessionID) return
            sendEvent(hookEvent(sessionID, "PostToolUse"))
            return
          }
          default:
            return
        }
      } catch (_err) {
        // Fail open: an unmapped/malformed event must never throw into
        // OpenCode's event loop.
      }
    },

    async "tool.execute.before"(input) {
      try {
        heartbeat(input?.sessionID, "PreToolUse", input?.tool)
      } catch (_err) {
        // Fail open: never block tool execution on a reporting failure.
      }
    },

    async "tool.execute.after"(input) {
      try {
        heartbeat(input?.sessionID, "PostToolUse", input?.tool)
      } catch (_err) {
        // Fail open: never block tool execution on a reporting failure.
      }
    },

    async "permission.ask"(input) {
      try {
        const sessionID = (input as { sessionID?: string } | undefined)?.sessionID
        if (sessionID) {
          trackedSessions.add(rootSession(sessionID))
          sendEvent(hookEvent(sessionID, "PermissionRequest"))
        }
      } catch (_err) {
        // Fail open: never block the permission prompt on a reporting
        // failure — OpenCode's own default ("ask") still applies.
      }
    },
  }
}
