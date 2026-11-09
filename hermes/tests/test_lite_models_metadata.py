import unittest
from unittest.mock import patch

from agent import models_dev as metadata


class ModelMetadataTests(unittest.TestCase):
    def setUp(self):
        for name, value in (("_models_dev_cache", {}), ("_models_dev_cache_time", 0), ("_models_dev_retry_after", 0)):
            p = patch.object(metadata, name, value)
            p.start(); self.addCleanup(p.stop)
        for name, value in (("_load_disk_cache", {}), ("_disk_cache_age_seconds", None)):
            p = patch.object(metadata, name, return_value=value)
            p.start(); self.addCleanup(p.stop)

    def test_empty_cache_starts_background_and_never_fetches_inline(self):
        with patch.object(metadata, "_refresh_locked", side_effect=AssertionError("inline fetch")), patch.object(metadata, "_start_background_refresh_models_dev") as start:
            self.assertEqual({}, metadata.fetch_models_dev())
            start.assert_called_once()

    def test_warm_disk_cache_is_available_without_network(self):
        data = {"fixture": {"models": {"test-model": {}}}}
        with patch.object(metadata, "_load_disk_cache", return_value=data), patch.object(metadata, "_disk_cache_age_seconds", return_value=1), patch.object(metadata, "_refresh_locked", side_effect=AssertionError("inline fetch")):
            self.assertEqual(data, metadata.fetch_models_dev())

    def test_explicit_refresh_keeps_foreground_contract(self):
        data = {"fixture": {"models": {}}}
        with patch.object(metadata, "_refresh_locked", return_value=data) as refresh, patch.object(metadata, "_start_background_refresh_models_dev", side_effect=AssertionError("unexpected background")):
            self.assertEqual(data, metadata.fetch_models_dev(force_refresh=True))
            refresh.assert_called_once_with("foreground")

    def test_no_network_and_failure_backoff_are_preserved(self):
        with patch.object(metadata, "_refresh_locked", side_effect=AssertionError("network")), patch.object(metadata, "_start_background_refresh_models_dev", side_effect=AssertionError("background")):
            self.assertEqual({}, metadata.fetch_models_dev(allow_network=False))
            with patch.object(metadata, "_models_dev_retry_after", float("inf")):
                self.assertEqual({}, metadata.fetch_models_dev())


if __name__ == "__main__":
    unittest.main()
