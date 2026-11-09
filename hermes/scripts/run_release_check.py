#!/usr/bin/env python3
"""Execute one fixed suite in the exact candidate and bind its result to payload.

The five local suites use isolated containers. The fixed CM/browser suite runs
on the controlled host with explicit, hash-bound execution configuration.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import uuid
import re
import shutil
import verify_lite_release as verify
import campaign_evidence

SHARE = "/usr/local/share/hermes-lite"
SUITES = {"lite_image": "smoke_lite_image.py", "lite_provider": "smoke_lite_provider.py",
          "lite_agent": "smoke_lite_agent.py", "lite_non_native": "smoke_lite_non_native.py",
          "desktop_rpc": "smoke_lite_desktop_rpc.py",
          "cm_bff_browser": "smoke_lite_cm_bff_browser.py"}


def candidate_identity(archive_path, candidate):
    """Read the content-addressed OCI graph, not mutable image-name metadata."""
    with tarfile.open(archive_path, "r") as archive:
        def blob(digest):
            if not isinstance(digest, str) or len(digest) != 71 or not digest.startswith("sha256:"):
                raise ValueError("Invalid OCI content address")
            raw = archive.extractfile("blobs/sha256/" + digest[7:]).read(4 * 1024 * 1024 + 1)
            if len(raw) > 4 * 1024 * 1024 or "sha256:" + hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError("OCI metadata checksum mismatch")
            return json.loads(raw)
        manifest = blob(candidate)
        manifest_digest = candidate
        if "manifests" in manifest:
            platforms = [item for item in manifest["manifests"]
                         if item.get("platform", {}).get("os") == "linux"
                         and item["platform"].get("architecture") == "amd64"]
            if len(platforms) != 1:
                raise ValueError("Candidate must contain exactly one tested linux/amd64 manifest")
            manifest_digest = platforms[0]["digest"]
            manifest = blob(manifest_digest)
        return {"manifest_digest": manifest_digest, "config_digest": manifest["config"]["digest"]}


def candidate_config(archive_path, candidate):
    return candidate_identity(archive_path, candidate)["config_digest"]


def execute_browser(actual, release, config, binding, evidence_dir):
    if config is None or binding is None:
        return b"BLOCKED: real CM browser execution requires explicit binding and execution configuration\n", 1, None
    with tempfile.TemporaryDirectory(prefix="hermes-browser-check-") as temporary:
        directory = Path(temporary)
        for filename in (SUITES["cm_bff_browser"], "cm_bff_browser.mjs"):
            absolute = SHARE + "/tests/" + filename
            raw = subprocess.check_output(["docker", "run", "--rm", "--network", "none", "--entrypoint", "cat", actual, absolute], timeout=30)
            if hashlib.sha256(raw).hexdigest() != release["artifacts"].get(absolute):
                raise ValueError("Candidate browser script checksum mismatch")
            (directory / filename).write_bytes(raw)
        result_path = directory / "cm-bff-browser-result.json"
        command = [sys.executable, "-I", "-B", str(directory / SUITES["cm_bff_browser"]),
                   "--config", str(config.resolve()), "--output", str(result_path), "--binding", str(binding.resolve())]
        allowed = {"PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE", "LOCALAPPDATA"}
        env = {key: value for key, value in os.environ.items() if key.upper() in allowed}
        try:
            result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=1800, env=env)
            output, exit_code = result.stdout, result.returncode
        except subprocess.TimeoutExpired as error:
            output, exit_code = error.output or b"", 124
        descriptor = None
        if result_path.exists():
            data = verify.load_json(result_path)
            if result_path.stat().st_size > 1024 * 1024:
                raise ValueError("Browser result too large")
            files = data.get("artifacts", {})
            if not isinstance(files, dict) or len(files) > 32:
                raise ValueError("Browser artifact inventory too large")
            total = 0
            for relative, ref in files.items():
                if not isinstance(relative, str) or not re.fullmatch(r"browser-artifacts/[A-Za-z0-9][A-Za-z0-9._-]{0,119}", relative):
                    raise ValueError("Invalid browser artifact path")
                source = directory / relative
                if source.is_symlink() or not source.is_file() or source.stat().st_size > 8 * 1024 * 1024:
                    raise ValueError("Invalid browser artifact")
                total += source.stat().st_size
                if total > 32 * 1024 * 1024 or source.stat().st_size != ref.get("size") or verify.sha256_file(source) != ref.get("sha256"):
                    raise ValueError("Browser artifact checksum or size mismatch")
                destination = evidence_dir / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                with destination.open("xb") as stream:
                    stream.write(source.read_bytes())
            destination = evidence_dir / result_path.name
            with destination.open("xb") as stream:
                stream.write(result_path.read_bytes())
            descriptor = {"path": SHARE + "/evidence/" + result_path.name, "sha256": verify.sha256_file(destination)}
        elif exit_code == 0:
            output, exit_code = b"Browser suite produced no execution result\n", 126
        return output, exit_code, descriptor


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--metadata", type=Path, required=True, help="BuildKit metadata from the same OCI candidate build")
    parser.add_argument("--oci", type=Path, required=True, help="Same OCI archive; verifies the candidate-to-config content-addressed graph")
    parser.add_argument("--suite", choices=SUITES, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--binding", type=Path, help="Exact campaign binding; absent local-only evidence cannot be accepted")
    parser.add_argument("--execution-config", type=Path, help="Explicit CM browser configuration, never an arbitrary command")
    args = parser.parse_args()
    if args.output.exists() or args.output.with_suffix(".log").exists():
        raise SystemExit("Refusing to overwrite an existing execution report; use a new evidence directory")
    if args.output.name != args.suite + ".json":
        raise SystemExit("Report filename must be <suite>.json")
    metadata = json.loads(args.metadata.read_text(encoding="utf-8"))
    candidate = metadata["containerimage.digest"]
    actual = subprocess.check_output(["docker", "image", "inspect", args.image, "--format", "{{.Id}}"], text=True).strip()
    # Containerd retains the index. Classic Docker stores expose the config ID.
    config = candidate_config(args.oci, candidate)
    if actual not in {candidate, config}:
        raise SystemExit("Local image does not match the candidate OCI metadata/config")
    raw = subprocess.check_output(["docker", "run", "--rm", "--network", "none", "--entrypoint", "cat",
                                   actual, SHARE + "/release.json"], timeout=30)
    release = json.loads(raw)
    runner_hash = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    for filename in (Path(__file__), Path(verify.__file__), Path(campaign_evidence.__file__)):
        if release["artifacts"].get(SHARE + "/" + filename.name) != hashlib.sha256(filename.read_bytes()).hexdigest():
            raise SystemExit("Execution runner/helper differs from the source recorded in the candidate")
    binding_hash = None
    if args.binding:
        binding = verify.load_json(args.binding)
        campaign_evidence.validate_binding(binding, release, verify)
        identity = candidate_identity(args.oci, candidate)
        if (binding["candidate_image_digest"] != candidate or binding["candidate_config_digest"] != config
                or binding["candidate_manifest_digest"] != identity["manifest_digest"]):
            raise SystemExit("Campaign binding differs from the immutable candidate")
        binding_hash = verify.sha256_file(args.binding)
        if args.execution_config and verify.sha256_file(args.execution_config) != binding["execution_config_sha256"]:
            raise SystemExit("Browser execution configuration differs from campaign binding")
    if args.execution_config and args.suite != "cm_bff_browser":
        raise SystemExit("Only the CM browser suite accepts execution configuration")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    script = SHARE + "/tests/" + SUITES[args.suite]
    started = datetime.now(timezone.utc).isoformat()
    name = "hermes-release-check-" + uuid.uuid4().hex
    command = ["docker", "run", "--rm", "--name", name, "--network", "none", "--entrypoint", "python", actual, script]
    browser_result = None
    try:
        if args.suite == "cm_bff_browser":
            output, exit_code, browser_result = execute_browser(actual, release, args.execution_config, args.binding, args.output.parent)
        else:
            result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
            output, exit_code = result.stdout, result.returncode
    except subprocess.TimeoutExpired as error:
        output, exit_code = error.output or b"", 124
    finally:
        # A timeout can kill the Docker client before its --rm container exits.
        # This unpredictable owned name never targets another task's container.
        if args.suite != "cm_bff_browser":
            subprocess.run(["docker", "rm", "--force", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
    if len(output) > 8 * 1024 * 1024:
        output, exit_code = output[:8 * 1024 * 1024], 125
    report = {
        "schema_version": 2, "phase": "campaign", "binding_sha256": binding_hash,
        "suite": args.suite, "status": "passed" if exit_code == 0 else "failed",
        "exit_code": exit_code, "candidate_image_digest": candidate, "payload_sha256": release["payload_sha256"],
        "script_path": script, "script_sha256": release["artifacts"][script], "runner_sha256": runner_hash,
        "output_path": SHARE + "/evidence/" + args.suite + ".log",
        "output_sha256": hashlib.sha256(output).hexdigest(), "started_at": started,
        "finished_at": datetime.now(timezone.utc).isoformat(),
    }
    if browser_result:
        report["browser_result"] = browser_result
    args.output.parent.mkdir(parents=True, exist_ok=True)
    for path, content in [(args.output.with_suffix(".log"), output),
                          (args.output, (json.dumps(report, sort_keys=True, indent=2) + "\n").encode())]:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(content)
    print(args.suite + ": " + report["status"] + " (bound execution report: " + str(args.output) + ")")
    return 0 if exit_code == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
