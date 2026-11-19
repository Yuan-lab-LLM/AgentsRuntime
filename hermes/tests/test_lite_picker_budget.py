import unittest
from unittest.mock import patch

from hermes_cli import inventory, model_switch_providers


class PickerBudgetTests(unittest.TestCase):
    def test_normal_open_is_bounded_but_explicit_refresh_keeps_full_discovery(self):
        for refresh in (False, True):
            with self.subTest(refresh=refresh), patch.object(inventory, "build_models_payload", return_value={"providers": []}) as build, patch.object(inventory, "_apply_limits"), patch.object(inventory, "_apply_usage"), patch.object(inventory, "_prewarm_pricing_async"):
                ctx = type("Context", (), {"current_provider": "custom:fixture", "current_base_url": "https://fixture.invalid"})()
                inventory.build_model_options_payload(ctx, explicit_only=True, refresh=refresh)
                kwargs = build.call_args.kwargs
                self.assertEqual(not refresh, kwargs["fast_custom_probe"])
                self.assertEqual(refresh, kwargs["probe_custom_providers"])
                self.assertEqual(not refresh, kwargs["non_blocking_catalogs"])
                self.assertTrue(kwargs["explicit_only"])

    def test_discovery_budget_and_declared_model_fallback(self):
        for fast, expected in ((True, 1.5), (False, 5.0)):
            with patch.object(model_switch_providers, "_fetch_picker_live_models", side_effect=TimeoutError) as fetch:
                result = model_switch_providers._discover_endpoint_models(
                    "fixture", "https://fixture.invalid", "", True,
                    headers=None, api_mode=None, probe_live=True, discovery_allowed=True, fast_custom_probe=fast)
                self.assertEqual((None, False), result)
                self.assertEqual(expected, fetch.call_args.kwargs["timeout"])


if __name__ == "__main__":
    unittest.main()
