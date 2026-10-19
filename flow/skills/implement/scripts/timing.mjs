#!/usr/bin/env node
// Local advisory measurements only. Every writer owns a new immutable record.
import { randomUUID } from 'node:crypto';
import { closeSync, constants, lstatSync, mkdtempSync, openSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { basename, isAbsolute, join } from 'node:path';

const phases = new Set(['implementation', 'review', 'verification']);
const outcomes = new Set(['completed', 'failed', 'stopped']);
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const labelPattern = /^[a-z][a-z0-9-]{0,63}$/;
function requireValid(condition) {
  if (!condition) throw new Error('invalid timing data');
}
function clock() {
  return { at: new Date().toISOString(), monotonicNs: process.hrtime.bigint().toString() };
}
function validateClock(value) {
  requireValid(value && typeof value.at === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(value.at)
    && Number.isFinite(Date.parse(value.at)) && typeof value.monotonicNs === 'string'
    && /^\d{1,24}$/.test(value.monotonicNs));
}
function elapsed(start, end) {
  validateClock(start);
  validateClock(end);
  const ns = BigInt(end.monotonicNs) - BigInt(start.monotonicNs);
  requireValid(ns >= 0n);
  return Number(ns) / 1e9;
}
function readJson(path) {
  requireValid(lstatSync(path).isFile());
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try { return JSON.parse(readFileSync(fd, 'utf8')); }
  finally { closeSync(fd); }
}
function writeNew(path, value) {
  writeFileSync(path, `${JSON.stringify(value)}\n`, { flag: 'wx', mode: 0o600 });
}
function readRun(path) {
  requireValid(isAbsolute(path) && lstatSync(path).isDirectory());
  const run = readJson(join(path, 'run.json'));
  requireValid(run.schema === 1 && typeof run.runId === 'string' && uuid.test(run.runId));
  validateClock(run.started);
  return run;
}
function readStart(path, id) {
  requireValid(uuid.test(id));
  const start = readJson(join(path, `${id}.start.json`));
  requireValid(start.id === id && phases.has(start.phase)
    && typeof start.label === 'string' && labelPattern.test(start.label));
  validateClock(start.started);
  return start;
}

function main(args) {
  const [command, path, phaseOrId, labelOrOutcome] = args;
  requireValid((command === 'init' || command === 'summary') ? args.length === 2
    : (command === 'start' || command === 'end') && args.length === 4);
  requireValid(typeof path === 'string' && isAbsolute(path));
  if (command === 'init') {
    requireValid(lstatSync(path).isDirectory());
    const dir = mkdtempSync(join(path, 'cenci-timing-'));
    writeNew(join(dir, 'run.json'), { schema: 1, runId: randomUUID(), started: clock() });
    return dir;
  }
  const run = readRun(path);
  if (command === 'start') {
    requireValid(phases.has(phaseOrId) && labelPattern.test(labelOrOutcome));
    const id = randomUUID();
    writeNew(join(path, `${id}.start.json`), {
      id, phase: phaseOrId, label: labelOrOutcome, started: clock(),
    });
    return id;
  }
  if (command === 'end') {
    requireValid(outcomes.has(labelOrOutcome));
    const start = readStart(path, phaseOrId);
    const ended = clock();
    elapsed(start.started, ended); // A reset monotonic clock is not a measurement.
    writeNew(join(path, `${phaseOrId}.end.json`), { id: phaseOrId, outcome: labelOrOutcome, ended });
    return phaseOrId;
  }
  const now = clock();
  const files = readdirSync(path);
  // Unknown or partially written records make the summary unavailable, not green.
  requireValid(files.every(file => file === 'run.json'
    || (uuid.test(file.replace(/\.(start|end)\.json$/, '')) && /\.(start|end)\.json$/.test(file))));
  const spans = files.filter(file => file.endsWith('.start.json')).map(file => {
    const id = basename(file, '.start.json');
    const start = readStart(path, id);
    elapsed(run.started, start.started);
    elapsed(start.started, now);
    const endedFile = `${id}.end.json`;
    const hasEnd = files.includes(endedFile);
    const end = hasEnd ? readJson(join(path, endedFile)) : null;
    if (hasEnd) {
      requireValid(end && typeof end === 'object' && end.id === id && outcomes.has(end.outcome));
      elapsed(end.ended, now);
    }
    return { id, phase: start.phase, label: start.label, startedAt: start.started.at,
      endedAt: end?.ended.at ?? null, outcome: end?.outcome ?? 'incomplete',
      elapsedSeconds: end ? elapsed(start.started, end.ended) : null };
  }).sort((a, b) => a.startedAt.localeCompare(b.startedAt) || a.id.localeCompare(b.id));
  requireValid(files.filter(file => file.endsWith('.end.json')).every(file =>
    files.includes(file.replace(/\.end\.json$/, '.start.json'))));
  const accumulatedSeconds = {};
  const byLabel = Object.create(null);
  for (const span of spans) {
    if (span.elapsedSeconds !== null) {
      accumulatedSeconds[span.phase] = (accumulatedSeconds[span.phase] ?? 0) + span.elapsedSeconds;
    }
    if (span.phase === 'verification') byLabel[span.label] = (byLabel[span.label] ?? 0) + 1;
  }
  return { schema: 1, runId: run.runId, startedAt: run.started.at, observedAt: now.at,
    elapsedSeconds: elapsed(run.started, now), accumulatedSeconds,
    incompleteInvocations: spans.filter(span => span.outcome === 'incomplete').length,
    verification: { invocations: Object.values(byLabel).reduce((sum, count) => sum + count, 0),
      repeatedInvocations: Object.values(byLabel).reduce((sum, count) => sum + count - 1, 0), byLabel },
    spans };
}

try {
  const result = main(process.argv.slice(2));
  process.stdout.write(`${typeof result === 'string' ? result : JSON.stringify(result)}\n`);
} catch {
  // Never echo user arguments, paths, artifact contents, errors, or stack traces.
  process.stderr.write('timing: unavailable (invalid arguments, clock, or local artifact)\n');
  process.exitCode = 2;
}
