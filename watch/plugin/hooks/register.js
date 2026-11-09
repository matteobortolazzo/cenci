// cenci-watch mods module, one entry with two independent opt-ins:
//
// - `statusCommand` (#1183): the /cenci-status command. It shows `cenci status`
//   immediately, without starting a model turn, by running the plugin's own
//   hooks/status.sh.
// - `nativeMeasurements` (#1451): a `session.measure` hook that reads
//   `$.session.usage()` and sends an allowlisted sample (never `cost`) through
//   hooks/measure.sh, which runs `cenci notify -agent claude -measurement`. The
//   hook never waits for the delivery: each send is bounded at 5s, one per
//   session is in flight and the newest queued sample wins. Failures are silent.
//
// This module never runs a bare `cenci` itself: both wrappers resolve the
// binary through the same trusted chain as notify.sh, whose last step is a PATH
// lookup that only accepts a binary passing the self-adoption and liveness
// checks. Nothing here calls the model, the prompt box or another session.
//
// The plugin's options gate everything: while neither is exactly true this
// module adds no hooks and starts no process, so the settings hooks in
// hooks.json never depend on it.

const COMMAND = 'cenci-status'
const TIMEOUT_MS = 10000
const MEASURE_TIMEOUT_MS = 5000
const MAX_CHARS = 200

const MSG_NOT_FOUND =
  'cenci status unavailable: cenci binary not found (install cenci-watch or set CENCI_BIN)'
const MSG_TIMEOUT = 'cenci status did not finish within 10s'
const MSG_MALFORMED = 'cenci status returned unexpected output; run cenci status in a terminal'
const MSG_NAME_TAKEN = `/${COMMAND} is not available: that command name is already taken`

// The engine's process.run timeout rejection reads `aborted: still running
// after <N>ms`, optionally behind a `<plugin>: $.process.run: ` prefix. Match
// only that exact shape: a start failure that merely mentions a timeout must
// keep its own message (watch/AGENTS.md Critical Rule 1).
const TIMEOUT_REJECTION = /^(?:.*\$\.process\.run:\s*)?aborted: still running after \d+ms$/

// sanitize strips ANSI CSI sequences, then C0, DEL and C1 characters, keeps the
// first line and cuts it to MAX_CHARS code points, so untrusted process output
// fits one terminal line.
function sanitize(text) {
  const firstLine = String(text)
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, '')
    .split('\n')[0]
  return Array.from(firstLine.replace(/[\u0000-\u001f\u007f-\u009f]/g, ''))
    .slice(0, MAX_CHARS)
    .join('')
}

async function statusText($) {
  const root = $.plugin.root
  let result
  try {
    result = await $.process.run(['/bin/sh', `${root}/hooks/status.sh`], {
      cwd: root,
      timeoutMs: TIMEOUT_MS,
    })
  } catch (err) {
    const message = String(err && err.message)
    if (TIMEOUT_REJECTION.test(message)) return MSG_TIMEOUT
    return `could not start cenci status: ${sanitize(message)}`
  }

  if (result.exitCode === 127) return MSG_NOT_FOUND
  if (result.exitCode !== 0) {
    const reason = sanitize(result.stderr)
    return `cenci status failed (exit ${result.exitCode})${reason ? `: ${reason}` : ''}`
  }
  if (!result.stdout.startsWith('daemon: ')) return MSG_MALFORMED
  return result.stdout
}

const isObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value)

// pick copies only the listed keys of an object, renamed to their wire names. A
// value that is not an object passes through raw (undefined drops out of the
// JSON), so the daemon marks it invalid instead of this module guessing.
function pick(value, keys) {
  if (!isObject(value)) return value
  const out = {}
  for (const [from, to] of keys) {
    if (value[from] !== undefined) out[to] = value[from]
  }
  return out
}

// toSample builds the wire body from an allowlist: `cost`, `startedAt` and every
// other field of usage() stay in this process (#1451 P46). resetsAt is an ISO
// 8601 string in the declarations, so a string passes through and any other
// value is forwarded raw for the daemon to reject.
function toSample(sessionId, measuredAt, usage) {
  return {
    session_id: sessionId,
    measured_at: measuredAt,
    context: pick(usage.context, [
      ['tokens', 'tokens'],
      ['window', 'window'],
      ['percent', 'percent'],
    ]),
    rate_limits: Array.isArray(usage.rateLimits)
      ? usage.rateLimits.map((limit) =>
          pick(limit, [
            ['kind', 'kind'],
            ['percentUsed', 'percent_used'],
            ['resetsAt', 'resets_at'],
          ]),
        )
      : usage.rateLimits,
  }
}

// One delivery per session is in flight and at most one newer sample waits
// behind it; the newest queued sample wins. A slot exists only while its
// session has work in flight, so the map never outgrows the live sessions.
const deliveries = new Map()

// send runs measure.sh for one sample and then for whatever was queued behind
// it. Every outcome is swallowed: a missing binary (exit 127), a failed start
// and the 5s timeout rejection all leave the next sample to try again.
async function send($, sessionId, first) {
  try {
    const root = $.plugin.root
    const slot = deliveries.get(sessionId)
    let payload = first
    while (payload) {
      try {
        await $.process.run(['/bin/sh', `${root}/hooks/measure.sh`], {
          cwd: root,
          timeoutMs: MEASURE_TIMEOUT_MS,
          stdin: JSON.stringify(payload),
        })
      } catch {
        // Silent by design: a measurement never toasts.
      }
      payload = slot.queued
      slot.queued = null
    }
  } finally {
    // Whatever threw, free the slot so later samples for the session are sent.
    deliveries.delete(sessionId)
  }
}

// measure reads one sample and hands it to the session's coalescer without
// waiting for the delivery. Any failure to read it delivers nothing.
async function measure($) {
  let usage
  try {
    usage = await $.session.usage()
  } catch {
    return
  }
  if (!isObject(usage)) return
  const measuredAt = new Date().toISOString()

  let sessionId
  let cwd
  try {
    sessionId = await $.session.id()
    cwd = await $.session.cwd()
  } catch {
    // An unreadable cwd only skips watch.excludeCwd, which is applied by notify;
    // an unreadable session id leaves nothing to key the sample on.
  }
  if (typeof sessionId !== 'string' || sessionId === '') return

  const payload = { measurement: toSample(sessionId, measuredAt, usage) }
  if (typeof cwd === 'string') payload.cwd = cwd

  const slot = deliveries.get(sessionId)
  if (slot) {
    slot.queued = payload
    return
  }
  deliveries.set(sessionId, { queued: null })
  send($, sessionId, payload).catch(() => {})
}

function registerMeasurements(on) {
  on('session.measure', async ($, e, next) => {
    try {
      await measure($)
    } catch {
      // A measurement must never stop the event reaching the rest of the chain.
    }
    return next(e)
  })
}

export function register(on, options) {
  if (options.statusCommand === true) registerStatusCommand(on)
  if (options.nativeMeasurements === true) registerMeasurements(on)
}

function registerStatusCommand(on) {
  let registered = false

  on('session.start', async ($, e, next) => {
    if (!registered) {
      registered = true
      try {
        await $.command.register({
          name: COMMAND,
          description: 'Show cenci status (daemon, sessions, dispatch) without a model turn',
          immediate: true,
        })
      } catch {
        try {
          await $.ui.toast(MSG_NAME_TAKEN)
        } catch {
          // The toast is best effort; session.start must always reach next(e).
          // Defensive and untested by design: in `claude plugin test` the engine
          // drops a failed $.ui.toast without rejecting it to the module, and a
          // throwing hook is skipped, so neither the bottom handler nor
          // next.trace can tell this guard from a bare await.
        }
      }
    }
    return next(e)
  })

  on('command.run', { command: COMMAND }, async ($) => ({ text: await statusText($) }))
}
