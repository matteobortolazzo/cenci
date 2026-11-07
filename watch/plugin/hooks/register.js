// cenci-watch mods module (#1183): the opt-in /cenci-status command. It shows
// `cenci status` immediately, without starting a model turn, by running the
// plugin's own hooks/status.sh. This module never runs a bare `cenci` itself:
// status.sh resolves the binary through the same trusted chain as notify.sh,
// whose last step is a PATH lookup that only accepts a binary passing the
// self-adoption and liveness checks. Read-only: nothing here calls the model,
// the prompt box or another session.
//
// The plugin's `statusCommand` option gates everything: while it is not
// exactly true this module adds no hooks and starts no process, so the
// settings hooks in hooks.json never depend on it.

const COMMAND = 'cenci-status'
const TIMEOUT_MS = 10000
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

export function register(on, options) {
  if (options.statusCommand !== true) return

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
