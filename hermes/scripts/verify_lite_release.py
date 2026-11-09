#!/usr/bin/env python3
"""Fail-closed verification of the reviewed Hermes Lite release, using stdlib only."""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import re
import stat
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib
import urllib.request
import campaign_evidence

RELEASE_PATH = "/usr/local/share/hermes-lite/release.json"
SHARE = "/usr/local/share/hermes-lite"
SUITES = {
    "lite_image": "smoke_lite_image.py", "lite_provider": "smoke_lite_provider.py",
    "lite_agent": "smoke_lite_agent.py", "lite_non_native": "smoke_lite_non_native.py",
    "desktop_rpc": "smoke_lite_desktop_rpc.py",
    "cm_bff_browser": "smoke_lite_cm_bff_browser.py",
}
FIXED_FILES = [
    "/usr/local/bin/" + name for name in
    ("clawmanager-agent", "start-hermes-lite-backend", "start-hermes-lite-runtime", "hermes-apply-runtime-config", "hermes-lite-entrypoint", "node", "python3.14")
] + ["/usr/local/lib/libpython3.14.so.1.0"] + [SHARE + "/" + name for name in (
    "source-lock.json", "verify_lite_release.py", "run_release_check.py", "run_campaign.py", "campaign_evidence.py", "precompile_lite.py",
    "campaign_signing.py", "verify_lite_promotion_oci.py", "verify_campaign_signature.mjs", "release-trust.json", "acceptance-protocol.json")]
FIXED_FILES += [
    "/usr/local/share/clawmanager/hermes-proxy-policy-v1",
    "/usr/local/share/clawmanager/hermes/skills/redis-team-protocol/SKILL.md",
    "/usr/local/share/clawmanager/hermes/skills/redis-team-protocol/skill.json",
]
MANDATORY = FIXED_FILES + ["/opt/hermes-agent/" + name for name in (
    "hermes_cli/web_server.py", "hermes_cli/runtime_provider.py", "hermes_cli/lite_gateway_boundary.py",
    "hermes_cli/lite_non_native.py", "hermes_cli/env_loader.py", "hermes_cli/lite_environment.py",
    "tui_gateway/server.py", "tui_gateway/methods_session.py",
    "model_tools.py", "agent/tool_executor.py", "hermes_cli/tui_dist/entry.js",
    "agent/secret_scope.py",
    "hermes_cli/web_server_chat.py", "hermes_cli/web_server_lifecycle.py", "hermes_cli/runtime_provider_custom.py",
    "tui_gateway/agent_factory.py", "tui_gateway/rpc_dispatch.py", "tui_gateway/session_registry.py",
    "tui_gateway/session_lifecycle.py", "tui_gateway/session_notifications.py", "install-stamp.json", "pm/lock.json",
    "pm-runtime/pm-runtime.json", "hermes_cli/tui_dist/package.json", ".install_method",
    ".venv/bin/hermes", "pyproject.toml", "uv.lock", "package-lock.json",
    "plugins/platforms/redis_team/adapter.py", "plugins/platforms/redis_team/plugin.yaml",
)] + [SHARE + "/patches/" + name for name in (
    "apply_lite_models_metadata.py", "apply_lite_picker_budget.py", "apply_lite_model_catalog.py", "apply_lite_gateway_boundary.py", "lite_gateway_boundary.py", "apply_lite_non_native.py", "lite_non_native.py", "lite_environment.py", "apply_lite_tool_executor.py", "apply_lite_startup.py", "lite_startup_timing.py", "lite_skills_sync.py", "apply_team_completion_stop.py", "apply_lite_team_completion_stop.py", "apply_lite_skill_dedupe.py", "apply_profile_external_skill_dedupe.py",
)] + [SHARE + "/tests/" + name for name in (*SUITES.values(), "check_lite_tool_executor.py", "cm_bff_browser.mjs", "acceptance_model_stub.py")]
ARTIFACT_TREES = ("/opt/hermes-agent", "/usr/local/lib/python3.14", SHARE + "/patches", SHARE + "/tests")
HEX256 = re.compile(r"[0-9a-f]{64}")
PATCH_FILES = {"apply_lite_models_metadata.py", "apply_lite_picker_budget.py", "apply_lite_model_catalog.py", "apply_lite_gateway_boundary.py", "lite_gateway_boundary.py", "apply_lite_non_native.py", "lite_non_native.py", "lite_environment.py", "apply_lite_tool_executor.py", "apply_lite_startup.py", "lite_startup_timing.py", "lite_skills_sync.py", "apply_team_completion_stop.py", "apply_lite_team_completion_stop.py", "apply_lite_skill_dedupe.py", "apply_profile_external_skill_dedupe.py"}
TEST_FILES = set(SUITES.values()) | {"test_lite_models_metadata.py", "test_lite_picker_budget.py", "test_lite_model_catalog.py", "check_lite_tool_executor.py", "cm_bff_browser.mjs", "acceptance_model_stub.py", "smoke_lite_team.py"}


def sha256_file(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def load_json(path):
    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result
    return json.loads(Path(path).read_text(encoding="utf-8"), object_pairs_hook=unique_pairs)


def managed_path(root, absolute, ownership=True, max_bytes=256 * 1024 * 1024):
    require(isinstance(absolute, str) and absolute.startswith("/") and "\\" not in absolute
            and not any(c in absolute for c in "\0\r\n")
            and all(p not in {"", ".", ".."} for p in absolute[1:].split("/")), "invalid managed path")
    root = Path(root).resolve()
    target = root / absolute[1:]
    current = root
    for component in Path(absolute[1:]).parts:
        current = current / component
        info = current.lstat()
        require(not stat.S_ISLNK(info.st_mode), "managed path is a symlink")
        if ownership:
            require(getattr(info, "st_uid", -1) == 0 and not info.st_mode & 0o022, "unsafe managed ownership or permissions")
    require(target.is_file(), "managed artifact is not a regular file")
    require(target.stat().st_size <= max_bytes, "managed artifact too large")
    return target


def payload_hash(artifacts):
    return hashlib.sha256("".join(path + "\0" + artifacts[path] + "\n" for path in sorted(artifacts)).encode()).hexdigest()


def artifact_allowed(path):
    return path in FIXED_FILES or any(path.startswith(prefix + "/") for prefix in ARTIFACT_TREES)


def collect_artifacts(root="/", ownership=True):
    root = Path(root).resolve()
    paths = set(FIXED_FILES)
    for tree in ARTIFACT_TREES:
        directory = root / tree[1:]
        require(directory.is_dir() and not directory.is_symlink(), "missing managed artifact directory")
        # Path.walk follows no symlink directories; interpreter aliases are not
        # executable evidence. The actual Python executable is required above.
        for parent, _, files in directory.walk(follow_symlinks=False):
            for name in files:
                path = parent / name
                if not path.is_symlink():
                    paths.add("/" + path.relative_to(root).as_posix())
    require(set(MANDATORY) <= paths and len(paths) <= 32768, "incomplete or oversized artifact inventory")
    return {path: sha256_file(managed_path(root, path, ownership)) for path in sorted(paths)}


def generate_release(lock, root="/", ownership=True):
    artifacts = collect_artifacts(root, ownership)
    return {
        "schema_version": 2, "hermes_ref": lock["tag"], "hermes_commit": lock["commit"],
        "package_version": lock["package_version"], "node_version": lock["node_version"],
        "contract_version": 1, "rpc_protocol": "hermes-jsonrpc-v1", "backend_mode": "serve",
        "auth_mode": "password-cookie", "desktop_web_accepted": False,
        "artifacts": artifacts, "payload_sha256": payload_hash(artifacts),
    }


def verify_release(release, lock, root="/", require_accepted=False, ownership=True, require_promotion=True):
    require(isinstance(release, dict) and type(release.get("schema_version")) is int
            and release["schema_version"] == 2, "unsupported managed release schema")
    expected = {"hermes_ref": lock["tag"], "hermes_commit": lock["commit"],
                "package_version": lock["package_version"], "node_version": lock["node_version"],
                "contract_version": 1, "rpc_protocol": "hermes-jsonrpc-v1", "backend_mode": "serve",
                "auth_mode": "password-cookie"}
    require(all(type(release.get(key)) is type(value) and release[key] == value for key, value in expected.items()),
            "managed release identity mismatch")
    require(type(release.get("desktop_web_accepted")) is bool, "missing accepted state")
    artifacts = release.get("artifacts", {})
    require(isinstance(artifacts, dict) and set(MANDATORY) <= artifacts.keys() and len(artifacts) <= 32768,
            "incomplete or oversized artifact inventory")
    require(all(isinstance(path, str) and artifact_allowed(path) and isinstance(digest, str)
                and HEX256.fullmatch(digest) for path, digest in artifacts.items()), "invalid artifact entry")
    require(release.get("payload_sha256") == payload_hash(artifacts), "artifact payload mismatch")
    total_size = 0
    for path, digest in artifacts.items():
        require(artifact_allowed(path) and isinstance(digest, str) and HEX256.fullmatch(digest), "invalid artifact entry")
        actual = managed_path(root, path, ownership)
        if ownership and (path.startswith("/usr/local/bin/") or path == "/opt/hermes-agent/.venv/bin/hermes"):
            require(actual.stat().st_mode & 0o111, "managed executable is not executable")
        total_size += actual.stat().st_size
        require(total_size <= 1024 * 1024 * 1024, "artifact inventory too large")
        require(sha256_file(actual) == digest, "artifact checksum mismatch: " + path)
    if not release["desktop_web_accepted"]:
        require(not require_accepted, "release is not accepted")
        return
    acceptance = release.get("acceptance", {})
    require(isinstance(acceptance, dict), "invalid acceptance record")
    require(acceptance.get("payload_sha256") == release["payload_sha256"], "acceptance payload mismatch")
    candidate = acceptance.get("candidate_image_digest", "")
    require(isinstance(candidate, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", candidate), "missing tested candidate digest")
    reports = acceptance.get("reports", {})
    require(isinstance(reports, dict) and set(reports) == set(SUITES), "acceptance requires every mandatory suite")
    for suite, script in SUITES.items():
        descriptor = reports[suite]
        report_path = SHARE + "/evidence/" + suite + ".json"
        require(isinstance(descriptor, dict) and descriptor.get("path") == report_path, "unexpected report path")
        path = managed_path(root, report_path, ownership, max_bytes=64 * 1024)
        require(sha256_file(path) == descriptor.get("sha256"), "report checksum mismatch")
        report = load_json(path)
        validate_execution_report(report, suite, release, root, ownership)
    campaign_evidence.validate_campaign(release, root, ownership, sys.modules[__name__], require_promotion)


def validate_execution_report(report, suite, release, root="/", ownership=True, phase="campaign"):
    script_path = SHARE + "/tests/" + SUITES[suite]
    artifacts = release["artifacts"]
    fields = {"schema_version", "phase", "binding_sha256", "suite", "status", "exit_code", "candidate_image_digest",
              "payload_sha256", "script_path", "script_sha256", "runner_sha256", "output_path", "output_sha256", "started_at", "finished_at"}
    if suite == "cm_bff_browser" and phase == "campaign":
        fields.add("browser_result")
    require(isinstance(report, dict) and set(report) == fields, "unexpected execution report schema")
    require(isinstance(report, dict) and type(report.get("schema_version")) is int
            and report["schema_version"] == 2 and report.get("suite") == suite and report.get("phase") == phase
            and report.get("status") == "passed" and type(report.get("exit_code")) is int
            and report["exit_code"] == 0, "test execution did not pass")
    require(report.get("payload_sha256") == release["payload_sha256"]
            and report.get("candidate_image_digest") == release["acceptance"]["candidate_image_digest"], "report is for another candidate")
    require(report.get("script_path") == script_path and report.get("script_sha256") == artifacts[script_path],
            "tested script differs from candidate")
    runner = "run_release_check.py" if phase == "campaign" else "verify_lite_release.py"
    require(report.get("runner_sha256") == artifacts[SHARE + "/" + runner], "untrusted report runner")
    require(isinstance(report.get("binding_sha256"), str) and HEX256.fullmatch(report["binding_sha256"]), "missing report campaign binding")
    require(isinstance(report.get("output_sha256"), str) and HEX256.fullmatch(report["output_sha256"]), "missing execution output digest")
    prefix = "promotion-" if phase == "promotion" else ""
    output_path = SHARE + "/evidence/" + prefix + suite + ".log"
    require(report.get("output_path") == output_path, "unexpected execution output path")
    output = managed_path(root, output_path, ownership, max_bytes=8 * 1024 * 1024)
    require(sha256_file(output) == report["output_sha256"], "execution output checksum mismatch")
    started = campaign_evidence.timestamp(report.get("started_at"))
    finished = campaign_evidence.timestamp(report.get("finished_at"))
    require(started <= finished <= datetime.now(timezone.utc) + timedelta(minutes=5), "invalid execution timestamps")


def accept_release(release, lock, root="/", ownership=True):
    # Integrity validation is separate from execution. The controlled build must
    # produce reports through the fixed runner; hashes are not CI signatures and
    # do not establish truth of claims supplied by an untrusted image builder.
    import copy
    accepted = copy.deepcopy(release)
    reports = {}
    candidate = None
    for suite in SUITES:
        absolute = SHARE + "/evidence/" + suite + ".json"
        path = managed_path(root, absolute, ownership, max_bytes=64 * 1024)
        report = load_json(path)
        require(isinstance(report, dict), "invalid execution report")
        if candidate is None:
            candidate = report.get("candidate_image_digest")
        require(candidate == report.get("candidate_image_digest"), "mixed candidate acceptance reports")
        reports[suite] = {"path": absolute, "sha256": sha256_file(path)}
    accepted["desktop_web_accepted"] = True
    accepted["acceptance"] = {"candidate_image_digest": candidate, "payload_sha256": accepted["payload_sha256"], "reports": reports}
    def ref(name):
        absolute = SHARE + "/evidence/" + name
        return {"path": absolute, "sha256": sha256_file(managed_path(root, absolute, ownership, max_bytes=65536))}
    accepted["acceptance"].update({"binding": ref("acceptance-binding.json"), "signature": ref("campaign-signature.json"),
                                    "observations": {phase: ref("campaign-" + phase + ".json") for phase in ("before", "after")}})
    verify_release(accepted, lock, root, require_accepted=True, ownership=ownership, require_promotion=False)
    return accepted


def recheck_promotion(release, root="/", ownership=True):
    """Rerun the five fixed local suites after verifying signed browser evidence."""
    records = {}
    for suite, script in SUITES.items():
        if suite == "cm_bff_browser":
            continue
        script_path = SHARE + "/tests/" + script
        checked = managed_path(root, script_path, ownership)
        require(sha256_file(checked) == release["artifacts"][script_path], "promotion script changed")
        report_path = Path(root) / (SHARE + "/evidence/promotion-" + suite + ".json")[1:]
        output_path = report_path.with_suffix(".log")
        require(not report_path.exists() and not output_path.exists(), "promotion evidence already exists")
        started = datetime.now(timezone.utc).isoformat()
        try:
            result = subprocess.run([sys.executable, str(checked)], cwd=root, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, timeout=600)
            output, exit_code = result.stdout, result.returncode
        except subprocess.TimeoutExpired as error:
            output, exit_code = error.output or b"", 124
        require(len(output) <= 8 * 1024 * 1024, "promotion output too large")
        record = {"schema_version": 2, "phase": "promotion", "suite": suite, "status": "passed" if exit_code == 0 else "failed",
                  "exit_code": exit_code, "candidate_image_digest": release["acceptance"]["candidate_image_digest"],
                  "payload_sha256": release["payload_sha256"], "script_path": script_path,
                  "script_sha256": release["artifacts"][script_path], "started_at": started,
                  "finished_at": datetime.now(timezone.utc).isoformat(),
                  "binding_sha256": release["acceptance"]["binding"]["sha256"],
                  "runner_sha256": release["artifacts"][SHARE + "/verify_lite_release.py"],
                  "output_path": SHARE + "/evidence/promotion-" + suite + ".log",
                  "output_sha256": hashlib.sha256(output).hexdigest()}
        for path, raw in ((output_path, output), (report_path, (json.dumps(record, sort_keys=True, indent=2) + "\n").encode())):
            with path.open("xb") as stream:
                stream.write(raw)
            path.chmod(0o444)
        records[suite] = {"path": SHARE + "/evidence/" + report_path.name, "sha256": sha256_file(report_path)}
        require(exit_code == 0, "promotion suite failed: " + suite)
    return records


def require(value, message):
    if not value:
        raise ValueError(message)


def load_lock(path):
    lock = load_json(path)
    require(lock.get("schema_version") == 1, "unsupported lock schema")
    require(re.fullmatch(r"v(0|[1-9]\d{0,2})\.\d+\.\d+", lock["tag"]), "release tag must be fixed")
    require(re.fullmatch(r"[0-9a-f]{40}", lock["commit"]), "commit must be a full SHA")
    require(re.fullmatch(r"[0-9a-f]{40}", lock["tag_object"]), "tag object must be a full SHA")
    require(re.fullmatch(r"\d+\.\d+\.\d+", lock["package_version"]), "invalid package version")
    require(lock["tag"] == "v" + lock["package_version"], "release tag/version mismatch")
    require(lock.get("source_package_version") == "0.0.0" and lock.get("python_version") == "3.14",
            "unsupported source metadata/runtime")
    require(lock["repository"] == "https://github.com/NousResearch/hermes-agent.git", "unexpected upstream")
    expected = "https://codeload.github.com/NousResearch/hermes-agent/tar.gz/" + lock["commit"]
    require(lock["archive_url"] == expected, "archive URL must name the locked commit")
    for digest in [lock["archive_sha256"], *lock["source_sha256"].values()]:
        require(re.fullmatch(r"[0-9a-f]{64}", digest), "invalid checksum")
    for field, expected in (("patch_sha256", PATCH_FILES), ("test_sha256", TEST_FILES),
                            ("acceptance_sha256", {"release-trust.json", "acceptance-protocol.json"})):
        entries = lock.get(field)
        require(isinstance(entries, dict) and set(entries) == expected, "incomplete reviewed local inputs: " + field)
        require(all(isinstance(digest, str) and HEX256.fullmatch(digest) for digest in entries.values()), "invalid reviewed input checksum")
    return lock


def verify_local_inputs(lock, directory, key):
    entries = lock.get(key, {})
    require(entries, "missing reviewed local input hashes: " + key)
    for name, digest in entries.items():
        require(Path(name).name == name and HEX256.fullmatch(digest), "invalid reviewed input")
        require(sha256_file(Path(directory) / name) == digest, "local input checksum mismatch: " + name)


def verify_upstream(lock):
    output = subprocess.check_output([
        "git", "ls-remote", "--tags", lock["repository"],
        "refs/tags/" + lock["tag"], "refs/tags/" + lock["tag"] + "^{}",
    ], text=True, timeout=60)
    refs = {line.split()[1]: line.split()[0] for line in output.splitlines()}
    ref = "refs/tags/" + lock["tag"]
    require(refs.get(ref) == lock["tag_object"], "upstream tag object changed")
    require(refs.get(ref + "^{}", refs.get(ref)) == lock["commit"], "upstream tag commit changed")


def verify_source(lock, root):
    root = Path(root)
    for relative, digest in lock["source_sha256"].items():
        require((root / relative).is_file(), "missing locked source: " + relative)
        require(hashlib.sha256((root / relative).read_bytes()).hexdigest() == digest,
                "source checksum mismatch: " + relative)
    project = tomllib.loads((root / "pyproject.toml").read_text(encoding="utf-8"))
    # SemVer releases use a placeholder distribution version. The immutable tag
    # and a build-time upstream install stamp identify the runnable release.
    require(project["project"]["version"] == lock["source_package_version"], "Python source package version mismatch")
    require((root / ".python-version").read_text().strip() == lock["python_version"], "Python runtime mismatch")
    for relative, expected in lock["node_packages"].items():
        actual = json.loads((root / relative).read_text(encoding="utf-8"))["version"]
        require(actual == expected, "Node package version mismatch: " + relative)


def download_source(lock, destination, archive_cache=None, local_archive=None):
    destination = Path(destination)
    require(not destination.exists(), "download destination already exists")
    with tempfile.TemporaryDirectory() as temporary:
        archive = Path(local_archive) if local_archive else Path(temporary) / "source.tar.gz"
        cached = Path(archive_cache) / (lock["archive_sha256"] + ".tar.gz") if archive_cache else None
        if local_archive:
            require(archive.is_file(), "local source archive is missing")
        elif cached and cached.is_file():
            archive = cached
        else:
            with urllib.request.urlopen(lock["archive_url"], timeout=120) as response, archive.open("wb") as output:
                shutil.copyfileobj(response, output)
        require(hashlib.sha256(archive.read_bytes()).hexdigest() == lock["archive_sha256"], "archive checksum mismatch")
        if cached and archive != cached:
            cached.parent.mkdir(parents=True, exist_ok=True)
            # Cache only verified complete archives; every reuse is hashed again.
            with tempfile.NamedTemporaryFile(dir=cached.parent, delete=False) as output:
                staging = Path(output.name)
                with archive.open("rb") as source:
                    shutil.copyfileobj(source, output)
            staging.replace(cached)
        unpacked = Path(temporary) / "unpacked"
        with tarfile.open(archive) as source:
            source.extractall(unpacked, filter="data")
        root = unpacked / ("hermes-agent-" + lock["commit"])
        verify_source(lock, root)
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(root), str(destination))
    (destination / ".hermes_build_sha").write_text(lock["commit"] + "\n", encoding="utf-8")


def write_install_stamp(lock, destination):
    destination = Path(destination)
    require((destination / "pm-runtime/pm-runtime.json").is_file(), "missing resident PM runtime")
    subprocess.run([
        sys.executable, str(destination / "scripts/write_install_stamp.py"),
        "--output", str(destination / "install-stamp.json"), "--commit", lock["commit"],
        "--base-version", lock["package_version"], "--distance", "0",
        "--source", "docker", "--distribution", "docker", "--update-mechanism", "external",
    ], check=True, timeout=60)
    path = destination / "install-stamp.json"
    stamp = load_json(path)
    stamp["pmRuntime"] = str(destination / "pm-runtime")
    path.write_text(json.dumps(stamp, indent=2) + "\n", encoding="utf-8")
    (destination / ".install_method").write_text("docker\n", encoding="utf-8")


def verify_runtime(lock, hermes, node):
    output = subprocess.check_output([hermes, "--version"], text=True, timeout=60)
    require(re.search(r"(?<![\d.])v?" + re.escape(lock["package_version"]) + r"(?![\d.])", output),
            "hermes --version mismatch")
    node_version = subprocess.check_output([node, "--version"], text=True, timeout=20).strip()
    require(node_version == "v" + lock["node_version"], "Node runtime version mismatch")
    verify_install_stamp(lock, Path("/opt/hermes-agent"))


def verify_install_stamp(lock, root):
    stamp = load_json(Path(root) / "install-stamp.json")
    expected = {"schemaVersion": 2, "commit": lock["commit"], "baseVersion": lock["package_version"],
                "distance": 0, "source": "docker", "distribution": "docker", "updateMechanism": "external",
                "pmRuntime": str(Path(root) / "pm-runtime")}
    require(all(type(stamp.get(key)) is type(value) and stamp[key] == value for key, value in expected.items()),
            "runtime install stamp mismatch")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock", required=True)
    parser.add_argument("--upstream", action="store_true")
    parser.add_argument("--source", type=Path)
    parser.add_argument("--download", type=Path)
    parser.add_argument("--archive-cache", type=Path, help="Cache verified upstream archives by SHA-256")
    parser.add_argument("--archive", type=Path, help="Use a local archive, with the same mandatory SHA-256/source checks")
    parser.add_argument("--write-install-stamp", type=Path, help="Stamp the completed packaged runtime")
    parser.add_argument("--runtime", action="store_true")
    parser.add_argument("--patch-root", type=Path)
    parser.add_argument("--test-root", type=Path)
    parser.add_argument("--acceptance-root", type=Path)
    parser.add_argument("--generate-release", action="store_true")
    parser.add_argument("--verify-release", action="store_true")
    parser.add_argument("--accept-release", action="store_true")
    parser.add_argument("--candidate-image", help="Immutable repository@sha256 reference used as the accepted layer base")
    parser.add_argument("--hermes", default="hermes")
    parser.add_argument("--node", default="node")
    args = parser.parse_args()
    lock = load_lock(args.lock)
    if args.upstream:
        verify_upstream(lock)
    if args.source:
        verify_source(lock, args.source)
    if args.download:
        download_source(lock, args.download, args.archive_cache, args.archive)
    if args.write_install_stamp:
        write_install_stamp(lock, args.write_install_stamp)
    if args.runtime:
        verify_runtime(lock, args.hermes, args.node)
    if args.patch_root:
        verify_local_inputs(lock, args.patch_root, "patch_sha256")
    if args.test_root:
        verify_local_inputs(lock, args.test_root, "test_sha256")
    if args.acceptance_root:
        verify_local_inputs(lock, args.acceptance_root, "acceptance_sha256")
    if args.generate_release:
        release = generate_release(lock)
        Path(RELEASE_PATH).write_text(json.dumps(release, sort_keys=True, indent=2) + "\n", encoding="utf-8")
        Path(RELEASE_PATH).chmod(0o444)
    if args.verify_release or args.accept_release:
        path = managed_path("/", RELEASE_PATH, max_bytes=4 * 1024 * 1024)
        release = load_json(path)
        if args.accept_release:
            require(release.get("desktop_web_accepted") is False, "promotion requires an unaccepted candidate")
            require(args.candidate_image and re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", args.candidate_image),
                    "acceptance requires an immutable candidate image")
            release = accept_release(release, lock)
            require(release["acceptance"]["candidate_image_digest"] == args.candidate_image.rsplit("@", 1)[1],
                    "accepted layer must derive from the tested candidate digest")
            release["acceptance"]["promotion_reports"] = recheck_promotion(release)
            # Tests may use temporary state; no managed runtime file may change.
            verify_release(release, lock, require_accepted=True)
            path.write_text(json.dumps(release, sort_keys=True, indent=2) + "\n", encoding="utf-8")
            path.chmod(0o444)
        else:
            verify_release(release, lock)
    print("Verified Hermes Lite " + lock["tag"] + " / " + lock["commit"])


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit("Hermes Lite verification failed: " + str(error))
