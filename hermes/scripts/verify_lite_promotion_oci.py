#!/usr/bin/env python3
"""Verify exact OCI ancestry: acceptance may add only release/evidence files."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

import campaign_evidence
import verify_lite_release as verify

SHARE = "usr/local/share/hermes-lite"


class OCI:
    def __init__(self, path, digest):
        self.archive = tarfile.open(path, "r")
        try:
            self._load(digest)
        except BaseException:
            self.archive.close()
            raise

    def _load(self, digest):
        self.digest = digest
        manifest = self.json(digest)
        self.manifest_digest = digest
        if "manifests" in manifest:
            platforms = [entry for entry in manifest["manifests"] if entry.get("platform", {}).get("os") == "linux"
                         and entry["platform"].get("architecture") == "amd64"]
            verify.require(len(platforms) == 1, "expected one linux/amd64 manifest")
            self.manifest_digest = platforms[0]["digest"]
            manifest = self.json(self.manifest_digest)
        self.manifest = manifest
        self.config = self.json(manifest["config"]["digest"])

    def blob(self, digest):
        verify.require(isinstance(digest, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", digest), "invalid OCI digest")
        stream = self.archive.extractfile("blobs/sha256/" + digest[7:])
        verify.require(stream is not None, "missing OCI blob")
        raw = stream.read()
        verify.require("sha256:" + hashlib.sha256(raw).hexdigest() == digest, "OCI blob checksum mismatch")
        return raw

    def json(self, digest):
        return json.loads(self.blob(digest))

    def layer(self, descriptor):
        raw = self.blob(descriptor["digest"])
        verify.require(len(raw) == descriptor["size"], "OCI layer size mismatch")
        kind = descriptor["mediaType"]
        verify.require(kind in {"application/vnd.oci.image.layer.v1.tar", "application/vnd.oci.image.layer.v1.tar+gzip"}, "unsupported promotion layer compression")
        stream = gzip.GzipFile(fileobj=io.BytesIO(raw)) if kind.endswith("+gzip") else io.BytesIO(raw)
        return tarfile.open(fileobj=stream, mode="r|"), hashlib.sha256()

    def files(self, wanted):
        found = {}
        for descriptor in reversed(self.manifest["layers"]):
            archive, _ = self.layer(descriptor)
            with archive:
                for entry in archive:
                    name = entry.name.removeprefix("./").rstrip("/")
                    if name in wanted and name not in found:
                        verify.require(entry.isfile() and not entry.issym() and not entry.islnk(), "candidate evidence is not a regular file")
                        verify.require(entry.size <= 4 * 1024 * 1024, "candidate evidence too large")
                        found[name] = archive.extractfile(entry).read()
            if set(found) == set(wanted):
                return found
        raise ValueError("candidate is missing required release policy")


def allowed_file(name):
    if name == SHARE + "/release.json":
        return True
    if not name.startswith(SHARE + "/evidence/"):
        return False
    relative = name[len(SHARE + "/evidence/"):]
    fixed = {"acceptance-binding.json", "campaign-signature.json", "campaign-before.json", "campaign-after.json",
             "cm-provenance.json", "renderer-asset-manifest.json", "cm-bff-browser-result.json"}
    fixed |= {suite + suffix for suite in verify.SUITES for suffix in (".json", ".log")}
    fixed |= {"promotion-" + suite + suffix for suite in verify.SUITES if suite != "cm_bff_browser" for suffix in (".json", ".log")}
    return relative in fixed or re.fullmatch(r"browser-artifacts/[A-Za-z0-9][A-Za-z0-9._-]{0,119}", relative) is not None


def verify_ancestry(candidate, accepted):
    base_layers = candidate.manifest["layers"]
    final_layers = accepted.manifest["layers"]
    verify.require(final_layers[:len(base_layers)] == base_layers and 1 <= len(final_layers) - len(base_layers) <= 4,
                   "accepted OCI must append layers to the exact candidate")
    for descriptor in base_layers:
        candidate.blob(descriptor["digest"])
        accepted.blob(descriptor["digest"])
    base_config, final_config = candidate.config, accepted.config
    ignored = {"rootfs", "history", "created"}
    verify.require({key: value for key, value in base_config.items() if key not in ignored}
                   == {key: value for key, value in final_config.items() if key not in ignored}, "accepted image configuration changed")
    base_root, final_root = base_config["rootfs"], final_config["rootfs"]
    verify.require(base_root["type"] == final_root["type"] == "layers"
                   and final_root["diff_ids"][:len(base_root["diff_ids"])] == base_root["diff_ids"]
                   and len(final_root["diff_ids"]) == len(final_layers), "accepted image rootfs ancestry changed")
    verify.require(final_config.get("history", [])[:len(base_config.get("history", []))] == base_config.get("history", []), "accepted history lost candidate ancestry")
    overlay, total = {}, 0
    ancestors = {"usr", "usr/local", "usr/local/share", SHARE, SHARE + "/evidence", SHARE + "/evidence/browser-artifacts"}
    for index, descriptor in enumerate(final_layers[len(base_layers):], start=len(base_layers)):
        raw = accepted.blob(descriptor["digest"])
        verify.require(descriptor["mediaType"] in {"application/vnd.oci.image.layer.v1.tar", "application/vnd.oci.image.layer.v1.tar+gzip"}, "unsupported promotion layer compression")
        verify.require(len(raw) == descriptor["size"], "promotion layer size mismatch")
        unpacked = gzip.GzipFile(fileobj=io.BytesIO(raw)).read(192 * 1024 * 1024 + 1) if descriptor["mediaType"].endswith("+gzip") else raw
        verify.require(len(unpacked) <= 192 * 1024 * 1024, "promotion layer too large")
        verify.require("sha256:" + hashlib.sha256(unpacked).hexdigest() == final_root["diff_ids"][index], "promotion layer diff ID mismatch")
        seen = set()
        with tarfile.open(fileobj=io.BytesIO(unpacked), mode="r:") as archive:
            for entry in archive:
                name = entry.name.removeprefix("./").rstrip("/")
                verify.require(not name.startswith("/") and "\\" not in name and all(part not in {"", ".", ".."} for part in name.split("/"))
                               and name not in seen, "unsafe or repeated promotion path")
                seen.add(name)
                verify.require(entry.uid == 0 and entry.gid == 0 and not entry.mode & 0o022 and not entry.mode & 0o7000, "unsafe promotion file ownership or mode")
                if entry.isdir():
                    verify.require(name in ancestors and entry.mode == 0o755, "promotion changed unrelated directory or ancestor mode")
                    continue
                verify.require(entry.isfile() and not entry.issym() and not entry.islnk() and allowed_file(name), "promotion changed a runtime file or added an unsupported entry")
                verify.require(entry.size <= (4 * 1024 * 1024 if name.endswith("release.json") else 8 * 1024 * 1024), "promotion evidence file too large")
                total += entry.size
                verify.require(total <= 192 * 1024 * 1024, "promotion evidence too large")
                overlay[name] = archive.extractfile(entry).read()
    policy_names = ["release.json", "acceptance-protocol.json", "release-trust.json", "verify_campaign_signature.mjs"]
    policy = candidate.files({SHARE + "/" + name for name in policy_names})
    original = json.loads(policy[SHARE + "/release.json"])
    for name in policy_names[1:]:
        verify.require(hashlib.sha256(policy[SHARE + "/" + name]).hexdigest() == original["artifacts"].get("/" + SHARE + "/" + name), "candidate policy checksum mismatch")
    verify.require(SHARE + "/release.json" in overlay, "accepted release record missing")
    final = json.loads(overlay[SHARE + "/release.json"])
    mutable = {"desktop_web_accepted", "acceptance"}
    verify.require(original.get("desktop_web_accepted") is False and final.get("desktop_web_accepted") is True
                   and {key: value for key, value in original.items() if key not in mutable}
                   == {key: value for key, value in final.items() if key not in mutable}, "accepted release changed candidate payload")
    verify.require(final["acceptance"]["candidate_image_digest"] == candidate.digest, "accepted layer names another candidate")
    with tempfile.TemporaryDirectory(prefix="hermes-oci-acceptance-") as temporary:
        root = Path(temporary)
        for name, content in {**policy, **overlay}.items():
            destination = root / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(content)
        binding, statement, times = campaign_evidence.campaign_statement(final, root, False, verify)
        verify.require(binding["candidate_manifest_digest"] == candidate.manifest_digest and binding["candidate_config_digest"] == candidate.manifest["config"]["digest"], "signed campaign names another OCI platform/config")
        campaign_evidence.validate_promotion_reports(final, root, False, verify, statement["binding_sha256"], times["after"])
        signature = campaign_evidence.descriptor(root, final["acceptance"]["signature"], "/" + SHARE + "/evidence/campaign-signature.json", verify, False)
        trust_path = root / SHARE / "release-trust.json"
        verify.require(campaign_evidence.decode_signature(verify.load_json(signature), verify.load_json(trust_path), verify) == statement, "signed OCI evidence mismatch")
        helper = Path(__file__).with_name("verify_campaign_signature.mjs")
        verify.require(verify.sha256_file(helper) == original["artifacts"]["/" + SHARE + "/verify_campaign_signature.mjs"], "candidate signature verifier checksum mismatch")
        node = shutil.which("node")
        verify.require(node is not None, "host Node is required for offline OCI signature verification")
        checked = subprocess.run([node, str(helper), str(trust_path), str(signature)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
        verify.require(checked.returncode == 0 and checked.stdout == b"Verified campaign signature\n", "accepted OCI signature rejected")
    return {"schema_version": 1, "candidate_image_digest": candidate.digest, "accepted_image_digest": accepted.digest,
            "candidate_manifest_digest": candidate.manifest_digest, "accepted_manifest_digest": accepted.manifest_digest,
            "added_layers": len(final_layers) - len(base_layers), "payload_sha256": original["payload_sha256"], "status": "passed"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate-oci", type=Path, required=True)
    parser.add_argument("--candidate-digest", required=True)
    parser.add_argument("--accepted-oci", type=Path, required=True)
    parser.add_argument("--accepted-digest", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    verify.require(not args.output.exists(), "refusing to overwrite OCI verification evidence")
    candidate, accepted = OCI(args.candidate_oci, args.candidate_digest), OCI(args.accepted_oci, args.accepted_digest)
    try:
        result = verify_ancestry(candidate, accepted)
    finally:
        candidate.archive.close()
        accepted.archive.close()
    with args.output.open("x", encoding="utf-8", newline="\n") as stream:
        stream.write(json.dumps(result, sort_keys=True, indent=2) + "\n")
    print("Verified accepted OCI ancestry, signed evidence and unchanged runtime configuration")


if __name__ == "__main__":
    main()
