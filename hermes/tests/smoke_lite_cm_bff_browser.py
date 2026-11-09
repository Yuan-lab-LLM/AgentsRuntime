#!/usr/bin/env python3
"""Execute the immutable candidate's real CM browser harness; never infer passes.

Run on the acceptance host. Inputs are restricted files, the JavaScript driver
is the fixed sibling artifact, and every case must execute successfully. The
separate release runner binds these observations to its immutable OCI candidate.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
from urllib.parse import urlsplit

CASES = ("candidate_admission_identity", "cm_build_renderer_resources", "browser_login_ownership",
         "desktop_boot_original_dom", "cm_cookie_scope", "http_contract", "browser_origin_rejection",
         "ws_single_use_identity", "ui_prompt_stream_history", "rpc_session_lifecycle_reconnect",
         "interrupt", "approval_once", "approval_deny", "clarify", "three_replica_tickets",
         "lease_renewal_logout", "secret_surface_audit", "stub_only_observation")
HASH = re.compile(r"[0-9a-f]{64}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
CATEGORY = re.compile(r"[a-z][a-z0-9_]{0,79}\Z")


class GateError(Exception):
    pass


def require(ok, category):
    if not ok:
        raise GateError(category)


def unique_object(pairs):
    obj = {}
    for key, value in pairs:
        require(key not in obj, "duplicate_json_key")
        obj[key] = value
    return obj


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def restricted(path):
    path = Path(path)
    require(path.is_absolute() and not path.is_symlink(), "restricted_file_path")
    info = path.stat()
    require(stat.S_ISREG(info.st_mode), "restricted_file_type")
    if os.name != "nt":
        require(info.st_uid in {0, os.geteuid()} and not info.st_mode & 0o077, "restricted_file_permissions")
    else:
        # Windows mode bits do not describe a DACL. Paths are base64 data,
        # never shell syntax. Only current user, SYSTEM and Administrators may
        # have allow ACEs. This subprocess never reads file contents.
        data = base64.b64encode(str(path).encode()).decode()
        code = ("trap {exit 1};$ErrorActionPreference='Stop';"
                "$p=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + data + "'));"
                "$a=[IO.File]::GetAccessControl($p);$u=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value;"
                "$owner=$a.GetOwner([Security.Principal.SecurityIdentifier]).Value;"
                "$ok=$owner -in @($u,'S-1-5-18','S-1-5-32-544');foreach($r in $a.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])){if($r.AccessControlType -eq 'Allow'){"
                "$s=$r.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value;"
                "if($s -notin @($u,'S-1-5-18','S-1-5-32-544')){$ok=$false}}};"
                "if($ok){exit 0}else{exit 1}")
        exe = Path(os.environ.get("SystemRoot", "C:/Windows")) / "System32/WindowsPowerShell/v1.0/powershell.exe"
        p = subprocess.run([str(exe), "-NoProfile", "-NonInteractive", "-EncodedCommand",
                            base64.b64encode(code.encode("utf-16le")).decode()], timeout=10,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                           creationflags=subprocess.CREATE_NO_WINDOW)
        require(p.returncode == 0, "restricted_file_permissions")
    return path


def read_json(path, private=False):
    path = restricted(path) if private else Path(path)
    require(path.is_absolute() and not path.is_symlink(), "json_file_path")
    with path.open("rb") as stream:
        info = os.fstat(stream.fileno())
        require(os.path.samestat(info, path.stat()) and info.st_size <= 1048576, "json_file_changed_or_large")
        raw = stream.read(1048577)
    require(len(raw) <= 1048576, "json_file_too_large")
    obj = json.loads(raw, object_pairs_hook=unique_object)
    require(isinstance(obj, dict), "json_object_required")
    return obj, hashlib.sha256(raw).hexdigest()


def playwright_digest(package):
    package = Path(package)
    require(package.is_absolute() and package.name == "playwright", "playwright_path_invalid")
    rows, total = [], 0
    for name in ("playwright", "playwright-core"):
        directory = package.parent / name
        require(directory.is_dir() and not directory.is_symlink(), "playwright_package_missing")
        for current, dirs, files in os.walk(directory, followlinks=False):
            for item in dirs + files:
                require(not (Path(current) / item).is_symlink(), "playwright_link_forbidden")
            for filename in files:
                path = Path(current) / filename
                total += path.stat().st_size
                require(len(rows) < 30000 and total <= 512 * 1048576, "playwright_package_too_large")
                rows.append((path.relative_to(package.parent).as_posix(), digest(path)))
    return hashlib.sha256("".join(p + "\0" + h + "\n" for p, h in sorted(rows)).encode()).hexdigest()


def validate(config, binding, config_hash, here):
    keys = {"schema_version", "namespace", "cm_origin", "cm_identity", "candidate_image_digest",
            "candidate_payload_sha256", "instance_id", "other_instance_id", "credentials_file",
            "secret_canaries_file", "ca_file", "runner", "stub_observation_url", "stub_observer_credentials_file", "renderer_asset_manifest_file"}
    require(set(config) == keys and type(config.get("schema_version")) is int and config.get("schema_version") == 1, "config_schema_invalid")
    require(re.fullmatch(r"[a-z0-9][a-z0-9-]{1,62}", config["namespace"] or "")
            and config["namespace"] not in {"default", "clawmanager-system", "kube-system"}, "isolated_namespace_required")
    url = urlsplit(config["cm_origin"])
    require(url.scheme == "https" and url.hostname and not url.username and not url.password
            and not url.path and not url.query and not url.fragment, "cm_https_origin_required")
    stub = urlsplit(config["stub_observation_url"])
    require((stub.scheme == "https" or stub.scheme == "http" and stub.hostname in {"localhost", "127.0.0.1", "::1"})
            and not stub.username and not stub.password and not stub.query and not stub.fragment
            and stub.path == "/__acceptance__/observation", "stub_observation_url_invalid")
    require(all(type(config[k]) is int and 0 < config[k] <= 2147483647 for k in ("instance_id", "other_instance_id"))
            and config["instance_id"] != config["other_instance_id"], "instance_scope_invalid")
    identity = config["cm_identity"]
    require(isinstance(identity, dict) and set(identity) == {"image_digest", "version", "commit", "build_time", "renderer_build_input_sha256", "renderer_asset_manifest_sha256"}, "cm_identity_invalid")
    require(DIGEST.fullmatch(identity["image_digest"]) and DIGEST.fullmatch(config["candidate_image_digest"])
            and HASH.fullmatch(config["candidate_payload_sha256"])
            and all(HASH.fullmatch(identity[k]) for k in ("renderer_build_input_sha256", "renderer_asset_manifest_sha256")), "identity_digest_invalid")
    require(type(binding.get("schema_version")) is int and binding.get("schema_version") == 1 and binding.get("protocol") == "hermes-lite-campaign-v1"
            and binding.get("scope") == "real-cm-bff-browser" and re.fullmatch(r"[0-9a-f]{32}", binding.get("run_id", "")), "binding_schema_invalid")
    require(all(isinstance(binding.get(k), str) and DIGEST.fullmatch(binding[k]) for k in ("candidate_manifest_digest", "candidate_config_digest")), "candidate_oci_binding_missing")
    require(all(isinstance(binding.get(k), str) and re.fullmatch(r"v?\d+\.\d+\.\d+(?:\.\d+)?", binding[k])
                for k in ("node_version", "browser_version", "playwright_version")), "browser_dependency_version_missing")
    require(binding.get("execution_config_sha256") == config_hash, "execution_config_binding_mismatch")
    for key, expected in (("candidate_image_digest", config["candidate_image_digest"]), ("payload_sha256", config["candidate_payload_sha256"]),
                          ("cm_image_digest", identity["image_digest"]), ("renderer_build_input_sha256", identity["renderer_build_input_sha256"]),
                          ("renderer_asset_manifest_sha256", identity["renderer_asset_manifest_sha256"])):
        require(binding.get(key) == expected, "campaign_binding_mismatch")
    require(binding.get("harness_sha256") == digest(here / "cm_bff_browser.mjs"), "harness_binding_mismatch")
    runner = config["runner"]
    require(isinstance(runner, dict) and set(runner) == {"node_path", "playwright_package_path", "browser_path"}, "runner_schema_invalid")
    for name in ("node", "browser"):
        path = Path(runner[name + "_path"])
        require(path.is_absolute() and path.is_file() and not path.is_symlink(), "runner_binary_path_invalid")
        require(binding.get(name + "_sha256") == digest(path), "runner_binary_binding_mismatch")
    require(binding.get("playwright_sha256") == playwright_digest(runner["playwright_package_path"]), "playwright_binding_mismatch")
    package, _ = read_json(Path(runner["playwright_package_path"]) / "package.json")
    require(package.get("version") == binding.get("playwright_version"), "playwright_version_mismatch")
    credentials, _ = read_json(config["credentials_file"], True)
    require(set(credentials) == {"username", "password"} and all(isinstance(v, str) and 0 < len(v) <= 1024 for v in credentials.values()), "credentials_schema_invalid")
    canaries, _ = read_json(config["secret_canaries_file"], True)
    require(set(canaries) == {"values"} and isinstance(canaries["values"], list) and 2 <= len(canaries["values"]) <= 32
            and all(isinstance(v, str) and 16 <= len(v) <= 4096 for v in canaries["values"]), "secret_canaries_invalid")
    observer, _ = read_json(config["stub_observer_credentials_file"], True)
    require(set(observer) == {"token"} and isinstance(observer["token"], str) and 16 <= len(observer["token"]) <= 4096, "observer_credentials_invalid")
    manifest, manifest_hash = read_json(config["renderer_asset_manifest_file"], True)
    require(manifest_hash == binding["renderer_asset_manifest_sha256"] and 3 <= len(manifest) <= 1024, "renderer_manifest_binding_mismatch")
    for name, h in manifest.items():
        require(isinstance(name, str) and re.fullmatch(r"[A-Za-z0-9_./-]+", name) and not name.startswith("/")
                and all(p not in {"", ".", ".."} for p in name.split("/")) and isinstance(h, str) and HASH.fullmatch(h), "renderer_manifest_entry_invalid")
    ca = Path(config["ca_file"])
    require(ca.is_absolute() and ca.is_file() and not ca.is_symlink() and ca.stat().st_size <= 1048576, "ca_file_invalid")


def result(category="preflight_not_run"):
    return {"schema_version": 1, "suite": "cm_bff_browser", "status": "failed", "category": category,
            "run_id": "", "binding_sha256": "", "cases": {name: "not_run" for name in CASES}}


def validate_result(value):
    require(isinstance(value, dict) and set(value) <= {"schema_version", "suite", "status", "category", "cases", "counts", "identity", "artifacts", "run_id", "binding_sha256"}, "browser_result_schema_invalid")
    require(type(value.get("schema_version")) is int and value.get("schema_version") == 1 and value.get("suite") == "cm_bff_browser"
            and value.get("status") in {"passed", "failed"} and CATEGORY.fullmatch(value.get("category", "")), "browser_result_schema_invalid")
    cases = value.get("cases")
    require(isinstance(cases, dict) and set(cases) == set(CASES), "browser_result_cases_invalid")
    require(all(v in {"passed", "failed", "not_run"} for v in cases.values()), "browser_result_case_invalid")
    require(value["status"] != "passed" or all(c == "passed" for c in cases.values()), "browser_incomplete_acceptance")
    require(re.fullmatch(r"[0-9a-f]{32}", value.get("run_id", "")) and HASH.fullmatch(value.get("binding_sha256", "")), "browser_result_binding_invalid")
    for key, number in value.get("counts", {}).items():
        require(CATEGORY.fullmatch(key) and type(number) is int and 0 <= number <= 10000000, "browser_result_counts_invalid")
    require(not value.get("artifacts"), "browser_unverified_artifact")
    identity = value.get("identity", {})
    require(set(identity) <= {"candidate_image_digest", "payload_sha256", "cm_image_digest", "renderer_build_input_sha256", "renderer_asset_manifest_sha256", "harness_sha256", "run_id"}, "browser_result_identity_invalid")
    for key, h in identity.items():
        pattern = DIGEST if key.endswith("image_digest") else re.compile(r"[0-9a-f]{32}\Z") if key == "run_id" else HASH
        require(isinstance(h, str) and pattern.fullmatch(h), "browser_result_identity_invalid")
    return value


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", "--execution-config", dest="config", type=Path, required=True)
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args(argv)
    report = result()
    try:
        require(args.output is None or args.output.is_absolute() and not args.output.exists(), "output_exists_or_relative")
        config, config_hash = read_json(args.config, True)
        binding, binding_hash = read_json(args.binding, True)
        here = Path(__file__).resolve().parent
        validate(config, binding, config_hash, here)
        env = {k: os.environ[k] for k in ("SystemRoot", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE") if k in os.environ}
        env.update({"NODE_EXTRA_CA_CERTS": config["ca_file"], "NODE_OPTIONS": "", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD": "1"})
        p = subprocess.run([config["runner"]["node_path"], str(here / "cm_bff_browser.mjs"), "--config", str(args.config), "--binding", str(args.binding)],
                           env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=1500,
                           creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
        require(len(p.stdout) <= 262144 and len(p.stderr) <= 262144, "browser_output_limit")
        report = validate_result(json.loads(p.stdout, object_pairs_hook=unique_object))
        require(report["run_id"] == binding["run_id"] and report["binding_sha256"] == binding_hash, "browser_result_binding_mismatch")
        require((p.returncode == 0) == (report["status"] == "passed"), "browser_exit_status_mismatch")
    except GateError as error:
        report = result(str(error))
    except subprocess.TimeoutExpired:
        report = result("browser_timeout")
    except Exception:
        report = result("browser_preflight_or_execution_error")
    if args.output is not None and report.get("run_id"):
        try:
            directory = args.output.parent / "browser-artifacts"
            directory.mkdir(mode=0o700, exist_ok=True)
            require(not directory.is_symlink(), "artifact_directory_link")
            summary = {k: report[k] for k in ("schema_version", "run_id", "binding_sha256", "cases", "counts") if k in report}
            summary["evidence_kind"] = "browser_execution_summary_not_screenshot"
            raw = (json.dumps(summary, sort_keys=True, separators=(",", ":")) + "\n").encode()
            fd = os.open(directory / "surface-summary.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "wb") as stream:
                stream.write(raw)
            report["artifacts"] = {"browser-artifacts/surface-summary.json": {"sha256": hashlib.sha256(raw).hexdigest(), "size": len(raw)}}
        except Exception:
            report = result("artifact_write_failed")
    encoded = (json.dumps(report, sort_keys=True, separators=(",", ":")) + "\n").encode()
    if args.output is not None:
        try:
            fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "wb") as stream:
                stream.write(encoded)
        except OSError:
            report = result("output_write_failed")
            encoded = (json.dumps(report, sort_keys=True, separators=(",", ":")) + "\n").encode()
    sys.stdout.buffer.write(encoded)
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
