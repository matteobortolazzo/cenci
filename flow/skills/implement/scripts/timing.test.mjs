import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const helper = fileURLToPath(new URL('timing.mjs', import.meta.url));
function fixture(t) {
  const parent = mkdtempSync(join(tmpdir(), 'cenci-timing-test-'));
  t.after(() => rmSync(parent, { recursive: true, force: true }));
  return parent;
}
function invoke(...args) {
  return spawnSync(process.execPath, [helper, ...args], { encoding: 'utf8' });
}
function cli(...args) {
  const result = invoke(...args);
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
function summary(run) { return JSON.parse(cli('summary', run)); }
function parallel(...args) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [helper, ...args]);
    let out = '', err = '';
    child.stdout.on('data', chunk => { out += chunk; });
    child.stderr.on('data', chunk => { err += chunk; });
    child.on('error', reject);
    child.on('close', code => code === 0 ? resolve(out.trim()) : reject(new Error(err)));
  });
}

test('independent runs have private unique artifacts and no invented measurements', t => {
  const parent = fixture(t);
  const first = cli('init', parent), second = cli('init', parent);
  assert.notEqual(first, second);
  assert.equal(statSync(first).mode & 0o777, 0o700);
  const result = summary(first);
  assert.notEqual(result.runId, summary(second).runId);
  assert.deepEqual(result.spans, []);
  assert.equal(result.verification.invocations, 0);
  assert.deepEqual(result.accumulatedSeconds, {});
});

test('records real elapsed time, identity, outcomes, and repeated verification groups', async t => {
  const run = cli('init', fixture(t));
  const id = cli('start', run, 'verification', 'full-suite');
  await new Promise(resolve => setTimeout(resolve, 40));
  cli('end', run, id, 'failed');
  const retry = cli('start', run, 'verification', 'full-suite');
  cli('end', run, retry, 'completed');
  const result = summary(run);
  assert.equal(result.verification.invocations, 2);
  assert.equal(result.verification.repeatedInvocations, 1);
  assert.deepEqual(result.verification.byLabel, { 'full-suite': 2 });
  const span = result.spans.find(item => item.id === id);
  assert.equal(span.phase, 'verification');
  assert.equal(span.label, 'full-suite');
  assert.equal(span.outcome, 'failed');
  assert.ok(span.elapsedSeconds >= 0.04);
  assert.ok(Date.parse(span.endedAt) >= Date.parse(span.startedAt));
  assert.ok(result.elapsedSeconds >= span.elapsedSeconds);
  assert.equal(result.accumulatedSeconds.verification, result.spans.reduce((sum, item) => sum + item.elapsedSeconds, 0));
});

test('unfinished work stays unknown and duplicate completion cannot overwrite evidence', t => {
  const run = cli('init', fixture(t));
  const id = cli('start', run, 'implementation', 'lane-1');
  const pending = summary(run).spans[0];
  assert.equal(pending.outcome, 'incomplete');
  assert.equal(pending.elapsedSeconds, null);
  assert.equal(pending.endedAt, null);
  assert.equal(summary(run).incompleteInvocations, 1);
  cli('end', run, id, 'stopped');
  assert.equal(invoke('end', run, id, 'completed').status, 2);
  assert.equal(summary(run).spans[0].outcome, 'stopped');
});

test('overlapping phase accumulation is separate from elapsed wall time', t => {
  const run = cli('init', fixture(t));
  const ids = [cli('start', run, 'implementation', 'lane-1'), cli('start', run, 'implementation', 'lane-2')];
  for (const id of ids) cli('end', run, id, 'completed');
  const now = process.hrtime.bigint();
  const recordClock = offset => ({ at: new Date(Date.now() + offset * 1000).toISOString(),
    monotonicNs: (now + BigInt(offset) * 1_000_000_000n).toString() });
  const metadata = JSON.parse(readFileSync(join(run, 'run.json')));
  metadata.started = recordClock(-20);
  writeFileSync(join(run, 'run.json'), JSON.stringify(metadata));
  for (const id of ids) {
    const startPath = join(run, `${id}.start.json`), endPath = join(run, `${id}.end.json`);
    const start = JSON.parse(readFileSync(startPath)), end = JSON.parse(readFileSync(endPath));
    start.started = recordClock(-18);
    end.ended = recordClock(-3);
    writeFileSync(startPath, JSON.stringify(start));
    writeFileSync(endPath, JSON.stringify(end));
  }
  const result = summary(run);
  assert.equal(result.accumulatedSeconds.implementation, 30);
  assert.deepEqual(result.spans.map(span => span.elapsedSeconds), [15, 15]);
  assert.ok(result.elapsedSeconds >= 20);
});

test('all accepted verification labels count correctly, including constructor', t => {
  const run = cli('init', fixture(t));
  const id = cli('start', run, 'verification', 'constructor');
  cli('end', run, id, 'completed');
  const result = summary(run);
  assert.equal(result.verification.invocations, 1);
  assert.equal(result.verification.byLabel.constructor, 1);
});

test('parallel workers retain independent overlapping spans', async t => {
  const run = cli('init', fixture(t));
  const ids = await Promise.all(Array.from({ length: 6 }, (_, i) => parallel('start', run, 'implementation', `lane-${i}`)));
  assert.equal(new Set(ids).size, 6);
  await Promise.all(ids.map(id => parallel('end', run, id, 'completed')));
  const result = summary(run);
  assert.equal(result.spans.length, 6);
  assert.equal(result.incompleteInvocations, 0);
  assert.equal(result.accumulatedSeconds.implementation,
    result.spans.reduce((sum, span) => sum + span.elapsedSeconds, 0));
  assert.ok(Math.max(...result.spans.map(span => Date.parse(span.startedAt)))
    <= Math.min(...result.spans.map(span => Date.parse(span.endedAt))));
});

test('bad arguments, missing artifacts, traversal, and write failures return sanitized errors', t => {
  const parent = fixture(t), run = cli('init', parent);
  writeFileSync(join(parent, 'file'), 'fixture');
  for (const args of [[], ['init'], ['init', join(parent, 'file')], ['init', parent, 'extra'],
    ['start', run, 'secret-command', 'safe'], ['start', run, 'review', 'token=secret-value'],
    ['end', run, '../outside', 'completed'], ['end', run, 'missing', 'completed'],
    ['summary', join(parent, 'absent')], ['summary', parent]]) {
    const result = invoke(...args);
    assert.equal(result.status, 2, JSON.stringify(args));
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /^timing: unavailable /);
    assert.doesNotMatch(result.stderr, /secret-value|Error:|at file:/);
  }
  const id = cli('start', run, 'review', 'code-review');
  assert.equal(invoke('end', run, id, 'secret-value').status, 2);
  assert.equal(summary(run).spans[0].outcome, 'incomplete');
});

test('corrupt or symlinked artifacts cannot produce a plausible summary', t => {
  const parent = fixture(t), run = cli('init', parent);
  const id = cli('start', run, 'review', 'acceptance');
  const startPath = join(run, `${id}.start.json`);
  const original = readFileSync(startPath, 'utf8');
  writeFileSync(startPath, '{broken');
  assert.equal(invoke('summary', run).status, 2);
  writeFileSync(startPath, original);
  const start = JSON.parse(original);
  start.started.monotonicNs = (process.hrtime.bigint() + 1_000_000_000_000n).toString();
  writeFileSync(startPath, JSON.stringify(start));
  assert.equal(invoke('end', run, id, 'completed').status, 2);
  rmSync(startPath);
  symlinkSync(join(parent, 'external-secret'), startPath);
  assert.equal(invoke('summary', run).status, 2);
  assert.deepEqual(readdirSync(parent).sort(), [run.split('/').at(-1)]);
});

test('existing malformed completion records are errors, not incomplete work', t => {
  const run = cli('init', fixture(t));
  const id = cli('start', run, 'review', 'acceptance');
  const endPath = join(run, `${id}.end.json`);
  for (const record of ['null', 'false', '0', '""', '{}', '[]']) {
    writeFileSync(endPath, record);
    const result = invoke('summary', run);
    assert.equal(result.status, 2, record);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /^timing: unavailable /);
  }
});

test('both client procedures route to executable shared timing commands', t => {
  const skillDir = fileURLToPath(new URL('../', import.meta.url));
  const directive = 'Read `timing.md` and apply its local timing procedure from worktree setup through the final summary.';
  for (const client of ['SKILL.md', 'codex.md']) {
    assert.ok(readFileSync(join(skillDir, client), 'utf8').includes(directive), client);
  }
  const implementer = readFileSync(join(skillDir, '../../agents/implementer.md'), 'utf8');
  assert.ok(implementer.includes('**Timing exception to Shell discipline**'));
  assert.ok(implementer.includes('only `start`/`end` calls to the installed `skills/implement/scripts/timing.mjs`'));
  assert.ok(implementer.includes('Do not request approval solely for timing'));
  const doc = readFileSync(join(skillDir, 'timing.md'), 'utf8');
  const commands = [...doc.matchAll(/```bash\n([^`]+)\n```/g)].map(match => match[1]);
  assert.equal(commands.length, 4);
  const parent = fixture(t);
  let run, id;
  for (const command of commands) {
    assert.ok(command.startsWith('node "<plugin-root>/skills/implement/scripts/timing.mjs" '));
    const args = command.slice(command.indexOf('" ') + 2).match(/"[^"]*"|\S+/g)
      .map(arg => arg.replace(/^"|"$/g, ''))
      .map(arg => ({ '<temp-parent>': parent, '<timing-dir>': run, '<span-id>': id }[arg] ?? arg));
    const output = cli(...args);
    if (args[0] === 'init') run = output;
    if (args[0] === 'start') id = output;
    if (args[0] === 'summary') {
      const result = JSON.parse(output);
      assert.equal(result.spans.length, 1);
      assert.equal(result.spans[0].id, id);
      assert.equal(result.spans[0].phase, 'implementation');
      assert.equal(result.spans[0].outcome, 'completed');
    }
  }
});
