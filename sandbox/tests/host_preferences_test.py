import json
import importlib.util
from unittest import mock
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib
import unittest


HELPER = Path(__file__).resolve().parents[1] / "lib" / "host-preferences.py"


class PreferenceTests(unittest.TestCase):
    def test_concurrent_native_edit_survives_journal_recovery(self):
        from contextlib import chdir
        spec = importlib.util.spec_from_file_location("preferences", HELPER)
        helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(helper)
        for agent in ("claude", "codex"):
            with self.subTest(agent=agent), tempfile.TemporaryDirectory() as tmp, chdir(tmp):
                config = Path(helper.FILES[agent])
                edit = '{"model":"local"}' if agent == "claude" else 'model="local"'
                original = helper.atomic_write

                def intervening_edit(name, data, **kwargs):
                    if name == config.name:
                        config.write_text(edit)
                    original(name, data, **kwargs)

                with mock.patch.object(helper, "atomic_write", side_effect=intervening_edit):
                    with self.assertRaises(ValueError):
                        helper.reconcile(agent, {"model": "host"}, False)
                self.assertIn("model", helper.reconcile(agent, {"model": "updated"}, False)["overridden"])
                self.assertEqual(config.read_text(), edit)

    def test_interrupted_activation_retries_for_each_client(self):
        spec = importlib.util.spec_from_file_location("preferences", HELPER)
        helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(helper)
        for agent in ("claude", "codex"):
            for fail_at in (2, 3):
                with self.subTest(agent=agent, fail_at=fail_at), tempfile.TemporaryDirectory() as tmp:
                    from contextlib import chdir
                    with chdir(tmp):
                        write = helper.atomic_write
                        steps = []

                        def interrupted(name, data, **kwargs):
                            steps.append(name)
                            if len(steps) == fail_at:
                                raise OSError("interrupted")
                            write(name, data, **kwargs)

                        with mock.patch.object(helper, "atomic_write", side_effect=interrupted):
                            with self.assertRaises(OSError):
                                helper.reconcile(agent, {"model": "first"}, False)
                        helper.reconcile(agent, {"model": "second"}, False)
                        decode = json.loads if agent == "claude" else tomllib.loads
                        config = Path(helper.FILES[agent])
                        self.assertEqual(decode(config.read_text()), {"model": "second"})
                        helper.reconcile(agent, {}, False)
                        self.assertEqual(decode(config.read_text()), {})

    def test_native_configuration_lifecycle(self):
        for agent in ("claude", "codex"):
            with self.subTest(agent=agent), tempfile.TemporaryDirectory() as root:
                base = Path(root)
                directory = base / ("." + agent)
                directory.mkdir()
                if agent == "claude":
                    config = directory / "settings.json"
                    config.write_text('{"permissions":{"defaultMode":"bypassPermissions"},"hooks":{"Stop":[]},"effortLevel":"medium"}')
                    decode = json.loads
                    secondary = "effortLevel"
                else:
                    config = directory / "config.toml"
                    config.write_text('model_reasoning_effort = "medium"\ncheck_for_update_on_startup = false\n[tui]\nstatus_line=["model-name"]\n')
                    decode = tomllib.loads
                    secondary = "model_reasoning_effort"

                def run(values, mode="apply", ok=True):
                    result = subprocess.run(
                        [sys.executable, str(HELPER), agent, str(base), mode],
                        input=json.dumps(values), capture_output=True, text=True, check=False,
                    )
                    if ok:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        return json.loads(result.stdout)
                    self.assertNotEqual(result.returncode, 0)

                initial = decode(config.read_text())
                values = {"model": "model-one", secondary: "high"}
                before = config.read_bytes()
                preview = run(values, "audit")
                self.assertEqual(before, config.read_bytes())
                self.assertFalse((directory / ".cenci-preferences.json").exists())
                self.assertIn(secondary, preview["overridden"])
                run(values)
                self.assertEqual(decode(config.read_text()), dict(initial, model="model-one"))
                metadata = config.stat()
                run(values)
                self.assertEqual(metadata.st_ino, config.stat().st_ino)
                run({"model": "model-two"})
                self.assertEqual(decode(config.read_text())["model"], "model-two")
                run({})
                self.assertEqual(decode(config.read_text()), initial)
                run(values)
                config.write_text(config.read_text().replace("model-one", "local-model"))
                self.assertIn("model", run({"model": "model-three"})["overridden"])
                run({})
                self.assertEqual(decode(config.read_text()), dict(initial, model="local-model"))
                config.write_bytes(b"[truncated")
                run(values, ok=False)
                self.assertEqual(config.read_bytes(), b"[truncated")


if __name__ == "__main__":
    unittest.main()
