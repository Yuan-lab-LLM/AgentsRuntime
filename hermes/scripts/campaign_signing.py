"""Signing capability used only by run_campaign after its actual executions.

There is deliberately no command-line entry point to sign supplied reports.
The private key is an operator/CI trust boundary; hashes do not prove execution
against a malicious signer or image builder. This module never publishes images.
"""
import base64
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

import campaign_evidence
import verify_lite_release as verify


def sign_completed_campaign(evidence_dir, release, private_key_path):
    evidence_dir = Path(evidence_dir).resolve()
    destination = evidence_dir / "campaign-signature.json"
    if destination.exists():
        raise ValueError("Refusing to overwrite campaign signature")
    for filename in (Path(__file__), Path(verify.__file__), Path(campaign_evidence.__file__)):
        verify.require(verify.sha256_file(filename) == release["artifacts"].get(verify.SHARE + "/" + filename.name),
                       "signer/helper differs from candidate source")
    # Build an isolated, read-only validation tree from actual candidate files
    # and execution evidence. The caller supplies extracted candidate metadata.
    # Runtime artifact bytes were verified by each immutable-image suite runner.
    accepted = copy.deepcopy(release)
    def ref(filename):
        path = evidence_dir / filename
        return {"path": verify.SHARE + "/evidence/" + filename, "sha256": verify.sha256_file(path)}
    binding = verify.load_json(evidence_dir / "acceptance-binding.json")
    accepted["acceptance"] = {
        "candidate_image_digest": binding["candidate_image_digest"], "payload_sha256": release["payload_sha256"],
        "binding": ref("acceptance-binding.json"), "reports": {suite: ref(suite + ".json") for suite in verify.SUITES},
        "observations": {phase: ref("campaign-" + phase + ".json") for phase in ("before", "after")},
    }
    with tempfile.TemporaryDirectory(prefix="hermes-sign-campaign-") as temporary:
        root = Path(temporary)
        share = root / verify.SHARE[1:]
        share.mkdir(parents=True)
        shutil.copytree(evidence_dir, share / "evidence", symlinks=True)
        # These two files are fixed candidate inputs, never caller-picked policy.
        acceptance_inputs = Path(__file__).resolve().parents[1] / "acceptance"
        for filename in ("acceptance-protocol.json", "release-trust.json"):
            source = acceptance_inputs / filename
            if not source.exists():
                source = Path(__file__).resolve().parent / filename
            verify.require(verify.sha256_file(source) == release["artifacts"].get(verify.SHARE + "/" + filename), "signer candidate policy mismatch")
            shutil.copyfile(source, share / filename)
        _, statement, _ = campaign_evidence.campaign_statement(accepted, root, False, verify)
        trust = verify.load_json(share / "release-trust.json")
        raw = json.dumps(statement, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
        payload = share / "statement.bin"
        payload.write_bytes(b"hermes-lite-acceptance-v1\n" + raw)
        node = shutil.which("node")
        verify.require(node is not None, "Node is required for campaign signing")
        verify.require(verify.sha256_file(node) == binding["node_sha256"], "signing Node differs from verified campaign runner")
        code = "import{readFileSync}from'node:fs';import{createPrivateKey,createPublicKey,sign}from'node:crypto';const k=createPrivateKey(readFileSync(process.argv[1]));const p=createPublicKey(k).export({format:'jwk'});process.stdout.write(JSON.stringify({public:p.x,signature:sign(null,readFileSync(process.argv[2]),k).toString('base64')}))"
        outcome = subprocess.run([node, "--input-type=module", "-e", code, str(Path(private_key_path).resolve()), str(payload)],
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
                                 env={key: value for key, value in os.environ.items() if key.upper() in {"PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP"}})
        verify.require(outcome.returncode == 0, "campaign signing failed")
        signed = json.loads(outcome.stdout)
        public = base64.urlsafe_b64decode(signed["public"] + "=")
        verify.require(base64.b64encode(public).decode() == trust["public_key_base64"], "private key does not match candidate trust root")
        envelope = {"schema_version": 1, "algorithm": "Ed25519", "key_id": trust["key_id"],
                    "payload_base64": base64.b64encode(raw).decode(), "signature_base64": signed["signature"]}
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as stream:
            stream.write(json.dumps(envelope, sort_keys=True, indent=2) + "\n")
    return destination


if __name__ == "__main__":
    raise SystemExit("No signing CLI: run the fixed campaign with real candidate, CM and browser observations")
