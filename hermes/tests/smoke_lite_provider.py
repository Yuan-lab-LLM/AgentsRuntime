#!/usr/bin/env python3
"""Offline checks against the real locked Hermes resolver and on-disk auth pool."""
import os
from pathlib import Path
import secrets
import tempfile


def main():
    with tempfile.TemporaryDirectory(prefix="hermes-provider-smoke-") as temporary:
        state = Path(temporary)
        os.environ["HOME"] = str(state)
        os.environ["HERMES_HOME"] = str(state)
        os.environ["CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED"] = "true"
        current_key = secrets.token_urlsafe(32)
        old_key = secrets.token_urlsafe(32)
        os.environ["OPENAI_API_KEY"] = current_key
        managed = ("clawmanager", "clawmanager-auto", "clawmanager-deepseek")
        names = (*managed, "legacy-user")
        endpoint = "http://clawmanager-backend.smoke.svc:3000/v1"
        import hermes_yaml as yaml
        config = {
            "model": {"provider": "clawmanager", "default": "auto"},
            "providers": {name: {"name": name, "base_url": endpoint,
                                  "key_env": "OPENAI_API_KEY", "enabled": True,
                                  "api_mode": "chat_completions"} for name in names},
        }
        (state / "config.yaml").write_text(yaml.safe_dump(config), encoding="utf-8")
        from hermes_cli.runtime_provider import resolve_runtime_provider
        from hermes_cli.auth import write_credential_pool

        for name in managed:
            runtime = resolve_runtime_provider(requested=name, target_model="auto")
            assert runtime["api_key"] == current_key and runtime["base_url"] == endpoint, "clean managed provider did not resolve instance credentials"
            assert not runtime.get("credential_pool"), "managed provider unexpectedly uses a credential pool"
        for name in names:
            write_credential_pool("custom:" + name, [{
                "id": "legacy-fixture", "label": "saved", "auth_type": "api_key",
                "priority": -100, "source": "manual", "access_token": old_key,
                "base_url": endpoint,
            }])
        auth_path = state / "auth.json"
        previous = auth_path.read_bytes()
        for name in managed:
            runtime = resolve_runtime_provider(requested=name, target_model="auto")
            assert runtime["api_key"] == current_key and runtime["base_url"] == endpoint, "saved pool shadowed managed instance credentials"
            assert not runtime.get("credential_pool"), "managed provider loaded old credential pool"
            assert auth_path.read_bytes() == previous, "managed provider changed saved authentication state"
        legacy = resolve_runtime_provider(requested="legacy-user", target_model="auto")
        assert legacy["api_key"] == old_key and legacy.get("credential_pool"), "unmanaged provider pool behavior changed"
        os.environ["CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED"] = "false"
        legacy_mode = resolve_runtime_provider(requested="clawmanager", target_model="auto")
        assert legacy_mode["api_key"] == old_key and legacy_mode.get("credential_pool"), "flag-off provider pool behavior changed"
        # CM logical providers share one governed endpoint and credential.
        # Hermes must retain distinct catalogs rather than folding all models
        # into the first/default provider section.
        os.environ["CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED"] = "true"
        catalogs = {"clawmanager-auto": ("ClawManager", ["auto"]),
                    "clawmanager-icompify": ("icompify", [f"model-{n}" for n in range(10)]),
                    "clawmanager-second": ("second", ["other-model"])}
        providers = {slug: {"name": name, "base_url": endpoint,
                           "key_env": "OPENAI_API_KEY", "enabled": True,
                           "api_mode": "chat_completions", "discover_models": False,
                           "extra_headers": {"X-CM-Catalog-Provider": slug},
                           "default_model": ids[0], "models": {id: {} for id in ids}}
                     for slug, (name, ids) in catalogs.items()}
        from hermes_cli import model_switch
        from hermes_cli.models import CANONICAL_PROVIDERS
        rows = model_switch.list_authenticated_providers(
            current_provider="clawmanager-auto", current_base_url=endpoint,
            current_model="auto", user_providers=providers, custom_providers=[],
            probe_custom_providers=False, probe_current_custom_provider=False,
            excluded_providers=[p.slug for p in CANONICAL_PROVIDERS])
        grouped = {row["slug"]: row for row in rows if row["slug"] in catalogs}
        assert set(grouped) == set(catalogs), "CM providers collapsed at the shared gateway"
        for slug, (name, ids) in catalogs.items():
            assert grouped[slug]["name"].casefold() == name.casefold(), "provider display name lost"
            assert set(grouped[slug]["models"]) == set(ids), "provider catalog mixed with siblings"
        print("PASS: actual pinned Hermes inventory preserves ClawManager/auto and separate 10-model provider at one shared gateway")
        print("PASS: actual offline provider resolver uses instance credentials with clean/saved pools; auth state preserved; unmanaged/flag-off behavior retained")


if __name__ == "__main__":
    main()
