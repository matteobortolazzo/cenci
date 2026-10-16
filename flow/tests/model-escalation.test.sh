#!/usr/bin/env bash
# Exercise real role installation; Markdown assertions cover dispatch contracts,
# not live model quality or whether a client follows the instructions.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
python3 - "$SCRIPT_DIR/.." <<'PY'
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib
import unittest

FLOW = Path(sys.argv.pop()).resolve()


class ModelEscalation(unittest.TestCase):
    def test_install_adds_astra_specialist_without_overwriting_custom_roles(self):
        with tempfile.TemporaryDirectory(prefix="cenci-model-escalation-") as tmp:
            roles = Path(tmp) / ".codex/agents"
            roles.mkdir(parents=True)
            custom = 'name = "planner"\nmodel = "custom-model"\n'
            (roles / "planner.toml").write_text(custom)
            env = dict(os.environ, PLUGIN_ROOT=str(FLOW))
            for _ in range(2):
                subprocess.run(["sh", str(FLOW / "codex/install-agents.sh"), tmp],
                               env=env, check=True)
                specialist = tomllib.loads((roles / "domain-specialist.toml").read_text())
                self.assertEqual(specialist["model"], "gpt-6-astra")
                self.assertEqual(specialist["model_reasoning_effort"], "high")
                self.assertEqual((roles / "planner.toml").read_text(), custom)
                for name in ("implementer", "critical-reviewer", "context-gatherer", "structure-analyzer"):
                    actual = (roles / f"{name}.toml").read_bytes()
                    self.assertEqual(actual, (FLOW / f"templates/codex/agent-roles/{name}.toml").read_bytes())
                    self.assertNotEqual(tomllib.loads(actual.decode())["model"], "gpt-6-astra")
            customized = (roles / "domain-specialist.toml").read_text().replace("gpt-6-astra", "custom-specialist")
            (roles / "domain-specialist.toml").write_text(customized)
            subprocess.run(["sh", str(FLOW / "codex/install-agents.sh"), tmp], env=env, check=True)
            self.assertEqual((roles / "domain-specialist.toml").read_text(), customized)

    def test_shipped_specialists_are_read_only_and_mirrored(self):
        claude = (FLOW / "agents/domain-specialist.md").read_text()
        frontmatter = claude.split("---", 2)[1]
        for line in ("model: fable", "effort: high", "permissionMode: plan", "tools: Read, Grep, Glob"):
            self.assertIn(line, frontmatter.splitlines())
        template = (FLOW / "templates/codex/agent-roles/domain-specialist.toml").read_text()
        self.assertEqual(template, (FLOW.parent / ".codex/agents/domain-specialist.toml").read_text())
        role = tomllib.loads(template)
        for text in (claude, role["developer_instructions"]):
            self.assertIn("Do not edit files", text)
            self.assertIn("Do not delegate", text)
            self.assertIn("failure scenario", text)

    def test_codex_can_inspect_paths_without_execution_privileges(self):
        role = tomllib.loads((FLOW / "templates/codex/agent-roles/domain-specialist.toml").read_text())
        instructions = role["developer_instructions"]
        self.assertTrue("read-only local shell" in instructions, "Codex needs shell-backed file reads")
        self.assertTrue("run builds/tests" in instructions, "command execution must stay bounded")

    def test_parallel_lane_barrier_cannot_skip_diagnosis(self):
        text = (FLOW / "skills/implement/phases/phase-4-implement-green.md").read_text()
        context = text.split("## Delegation Context", 1)[1].split("## Rules", 1)[0]
        barrier = text.split("\n## Lane Verification Barrier\n", 1)[1]
        with self.subTest(section="delegation context"):
            self.assertTrue("two failed attempts" in context, "new implementers must receive the return condition")
        with self.subTest(section="lane barrier"):
            self.assertTrue("domain-specialist" in barrier, "lane-only entry skips the phase preamble")

    def test_routine_claude_pins_stay_unchanged(self):
        expected = {"agents/refiner.md": "opus", "agents/planner.md": "opus",
                    "agents/security-reviewer.md": "opus", "agents/implementer.md": "sonnet",
                    "agents/silent-failure-hunter.md": "sonnet", "agents/code-reviewer.md": "sonnet",
                    "agents/context-gatherer.md": "haiku", "agents/structure-analyzer.md": "haiku",
                    "skills/implement/SKILL.md": "sonnet", "skills/refine/SKILL.md": "sonnet",
                    "skills/babysit/SKILL.md": "sonnet", "skills/sync/SKILL.md": "haiku"}
        for path, model in expected.items():
            with self.subTest(path=path):
                self.assertIn(f"model: {model}", (FLOW / path).read_text().split("---", 2)[1].splitlines())

    def test_every_workflow_applies_the_shared_policy(self):
        # Separate subtests name each promised integration, including compact/lane
        # implementation and both planning entry paths.
        paths = (
            "skills/refine/SKILL.md", "skills/refine/codex.md",
            "skills/implement/phases/phase-1-plan.md", "skills/implement/codex.md",
            "skills/implement/phases/phase-3-test-red.md",
            "skills/implement/phases/phase-4-implement-green.md",
            "skills/implement/phases/phase-6-7-review.md",
            "skills/review/SKILL.md", "skills/review/codex.md",
            "skills/refactor/SKILL.md", "skills/refactor/codex.md",
            "skills/maintain/SKILL.md", "skills/maintain/codex.md",
            "skills/ci-repair/SKILL.md", "skills/address-review/SKILL.md",
            "skills/address-review/codex.md",
        )
        for path in paths:
            with self.subTest(path=path):
                text = (FLOW / path).read_text()
                self.assertTrue("docs/model-escalation.md" in text, f"{path}: missing escalation policy")
                self.assertTrue("domain-specialist" in text, f"{path}: missing specialist dispatch")

    def test_policy_has_triggers_dispatch_and_bounded_failure_behavior(self):
        policy = " ".join((FLOW / "docs/model-escalation.md").read_text().split())
        for requirement in (
            "cross-project", "sandbox boundaries", "validated config", "two failed attempts",
            "conflicting findings", "Task", "agent_type", 'fork_turns="none"',
            'model="gpt-6-astra"', "CLAUDE_CODE_SUBAGENT_MODEL",
            "one specialist pass", "does not reset", "Do not retry a model-access failure",
            "Existing approval", "read the cited source", "OpenCode",
        ):
            with self.subTest(requirement=requirement):
                self.assertIn(requirement, policy)


unittest.main()
PY
