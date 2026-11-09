"""Shared, fail-closed schema checks for signed Hermes acceptance evidence."""
import base64
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess

SHARE = "/usr/local/share/hermes-lite"
PROTOCOL = "hermes-lite-campaign-v1"
DIGEST_FIELDS = ("candidate_image_digest", "candidate_manifest_digest", "candidate_config_digest", "cm_image_digest")
HASH_FIELDS = ("payload_sha256", "cm_provenance_sha256", "renderer_build_input_sha256", "renderer_asset_manifest_sha256",
               "harness_sha256", "campaign_runner_sha256", "execution_config_sha256", "node_sha256", "browser_sha256", "playwright_sha256")
VERSION_FIELDS = ("node_version", "browser_version", "playwright_version")
OBSERVATION_CHECKS = ("runtime_identity", "cm_identity", "renderer_identity", "isolation")
BROWSER_CHECKS = ("candidate_admission_identity", "cm_build_renderer_resources", "browser_login_ownership", "desktop_boot_original_dom",
                  "cm_cookie_scope", "http_contract", "browser_origin_rejection", "ws_single_use_identity", "ui_prompt_stream_history",
                  "rpc_session_lifecycle_reconnect", "interrupt", "approval_once", "approval_deny", "clarify", "three_replica_tickets",
                  "lease_renewal_logout", "secret_surface_audit", "stub_only_observation")


def timestamp(value):
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)", value):
        raise ValueError("invalid campaign timestamp")
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def validate_binding(binding, release, api):
    required = {"schema_version", "protocol", "scope", "run_id", "started_at", *DIGEST_FIELDS, *HASH_FIELDS, *VERSION_FIELDS}
    api.require(isinstance(binding, dict) and set(binding) == required and type(binding["schema_version"]) is int
                and binding["schema_version"] == 1 and binding["protocol"] == PROTOCOL
                and binding["scope"] == "real-cm-bff-browser", "invalid campaign binding schema")
    api.require(isinstance(binding["run_id"], str) and re.fullmatch(r"[0-9a-f]{32}", binding["run_id"]), "invalid campaign run ID")
    for key in DIGEST_FIELDS:
        api.require(isinstance(binding[key], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", binding[key]), "invalid campaign digest: " + key)
    for key in HASH_FIELDS:
        api.require(isinstance(binding[key], str) and api.HEX256.fullmatch(binding[key]), "invalid campaign hash: " + key)
    for key in VERSION_FIELDS:
        api.require(isinstance(binding[key], str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]{0,79}", binding[key]), "invalid campaign version")
    api.require(timestamp(binding["started_at"]) <= datetime.now(timezone.utc), "campaign starts in the future")
    api.require(binding["payload_sha256"] == release["payload_sha256"], "campaign payload mismatch")
    api.require(binding["harness_sha256"] == release["artifacts"].get(SHARE + "/tests/cm_bff_browser.mjs")
                and binding["campaign_runner_sha256"] == release["artifacts"].get(SHARE + "/run_campaign.py"), "campaign harness mismatch")


def descriptor(root, ref, absolute, api, ownership=True, limit=65536):
    api.require(isinstance(ref, dict) and set(ref) == {"path", "sha256"} and ref["path"] == absolute
                and isinstance(ref["sha256"], str) and api.HEX256.fullmatch(ref["sha256"]), "invalid campaign evidence descriptor")
    path = api.managed_path(root, absolute, ownership, max_bytes=limit)
    api.require(api.sha256_file(path) == ref["sha256"], "campaign evidence checksum mismatch")
    return path


def validate_observation(value, phase, binding, binding_hash, api):
    expected = {"schema_version": 1, "phase": phase, "status": "passed", "run_id": binding["run_id"],
                "binding_sha256": binding_hash, "runtime_image_digest": binding["candidate_manifest_digest"],
                "runtime_payload_sha256": binding["payload_sha256"], "cm_image_digest": binding["cm_image_digest"],
                "renderer_build_input_sha256": binding["renderer_build_input_sha256"],
                "renderer_asset_manifest_sha256": binding["renderer_asset_manifest_sha256"],
                "checks": {key: "passed" for key in OBSERVATION_CHECKS}}
    api.require(isinstance(value, dict) and set(value) == {*expected, "recorded_at"}
                and all(type(value.get(key)) is type(item) and value[key] == item for key, item in expected.items()), "campaign observation mismatch")
    when = timestamp(value["recorded_at"])
    api.require(timestamp(binding["started_at"]) <= when <= datetime.now(timezone.utc), "invalid observation time")
    return when


def decode_signature(envelope, trust, api):
    api.require(isinstance(trust, dict) and set(trust) == {"schema_version", "algorithm", "key_id", "key_origin", "public_key_base64"}
                and type(trust["schema_version"]) is int and trust["schema_version"] == 1 and trust["algorithm"] == "Ed25519"
                and trust["key_origin"] == "local-operator" and re.fullmatch(r"[a-z0-9-]{1,64}", trust["key_id"]), "invalid release trust root")
    api.require(isinstance(envelope, dict) and set(envelope) == {"schema_version", "algorithm", "key_id", "payload_base64", "signature_base64"}
                and type(envelope["schema_version"]) is int and envelope["schema_version"] == 1
                and envelope["algorithm"] == "Ed25519" and envelope["key_id"] == trust["key_id"], "untrusted campaign signer")
    values = []
    for obj, key, size in ((trust, "public_key_base64", 32), (envelope, "signature_base64", 64), (envelope, "payload_base64", None)):
        api.require(isinstance(obj[key], str), "invalid campaign signature encoding")
        value = base64.b64decode(obj[key], validate=True)
        api.require(base64.b64encode(value).decode() == obj[key] and (len(value) == size if size else len(value) <= 65536), "invalid campaign signature size")
        values.append(value)
    def unique(pairs):
        result = {}
        for key, value in pairs:
            api.require(key not in result, "duplicate signature payload key")
            result[key] = value
        return result
    return json.loads(values[-1].decode("utf-8"), object_pairs_hook=unique)


def verify_signature(trust_path, signature_path, root, api, ownership):
    verifier = api.managed_path(root, SHARE + "/verify_campaign_signature.mjs", ownership)
    node = api.managed_path(root, "/usr/local/bin/node", ownership)
    result = subprocess.run([str(node), str(verifier), str(trust_path), str(signature_path)],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
                            env={key: value for key, value in os.environ.items() if key.upper() in {"PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP"}})
    api.require(result.returncode == 0 and result.stdout == b"Verified campaign signature\n", "campaign signature verification failed")


def campaign_statement(release, root, ownership, api):
    acceptance = release["acceptance"]
    required = {"candidate_image_digest", "payload_sha256", "binding", "reports", "observations"}
    api.require(isinstance(acceptance, dict) and required <= set(acceptance)
                and set(acceptance) <= required | {"signature", "promotion_reports"}
                and acceptance["payload_sha256"] == release["payload_sha256"], "campaign acceptance payload or schema mismatch")
    api.require(isinstance(acceptance["reports"], dict) and set(acceptance["reports"]) == set(api.SUITES), "campaign requires exactly six reports")
    binding_path = descriptor(root, acceptance.get("binding"), SHARE + "/evidence/acceptance-binding.json", api, ownership)
    binding_hash = api.sha256_file(binding_path)
    binding = api.load_json(binding_path)
    validate_binding(binding, release, api)
    api.require(binding["candidate_image_digest"] == acceptance["candidate_image_digest"], "campaign candidate mismatch")
    for filename, key in (("cm-provenance.json", "cm_provenance_sha256"), ("renderer-asset-manifest.json", "renderer_asset_manifest_sha256")):
        path = api.managed_path(root, SHARE + "/evidence/" + filename, ownership, max_bytes=1024 * 1024)
        api.require(api.sha256_file(path) == binding[key], "campaign identity evidence mismatch")
    protocol = api.load_json(api.managed_path(root, SHARE + "/acceptance-protocol.json", ownership))
    api.require(protocol == {"schema_version": 1, "protocol": PROTOCOL, "report_schema_version": 2,
                            "suites": list(api.SUITES), "promotion_suites": [name for name in api.SUITES if name != "cm_bff_browser"],
                            "observation_checks": list(OBSERVATION_CHECKS), "browser_checks": list(BROWSER_CHECKS)}, "unsupported acceptance protocol")
    observations = acceptance.get("observations")
    api.require(isinstance(observations, dict) and set(observations) == {"before", "after"}, "missing campaign observations")
    times = {}
    for phase in ("before", "after"):
        path = descriptor(root, observations[phase], SHARE + "/evidence/campaign-" + phase + ".json", api, ownership)
        times[phase] = validate_observation(api.load_json(path), phase, binding, binding_hash, api)
    api.require(times["before"] <= times["after"], "campaign observation order is invalid")
    reports = {}
    for suite in api.SUITES:
        report_path = descriptor(root, acceptance["reports"][suite], SHARE + "/evidence/" + suite + ".json", api, ownership)
        report = api.load_json(report_path)
        api.validate_execution_report(report, suite, release, root, ownership)
        api.require(report.get("binding_sha256") == binding_hash and report.get("phase") == "campaign", "report campaign binding mismatch")
        api.require(times["before"] <= timestamp(report["started_at"]) <= timestamp(report["finished_at"]) <= times["after"], "report outside campaign observations")
        reports[suite] = {"sha256": acceptance["reports"][suite]["sha256"], "output_sha256": report["output_sha256"]}
        if suite == "cm_bff_browser":
            path = descriptor(root, report.get("browser_result"), SHARE + "/evidence/cm-bff-browser-result.json", api, ownership, 1024 * 1024)
            result = api.load_json(path)
            api.require(isinstance(result, dict) and type(result.get("schema_version")) is int and result.get("schema_version") == 1 and result.get("status") == "passed"
                        and result.get("binding_sha256") == binding_hash
                        and result.get("run_id") == binding["run_id"]
                        and result.get("cases") == {name: "passed" for name in BROWSER_CHECKS}, "browser acceptance cases incomplete")
            files = result.get("artifacts")
            api.require(isinstance(files, dict) and 0 < len(files) <= 32, "missing browser artifacts")
            total = 0
            for name, ref in files.items():
                api.require(isinstance(name, str) and re.fullmatch(r"browser-artifacts/[A-Za-z0-9][A-Za-z0-9._-]{0,119}", name)
                            and isinstance(ref, dict) and set(ref) == {"sha256", "size"} and type(ref["size"]) is int
                            and 0 <= ref["size"] <= 8 * 1024 * 1024, "invalid browser artifact descriptor")
                artifact = api.managed_path(root, SHARE + "/evidence/" + name, ownership, max_bytes=8 * 1024 * 1024)
                api.require(artifact.stat().st_size == ref["size"] and api.sha256_file(artifact) == ref["sha256"], "browser artifact checksum mismatch")
                total += ref["size"]
            api.require(total <= 32 * 1024 * 1024, "browser artifact inventory too large")
    statement = {"binding_sha256": binding_hash, "reports": reports,
                 "observations": {phase + "_sha256": observations[phase]["sha256"] for phase in observations}}
    return binding, statement, times


def validate_campaign(release, root, ownership, api, require_promotion=True):
    acceptance = release["acceptance"]
    binding, statement, times = campaign_statement(release, root, ownership, api)
    binding_hash = statement["binding_sha256"]
    signature_path = descriptor(root, acceptance.get("signature"), SHARE + "/evidence/campaign-signature.json", api, ownership)
    trust_path = api.managed_path(root, SHARE + "/release-trust.json", ownership)
    payload = decode_signature(api.load_json(signature_path), api.load_json(trust_path), api)
    api.require(payload == statement, "signed campaign evidence mismatch")
    verify_signature(trust_path, signature_path, root, api, ownership)
    if require_promotion:
        validate_promotion_reports(release, root, ownership, api, binding_hash, times["after"])
    return binding


def validate_promotion_reports(release, root, ownership, api, binding_hash, after):
    promotion = release["acceptance"].get("promotion_reports")
    suites = set(api.SUITES) - {"cm_bff_browser"}
    api.require(isinstance(promotion, dict) and set(promotion) == suites, "missing mandatory promotion reports")
    for suite in sorted(suites):
        path = descriptor(root, promotion[suite], SHARE + "/evidence/promotion-" + suite + ".json", api, ownership)
        report = api.load_json(path)
        api.validate_execution_report(report, suite, release, root, ownership, phase="promotion")
        api.require(report.get("binding_sha256") == binding_hash and timestamp(report["started_at"]) >= after, "invalid promotion binding or time")
