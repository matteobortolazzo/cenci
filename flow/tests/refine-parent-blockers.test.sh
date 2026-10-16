#!/usr/bin/env bash
# Exercise the actual jq selector shipped in the shared refinement procedure.
# The remaining assertions cover orchestration owned by the two skill adapters.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 1
python3 - "${SCRIPT_DIR}/../skills/refine" <<'PY'
import json
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
procedure = (root / "parent-blockers.md").read_text()
filters = re.findall(r"^jq -e -c '([^']+)'", procedure, re.M)
assert len(filters) == 1, "expected one executable open-parent-blocker selector"
selector = filters[0]

def node(number, state):
    return {"number": number, "state": state,
            "url": f"https://github.com/owner/repo/issues/{number}"}

def check(nodes, expected, total=None):
    payload = {"blockedBy": {"nodes": nodes,
                             "totalCount": len(nodes) if total is None else total}}
    result = subprocess.run(["jq", "-ce", "--arg", "repo", "owner/repo", selector],
                            input=json.dumps(payload), text=True, capture_output=True)
    if expected is None:
        assert result.returncode != 0, (payload, result.stdout)
    else:
        assert result.returncode == 0, result.stderr
        assert json.loads(result.stdout) == expected, result.stdout

check([node(20, "OPEN"), node(21, "CLOSED"), node(22, "OPEN")], [20, 22])
check([node(21, "CLOSED")], [])
check([], [])
check([node(20, "OPEN"), node(20, "OPEN")], [20])
check([node(20, "UNKNOWN")], None)
check([node(20, "OPEN")], None, total=2)
check([node(-1, "OPEN")], None)
check([node(1.5, "OPEN")], None)
check([node("20", "OPEN")], None)
check([{"number": 20, "state": "OPEN", "url": "https://github.com/other/repo/issues/20"}], None)
check([{"number": 20}], None)
for payload in ("", "not JSON", "{}", '{"blockedBy": null}',
                '{"blockedBy":{"nodes":[],"totalCount":null}}'):
    result = subprocess.run(["jq", "-ce", "--arg", "repo", "owner/repo", selector],
                            input=payload, text=True, capture_output=True)
    assert result.returncode != 0, payload

for name in ("SKILL.md", "codex.md"):
    doc = (root / name).read_text()
    flat = " ".join(doc.split())
    assert "parent-blockers.md" in doc, name
    assert "before rendering the manifest" in flat, name
    assert "before the first write" in flat, name
    assert "inherited open parent blockers + blocking siblings" in flat, name
    assert "Depends on #<blocker>" in doc, name
    assert "every expected blocker" in flat, name
    assert "first child never carries one" not in flat, name
    assert "the **first** child never carries one" not in flat, name

for rule in ("retry once", "STOP with zero writes", "already-linked blockers",
             "closed parent blockers", "same checkpoint", "totalCount"):
    assert rule in procedure, rule
print("refine-parent-blockers.test.sh: all checks passed.")
PY
