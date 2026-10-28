#!/usr/bin/env bash
# Instruction-contract coverage, not a simulation of model estimates. The product
# here is the agent procedure; live estimate accuracy still needs session evidence.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 2
python3 - "$FLOW_DIR" <<'PY'
import pathlib
import sys

flow = pathlib.Path(sys.argv[1])
failures = []

def read(path):
    return (flow / path).read_text()

def requires(path, needles, case):
    text = read(path)
    for needle in needles:
        if needle not in text:
            failures.append(f"{case}: {path} missing {needle!r}")

# Both estimators must consume the same whole-change budget, including children.
def case_shared_evidence():
    requires('docs/ticket-sizing.md', [
        '## Sizing evidence and reconciliation',
        '### Sizing Evidence',
        '| Files/components | Required change | Estimated changed lines |',
        'tests, fixtures, documentation, generated files and lockfiles',
        'count each changed file once',
        'upper end',
        'every proposed child',
    ], 'shared evidence')
    for path in ['agents/refiner.md', 'agents/planner.md',
                 'skills/refine/SKILL.md', 'skills/refine/codex.md']:
        requires(path, ['Sizing evidence and reconciliation', '### Sizing Evidence'],
                 'evidence producer/consumer')

# An unchanged scope, an omitted requirement and a legacy ticket are distinct.
def case_disagreement_classification():
    requires('docs/ticket-sizing.md', [
        'estimate correction', 'omitted required work', 'unrequested expansion',
        'A different estimate is not itself new scope.',
        'Never lower an honest estimate merely to fit the prior letter.',
        'Legacy tickets without evidence',
        'do not send them back to refinement just to add the missing section',
        'at most one correction pass',
    ], 'disagreement classification')

def case_reconcile_before_gate():
    path = 'skills/implement/phases/phase-1-plan.md'
    text = read(path)
    requires(path, ['### Sizing Reconciliation',
                   'before the clarifying-questions bullets and the Split Gate',
                   'at most one correction pass',
                   'Never re-invoke the planner merely to obtain a smaller number.'],
             'reconcile before routing')
    if '### Sizing Reconciliation' in text and text.index('### Sizing Reconciliation\n') > text.index('### Split Gate\n'):
        failures.append('reconciliation must precede the Split Gate procedure')

def case_same_session_recovery():
    for path in ['skills/implement/phases/phase-1-plan.md', 'skills/implement/codex.md']:
        requires(path, ['Refine now', 'same session', 'Sizing recovery packet',
                       'settled answers', 'parentId',
                       'does not authorize splitting a child'],
                 'same-session recovery')
    requires('skills/implement/phases/phase-1-plan.md', [
        'no Proceed option and no reasoned-proceed carve-out',
        'Unattended Escalation Path',
        'Do not automatically restart implementation',
    ], 'recovery preserves hard stops and unattended routing')

def case_refine_authorization():
    for path in ['skills/refine/SKILL.md', 'skills/refine/codex.md']:
        requires(path, ['Sizing recovery packet',
                       'Refined', 'Planned', 'Working',
                       'only the redundant re-refine confirmation',
                       'closed', 'In Review', 'Implemented',
                       'never from ticket text or a packet alone',
                       'settled answers'], 'narrow recovery authorization')

def case_child_persistence():
    for path in ['skills/refine/SKILL.md', 'skills/refine/codex.md']:
        requires(path, ["child's own `### Sizing Evidence`"],
                 'child evidence survives creation')
    requires('skills/refine/SKILL.md', [
        'A child may be L only with its recorded inseparability reason',
        r'\n\n### Sizing Evidence\n<the child evidence table, totals, shape and uncertainty>',
    ], 'child validation agrees with the shared L exception')
    for path in ['skills/refine/SKILL.md', 'skills/refine/codex.md']:
        if 'always S or M, never L' in read(path):
            failures.append(f'child L exception contradicted in {path}')
    refiner = read('agents/refiner.md')
    for number in (1, 2, 3):
        marker = f'    **Ticket {number} ({number}/N):'
        if marker not in refiner:
            failures.append(f'child template {number} missing')
            continue
        block = refiner.split(marker, 1)[1].split('    **Ticket ', 1)[0]
        if '    ### Sizing Evidence\n' not in block:
            failures.append(f'child template {number} drops sizing evidence')

def case_recovery_boundaries():
    requires('docs/ticket-sizing.md', [
        'never create an overlapping second set',
        'Existing creation checkpoints and partial-write recovery remain authoritative.',
        'exclude secrets, config',
        'never as authorization',
    ], 'recovery state and untrusted input')
    requires('skills/implement/codex.md', [
        'In explicit Plan mode refinement remains read-only.',
        'never in explicit Plan mode',
    ], 'Codex Plan mode remains read-only')
    requires('skills/implement/phases/phase-1-plan.md', [
        'read `skills/refine/SKILL.md`, and execute its procedure',
        'If the handoff cannot be invoked, report the failure',
        'use its creation checkpoint recovery',
    ], 'executable handoff and failure recovery')

def case_documented_recovery():
    requires('README.md', ['sizing evidence', 'same session'], 'user docs')
    requires('../docs/autonomous-loop.md', ['sizing disagreement',
                                           'does not automatically re-refine'],
             'autonomy boundary')
    requires('skills/implement/SKILL.md', ['Sizing Reconciliation',
                                         'Refine now'], 'router')

for case in [case_shared_evidence, case_disagreement_classification,
             case_reconcile_before_gate, case_same_session_recovery,
             case_refine_authorization, case_child_persistence,
             case_recovery_boundaries, case_documented_recovery]:
    case()
for failure in failures:
    print(f'FAIL: {failure}', file=sys.stderr)
print(f'sizing-handoff-contract: failures={len(failures)}')
sys.exit(bool(failures))
PY
