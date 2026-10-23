#!/usr/bin/env bash
# Documentation contract: ordinary UI browser verification is settled before
# questions reach either client's user-input tool. These checks establish policy
# placement and relay wiring, not a claim of live-model behavioral coverage.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
python3 - "${SCRIPT_DIR}/.." <<'PY'
from pathlib import Path
import sys

root = Path(sys.argv[1])
agent = (root / "agents/refiner.md").read_text()
skill = (root / "skills/refine/SKILL.md").read_text()
codex = (root / "skills/refine/codex.md").read_text()
failures = []

def require(text, phrase, case):
    if phrase not in text:
        failures.append(case)

def between(text, start, end):
    return text.split(start, 1)[1].split(end, 1)[0]

shared = between(agent, "## Shared Question Policy", "## Lean Question Policy")
codex_shared = between(codex, "**Shared question policy.**", "**Lean refinement**")
for name, policy in (("refiner", shared), ("Codex", codex_shared)):
    require(policy, "The `Browser` requirement is never a question in either mode", f"{name}: browser policy applies outside lean-only section")
    require(policy, "never a question in either mode, whatever the classification", f"{name}: browser question is never asked, even for non-frontend work")
    require(policy, "For frontend/UI work, assume interactive browser verification", f"{name}: UI work defaults to browser verification")
    require(policy, "explicit user restriction", f"{name}: explicit browser restriction survives the default")
    require(policy, "security-sensitive, destructive, or unresolved product decisions", f"{name}: real consequential questions survive the default")

relay = between(skill, "6. **Q&A relay loop.**", "7. **Proposal received.**")
guard = between(relay, "**Browser default before relay (both modes).**", "**Interactive relay — unchanged:**") if "**Browser default before relay (both modes).**" in relay else ""
require(guard, "before either relay or lean-cap enforcement", "Claude: guard runs before asking or rejecting over-cap questions")
require(guard, "resolved `isFrontend`", "Claude: guard consumes classification rather than assuming every task is UI")
require(guard, "Auto-adopted from classification", "Claude: derived answer is distinguished from a user answer")
require(guard, "do not relay it, whatever the classification", "Claude: browser-only question is filtered even when the ticket is not frontend")
require(guard, "<required|not required>", "Claude: derived answer carries the step-8 outcome, including not required")
if "already entail it, do not relay it" in guard:
    failures.append("Claude: guard must not filter browser questions only when classification entails them")
require(guard, "complete accumulated history", "Claude: derived answer reaches the next analysis")
require(guard, "Retain every other question", "Claude: guard cannot drop real unanswered questions")
require(guard, "any remaining questions actually relayed still count", "Claude: mixed rounds cannot bypass the question cap")
require(guard, "at most one correction re-invocation", "Claude: browser-only output cannot cause an infinite retry loop")
require(guard, "STOP with zero GitHub writes", "Claude: repeated policy violations fail closed")
require(guard, "explicit user restriction", "Claude: guard respects an explicit browser restriction")
require(codex_shared, "Before presenting any question", "Codex: default is checked at the user-input boundary")
require(codex_shared, "do not consume a question slot", "Codex: settled browser choice does not consume caps")
require(codex_shared, "browser-based verification, whatever the classification", "Codex: browser-only question is filtered even when the ticket is not frontend")

# A mutation restoring the old lean-only placement must fail the same assertion
# even though the required sentence is still present elsewhere in the real agent.
marker = "The `Browser` requirement is never a question in either mode"
mutated = agent.replace(marker, "REMOVED_BROWSER_RULE").replace("## Lean Question Policy", "## Lean Question Policy\n" + marker, 1)
before = len(failures)
require(between(mutated, "## Shared Question Policy", "## Lean Question Policy"), marker, "mutation")
if len(failures) == before + 1:
    failures.pop()
else:
    failures.append("shared-policy assertion missed a move back into lean-only policy")
for failure in failures:
    print(f"FAIL: {failure}", file=sys.stderr)
if failures:
    sys.exit(1)
print("refine-browser-default.test.sh: all checks passed.")
PY
