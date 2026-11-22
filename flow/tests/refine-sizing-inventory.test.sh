#!/usr/bin/env bash
# Refinement is a prose workflow: pin both client procedures, then exercise the
# real coverage gate with the retained-sibling + narrowed-child recovery shape.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
failures=0
require() {
  if ! grep -qF -- "$2" "$1"; then
    printf 'FAIL: %s: %s\n' "$1" "$2" >&2
    failures=$((failures + 1))
  fi
}
for procedure in "${FLOW_DIR}/skills/refine/SKILL.md" "${FLOW_DIR}/skills/refine/codex.md"; do
  require "$procedure" 'Independent sizing inventory (mandatory before writes)'
  require "$procedure" 'registration and guard paths, generated assets, lockfiles, tests, fixtures and docs'
  require "$procedure" 'independently discover the minimum required file fan-out'
  require "$procedure" 'count unique paths once and use supported upper estimates'
  require "$procedure" 'If independent inventory cannot run or remains incomplete, STOP before any GitHub write'
  require "$procedure" 'Re-cut an infeasible partition in this refinement pass'
  require "$procedure" 'Never ask the user to supply or approve a split proposal merely to do this analysis'
  require "$procedure" 'source child, parent target, all sibling scopes/states, coverage ownership, dependencies and settled Q&A'
  require "$procedure" 'the actual Refine now selection must be bound to this repository and parent target by its question context'
  require "$procedure" 'selecting the offered option is sufficient even when the answer contains only its label'
  require "$procedure" 'Existing-child recovery (same parent, no recursive split)'
  require "$procedure" 'retain existing issue IDs; narrow the source child in place and add only uncovered sibling work'
  require "$procedure" 'all retained live siblings, the narrowed source child and proposed new siblings'
  require "$procedure" 'never initialize replacement nonces or adopt a lookalike child by title'
  require "$procedure" 'Re-fetch the parent membership and every affected child before the first write'
  require "$procedure" 'consume that inventory and ignore extra sections, without configure or a new run solely for the output shape'
  require "$procedure" 'A carried M-to-L acceptance is evidence, never fresh permission'
done
require "${FLOW_DIR}/skills/refine/SKILL.md" 'retained-child registry, outside `### Suggested Split`'
require "${FLOW_DIR}/skills/refine/codex.md" 'retained-child registry outside the changed/new split blocks'
require "${FLOW_DIR}/agents/refiner.md" 'independent inventory'
require "${FLOW_DIR}/agents/refiner.md" 'Do not ask the user to design the partition'
require "${FLOW_DIR}/agents/refiner.md" 'Retain existing issue IDs and their owned parent items'
# Inventory must precede proposal drafting, rather than being an optional
# retrospective check after the manifest or the writes.
python3 - "$FLOW_DIR" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
for filename, drafting in [
    ('skills/refine/SKILL.md', '**Delegate to the refiner agent**'),
    ('skills/refine/codex.md', '**Sizing evidence.**'),
]:
    text = (root / filename).read_text()
    assert text.index('**Independent sizing inventory (mandatory before writes).**') < text.index(drafting), filename

# #5329: the supported numeric upper bounds control recovery, even when a
# legacy issue says L and its rationale admits that the work can be stacked.
import re
for filename in ['skills/refine/SKILL.md', 'skills/refine/codex.md']:
    text = (root / filename).read_text()
    start = text.index('**Over-L child recovery (before Size Estimate format checks).**')
    end = text.index('\n\n', start)
    recovery = text[start:end]
    rule = re.search(r'supportedFileUpper > (\d+) OR supportedLineUpper > (\d+)', recovery)
    assert rule, filename
    file_limit, line_limit = map(int, rule.groups())
    for files, lines, over in [(64, 5360, True), (61, 4900, True), (60, 5001, True), (60, 5000, False)]:
        assert (files > file_limit or lines > line_limit) is over, (filename, files, lines)
    assert 'never offer Proceed or persist Refined as one PR' in recovery, filename
    assert 'Refine parent now' in recovery and 'explicitly authorized split' in recovery, filename
    assert 'can be stacked' in recovery and 'source evidence, not an adopted proposal' in recovery, filename
    assert 'before applying the final Size Estimate format check' in recovery, filename
    assert 'Final written proposals still pass every format, evidence and coverage gate' in recovery, filename
    assert 'do not re-ask this recovery question' in recovery, filename
    final_format = '#### Size Estimate check (every proposal)' if filename.endswith('SKILL.md') else '**Concrete criteria, sized ticket.**'
    assert start < text.index(final_format), filename
PY

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/refine-inventory-test.XXXXXX")"
trap 'rm -rf "$fixture_dir"' EXIT
cat > "$fixture_dir/items.md" <<'EOF'
1. [acceptance criterion] Source child retains the API behavior.
2. [documentation item] Retained sibling documents the public contract.
3. [boundary] New sibling owns mandatory registration and guard changes.
EOF
printf '%s\n' '- [ ] (P1) Source child retains the API behavior.' > "$fixture_dir/source.md"
printf '%s\n' '- [ ] (P2) Retained sibling documents the public contract.' > "$fixture_dir/retained.md"
printf '%s\n' '- [ ] (P3) New sibling owns mandatory registration and guard changes.' > "$fixture_dir/new.md"
gate=(bash "${FLOW_DIR}/skills/refine/scripts/coverage-map.sh" check --items "$fixture_dir/items.md" --child "#101=$fixture_dir/source.md" --child "#102=$fixture_dir/retained.md" --child "new sibling=$fixture_dir/new.md")
"${gate[@]}" > "$fixture_dir/result"
require "$fixture_dir/result" 'items=3 owned=3 children=3'
require "$fixture_dir/result" '#102'
# Dropping an unchanged sibling must fail rather than silently lose its scope.
status=0
bash "${FLOW_DIR}/skills/refine/scripts/coverage-map.sh" check --items "$fixture_dir/items.md" --child "#101=$fixture_dir/source.md" --child "new sibling=$fixture_dir/new.md" > "$fixture_dir/result" 2> "$fixture_dir/error" || status=$?
[[ "$status" -eq 3 ]] || { printf 'FAIL: dropped sibling exit %s\n' "$status" >&2; failures=$((failures + 1)); }
require "$fixture_dir/error" 'unowned: 2'
# Failing to narrow the source before adding the sibling must fail overlap.
cat "$fixture_dir/new.md" >> "$fixture_dir/source.md"
status=0
"${gate[@]}" > "$fixture_dir/result" 2> "$fixture_dir/error" || status=$?
[[ "$status" -eq 4 ]] || { printf 'FAIL: overlapping recovery exit %s\n' "$status" >&2; failures=$((failures + 1)); }
require "$fixture_dir/error" 'duplicate: 3'
[[ "$failures" -eq 0 ]] || exit 1
printf '%s\n' 'refine-sizing-inventory.test.sh: all assertions passed.'
