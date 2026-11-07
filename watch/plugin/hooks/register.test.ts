// Tests for hooks/register.js, the mods module behind the opt-in /cenci-status
// command (#1183). Run with `claude plugin test ./plugin` (make -C watch
// test-claude-plugin). Every test loads the real module and stands in for the
// engine beneath it: command.register, process.run and ui.toast are stubs that
// record what the module did, and the model, prompt and session-send entry
// points are stubs that fail the test if anything reaches them.
import { expect, test } from 'claude-code/testing'
import type { Plugin } from 'claude-code/testing'
import type { On } from 'claude-code'

const COMMAND = 'cenci-status'
const PLUGIN_ROOT = decodeURIComponent(new URL('..', import.meta.url).pathname).replace(/\/$/, '')
const ENABLED = { options: { statusCommand: true } }
const SESSION = { cwd: '/', surface: 'terminal', isInteractive: true } as const

const MSG_NOT_FOUND =
  'cenci status unavailable: cenci binary not found (install cenci-watch or set CENCI_BIN)'
const MSG_TIMEOUT = 'cenci status did not finish within 10s'
const MSG_MALFORMED = 'cenci status returned unexpected output; run cenci status in a terminal'

const RUNNING = 'daemon: running (pid 42)\nsessions: none\n'

type Outcome =
  | { exitCode: number; stdout: string; stderr: string }
  // process.run rejects: the stub denies with this reason, which the engine
  // turns into the caller's rejection.
  | { reject: string }

type Recorder = {
  // What the stubbed status.sh process does on its next run; a test may change
  // it between commands.
  outcome: Outcome
  registers: { name: string; description: string; immediate?: boolean }[]
  runs: { argv: readonly string[]; init?: { cwd?: string; timeoutMs?: number } }[]
  toasts: string[]
  sessionStartsReached: number
  forbidden: string[]
}

// wire stands in for the engine beneath the module. `outcome` is what the
// status.sh process does; `denyRegister` makes the engine refuse the command
// name; a name the stub already registered is refused too, as a live engine
// refuses a taken name.
function wire(
  on: On,
  outcome: Outcome = { exitCode: 0, stdout: RUNNING, stderr: '' },
  denyRegister = false,
): Recorder {
  const rec: Recorder = {
    outcome,
    registers: [],
    runs: [],
    toasts: [],
    sessionStartsReached: 0,
    forbidden: [],
  }
  const live = new Set<string>()

  on('command.register', (_, e) => {
    rec.registers.push(e)
    if (denyRegister || live.has(e.name)) return { deny: `/${e.name} is already registered` }
    live.add(e.name)
    return { value: { command: e.name } }
  })
  on('process.run', (_, e) => {
    rec.runs.push({ argv: e.argv, init: e.init })
    if ('reject' in rec.outcome) return { deny: rec.outcome.reject }
    return { value: rec.outcome }
  })
  on('ui.toast', (_, e) => {
    rec.toasts.push(e.text)
    return { value: undefined }
  })
  on('session.start', (_, e) => {
    rec.sessionStartsReached += 1
    return { cwd: e.cwd }
  })

  // The command never talks to the model, the prompt box or other sessions.
  on('model.complete', () => {
    rec.forbidden.push('model.complete')
    throw new Error('model.complete must never be called')
  })
  on('prompt.submit', () => {
    rec.forbidden.push('prompt.submit')
    throw new Error('prompt.submit must never be called')
  })
  on('prompt.fill', () => {
    rec.forbidden.push('prompt.fill')
    throw new Error('prompt.fill must never be called')
  })
  on('session.send', () => {
    rec.forbidden.push('session.send')
    throw new Error('session.send must never be called')
  })
  return rec
}

test('enabled: session.start registers /cenci-status once, immediate', ENABLED, async ($, on) => {
  const rec = wire(on)
  await $.session.start(SESSION)

  expect(rec.registers.length).toBe(1)
  expect(rec.registers[0].name).toBe(COMMAND)
  expect(rec.registers[0].immediate).toBe(true)
  expect(rec.runs.length).toBe(0)
})

test('disabled by default: nothing is registered and no process runs', async ($, on) => {
  const rec = wire(on)
  on('command.run', () => ({ text: 'engine answered' }))
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(rec.registers.length).toBe(0)
  expect(rec.runs.length).toBe(0)
  expect(rec.sessionStartsReached).toBe(1)
  expect(text).toBe('engine answered')
})

test('statusCommand false: nothing is registered and no process runs', { options: { statusCommand: false } }, async ($, on) => {
  const rec = wire(on)
  on('command.run', () => ({ text: 'engine answered' }))
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(rec.registers.length).toBe(0)
  expect(rec.runs.length).toBe(0)
  expect(text).toBe('engine answered')
})

test('exit 0 with daemon output: stdout is shown verbatim', ENABLED, async ($, on) => {
  const stdout = 'daemon: running (pid 42)\nsessions: 2 live\n'
  wire(on, { exitCode: 0, stdout, stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(stdout)
})

test('the process is /bin/sh <root>/hooks/status.sh, cwd the plugin root, 10s', ENABLED, async ($, on) => {
  const rec = wire(on)
  await $.session.start(SESSION)
  await $.command.run({ command: COMMAND })

  expect(rec.runs.length).toBe(1)
  expect(rec.runs[0].argv).toEqual(['/bin/sh', `${PLUGIN_ROOT}/hooks/status.sh`])
  expect(rec.runs[0].init?.cwd).toBe(PLUGIN_ROOT)
  expect(rec.runs[0].init?.timeoutMs).toBe(10000)
})

test('sessions: none and sessions: unavailable stay different and are never replaced', ENABLED, async ($, on) => {
  const none = 'daemon: running (pid 42)\nsessions: none\n'
  const unavailable = 'daemon: not running\nsessions: unavailable (daemon not reachable)\n'

  const rec = wire(on, { exitCode: 0, stdout: none, stderr: '' })
  await $.session.start(SESSION)
  const shownNone = (await $.command.run({ command: COMMAND })).text
  rec.outcome = { exitCode: 0, stdout: unavailable, stderr: '' }
  const shownUnavailable = (await $.command.run({ command: COMMAND })).text

  expect(rec.runs.length).toBe(2)
  expect(shownNone).toBe(none)
  expect(shownUnavailable).toBe(unavailable)
  expect(shownNone).not.toBe(shownUnavailable)
})

test('sessions: unavailable (daemon not reachable) is shown as-is', ENABLED, async ($, on) => {
  const unavailable = 'daemon: not running\nsessions: unavailable (daemon not reachable)\n'
  wire(on, { exitCode: 0, stdout: unavailable, stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(unavailable)
  expect(text).toContain('sessions: unavailable (daemon not reachable)')
  expect(text).not.toContain('sessions: none')
})

test('sessions: none is shown as-is, not as unavailable', ENABLED, async ($, on) => {
  const none = 'daemon: running (pid 42)\nsessions: none\n'
  wire(on, { exitCode: 0, stdout: none, stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(none)
  expect(text).not.toContain('unavailable')
})

test('a snapshot-read error line is shown verbatim, not reported as zero sessions', ENABLED, async ($, on) => {
  const stdout = 'daemon: running (pid 42)\nsessions: unavailable (error reading snapshot: i/o timeout)\n'
  wire(on, { exitCode: 0, stdout, stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(stdout)
  expect(text).not.toContain('sessions: none')
})

test('exit 127 shows the binary-not-found message', ENABLED, async ($, on) => {
  wire(on, { exitCode: 127, stdout: '', stderr: 'cenci binary not found\n' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(MSG_NOT_FOUND)
})

test('another non-zero exit shows its code and the first stderr line', ENABLED, async ($, on) => {
  wire(on, { exitCode: 3, stdout: '', stderr: 'socket refused\nsecond line\n' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe('cenci status failed (exit 3): socket refused')
})

test('a non-zero exit with empty stderr shows only the code', ENABLED, async ($, on) => {
  wire(on, { exitCode: 2, stdout: 'daemon: running (pid 42)\n', stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe('cenci status failed (exit 2)')
})

test('stderr ANSI escapes, control characters and later lines are stripped', ENABLED, async ($, on) => {
  wire(on, {
    exitCode: 3,
    stdout: '',
    stderr: '\u001b[31mboom\u001b[0m\u0007 bell\nsecond line\n',
  })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe('cenci status failed (exit 3): boom bell')
})

test('stderr CRLF, DEL and C1 control characters are stripped on one line', ENABLED, async ($, on) => {
  wire(on, { exitCode: 4, stdout: '', stderr: 'a\u0085b\u007fc\u009bd\r\nsecond\r\n' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe('cenci status failed (exit 4): abcd')
})

test('the stderr line is cut to 200 characters', ENABLED, async ($, on) => {
  wire(on, { exitCode: 3, stdout: '', stderr: `${'x'.repeat(300)}\n` })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(`cenci status failed (exit 3): ${'x'.repeat(200)}`)
})

test('the 200-character cut counts code points, not UTF-16 units', ENABLED, async ($, on) => {
  wire(on, { exitCode: 3, stdout: '', stderr: `${'x'.repeat(199)}\u{1F600}tail\n` })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(`cenci status failed (exit 3): ${'x'.repeat(199)}\u{1F600}`)
})

test('a process.run timeout shows the 10s message', ENABLED, async ($, on) => {
  // The engine's timeout rejection reads `aborted: still running after <ms>ms`.
  wire(on, { reject: 'aborted: still running after 10000ms' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(MSG_TIMEOUT)
})

test('a process.run timeout behind the engine prefix shows the 10s message', ENABLED, async ($, on) => {
  wire(on, { reject: 'some-plugin: $.process.run: aborted: still running after 10000ms' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(MSG_TIMEOUT)
})

test('another process.run rejection shows could-not-start with its message', ENABLED, async ($, on) => {
  wire(on, { reject: 'failed to start: ENOENT: no such file or directory' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toStartWith('could not start cenci status: ')
  expect(text).toContain('failed to start: ENOENT: no such file or directory')
  expect(text).not.toBe(MSG_TIMEOUT)
})

test('a start failure that merely mentions a timeout is not the timeout message', ENABLED, async ($, on) => {
  wire(on, { reject: 'failed to start: EACCES: timeout-helper is not executable' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toStartWith('could not start cenci status: ')
  expect(text).toContain('failed to start: EACCES: timeout-helper is not executable')
})

test('a rejection message has its control characters and later lines stripped', ENABLED, async ($, on) => {
  wire(on, { reject: 'failed to start: \u001b[31mEPERM\u001b[0m\u0007\nsecond line' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toStartWith('could not start cenci status: ')
  expect(text).toEndWith('failed to start: EPERM')
  expect(text).not.toContain('\u001b')
  expect(text).not.toContain('\n')
  expect(text).not.toContain('second line')
})

test('exit 0 with stdout that does not start with "daemon: " is malformed', ENABLED, async ($, on) => {
  wire(on, { exitCode: 0, stdout: 'something else entirely\n', stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(MSG_MALFORMED)
})

test('exit 0 with empty stdout is malformed', ENABLED, async ($, on) => {
  wire(on, { exitCode: 0, stdout: '', stderr: '' })
  await $.session.start(SESSION)
  const { text } = await $.command.run({ command: COMMAND })

  expect(text).toBe(MSG_MALFORMED)
})

// OBSERVER sits above the module in the chain and reports, through the cwd it
// answers with, how next.trace settled the module's own session.start hook
// (tier `user`, the plugin under test's default): `returned` when the hook
// answered itself, `skipped` when it threw before next(e). The bottom stub is
// reached either way, because the engine runs the chain without a throwing
// hook, so only this trace tells the two apart. The register body closes over
// nothing of this file.
const OBSERVER: Plugin = {
  name: 'observer',
  tier: 'prepend',
  register(on) {
    on('session.start', async (_, e, next) => {
      const { cwd } = await next(e)
      const own = next.trace.filter((t) => t.tier === 'user').map((t) => t.outcome)
      return { cwd: `${cwd}#${own.join(',')}` }
    })
  },
}

test(
  'a denied registration shows a toast naming /cenci-status, and the hook still answers session.start itself',
  { ...ENABLED, plugins: [OBSERVER] },
  async ($, on) => {
    const rec = wire(on, { exitCode: 0, stdout: RUNNING, stderr: '' }, true)
    const { cwd } = await $.session.start(SESSION)

    expect(rec.registers.length).toBe(1)
    expect(rec.toasts.length).toBe(1)
    expect(rec.toasts[0]).toContain('/cenci-status')
    expect(rec.toasts[0]).toMatch(/\btaken\b/)
    expect(rec.sessionStartsReached).toBe(1)
    expect(cwd).toBe(`${SESSION.cwd}#returned`)
  },
)

test('a reload (session.start twice) leaves one registration and no collision message', ENABLED, async ($, on) => {
  const rec = wire(on)
  await $.session.start(SESSION)
  await $.session.start(SESSION)

  expect(rec.registers.length).toBe(1)
  expect(rec.toasts).toEqual([])
  expect(rec.sessionStartsReached).toBe(2)
})

test('registration and command runs never reach the model, prompt or session-send entry points', ENABLED, async ($, on) => {
  const rec = wire(on)
  await $.session.start(SESSION)
  await $.command.run({ command: COMMAND })
  rec.outcome = { exitCode: 3, stdout: '', stderr: 'boom\n' }
  await $.command.run({ command: COMMAND })
  rec.outcome = { reject: 'aborted: still running after 10000ms' }
  await $.command.run({ command: COMMAND })

  expect(rec.runs.length).toBe(3)
  expect(rec.forbidden).toEqual([])
})
