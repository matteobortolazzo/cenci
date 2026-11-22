#!/usr/bin/env bash
# Exercise the documented publish/arm entry points against a live-label fixture.
set -euo pipefail
TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 - "${TEST_DIR}/.." <<'PY'
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

flow = Path(sys.argv[1]).resolve()
helper = flow / 'skills/implement/scripts/accepted-size-hold.sh'
assert helper.is_file(), 'missing supervisor-effective accepted-size hold'
with tempfile.TemporaryDirectory(prefix='cenci-accepted-size-hold-') as temp:
    root = Path(temp)
    state = root / 'state.json'
    bindir = root / 'bin'
    bindir.mkdir()
    gh = bindir / 'gh'
    gh.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
p = Path(os.environ['HOLD_STATE'])
s = json.loads(p.read_text())
a = sys.argv[1:]
s['calls'].append(a)
p.write_text(json.dumps(s))
if a[:2] == ['issue', 'view']:
    assert a == ['issue', 'view', '42', '--repo', 'owner/repo', '--json', 'id,number,labels'], a
    if s.get('read_fail') or (s.get('verify_fail') and s.get('edited')):
        sys.exit(1)
    if s.get('malformed'):
        print('{}'); sys.exit(0)
    print(json.dumps({'id': s.get('id', 'I_source42'), 'number': s.get('number', 42), 'labels': [{'name': x} for x in s['labels']]}))
elif a[:2] == ['issue', 'edit']:
    assert a == ['issue', 'edit', '42', '--repo', 'owner/repo', '--remove-label', 'automerge:ok'], a
    if s.get('edit_fail'):
        sys.exit(1)
    if not s.get('retained_grant'):
        s['labels'] = [x for x in s['labels'] if x != 'automerge:ok']
    s['edited'] = True
    p.write_text(json.dumps(s))
elif a[:2] == ['pr', 'view']:
    assert a == ['pr', 'view', '7', '--repo', 'owner/repo', '--json', 'closingIssuesReferences'], a
    if s.get('pr_fail'):
        sys.exit(1)
    print(json.dumps({'closingIssuesReferences': s.get('references', [{'id': 'I_source42', 'number': n} for n in s.get('closing', [42])])}))
elif a[:2] == ['pr', 'create']:
    assert 'automerge:ok' not in s['labels'], 'grant present at create'
elif a[:1] == ['babysit']:
    assert 'automerge:ok' not in s['labels'], 'grant present at arm'
else:
    raise AssertionError(a)
''')
    gh.chmod(0o755)
    (bindir / 'cenci').symlink_to(gh)
    env = dict(os.environ, PATH=f'{bindir}:{os.environ["PATH"]}', HOLD_STATE=str(state),
               CLAUDE_PLUGIN_ROOT=str(flow), PLUGIN_ROOT=str(flow))

    def reset(**kwargs):
        state.write_text(json.dumps(dict(labels=['Working', 'track:workflow', 'automerge:ok'],
                                         calls=[], **kwargs)))

    def run(args, ok=True):
        result = subprocess.run(args, env=env, text=True, capture_output=True)
        assert (result.returncode == 0) == ok, (args, result.stdout, result.stderr)
        if not ok:
            assert 'hold=verified' not in result.stdout
        return result

    # An accepted M-to-L plan can finish with an S diff: size thresholds alone
    # cannot supply its promised founder-review hold.
    repo = root / 'repo'
    run(['git', 'init', '-q', '--initial-branch=main', str(repo)])
    run(['git', '-C', str(repo), 'config', 'user.email', 'fixture@example.invalid'])
    run(['git', '-C', str(repo), 'config', 'user.name', 'Fixture'])
    (repo / 'example.txt').write_text('base\n')
    run(['git', '-C', str(repo), 'add', 'example.txt'])
    run(['git', '-C', str(repo), 'commit', '-qm', 'base'])
    run(['git', '-C', str(repo), 'checkout', '-qb', 'feature'])
    (repo / 'example.txt').write_text('base\naccepted correction\n')
    run(['git', '-C', str(repo), 'add', 'example.txt'])
    run(['git', '-C', str(repo), 'commit', '-qm', 'small final diff'])
    plan = root / 'plan.md'
    plan.write_text('''## Ticket Details
### Size Estimate: M
Bound: <= 30 changed files, <= 2,000 changed lines, one feature slice, at most one migration.
Rationale: original estimate.
## Implementation Plan
### Size Estimate: L
Bound: <= 60 changed files, <= 5,000 changed lines, unchanged original-M scope; reason to retain one PR: coupled review.
Rationale: accepted estimate correction.
### Size Acceptance
Target: owner/repo#42
Original size: M
Accepted size: L
Scope: unchanged requirements snapshot
Supported estimate: 40 files, 3000 lines; complete sizing evidence
Lanes: exclusive first and second lane snapshots, each within M
Reason to retain one PR: coupled review
Human approval: founder answer recorded on source issue
Review: full founder read; no automerge
''')
    measurement = run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                       str(repo), 'main', '--plan', str(plan)]).stdout
    assert 'size=S ticket=L' in measurement and 'accepted=true' in measurement, measurement

    # Both clients' actual commands must guard creation and arming, including retries.
    for doc in ['skills/implement/phases/phase-9-pr.md', 'skills/implement/codex.md']:
        text = (flow / doc).read_text()
        commands = re.findall(r'^bash .*accepted-size-hold\.sh.*$', text, re.M)
        assert len(commands) == 2, (doc, 'creation and arm commands required', commands)
        assert '--pr' not in commands[0] and '--pr' in commands[1], doc
        create = text.index('gh pr create', text.index(commands[0]))
        arm_match = re.search(r'cenci babysit <pr(?:-number)?>', text[text.index(commands[1]):])
        assert arm_match, (doc, 'missing guarded supervisor launch')
        arm = text.index(commands[1]) + arm_match.start()
        assert text.index(commands[0]) < create < text.index(commands[1]) < arm, doc
        assert 'regardless of the measured size' in text, doc
        assert 'already exists' in text and 'archived' in text, doc
        for command, action in zip(commands, [['gh', 'pr', 'create'], ['cenci', 'babysit', '7']]):
            command = command.replace('<owner>/<repo>', 'owner/repo').replace('<ticketId>', '42').replace('<pr>', '7')
            reset()
            run(['bash', '-c', command])
            run(action)
            s = json.loads(state.read_text())
            assert s['labels'] == ['Working', 'track:workflow'], (doc, s)
            # Retried/reused PRs may have a newly re-granted label: revoke again.
            s['labels'].append('automerge:ok')
            state.write_text(json.dumps(s))
            run(['bash', '-c', command])
            run(action)
            assert len([a for a in json.loads(state.read_text())['calls'] if a[:2] == ['issue', 'edit']]) == 2
            for case in ['read_fail', 'edit_fail', 'verify_fail', 'retained_grant']:
                reset(**{case: True})
                # Drive the documented command as a real gate: no action may
                # occur after failure, even if creation/arming was retried.
                run(['bash', '-c', command + ' && ' + ' '.join(action)], ok=False)
                assert not any(a[:2] == ['pr', 'create'] or a[:1] == ['babysit']
                               for a in json.loads(state.read_text())['calls'])

    for case in ['read_fail', 'edit_fail', 'verify_fail', 'retained_grant', 'malformed', 'pr_fail']:
        reset(**{case: True})
        run(['bash', str(helper), 'owner/repo', '42', '--pr', '7'], ok=False)
    for closing in [[], [43]]:
        reset(closing=closing)
        run(['bash', str(helper), 'owner/repo', '42', '--pr', '7'], ok=False)
    # Numbers are repository-local. Another repo's #42 is not the revoked
    # source; missing or malformed node IDs cannot establish the connection.
    for reference in [{'number': 42, 'id': 'I_foreign42'}, {'number': 42},
                      {'number': 42, 'id': None}, {'number': 42, 'id': []}]:
        reset(references=[reference])
        run(['bash', str(helper), 'owner/repo', '42', '--pr', '7'], ok=False)
    for issue_id in [None, '', 42, []]:
        reset(id=issue_id)
        run(['bash', str(helper), 'owner/repo', '42', '--pr', '7'], ok=False)
    reset(number=43)
    run(['bash', str(helper), 'owner/repo', '42'], ok=False)
    reset()
    run(['bash', str(helper), 'owner/repo', 'parent'], ok=False)
    assert not json.loads(state.read_text())['calls'], 'invalid source must not touch GitHub'
    for arguments in [['owner/repo', '42', '--pr', 'bad'], ['bad repo', '42']]:
        reset()
        run(['bash', str(helper), *arguments], ok=False)
        assert not json.loads(state.read_text())['calls']
    # An already absent grant is idempotent and preserves unrelated labels.
    reset()
    s = json.loads(state.read_text())
    s['labels'].remove('automerge:ok')
    state.write_text(json.dumps(s))
    run(['bash', str(helper), 'owner/repo', '42', '--pr', '7'])
    assert not any(a[:2] == ['issue', 'edit'] for a in json.loads(state.read_text())['calls'])

print('accepted-size-hold: all behavioral fixtures passed')
PY
