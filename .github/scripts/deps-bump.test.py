#!/usr/bin/env python3
"""Execute deps-bump.yml's real shell against local Git and fake GitHub/upstreams."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/deps-bump.yml"
SHIM = r'''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
args = sys.argv[1:]
root = pathlib.Path(os.environ["CASE_ROOT"])
tool = pathlib.Path(sys.argv[0]).name
with (root / "calls").open("a") as f:
    f.write(json.dumps([tool, *args]) + "\n")
if "HOST_SECRET" in os.environ:
    sys.exit("ambient environment leaked into workflow")
def option(name):
    return args[args.index(name) + 1]
if tool == "git":
    if args[:1] == ["ls-remote"] and (root / "remote-error").exists():
        sys.exit(128)
    os.execv(os.environ["REAL_GIT"], ["git", *args])
if tool == "sleep":
    sys.exit(0)
if tool == "curl":
    url = args[-1]
    fixture = {"https://go.dev/VERSION?m=text": "go",
        "https://builds.dotnet.microsoft.com/dotnet/release-metadata/releases-index.json": "dotnet",
        "https://raw.githubusercontent.com/nodejs/Release/main/schedule.json": "node-schedule",
        "https://nodejs.org/dist/index.json": "node-dist"}.get(url)
    if fixture is None:
        sys.exit("unexpected curl request: " + repr(args))
    print((root / fixture).read_text())
    sys.exit(0)
if tool != "gh":
    sys.exit("unexpected tool")
if args == ["api", "repos/astral-sh/uv/releases/latest", "--jq", ".tag_name"]:
    print((root / "uv").read_text())
elif args[:2] == ["pr", "list"]:
    assert option("--base") == "main" and option("--state") == "all", args
    assert option("--json") == "url,state,isCrossRepository", args
    if (root / "query-error").exists():
        sys.exit("GitHub unavailable")
    branch = option("--head")
    path = root / (branch + ".pr")
    pr = json.loads(path.read_text()) if path.exists() else None
    fork_path = root / "fork-pr"
    prs = [json.loads(fork_path.read_text())] if fork_path.exists() else []
    if pr:
        prs.append(pr)
    # Use real jq so --jq's expression is exercised, rather than ignored.
    result = subprocess.run(["jq", "-r", option("--jq")],
        input=json.dumps(prs), text=True, check=True, capture_output=True)
    print(result.stdout, end="")
elif args[:2] == ["pr", "create"]:
    branch = option("--head")
    assert option("--base") == "main", args
    subprocess.run([os.environ["REAL_GIT"], "ls-remote", "--exit-code", "--heads", "origin", branch],
        check=True, stdout=subprocess.DEVNULL)
    if (root / "create-error").exists():
        sys.exit("GitHub Actions is not permitted to create or approve pull requests")
    path = root / (branch + ".pr")
    assert not path.exists(), "duplicate PR creation"
    pr = {"state": "OPEN", "url": "https://example.test/pull/" + branch, "isCrossRepository": False}
    path.write_text(json.dumps(pr))
    print(pr["url"])
elif args[:2] in (["pr", "merge"], ["pr", "view"]):
    branch = args[2].rsplit("/", 1)[-1]
    path = root / (branch + ".pr")
    pr = json.loads(path.read_text())
    if args[1] == "merge":
        assert {"--auto", "--squash", "--delete-branch"} <= set(args), args
        if (root / "merge-error").exists():
            sys.exit("auto-merge disabled")
        pr["state"] = (root / "poll-state").read_text() if (root / "poll-state").exists() else "MERGED"
        path.write_text(json.dumps(pr))
    else:
        assert args[3:] == ["--json", "state", "--jq", ".state"], args
        if (root / "view-error").exists():
            sys.exit("GitHub unavailable while polling")
        print(pr["state"])
elif args == ["workflow", "run", "sandbox — Version Bump"]:
    if (root / "dispatch-error").exists():
        sys.exit("dispatch unavailable")
else:
    sys.exit("unexpected gh request: " + repr(args))
'''


class DependencyRecovery(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="deps-bump-test-")
        self.addCleanup(self.tmp.cleanup)
        self.case = Path(self.tmp.name)
        self.repo = self.case / "work"
        self.repo.mkdir()
        self.remote = self.case / "origin.git"
        self.real_git = shutil.which("git")
        self.git_env = {"PATH": os.environ["PATH"], "HOME": str(self.case),
                        "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
                        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": "2",
                        "GIT_CONFIG_KEY_0": "gc.auto", "GIT_CONFIG_VALUE_0": "0",
                        "GIT_CONFIG_KEY_1": "maintenance.auto", "GIT_CONFIG_VALUE_1": "false"}
        self.git("init", "--quiet", "--initial-branch=main")
        self.git("config", "user.name", "test")
        self.git("config", "user.email", "test@example.test")
        self.git("config", "gc.auto", "0")
        self.git("init", "--quiet", "--bare", "--initial-branch=main", str(self.remote))
        files = {
            "sandbox/Dockerfile": "ARG GO_VERSION=1.0.0\nARG DOTNET_SDK_VERSION=8.0.100\nARG NODE_MAJOR=20\n",
            "sandbox/Dockerfile.base": "ARG UV_VERSION=0.1.0\n",
            "sandbox/fragments/go.dockerfile": "ARG GO_VERSION=1.0.0\n",
            "sandbox/fragments/dotnet.dockerfile": "ARG DOTNET_SDK_VERSION=8.0.100\n",
            "sandbox/fragments/node.dockerfile": "ARG NODE_MAJOR=20\n",
        }
        for name, content in files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        self.git("add", ".")
        self.git("commit", "--quiet", "-m", "fixture")
        self.git("remote", "add", "origin", str(self.remote))
        self.git("push", "--quiet", "-u", "origin", "main")
        self.write("go", "go1.0.1")
        self.write("uv", "v0.1.0")
        self.write("dotnet", json.dumps({"releases-index": [
            {"channel-version": "8.0", "latest-sdk": "8.0.100", "support-phase": "active"}]}))
        self.write("node-schedule", '{"v20":{"end":"2999-01-01"}}')
        self.write("node-dist", '[{"version":"v22.0.0","lts":"Jod"}]')
        self.bin = self.case / "bin"
        self.bin.mkdir()
        for tool in ("gh", "git", "curl", "sleep"):
            path = self.bin / tool
            path.write_text(SHIM)
            path.chmod(0o755)
        self.script = self.case / "workflow.sh"
        block = WORKFLOW.read_text().split("      - name: Check for dependency updates\n", 1)[1]
        block = block.split("        run: |\n", 1)[1]
        self.script.write_text(textwrap.dedent(block))

    def write(self, name, content=""):
        (self.case / name).write_text(content)

    def git(self, *args):
        return subprocess.check_output([self.real_git, *args], cwd=self.repo,
                                       env=self.git_env, stderr=subprocess.DEVNULL, text=True).strip()

    def run_workflow(self, expected=0):
        # Scrub all inherited credentials; the fake tools reject a sentinel leak.
        with patch.dict(os.environ, {"HOST_SECRET": "must-not-reach-tools"}):
            result = subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", str(self.script)],
                cwd=self.repo, env={**self.git_env, "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                    "HOME": str(self.case), "CASE_ROOT": str(self.case), "REAL_GIT": self.real_git},
                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        self.assertNotIn("must-not-reach-tools", result.stdout + result.stderr)
        return result

    def calls(self, *prefix):
        return [call for call in map(json.loads, (self.case / "calls").read_text().splitlines())
                if call[:len(prefix)] == list(prefix)]

    def seed_branch(self, branch="go-bump-v1.0.1"):
        self.git("checkout", "--quiet", "-b", branch)
        path = self.repo / "sandbox/Dockerfile"
        path.write_text(path.read_text().replace("GO_VERSION=1.0.0", "GO_VERSION=1.0.1"))
        self.git("add", ".")
        self.git("commit", "--quiet", "-m", "existing bump")
        self.git("push", "--quiet", "origin", branch)
        self.git("checkout", "--quiet", "main")
        return self.git("rev-parse", branch)

    def seed_pr(self, state="OPEN", branch="go-bump-v1.0.1"):
        self.write(branch + ".pr", json.dumps({"state": state, "url": "https://example.test/pull/" + branch,
                                              "isCrossRepository": False}))

    def seed_fork_pr(self):
        self.write("fork-pr", json.dumps({"state": "OPEN", "url": "https://example.test/pull/untrusted",
                                          "isCrossRepository": True}))

    def test_new_branch_stamps_commits_opens_merges_and_dispatches(self):
        self.run_workflow()
        for path in ("sandbox/Dockerfile", "sandbox/fragments/go.dockerfile"):
            self.assertIn("ARG GO_VERSION=1.0.1", self.git("show", "origin/go-bump-v1.0.1:" + path))
        self.assertEqual(len(self.calls("gh", "pr", "create")), 1)
        self.assertEqual(len(self.calls("gh", "pr", "merge")), 1)
        self.assertEqual(len(self.calls("gh", "workflow", "run")), 1)

    def test_creation_denied_then_rerun_recovers_go_and_uv_without_rewriting(self):
        self.write("uv", "v0.1.1")
        self.write("create-error")
        self.run_workflow(1)
        branches = ["go-bump-v1.0.1", "uv-bump-v0.1.1"]
        before = [self.git("rev-parse", "origin/" + branch) for branch in branches]
        (self.case / "create-error").unlink()
        self.run_workflow()
        self.assertEqual(before, [self.git("rev-parse", "origin/" + branch) for branch in branches])
        self.assertEqual(len(self.calls("gh", "workflow", "run")), 2)
        self.assertEqual(len(self.calls("gh", "pr", "create")), 4)

    def test_existing_open_pr_resumes_merge_without_duplicate(self):
        before = self.seed_branch()
        self.seed_pr()
        self.run_workflow()
        self.assertEqual(before, self.git("rev-parse", "origin/go-bump-v1.0.1"))
        self.assertEqual(self.calls("gh", "pr", "create"), [])
        self.assertEqual(len(self.calls("gh", "workflow", "run")), 1)

    def test_same_named_fork_pr_does_not_prevent_creating_trusted_pr(self):
        self.seed_branch()
        self.seed_fork_pr()
        self.run_workflow()
        self.assertEqual(len(self.calls("gh", "pr", "create")), 1)
        self.assertEqual([call[3] for call in self.calls("gh", "pr", "merge")],
                         ["https://example.test/pull/go-bump-v1.0.1"])

    def test_same_named_fork_pr_does_not_shadow_trusted_existing_pr(self):
        self.seed_branch()
        self.seed_fork_pr()
        self.seed_pr()
        self.run_workflow()
        self.assertEqual(self.calls("gh", "pr", "create"), [])
        self.assertEqual([call[3] for call in self.calls("gh", "pr", "merge")],
                         ["https://example.test/pull/go-bump-v1.0.1"])

    def test_closed_pr_is_visible_failure_and_not_reopened(self):
        self.seed_branch()
        self.seed_pr("CLOSED")
        result = self.run_workflow(1)
        self.assertIn("closed", result.stderr.lower())
        self.assertEqual(self.calls("gh", "pr", "create"), [])
        self.assertEqual(self.calls("gh", "pr", "merge"), [])

    def test_merged_pr_is_not_reopened_or_merged_again(self):
        self.seed_branch()
        self.seed_pr("MERGED")
        self.run_workflow()
        self.assertEqual(self.calls("gh", "pr", "create"), [])
        self.assertEqual(self.calls("gh", "pr", "merge"), [])
        self.assertEqual(len(self.calls("gh", "workflow", "run")), 1)

    def test_up_to_date_dependencies_do_not_open_prs(self):
        self.write("go", "go1.0.0")
        self.run_workflow()
        self.assertEqual(self.calls("gh", "pr", "create"), [])

    def test_query_failure_does_not_create_or_push_branch(self):
        self.write("query-error")
        self.run_workflow(1)
        self.assertEqual(self.calls("gh", "pr", "create"), [])
        self.assertEqual(self.calls("git", "push"), [])

    def test_remote_lookup_failure_is_not_treated_as_missing_branch(self):
        self.write("remote-error")
        self.run_workflow(1)
        self.assertEqual(self.calls("gh", "pr", "create"), [])
        self.assertEqual(self.calls("git", "push"), [])

    def test_merge_failure_does_not_dispatch(self):
        self.seed_branch()
        self.seed_pr()
        self.write("merge-error")
        self.run_workflow(1)
        self.assertEqual(self.calls("gh", "workflow", "run"), [])

    def test_dispatch_failure_is_visible(self):
        self.write("dispatch-error")
        self.run_workflow(1)
        self.assertEqual(len(self.calls("gh", "workflow", "run")), 1)

    def test_poll_lookup_failure_does_not_dispatch(self):
        self.write("view-error")
        result = self.run_workflow(1)
        self.assertIn("could not read PR state", result.stderr)
        self.assertEqual(self.calls("gh", "workflow", "run"), [])

    def test_poll_timeout_and_closed_state_do_not_dispatch(self):
        for state in ("OPEN", "CLOSED"):
            with self.subTest(state=state):
                self.write("poll-state", state)
                self.seed_pr()
                self.run_workflow(1)
                self.assertEqual(self.calls("gh", "workflow", "run"), [])

    def test_dotnet_and_node_manual_prs_recover_and_remain_manual(self):
        self.write("go", "go1.0.0")
        self.write("dotnet", json.dumps({"releases-index": [
            {"channel-version": "8.0", "latest-sdk": "8.0.100", "support-phase": "active"},
            {"channel-version": "9.0", "latest-sdk": "9.0.100", "support-phase": "active"}]}))
        self.write("node-schedule", '{"v20":{"end":"2000-01-01"}}')
        self.write("create-error")
        self.run_workflow(1)
        branches = ("dotnet-bump-v9.0.100", "node-bump-v22")
        before = [self.git("rev-parse", "origin/" + branch) for branch in branches]
        (self.case / "create-error").unlink()
        self.run_workflow()
        self.run_workflow()  # Existing open manual PRs are reused on the next run.
        self.assertEqual(before, [self.git("rev-parse", "origin/" + branch) for branch in branches])
        for branch in branches:
            self.assertEqual(json.loads((self.case / (branch + ".pr")).read_text())["state"], "OPEN")
        for path in ("sandbox/Dockerfile", "sandbox/fragments/dotnet.dockerfile"):
            self.assertIn("ARG DOTNET_SDK_VERSION=9.0.100", self.git("show", "origin/dotnet-bump-v9.0.100:" + path))
        for path in ("sandbox/Dockerfile", "sandbox/fragments/node.dockerfile"):
            self.assertIn("ARG NODE_MAJOR=22", self.git("show", "origin/node-bump-v22:" + path))
        self.assertEqual(len(self.calls("gh", "pr", "create")), 4)
        self.assertEqual(self.calls("gh", "pr", "merge"), [])
        self.assertEqual(self.calls("gh", "pr", "view"), [])
        self.assertEqual(self.calls("gh", "workflow", "run"), [])

    def test_dotnet_in_band_existing_branch_recovers_auto_merge(self):
        self.write("go", "go1.0.0")
        self.write("dotnet", json.dumps({"releases-index": [
            {"channel-version": "8.0", "latest-sdk": "8.0.101", "support-phase": "active"}]}))
        self.write("create-error")
        self.run_workflow(1)
        (self.case / "create-error").unlink()
        self.run_workflow()
        self.assertEqual(len(self.calls("gh", "workflow", "run")), 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
