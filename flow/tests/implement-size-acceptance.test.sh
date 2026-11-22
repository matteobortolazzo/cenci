#!/usr/bin/env bash
# Procedure contracts plus a real pr-size invocation against a local diff.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 2
python3 - "$FLOW_DIR" <<'PY'
import pathlib
import subprocess
import sys
import tempfile
import tomllib

flow = pathlib.Path(sys.argv[1])
failures = []

def require(path, needles, case):
    text = (flow / path).read_text()
    for needle in needles:
        if needle not in text:
            failures.append(f'{case}: {path} missing {needle!r}')

def require_branch(path, start, end, needles, case):
    text = (flow / path).read_text()
    try:
        branch = text.split(start, 1)[1].split(end, 1)[0]
    except IndexError:
        failures.append(f'{case}: {path} missing branch {start!r}')
        return
    for needle in needles:
        if needle not in branch:
            failures.append(f'{case}: {path} branch {start!r} missing {needle!r}')

phase = 'skills/implement/phases/phase-1-plan.md'
codex = 'skills/implement/codex.md'
for path in (phase, codex):
    require(path, ['### Accepted M-to-L correction', 'Accept corrected L — keep the original scope',
                   'unchanged original-M scope', 'omitted required work',
                   'full founder read', 'at least two delegated lanes',
                   'never inferred from unattended execution',
                   '### Size Acceptance', 'Original size: M', 'Accepted size: L',
                   'Target:', 'Scope:', 'Supported estimate:', 'Human approval:',
                   'stale or mismatched acceptance',
                   'original M estimate', 'same target, scope, evidence, supported upper totals and lanes'],
            'explicit corrected-size decision and durable binding')
    require(path, ['Beyond L', 'unsupported evidence', 'unrequested expansion',
                   'single-lane L', 'no Proceed option'], 'hard-stop boundaries')
    require(path, ['slash-command invocation is unavailable', 'execute the procedure inline',
                   'one self-contained, copyable', 'without a second request',
                   'sibling', 'dependencies', 'Q&A', 'partial-write'],
            'actual recovery dispatch and immediate complete fallback')
    require(path, ['Release the stopped implementation issue',
                   'gh issue edit <sourceId> --repo <owner>/<repo> --remove-label Working',
                   'original child', 'preserve the assignee and all other labels',
                   'retry the failed step once', 'already absent',
                   'Refine now and Stop', 'Input Needed',
                   'never in explicit Plan mode', 'never on Proceed'],
            'stopped implementation releases Working without collateral changes')

require(phase, ['**Sizing decision on draft resume.**',
                'answer to the synthesized lean sizing question',
                'before step 6', 're-escalate',
                'never silently persist an unaccepted correction'],
        'authorized lean answer cannot bypass the size gate')
require(phase, ['**Saved acceptance reuse.**',
                'Do not ask the same sizing question again',
                'does not approve the implementation plan'], 'reuse stays narrow')
require('agents/planner.md', ['### Size Acceptance', 'eligible original-M correction',
                            'never manufacture human acceptance',
                            'Original ticket bound:', 'Effective accepted bound:'],
        'planner emits honest old and effective comparisons')

# Refinement calls the same role for evidence, not implementation planning.
# Both role instructions must select that bounded mode before their ordinary
# full-plan output contract, and both callers must actually request it.
planner = (flow / 'agents/planner.md').read_text()
inventory_heading = '## Refinement inventory mode'
if inventory_heading not in planner or planner.index(inventory_heading) > planner.index('## Before Planning'):
    failures.append('planner inventory-mode dispatch must precede Before Planning')
require_branch('agents/planner.md', inventory_heading, '## Before Planning',
               ['independent sizing inventory', 'read-only', '### Sizing Evidence',
                'registration and guard paths', 'generated assets', 'lockfiles',
                'tests, fixtures and docs', 'count unique paths once',
                'supported upper estimates', 'uncertainty',
                'Do not produce an implementation plan', 'do not ask questions',
                'never infer approval', 'Return after the inventory'],
               'planner bounded refinement mode overrides normal planning')
require('templates/codex/agent-roles/planner.toml',
        ['independent sizing inventory', 'read-only inventory mode takes precedence',
         'registration and guard paths', 'generated assets', 'lockfiles',
         'tests, fixtures and docs', 'count unique paths once',
         'supported upper estimates', 'uncertainty',
         'Do not produce an implementation plan', 'do not ask questions',
         'never infer approval', 'Return after the inventory'],
        'portable planner role preserves the same inventory-only contract')
template_planner = tomllib.loads((flow / 'templates/codex/agent-roles/planner.toml').read_text())
installed_planner = tomllib.loads((flow.parent / '.codex/agents/planner.toml').read_text())
if installed_planner['developer_instructions'] != template_planner['developer_instructions']:
    failures.append('tracked installed planner instructions must match the current portable role template')
for caller in ['skills/refine/SKILL.md', 'skills/refine/codex.md']:
    text = (flow / caller).read_text().lower()
    for needle in ['independent sizing inventory', '`planner`', 'refinement inventory mode',
                   'complete independent path/budget inventory', 'ignore extra sections',
                   'without configure']:
        if needle not in text:
            failures.append(f'{caller}: inventory role/caller compatibility missing {needle!r}')
require('skills/implement/phases/phase-3-test-red.md',
        ['### Size Acceptance', 'same target, scope, evidence, supported upper totals and lanes',
         'full founder read'], 'apply-time acceptance validation')
require('skills/implement/phases/phase-9-pr.md',
        ['### Size Acceptance', 'original M estimate', 'full founder read',
         'original M with confirmed L requires a valid acceptance record',
         '--ticket L cannot bypass'],
        'PR carries decision and review requirement')
require_branch('skills/implement/phases/phase-3-test-red.md',
               'A lane larger than M, or a single-lane L,', '\n\n',
               ['Release the stopped implementation issue', 'source issue', 'explicit Plan mode', 'ticketless'],
               'over-M/single-lane preflight branch releases source Working')
require_branch('skills/implement/phases/phase-3-test-red.md',
               '**Structural re-check', '\n\n',
               ['Release the stopped implementation issue'],
               'structural over-M/single-lane branch retains cleanup')
require_branch(codex, 'second resume, is reported to the root as a split signal',
               'In ticket mode, `/plan`',
               ['Release the stopped implementation issue', 'original child', 'explicit Plan mode', 'ticketless'],
               'Codex context-budget branch releases source Working')
require('skills/implement/SKILL.md', ['Accepted M-to-L correction', 'Size Acceptance'],
        'router names corrected-size branch')

# Original details may precede the confirmed size. Prefer Implementation Plan;
# a missing confirmed estimate must not silently read the original ticket size.
with tempfile.TemporaryDirectory(prefix='cenci-size-acceptance-') as tmp:
    root = pathlib.Path(tmp)
    def git(*args):
        return subprocess.run(['git', '-C', str(root), *args], check=True,
                              capture_output=True, text=True).stdout.strip()
    git('init', '-q')
    git('config', 'user.name', 'Sizing fixture')
    git('config', 'user.email', 'sizing@example.invalid')
    (root / 'change.txt').write_text('base\n')
    git('add', 'change.txt')
    git('commit', '-qm', 'base')
    base = git('rev-parse', 'HEAD')
    (root / 'change.txt').write_text('base\n' + 'changed\n' * 2100)
    git('add', 'change.txt')
    git('commit', '-qm', 'L diff')
    cases = [
        ('accepted', '## Ticket Details\n### Size Estimate: M\noriginal\n'
         '## Implementation Plan\n### Size Estimate: L\nconfirmed\n'
         '### Size Acceptance\nTarget: owner/repo#1\nOriginal size: M\nAccepted size: L\n'
         'Scope: unchanged criteria\nSupported estimate: 1 file; 2100 lines; evidence snapshot\n'
         'Lanes: two exclusive M lanes\nReason to retain one PR: same slice\n'
         'Review: full founder read; no automerge\nHuman approval: explicit user turn 3\n', 'L', 'none'),
        ('legacy', '### Size Estimate: M\nlegacy\n', 'M', 'above'),
        ('missing-confirmed', '## Ticket Details\n### Size Estimate: M\noriginal\n'
         '## Implementation Plan\n### Summary\nmissing\n', 'unknown', 'unknown'),
        ('fenced', '## Ticket Details\n### Size Estimate: L\noriginal\n'
         '## Implementation Plan\n```markdown\n### Size Estimate: S\n```\n'
         '### Size Estimate: L\nconfirmed\n', 'L', 'none'),
    ]
    for name, plan, ticket, mismatch in cases:
        path = root / f'{name}.md'
        path.write_text(plan)
        result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                                 str(root), base, '--plan', str(path)],
                                capture_output=True, text=True)
        expected = f'size=L ticket={ticket} mismatch={mismatch}'
        if result.returncode or expected not in result.stdout:
            failures.append(f'PR confirmed size {name}: expected {expected!r}, got {result.stdout!r} {result.stderr!r}')
        if name == 'accepted' and ('vs accepted L (original ticket M)' not in result.stdout
                                   or 'full founder read required before merge; no automerge' not in result.stdout):
            failures.append('accepted correction must preserve original M and full L review in actual PR output')

    accepted_plan = cases[0][1]
    path = root / 'accepted-override.md'
    path.write_text(accepted_plan)
    result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                             str(root), base, '--plan', str(path), '--ticket', 'L'],
                            capture_output=True, text=True)
    if result.returncode or 'vs accepted L (original ticket M)' not in result.stdout or 'accepted=true' not in result.stdout:
        failures.append(f'explicit matching override must retain accepted-size audit/review output: {result!r}')
    result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                             str(root), base, '--plan', str(path), '--ticket', 'M'],
                            capture_output=True, text=True)
    if result.returncode != 2 or 'conflicts with the plan Size Acceptance' not in result.stderr or result.stdout:
        failures.append(f'conflicting override must not discard acceptance: {result!r}')
    # Real bundles fence attacker-controlled Ticket Details with a delimiter
    # longer than any body fence. Its headings never become plan sections.
    fenced_original = accepted_plan.replace('## Ticket Details\n### Size Estimate: M\noriginal\n',
        '## Ticket Details\n````text\n## Description\noriginal\n'
        '### Size Estimate: M\n```text\n## Implementation Plan\n'
        '### Size Estimate: S\n```\n````\n')
    path = root / 'fenced-original.md'
    path.write_text(fenced_original)
    result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                             str(root), base, '--plan', str(path)],
                            capture_output=True, text=True)
    if result.returncode or 'size=L ticket=L mismatch=none' not in result.stdout or 'vs accepted L (original ticket M)' not in result.stdout:
        failures.append(f'real fenced Ticket Details must preserve original M without accepting embedded plan headings: {result!r}')
    for name, malformed in [
        ('missing-human', accepted_plan.replace('Human approval: explicit user turn 3\n', '')),
        ('empty-human', accepted_plan.replace('Human approval: explicit user turn 3', 'Human approval:   ')),
        ('missing-original', accepted_plan.replace('## Ticket Details\n### Size Estimate: M\noriginal\n', '')),
        ('wrong-confirmed', accepted_plan.replace('### Size Estimate: L', '### Size Estimate: M')),
        ('duplicate-acceptance', accepted_plan + '\n### Size Acceptance\nHuman approval: ambiguous duplicate\n'),
        ('duplicate-human', accepted_plan + 'Human approval: conflicting second claim\n'),
        ('conflicting-size', accepted_plan + 'Accepted size: M\n'),
    ]:
        path = root / f'{name}.md'
        path.write_text(malformed)
        for override in [[], ['--ticket', 'L']]:
            result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                                     str(root), base, '--plan', str(path), *override],
                                    capture_output=True, text=True)
            if result.returncode != 2 or 'incomplete or inconsistent' not in result.stderr or result.stdout:
                failures.append(f'malformed acceptance {name} {override!r} must fail closed, got {result!r}')

    # A confirmed L alone is not approval of an original-M correction. Check
    # both plan orders/fencing and the CLI override so neither is an escape.
    no_acceptance = accepted_plan.split('### Size Acceptance\n', 1)[0]
    fenced_no_acceptance = fenced_original.split('### Size Acceptance\n', 1)[0]
    for name, unsafe_plan in [('missing-acceptance', no_acceptance),
                              ('fenced-missing-acceptance', fenced_no_acceptance),
                              ('override-only', no_acceptance.replace('### Size Estimate: L', '### Size Estimate: M'))]:
        path = root / f'{name}.md'
        path.write_text(unsafe_plan)
        overrides = [[], ['--ticket', 'L']] if name != 'override-only' else [['--ticket', 'L']]
        for override in overrides:
            result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                                     str(root), base, '--plan', str(path), *override],
                                    capture_output=True, text=True)
            if result.returncode != 2 or 'Size Acceptance' not in result.stderr or result.stdout:
                failures.append(f'unaccepted original-M correction {name} {override!r} must fail closed, got {result!r}')

    # Existing --ticket behavior remains available for unrelated legacy plans
    # that have no original-M baseline or corrected-size acceptance record.
    path = root / 'legacy-override.md'
    path.write_text('## Implementation Plan\n### Size Estimate: M\nlegacy\n')
    result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                             str(root), base, '--plan', str(path), '--ticket', 'L'],
                            capture_output=True, text=True)
    if result.returncode or 'size=L ticket=L mismatch=none' not in result.stdout:
        failures.append(f'unrelated legacy explicit size override regressed: {result!r}')

# Exercise the actual measured L/beyond-L boundaries with original-M acceptance
# records. These are diff/reporting checks; models still validate the scope and
# human provenance in the procedure, not in this measurement script.
for count, total, expected_size, mismatch in [(31, 2090, 'L', 'none'),
                                               (38, 2040, 'L', 'none'),
                                               (61, 2090, 'XL', 'beyond'),
                                               (38, 5001, 'XL', 'beyond')]:
    with tempfile.TemporaryDirectory(prefix='cenci-size-boundary-') as tmp:
        root = pathlib.Path(tmp)
        git('init', '-q')
        git('config', 'user.name', 'Sizing fixture')
        git('config', 'user.email', 'sizing@example.invalid')
        (root / 'base.txt').write_text('base\n')
        git('add', 'base.txt')
        git('commit', '-qm', 'base')
        base = git('rev-parse', 'HEAD')
        for number in range(count):
            lines = total // count + (number < total % count)
            (root / f'change-{number}.txt').write_text('changed\n' * lines)
        git('add', '.')
        git('commit', '-qm', 'sizing boundary')
        path = root / 'plan.md'
        path.write_text(accepted_plan)
        result = subprocess.run(['bash', str(flow / 'skills/implement/scripts/pr-size.sh'),
                                 str(root), base, '--plan', str(path)],
                                capture_output=True, text=True)
        expected = f'files={count} lines={total} size={expected_size} ticket=L mismatch={mismatch}'
        if result.returncode or expected not in result.stdout:
            failures.append(f'measured boundary {count}/{total}: expected {expected!r}, got {result.stdout!r}')
        if mismatch == 'beyond' and 'it must be split; do not merge' not in result.stdout:
            failures.append(f'beyond-L {count}/{total} must remain a split stop after prior acceptance')

for failure in failures:
    print(f'FAIL: {failure}', file=sys.stderr)
print(f'implement-size-acceptance: failures={len(failures)}')
sys.exit(bool(failures))
PY
