#!/usr/bin/env bash
# Execute policy resolution against real configs; pin both clients' mutation
# boundary and the safety checks needed when final review is disabled.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 1
python3 - "${SCRIPT_DIR}/../skills/refine" <<'PY'
import json
import pathlib
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1])
resolver = root / "scripts/resolve-policy.sh"
assert resolver.is_file(), "refine must ship an executable policy resolver"

def resolve(config):
    with tempfile.TemporaryDirectory(prefix="refine-policy-") as directory:
        path = pathlib.Path(directory) / "config.json"
        path.write_text(json.dumps(config))
        result = subprocess.run(["bash", str(resolver), str(path)],
                                capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    return json.loads(result.stdout), result.stderr

defaults = {"confirmBeforeApply": False, "automerge": "off"}
for config in ({}, {"refinement": {}}, {"refinement": {"autonomy": "lean"},
              "planning": {"autonomy": "lean", "attended": False},
              "dispatch": {"planRefined": True}, "automerge": {"enabled": True}}):
    policy, notice = resolve(config)
    assert policy == defaults and not notice, (config, policy, notice)
for invalid in (None, [], "lean", True, 0):
    policy, notice = resolve({"refinement": invalid})
    assert policy == {"confirmBeforeApply": True, "automerge": "off"}
    assert "refinement invalid" in notice and "using confirmBeforeApply=true, automerge=off" in notice

# All combinations are independent of both planning and question autonomy.
for confirm in (False, True):
    for automerge in ("off", "eligible"):
        for question_mode in ("interactive", "lean"):
            for planning in ({}, {"autonomy": "lean", "attended": False},
                             {"autonomy": "interactive", "attended": True}):
                for plan_refined in (False, True):
                    for merge_enabled in (False, True):
                        config = {"refinement": {"confirmBeforeApply": confirm,
                                  "automerge": automerge, "autonomy": question_mode},
                                  "planning": planning, "dispatch": {"planRefined": plan_refined},
                                  "automerge": {"enabled": merge_enabled}}
                        policy, notice = resolve(config)
                        assert policy == {"confirmBeforeApply": confirm, "automerge": automerge}
                        assert not notice

for invalid in (None, "false", "true", 0, 1, [], {}, "eligible"):
    policy, notice = resolve({"refinement": {"confirmBeforeApply": invalid,
                                              "automerge": "eligible"}})
    assert policy == {"confirmBeforeApply": True, "automerge": "eligible"}
    assert "refinement.confirmBeforeApply" in notice and "using true" in notice
for invalid in (None, True, False, 0, [], {}, "on", "ELIGIBLE"):
    policy, notice = resolve({"refinement": {"automerge": invalid}})
    assert policy == defaults
    assert "refinement.automerge" in notice and "using off" in notice

with tempfile.TemporaryDirectory(prefix="refine-policy-") as directory:
    path = pathlib.Path(directory) / "bad.json"
    for payload in ("{", "null", "[]", "false", "{} {}"):
        path.write_text(payload)
        result = subprocess.run(["bash", str(resolver), str(path)],
                                capture_output=True, text=True)
        assert result.returncode != 0 and not result.stdout, payload
    result = subprocess.run(["bash", str(resolver), str(path.parent / "missing")],
                            capture_output=True, text=True)
    assert result.returncode != 0 and not result.stdout

for name in ("SKILL.md", "codex.md"):
    doc = (root / name).read_text()
    flat = " ".join(doc.split())
    for stale_boundary in ("reached only via the gate's Confirm branch",
                           "Reached immediately after the gate's Confirm,",
                           "first action after the gate's Confirm,"):
        assert stale_boundary not in flat, (name, stale_boundary)
    for rule in ("resolve-policy.sh", "confirmBeforeApply", "refinement.automerge",
                 "refinementAutomerge is exactly", "AND NOT", "policy-ready branch",
                 "never silently decide security-sensitive, destructive, or unresolved questions",
                 "every withheld question has a matching safe recorded decision",
                 "STOP with zero GitHub writes", "fresh refinement run"):
        assert rule in flat, (name, rule)
    assert "matches the rendered manifest" in flat, name

skill = (root / "SKILL.md").read_text()
assert skill.index("**Ticket ID validation**") < skill.index("3. **Create the per-run temp-file token.**") < skill.index('\nbash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/resolve-policy.sh"') < skill.index("4. **Write the context bundle.**")
assert "-policy-config.json" in skill
assert "Remove this policy scratch file on both helper success and failure" in skill
assert 'effective grant(T) = refinementAutomerge is exactly "eligible" AND ### Automation verdict for T is exactly "grant" AND NOT browserRequired AND NOT visual-check-signals-match' in skill
assert 'child\'s own `childBrowserRequired(K)` and `childVisualCheck(K)`' in skill
assert "Steps 11-12 consume that already-computed value; they never re-derive it" in skill
assert "each child's own earned `automerge:ok`/`Browser`/`ui:visual-check`" in skill

codex = (root / "codex.md").read_text()
assert '`refinementAutomerge is exactly "eligible"` AND `### Automation` verdict is' in codex
assert "Require `/plan`" not in codex
assert "normal invocation continues through preparation and apply in the same run" in codex
assert "Explicit Plan mode remains read-only" in codex
assert "Only a Confirm proceeds" not in codex
agent = (root.parent.parent / "agents/refiner.md").read_text()
assert "refinementConfirmBeforeApply" in agent
assert "never silently decide security-sensitive, destructive, or unresolved questions" in agent
blockers = (root / "parent-blockers.md").read_text()
assert "fresh refinement run" in blockers
assert "request a fresh refinement\nconfirmation" not in blockers
print("refine-independent-policy.test.sh: all checks passed.")
PY
