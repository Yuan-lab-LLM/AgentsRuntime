"""Run against the patched image, without network access or real workspace data."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from hermes_cli import model_catalog as catalog


class ColdCatalogTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.path = self.root / "a.json"
        self.manifest = {"version": 1, "providers": {"nous": {"models": [{"id": "fixture-model"}]}}}
        self.config = {"enabled": True, "url": "https://catalog.invalid/", "ttl_hours": 1, "providers": {}}
        self.path_patch = patch.object(catalog, "_cache_path", side_effect=lambda: self.path)
        self.config_patch = patch.object(catalog, "_load_catalog_config", return_value=self.config)
        self.path_patch.start(); self.addCleanup(self.path_patch.stop)
        self.config_patch.start(); self.addCleanup(self.config_patch.stop)
        catalog.reset_cache(); self.addCleanup(catalog.reset_cache)

    def test_cold_picker_uses_builtin_fallback_without_inline_network(self):
        with patch.object(catalog, "_fetch_manifest_with_fallback", side_effect=AssertionError("inline network")), patch.object(catalog, "_spawn_catalog_swr_refresh") as refresh:
            self.assertEqual({}, catalog.get_catalog())
            self.assertIsNone(catalog.get_curated_nous_models())
            self.assertIsNone(catalog.get_curated_openrouter_models())
            self.assertTrue(refresh.called)
        self.assertFalse(self.path.exists())

    def test_background_result_is_visible_on_next_request(self):
        with patch.object(catalog, "_spawn_catalog_swr_refresh"):
            self.assertEqual({}, catalog.get_catalog())
        self.path.write_text(json.dumps(self.manifest))
        with patch.object(catalog, "_fetch_manifest_with_fallback", side_effect=AssertionError("inline network")):
            self.assertEqual(self.manifest, catalog.get_catalog())
            self.assertEqual(["fixture-model"], catalog.get_curated_nous_models())

    def test_cached_profile_does_not_leak_into_another_empty_profile(self):
        self.path.write_text(json.dumps(self.manifest))
        self.assertEqual(self.manifest, catalog.get_catalog())
        self.path = self.root / "b.json"
        with patch.object(catalog, "_spawn_catalog_swr_refresh"):
            self.assertEqual({}, catalog.get_catalog())
        self.path = self.root / "a.json"
        self.assertEqual(self.manifest, catalog.get_catalog())

    def test_explicit_refresh_and_disabled_config_keep_upstream_semantics(self):
        with patch.object(catalog, "_fetch_manifest_with_fallback", return_value=self.manifest) as fetch:
            self.assertEqual(self.manifest, catalog.get_catalog(force_refresh=True))
            fetch.assert_called_once_with(self.config["url"], catalog.DEFAULT_FETCH_TIMEOUT)
        self.config["enabled"] = False
        with patch.object(catalog, "_spawn_catalog_swr_refresh", side_effect=AssertionError("disabled refresh")), patch.object(catalog, "_fetch_manifest_with_fallback", side_effect=AssertionError("disabled network")):
            self.assertEqual({}, catalog.get_catalog(force_refresh=True))


if __name__ == "__main__":
    unittest.main()
