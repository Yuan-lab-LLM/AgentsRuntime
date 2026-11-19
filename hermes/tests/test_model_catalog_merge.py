"""Offline tests for the exact helper embedded in the Pro startup script."""
import ast
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
import yaml

script = Path(__file__).resolve().parents[1] / "rootfs/usr/local/bin/hermes-apply-runtime-config"
source = script.read_text(encoding="utf-8").split("<<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]
tree = ast.parse(source)
helper = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "reconcile_managed_providers")
namespace = {"json": json}
exec(compile(ast.Module(body=[helper], type_ignores=[]), str(script), "exec"), namespace)
merge = namespace["reconcile_managed_providers"]

def provider(endpoint="http://gateway/v1", models=None, key="OPENAI_API_KEY"):
    return {"base_url": endpoint, "key_env": key, "models": models or {"auto": {}}}

class ModelCatalogMergeTest(unittest.TestCase):
    def test_refresh_and_idempotence(self):
        previous = {"clawmanager": provider(models={"auto": {"supports_tools": True}, "removed": {}}),
                    "retired": provider(), "user": provider("https://user.example", key="USER_KEY")}
        previous["clawmanager"].update({"api_key": "stale-key", "api": "https://stale.invalid", "key_cmd": "old-command", "enabled": False})
        original = json.dumps(previous)
        desired = {"clawmanager": provider(models={"auto": {}, "new": {}})}
        actual = merge(previous, desired, "http://gateway/v1/", "instance-key")
        self.assertNotIn("retired", actual)
        self.assertEqual(actual["user"], previous["user"])
        self.assertEqual(set(actual["clawmanager"]["models"]), {"auto", "new"})
        self.assertTrue(actual["clawmanager"]["models"]["auto"]["supports_tools"])
        for key in ("api_key", "api", "url", "key_cmd"):
            self.assertEqual(actual["clawmanager"][key], "")
        self.assertTrue(actual["clawmanager"]["enabled"])
        self.assertEqual(json.dumps(previous), original)
        self.assertEqual(merge(actual, desired, "http://gateway/v1", "instance-key"), actual)

    def test_foreign_collision_rejected(self):
        for previous in ([], {"clawmanager": provider("https://user.example")},
                         {"clawmanager": provider(key="USER_KEY")}):
            with self.assertRaises(ValueError):
                merge(previous, {"clawmanager": provider()}, "http://gateway/v1", "instance-key")

    def test_same_url_user_provider_preserved(self):
        previous = {"user": provider(key="USER_KEY")}
        actual = merge(previous, {"clawmanager": provider()}, "http://gateway/v1", "instance-key")
        self.assertEqual(actual["user"], previous["user"])

    def test_partial_legacy_connection_update_is_not_an_empty_catalog(self):
        previous = {"old-managed": provider()}
        actual = merge(previous, {"clawmanager": provider()}, "http://gateway/v1", "instance-key", authoritative=False)
        self.assertIn("old-managed", actual)

    def test_both_startup_branches_use_reconciliation(self):
        self.assertEqual(source.count('data["providers"] = reconcile_managed_providers('), 2)
        self.assertNotIn('data["providers"] = providers_cfg', source)

    def test_complete_startup_preserves_sessions_and_user_providers(self):
        for qualified in (True, False):
            with self.subTest(qualified=qualified), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                config = root / "config.yaml"
                providers = {"user": provider("https://user.example", key="USER_KEY"),
                             "retired": provider(), "clawmanager": provider(models={"removed": {}, "auto": {"supports_tools": True}})}
                config.write_text(yaml.safe_dump({"providers": providers, "user_setting": "keep"}), encoding="utf-8")
                session = root / "state.db"
                session.write_bytes(b"opaque session database - do not rewrite")
                canonical = {"base_url": "http://gateway/v1", "api_key_source": "CLAWMANAGER_LLM_API_KEY",
                             "models_qualified": qualified, "model_ids": ["clawmanager/auto"] if qualified else ["auto"],
                             "providers": [{"provider_id": "clawmanager", "model_ids": ["auto"]}]}
                env = {key: value for key, value in os.environ.items() if not key.startswith(("CLAWMANAGER_", "HERMES_", "OPENAI_"))}
                env.update({"HERMES_HOME": str(root), "HOME": str(root),
                            "CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON": json.dumps(canonical),
                            "CLAWMANAGER_LLM_BASE_URL": "http://gateway/v1", "CLAWMANAGER_LLM_API_KEY": "dummy-instance-key"})
                for _ in range(2):
                    completed = subprocess.run([sys.executable, "-"], input=source, env=env, capture_output=True, text=True, timeout=30)
                    self.assertEqual(completed.returncode, 0, completed.stderr)
                    result = yaml.safe_load(config.read_text(encoding="utf-8"))
                    self.assertEqual(result["providers"]["user"], providers["user"])
                    self.assertNotIn("retired", result["providers"])
                    self.assertNotIn("removed", result["providers"]["clawmanager"]["models"])
                    self.assertEqual(result["user_setting"], "keep")
                    self.assertEqual(session.read_bytes(), b"opaque session database - do not rewrite")
                for invalid in ("[]", "providers: []"):
                    config.write_text(invalid, encoding="utf-8")
                    failed = subprocess.run([sys.executable, "-"], input=source, env=env, capture_output=True, text=True, timeout=30)
                    self.assertNotEqual(failed.returncode, 0)
                    self.assertEqual(config.read_text(encoding="utf-8"), invalid)

if __name__ == "__main__":
    unittest.main()
