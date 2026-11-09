// Tests for the opt-in native-measurement producer in hooks/register.js (#1451).
// With the `nativeMeasurements` option on, the module's `session.measure` hook
// reads `$.session.usage()`, builds an allowlisted snake_case sample (no `cost`)
// and hands it, on stdin, to hooks/measure.sh, which runs `cenci notify -agent
// claude -measurement`. Run with `claude plugin test ./plugin` (make -C watch
// test-claude-plugin).
//
// Every test loads the real module and stands in for the engine beneath it:
// session.usage, session.id, session.cwd, process.run and ui.toast are stubs
// that record what the module did; the model, prompt and session-send entry
// points fail the test if anything reaches them. A process.run stub can hold
// its answer back, so a delivery can stay in flight while later samples arrive.
//
// Guard tests read next.trace from a `prepend`-tier OBSERVER instead of a
// sentinel at the bottom of the chain (watch/docs/test-strategy.md): the engine
// skips a hook that throws and still reaches the stub below it, so only the
// trace tells `returned` from `skipped`.
//
// API facts these tests rely on were checked against the Claude Code 2.1.319
// declarations: `$.session.id()` and `$.session.cwd()` are async functions,
// `$.session.usage()` answers `{ startedAt, context, rateLimits, cost? }` with
// `resetsAt` an ISO 8601 string, `cost` is `{ usd }`, and `$.process.run` takes
// `{ cwd, env, stdin, timeoutMs }`.
import { expect, test } from 'claude-code/testing'
import type { Engine, Plugin } from 'claude-code/testing'
import type { On } from 'claude-code'

const PLUGIN_ROOT = decodeURIComponent(new URL('..', import.meta.url).pathname).replace(/\/$/, '')
const ON = { options: { nativeMeasurements: true } }
const SESSION_ID = 'sess-1'
const SESSION_CWD = '/home/someone/work/repo'
const TIMEOUT_MS = 5000
const COST_USD = 12.3456

type Outcome =
  | { exitCode: number; stdout: string; stderr: string }
  // process.run rejects: the stub denies with this reason, which the engine
  // turns into the caller's rejection.
  | { reject: string }

type Answer = { value: unknown } | { deny: string }

type Run = {
  argv: readonly string[]
  init?: { cwd?: string; timeoutMs?: number; stdin?: string; env?: Record<string, string> }
}

type Recorder = {
  // What $.session.usage() answers on its next call; a test changes it between
  // samples.
  usage: Answer
  usageCalls: number
  sessionId: string
  cwd: string
  // What the measure.sh process does on its next run when it is not held.
  outcome: Outcome
  // While true a process.run stays pending until the test settles it, oldest first.
  hold: boolean
  pending: ((outcome: Outcome) => void)[]
  runs: Run[]
  toasts: string[]
  measuresReached: number
  forbidden: string[]
}

const OK: Outcome = { exitCode: 0, stdout: '', stderr: '' }

function usageOf(tokens: number, overrides: Record<string, unknown> = {}) {
  return {
    startedAt: 1700000000000,
    context: { tokens, window: 200000, percent: Math.round((tokens / 200000) * 100) },
    rateLimits: [
      { kind: 'five_hour', percentUsed: 23.5, resetsAt: '2026-11-08T15:00:00Z' },
      { kind: 'seven_day', percentUsed: 7, resetsAt: '2026-11-12T09:30:00Z' },
    ],
    cost: { usd: COST_USD },
    ...overrides,
  }
}

// wire stands in for the engine beneath the module.
function wire(on: On): Recorder {
  const rec: Recorder = {
    usage: { value: usageOf(1000) },
    usageCalls: 0,
    sessionId: SESSION_ID,
    cwd: SESSION_CWD,
    outcome: OK,
    hold: false,
    pending: [],
    runs: [],
    toasts: [],
    measuresReached: 0,
    forbidden: [],
  }

  on('session.measure', (_, e) => {
    rec.measuresReached += 1
    return { changed: e.changed }
  })
  on('session.usage', () => {
    rec.usageCalls += 1
    return rec.usage as never
  })
  on('session.id', () => ({ value: rec.sessionId }))
  on('session.cwd', () => ({ value: rec.cwd }))
  on('process.run', async (_, e) => {
    rec.runs.push({ argv: e.argv, init: e.init })
    const outcome = rec.hold
      ? await new Promise<Outcome>((resolve) => rec.pending.push(resolve))
      : rec.outcome
    if ('reject' in outcome) return { deny: outcome.reject }
    return { value: outcome }
  })
  on('ui.toast', (_, e) => {
    rec.toasts.push(e.text)
    return { value: undefined }
  })

  // A measurement never talks to the model, the prompt box or other sessions.
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

// OBSERVER sits above the module and reports, through the `changed` list it
// answers with, how next.trace settled the module's own session.measure hook
// (tier `user`, the plugin under test's default). The register body closes over
// nothing of this file.
const OBSERVER: Plugin = {
  name: 'observer',
  tier: 'prepend',
  register(on) {
    on('session.measure', async (_, e, next) => {
      const result = await next(e)
      const own = next.trace.filter((t) => t.tier === 'user').map((t) => t.outcome)
      return { changed: [...result.changed, `trace:${own.join(',')}`] as never }
    })
  },
}
const OBSERVED = { ...ON, plugins: [OBSERVER] }

// sample raises one session.measure event and returns how the module's own hook
// settled (`returned`, `skipped`, or '' when the module has no such hook).
async function sample($: Engine): Promise<string> {
  const result = await Promise.race([
    $.session.measure({ context: { window: 200000 }, rateLimits: [], changed: ['context'] }),
    sleep(2000).then(() => {
      throw new Error('session.measure did not settle: the hook is waiting for its delivery')
    }),
  ])
  const trace = (result.changed as unknown as string[]).find((c) => String(c).startsWith('trace:'))
  return trace === undefined ? '' : trace.slice('trace:'.length)
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

// settle lets the module's unawaited delivery work run to its next wait.
async function settle() {
  for (let i = 0; i < 10; i++) await sleep(1)
}

async function until(what: string, cond: () => boolean) {
  for (let i = 0; i < 100 && !cond(); i++) await sleep(5)
  if (!cond()) throw new Error(`timed out waiting for: ${what}`)
}

function stdinOf(run: Run): { cwd?: string; measurement: Record<string, any> } {
  expect(typeof run.init?.stdin).toBe('string')
  return JSON.parse(run.init?.stdin as string)
}

function assertBoundedMeasureRuns(rec: Recorder) {
  for (const run of rec.runs) {
    expect(run.argv).toEqual(['/bin/sh', `${PLUGIN_ROOT}/hooks/measure.sh`])
    expect(run.init?.cwd).toBe(PLUGIN_ROOT)
    expect(run.init?.timeoutMs).toBe(TIMEOUT_MS)
  }
}

// -- P9: off by default ------------------------------------------------------

test('default options: no usage read and no process for a measurement (P9)', async ($, on) => {
  const rec = wire(on)
  await sample($)

  expect(rec.usageCalls).toBe(0)
  expect(rec.runs.length).toBe(0)
  expect(rec.measuresReached).toBe(1)
})

test('nativeMeasurements false: no usage read and no process (P9)', { options: { nativeMeasurements: false } }, async ($, on) => {
  const rec = wire(on)
  await sample($)

  expect(rec.usageCalls).toBe(0)
  expect(rec.runs.length).toBe(0)
  expect(rec.measuresReached).toBe(1)
})

test('only statusCommand on: a measurement starts nothing (P9)', { options: { statusCommand: true } }, async ($, on) => {
  const rec = wire(on)
  await sample($)

  expect(rec.usageCalls).toBe(0)
  expect(rec.runs.length).toBe(0)
  expect(rec.measuresReached).toBe(1)
})

// -- the delivered sample ----------------------------------------------------

test('a measurement runs /bin/sh <root>/hooks/measure.sh, cwd the plugin root, 5s, payload on stdin (P44)', ON, async ($, on) => {
  const rec = wire(on)
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  expect(rec.usageCalls).toBe(1)
  expect(rec.runs.length).toBe(1)
  // The payload is stdin only: argv is exactly the shell and the script.
  assertBoundedMeasureRuns(rec)
  expect(rec.runs[0].argv.length).toBe(2)
})

test('the stdin payload is {cwd, measurement} with the session id and cwd the mod read and snake_case fields', ON, async ($, on) => {
  const rec = wire(on)
  rec.sessionId = 'transcript-id-42'
  rec.cwd = '/srv/other/project'
  const before = Date.now()
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)
  const after = Date.now()

  const payload = stdinOf(rec.runs[0])
  expect(Object.keys(payload).sort()).toEqual(['cwd', 'measurement'])
  expect(payload.cwd).toBe('/srv/other/project')

  const { measured_at: measuredAt, ...rest } = payload.measurement
  expect(rest).toEqual({
    session_id: 'transcript-id-42',
    context: { tokens: 1000, window: 200000, percent: 1 },
    rate_limits: [
      { kind: 'five_hour', percent_used: 23.5, resets_at: '2026-11-08T15:00:00Z' },
      { kind: 'seven_day', percent_used: 7, resets_at: '2026-11-12T09:30:00Z' },
    ],
  })
  // measured_at is the moment usage() was read, as an RFC 3339 UTC string.
  expect(typeof measuredAt).toBe('string')
  const at = Date.parse(measuredAt)
  expect(Number.isNaN(at)).toBe(false)
  expect(new Date(at).toISOString()).toBe(measuredAt)
  expect(at).toBeGreaterThanOrEqual(before)
  expect(at).toBeLessThanOrEqual(after)
})

test('cost, startedAt and every other unlisted field never reach the process (P46)', ON, async ($, on) => {
  const rec = wire(on)
  rec.usage = {
    value: usageOf(1000, {
      cost: { usd: COST_USD },
      email: 'dev@example.com',
      context: {
        tokens: 1000,
        window: 200000,
        percent: 1,
        cost: { usd: COST_USD },
        breakdown: { gridRows: ['secret prompt row'] },
      },
      rateLimits: [
        {
          kind: 'five_hour',
          percentUsed: 23.5,
          resetsAt: '2026-11-08T15:00:00Z',
          cost: { usd: COST_USD },
          status: 'allowed',
        },
      ],
    }),
  }
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  const stdin = rec.runs[0].init?.stdin as string
  for (const forbidden of ['cost', 'usd', String(COST_USD), 'startedAt', '1700000000000', 'email', 'breakdown', 'secret prompt row', 'status']) {
    expect(stdin).not.toContain(forbidden)
  }
  const { measurement } = stdinOf(rec.runs[0])
  expect(Object.keys(measurement).sort()).toEqual(['context', 'measured_at', 'rate_limits', 'session_id'])
  expect(Object.keys(measurement.context).sort()).toEqual(['percent', 'tokens', 'window'])
  expect(measurement.rate_limits.length).toBe(1)
  expect(Object.keys(measurement.rate_limits[0]).sort()).toEqual(['kind', 'percent_used', 'resets_at'])
})

test('a usage without cost (off a ledger host) still delivers', ON, async ($, on) => {
  const rec = wire(on)
  const { cost: _cost, ...noCost } = usageOf(1000)
  rec.usage = { value: noCost }
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  expect(stdinOf(rec.runs[0]).measurement.session_id).toBe(SESSION_ID)
})

test('absent figures stay absent: no fabricated zeros and an empty rate_limits array stays empty', ON, async ($, on) => {
  const rec = wire(on)
  rec.usage = { value: { startedAt: 1, context: { window: 200000 }, rateLimits: [] } }
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  const { measurement } = stdinOf(rec.runs[0])
  expect(measurement.context).toEqual({ window: 200000 })
  expect(measurement.rate_limits).toEqual([])
})

test('a rate limit without resetsAt carries no resets_at key', ON, async ($, on) => {
  const rec = wire(on)
  rec.usage = {
    value: usageOf(1000, { rateLimits: [{ kind: 'spend_limit', percentUsed: 101 }] }),
  }
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  expect(stdinOf(rec.runs[0]).measurement.rate_limits).toEqual([{ kind: 'spend_limit', percent_used: 101 }])
})

test('a resetsAt that is not a string passes through raw, for the daemon to mark invalid', ON, async ($, on) => {
  // The declarations type resetsAt as an ISO 8601 string, so there is no unit to
  // convert from: anything else is forwarded untouched, never guessed at.
  const rec = wire(on)
  rec.usage = {
    value: usageOf(1000, {
      rateLimits: [
        { kind: 'five_hour', percentUsed: 1, resetsAt: 1762603200 },
        { kind: 'seven_day', percentUsed: 2, resetsAt: 'not a date' },
      ],
    }),
  }
  await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  const limits = stdinOf(rec.runs[0]).measurement.rate_limits
  expect(limits[0].resets_at).toBe(1762603200)
  expect(limits[1].resets_at).toBe('not a date')
})

test('the hook always answers through next(e) and the delivery never reaches the model, prompt or session-send entry points', OBSERVED, async ($, on) => {
  const rec = wire(on)
  const traces = [await sample($)]
  rec.outcome = { exitCode: 127, stdout: '', stderr: 'cenci binary not found\n' }
  traces.push(await sample($))
  rec.outcome = { reject: 'aborted: still running after 5000ms' }
  traces.push(await sample($))
  await until('three deliveries', () => rec.runs.length === 3)

  expect(traces).toEqual(['returned', 'returned', 'returned'])
  expect(rec.measuresReached).toBe(3)
  expect(rec.forbidden).toEqual([])
})

// -- P34: usage() failures ---------------------------------------------------

const NOT_OBJECTS: [string, Answer][] = [
  ['rejects', { deny: 'usage unavailable' }],
  ['returns null', { value: null }],
  ['returns undefined', { value: undefined }],
  ['returns an array', { value: [{ context: { window: 1 } }] }],
  ['returns an empty array', { value: [] }],
  ['returns a string', { value: 'not usage' }],
  ['returns a number', { value: 42 }],
  ['returns a boolean', { value: true }],
]

for (const [name, answer] of NOT_OBJECTS) {
  test(`usage() ${name}: the hook calls next(e) and delivers nothing (P34)`, OBSERVED, async ($, on) => {
    const rec = wire(on)
    rec.usage = answer
    const trace = await sample($)
    await settle()

    expect(rec.usageCalls).toBe(1)
    expect(trace).toBe('returned')
    expect(rec.measuresReached).toBe(1)
    expect(rec.runs.length).toBe(0)
    expect(rec.toasts).toEqual([])
    expect(rec.forbidden).toEqual([])
  })
}

test('a failed usage() read does not wedge later samples', OBSERVED, async ($, on) => {
  const rec = wire(on)
  rec.usage = { deny: 'usage unavailable' }
  expect(await sample($)).toBe('returned')
  rec.usage = { value: usageOf(2000) }
  expect(await sample($)).toBe('returned')
  await until('the second sample', () => rec.runs.length === 1)

  expect(stdinOf(rec.runs[0]).measurement.context.tokens).toBe(2000)
})

// -- P32: delivery failures are silent ---------------------------------------

const FAILURES: [string, Outcome][] = [
  ['measure.sh exits 127 (no cenci binary)', { exitCode: 127, stdout: '', stderr: 'cenci binary not found\n' }],
  ['measure.sh exits non-zero', { exitCode: 3, stdout: '', stderr: 'socket refused\n' }],
  ['the process cannot start', { reject: 'failed to start: ENOENT: no such file or directory' }],
  ['the 5s timeout rejects', { reject: 'aborted: still running after 5000ms' }],
  ['the 5s timeout rejects behind the engine prefix', { reject: 'some-plugin: $.process.run: aborted: still running after 5000ms' }],
]

for (const [name, outcome] of FAILURES) {
  test(`${name}: next(e) still runs, no toast, no error (P32)`, OBSERVED, async ($, on) => {
    const rec = wire(on)
    rec.outcome = outcome
    const trace = await sample($)
    await until('the delivery process', () => rec.runs.length === 1)
    await settle()

    expect(trace).toBe('returned')
    expect(rec.measuresReached).toBe(1)
    expect(rec.toasts).toEqual([])
    expect(rec.forbidden).toEqual([])
  })

  test(`${name}: the session's slot is freed, so the next sample is sent at once`, OBSERVED, async ($, on) => {
    const rec = wire(on)
    rec.outcome = outcome
    await sample($)
    await until('the first delivery', () => rec.runs.length === 1)
    await settle()
    rec.usage = { value: usageOf(2000) }
    await sample($)
    await until('the second delivery', () => rec.runs.length === 2)

    expect(stdinOf(rec.runs[1]).measurement.context.tokens).toBe(2000)
    expect(rec.toasts).toEqual([])
  })
}

// -- P33: one in flight per session, newest queued sample wins ---------------

test('the hook does not wait for its delivery: session.measure settles while the process is still running', OBSERVED, async ($, on) => {
  const rec = wire(on)
  rec.hold = true
  const trace = await sample($)
  await until('the delivery process', () => rec.runs.length === 1)

  expect(trace).toBe('returned')
  expect(rec.pending.length).toBe(1)
  rec.pending[0](OK)
})

test('one delivery in flight, samples 2 and 3 queue, and only sample 3 is sent once the first settles (P33)', ON, async ($, on) => {
  const rec = wire(on)
  rec.hold = true

  await sample($) // sample 1 (tokens 1000)
  await until('the first delivery', () => rec.runs.length === 1)

  rec.usage = { value: usageOf(2000) }
  await sample($) // sample 2 queues
  await settle()
  rec.usage = { value: usageOf(3000) }
  await sample($) // sample 3 replaces it
  const readAt = Date.now()
  await sleep(20)
  await settle()

  // Still one process while the first is in flight.
  expect(rec.runs.length).toBe(1)

  rec.pending[0](OK) // the first settles
  await until('the queued delivery', () => rec.runs.length === 2)
  await settle()

  expect(rec.runs.length).toBe(2)
  const first = stdinOf(rec.runs[0]).measurement
  const second = stdinOf(rec.runs[1]).measurement
  expect(first.context.tokens).toBe(1000)
  expect(second.context.tokens).toBe(3000)
  // The queued sample keeps the time it was read, not the time it was sent.
  expect(Date.parse(second.measured_at)).toBeLessThanOrEqual(readAt)

  rec.pending[1](OK)
  await settle()
  expect(rec.runs.length).toBe(2) // nothing else was queued
  assertBoundedMeasureRuns(rec)
  expect(rec.toasts).toEqual([])
})

test('a sample that arrives after the queue drained is sent at once (the slot is released)', ON, async ($, on) => {
  const rec = wire(on)
  rec.hold = true
  await sample($)
  await until('the first delivery', () => rec.runs.length === 1)
  rec.pending[0](OK)
  await settle()

  rec.usage = { value: usageOf(2000) }
  await sample($)
  await until('the second delivery', () => rec.runs.length === 2)

  expect(stdinOf(rec.runs[1]).measurement.context.tokens).toBe(2000)
  rec.pending[1](OK)
})

test('a delivery that times out after 5s leaves the queued sample to be sent, silently (P33)', OBSERVED, async ($, on) => {
  const rec = wire(on)
  rec.hold = true

  await sample($)
  await until('the first delivery', () => rec.runs.length === 1)
  rec.usage = { value: usageOf(2000) }
  await sample($)
  await settle()
  expect(rec.runs.length).toBe(1)

  // The engine kills the child at timeoutMs and rejects the call.
  rec.pending[0]({ reject: 'aborted: still running after 5000ms' })
  await until('the queued delivery', () => rec.runs.length === 2)
  await settle()

  expect(stdinOf(rec.runs[1]).measurement.context.tokens).toBe(2000)
  expect(rec.toasts).toEqual([])
  assertBoundedMeasureRuns(rec)
  rec.pending[1](OK)
})

test('a failed first delivery still lets the queued sample go out', ON, async ($, on) => {
  const rec = wire(on)
  rec.hold = true
  await sample($)
  await until('the first delivery', () => rec.runs.length === 1)
  rec.usage = { value: usageOf(2000) }
  await sample($)
  await settle()

  rec.pending[0]({ exitCode: 127, stdout: '', stderr: 'cenci binary not found\n' })
  await until('the queued delivery', () => rec.runs.length === 2)

  expect(stdinOf(rec.runs[1]).measurement.context.tokens).toBe(2000)
  expect(rec.toasts).toEqual([])
  rec.pending[1](OK)
})

test('at most one delivery is in flight per session: another session is not held back', ON, async ($, on) => {
  const rec = wire(on)
  rec.hold = true

  await sample($) // session sess-1
  await until('the first delivery', () => rec.runs.length === 1)
  rec.sessionId = 'sess-2'
  rec.usage = { value: usageOf(2000) }
  await sample($) // a different session
  await until('the other session delivery', () => rec.runs.length === 2)

  expect(stdinOf(rec.runs[0]).measurement.session_id).toBe(SESSION_ID)
  expect(stdinOf(rec.runs[1]).measurement.session_id).toBe('sess-2')

  // And each session still has only its own one in flight.
  rec.usage = { value: usageOf(3000) }
  await sample($)
  await settle()
  expect(rec.runs.length).toBe(2)

  rec.pending[0](OK)
  rec.pending[1](OK)
  await settle()
})

test('every measurement process carries the 5s timeout, however many are sent (P44)', ON, async ($, on) => {
  const rec = wire(on)
  for (let tokens = 1000; tokens <= 4000; tokens += 1000) {
    rec.usage = { value: usageOf(tokens) }
    await sample($)
    await until(`the delivery for ${tokens}`, () => rec.runs.length === tokens / 1000)
  }

  expect(rec.runs.length).toBe(4)
  assertBoundedMeasureRuns(rec)
})

// -- P44: no second module entry, no Node socket, no unbounded subprocess ----
//
// "No second module entry" is pinned by test.sh (hooks.json's `modules` is
// exactly ["./register.js"]). "No Node socket" is enforced by the engine itself:
// a hooks module may import only its own files and "claude-code", so a `net` or
// `child_process` import fails the module's load, and every test above would
// fail with it. What is left to pin here is behavior: the only process the
// module starts is the bounded measure.sh run (assertBoundedMeasureRuns), and
// it reaches no other process, network or model entry point.

test('a measurement reaches no process.spawn, http.fetch or other forbidden entry point (P44)', ON, async ($, on) => {
  const rec = wire(on)
  // process.spawn streams, so its hook is an async generator.
  on('process.spawn', async function* () {
    rec.forbidden.push('process.spawn')
    throw new Error('process.spawn must never be called: it has no timeout')
  } as never)
  on('http.fetch', () => {
    rec.forbidden.push('http.fetch')
    throw new Error('http.fetch must never be called')
  })
  await sample($)
  rec.outcome = { exitCode: 127, stdout: '', stderr: 'cenci binary not found\n' }
  await sample($)
  await until('both deliveries', () => rec.runs.length === 2)
  await settle()

  expect(rec.forbidden).toEqual([])
  assertBoundedMeasureRuns(rec)
})
