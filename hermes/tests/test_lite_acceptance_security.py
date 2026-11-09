"""Real signature checks and adversarial OCI promotion fixtures (no release)."""
import base64
import copy
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import campaign_evidence
import verify_lite_promotion_oci as oci
import verify_lite_release as verify
import campaign_signing


class SignatureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.node = shutil.which("node")
        self.assertIsNotNone(self.node, "Signature verification tests require Node, as the candidate does")
        self.trust, self.signature = self.root / "trust.json", self.root / "signature.json"
        code = """const fs=require('node:fs'),c=require('node:crypto');const k=c.generateKeyPairSync('ed25519');const raw=Buffer.from('{"binding_sha256":"fixture-only"}');const trust={schema_version:1,algorithm:'Ed25519',key_id:'test-only',key_origin:'local-operator',public_key_base64:Buffer.from(k.publicKey.export({format:'jwk'}).x,'base64url').toString('base64')};fs.writeFileSync(process.argv[1],JSON.stringify(trust));fs.writeFileSync(process.argv[2],JSON.stringify({schema_version:1,algorithm:'Ed25519',key_id:'test-only',payload_base64:raw.toString('base64'),signature_base64:c.sign(null,Buffer.concat([Buffer.from('hermes-lite-acceptance-v1\\n'),raw]),k.privateKey).toString('base64')}));"""
        subprocess.run([self.node, "-e", code, str(self.trust), str(self.signature)], check=True, capture_output=True)

    def check(self):
        return subprocess.run([self.node, str(ROOT / "scripts/verify_campaign_signature.mjs"), str(self.trust), str(self.signature)], capture_output=True)

    def test_real_ed25519_signature_is_accepted(self):
        result = self.check()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(b"Verified campaign signature\n", result.stdout)

    def test_payload_key_signature_and_algorithm_tampering_fail_closed(self):
        original = json.loads(self.signature.read_text())
        for field, changed in (("payload_base64", base64.b64encode(b'{"binding_sha256":"forged"}').decode()),
                               ("signature_base64", base64.b64encode(bytes(64)).decode()),
                               ("key_id", "other-signer"), ("algorithm", "none")):
            self.signature.write_text(json.dumps(dict(original, **{field: changed})), encoding="utf-8")
            with self.subTest(field=field):
                self.assertNotEqual(0, self.check().returncode)
        self.signature.write_text(json.dumps(original), encoding="utf-8")
        trust = json.loads(self.trust.read_text())
        trust["public_key_base64"] = base64.b64encode(bytes(32)).decode()
        self.trust.write_text(json.dumps(trust), encoding="utf-8")
        self.assertNotEqual(0, self.check().returncode)

    def test_signature_schema_rejects_embedded_key_duplicate_payload_and_bool(self):
        trust = verify.load_json(self.trust)
        signature = verify.load_json(self.signature)
        for changed in (dict(signature, public_key_base64=trust["public_key_base64"]), dict(signature, schema_version=True),
                        dict(signature, payload_base64=base64.b64encode(b'{"x":1,"x":2}').decode())):
            with self.assertRaises(ValueError):
                campaign_evidence.decode_signature(changed, trust, verify)

    def test_special_character_parent_path_cannot_skip_cli_verification(self):
        directory = self.root / "verifier # parent"
        directory.mkdir()
        helper = directory / "verify_campaign_signature.mjs"
        shutil.copyfile(ROOT / "scripts/verify_campaign_signature.mjs", helper)
        valid = subprocess.run([self.node, str(helper), str(self.trust), str(self.signature)], capture_output=True)
        self.assertEqual(0, valid.returncode)
        self.assertEqual(b"Verified campaign signature\n", valid.stdout)
        signature = verify.load_json(self.signature)
        signature["signature_base64"] = base64.b64encode(bytes(64)).decode()
        self.signature.write_text(json.dumps(signature), encoding="utf-8")
        rejected = subprocess.run([self.node, str(helper), str(self.trust), str(self.signature)], capture_output=True)
        self.assertNotEqual(0, rejected.returncode)
        self.assertNotEqual(b"Verified campaign signature\n", rejected.stdout)

    def test_signing_helper_has_no_sign_existing_report_cli(self):
        outcome = subprocess.run([sys.executable, str(ROOT / "scripts/campaign_signing.py"), "--help"], capture_output=True)
        self.assertNotEqual(0, outcome.returncode)
        self.assertIn(b"No signing CLI", outcome.stderr)


def layer(entries):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as archive:
        for name, content, kind in entries:
            info = tarfile.TarInfo(name)
            info.uid = info.gid = 0
            info.mode = 0o444
            if kind == "symlink":
                info.type, info.linkname = tarfile.SYMTYPE, "/etc/passwd"
            elif kind == "hardlink":
                info.type, info.linkname = tarfile.LNKTYPE, "usr/local/bin/node"
            else:
                info.size = len(content)
            archive.addfile(info, io.BytesIO(content))
    return output.getvalue()


class FakeOCI:
    def __init__(self, layers):
        self.raw = {"sha256:" + hashlib.sha256(raw).hexdigest(): raw for raw in layers}
        self.manifest = {"layers": [{"mediaType": "application/vnd.oci.image.layer.v1.tar", "size": len(raw),
                                    "digest": "sha256:" + hashlib.sha256(raw).hexdigest()} for raw in layers]}
        self.config = {"os": "linux", "architecture": "amd64", "config": {"Env": ["FIXED=true"], "Entrypoint": ["/usr/bin/tini"]},
                       "rootfs": {"type": "layers", "diff_ids": [item["digest"] for item in self.manifest["layers"]]},
                       "history": [{"created_by": "fixture layer"} for _ in layers]}

    def blob(self, digest):
        return self.raw[digest]


def write_oci(path, layers):
    fixture = FakeOCI(layers)
    blobs = dict(fixture.raw)
    config = json.dumps(fixture.config, sort_keys=True).encode()
    config_digest = "sha256:" + hashlib.sha256(config).hexdigest()
    blobs[config_digest] = config
    manifest = json.dumps({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                           "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "size": len(config), "digest": config_digest},
                           "layers": fixture.manifest["layers"]}, sort_keys=True).encode()
    manifest_digest = "sha256:" + hashlib.sha256(manifest).hexdigest()
    blobs[manifest_digest] = manifest
    with tarfile.open(path, "w") as archive:
        for digest, raw in blobs.items():
            entry = tarfile.TarInfo("blobs/sha256/" + digest[7:])
            entry.size = len(raw)
            archive.addfile(entry, io.BytesIO(raw))
    return manifest_digest, config_digest


class PromotionOCITests(unittest.TestCase):
    def test_ancestor_directory_cannot_become_inaccessible(self):
        base = layer([])
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w") as archive:
            info = tarfile.TarInfo("usr")
            info.type, info.mode, info.uid, info.gid = tarfile.DIRTYPE, 0, 0, 0
            archive.addfile(info)
        with self.assertRaisesRegex(ValueError, "ancestor mode"):
            oci.verify_ancestry(FakeOCI([base]), FakeOCI([base, raw.getvalue()]))

    def test_runtime_configuration_and_ancestry_changes_are_rejected(self):
        base = layer([])
        extra = layer([(oci.SHARE + "/release.json", b"{}", "file")])
        candidate = FakeOCI([base])
        for mutate in (lambda item: item.config["config"]["Env"].append("BACKDOOR=1"),
                       lambda item: item.config["config"].update(Entrypoint=["sh"]),
                       lambda item: item.manifest["layers"].pop(0),
                       lambda item: item.config["rootfs"]["diff_ids"].__setitem__(0, "sha256:" + "f" * 64),
                       lambda item: item.config["history"].__setitem__(0, {"created_by": "forged base"})):
            accepted = FakeOCI([base, extra])
            mutate(accepted)
            with self.assertRaises(ValueError):
                oci.verify_ancestry(candidate, accepted)

    def test_unrelated_files_whiteouts_links_and_traversal_are_rejected(self):
        base = layer([])
        candidate = FakeOCI([base])
        for name, kind in (("usr/local/bin/clawmanager-agent", "file"), (oci.SHARE + "/source-lock.json", "file"),
                           (oci.SHARE + "/evidence/.wh.campaign-signature.json", "file"),
                           (oci.SHARE + "/evidence/../../evil", "file"),
                           (oci.SHARE + "/evidence/lite_image.json", "symlink"),
                           (oci.SHARE + "/evidence/lite_image.json", "hardlink")):
            accepted = FakeOCI([base, layer([(name, b"forged", kind)])])
            with self.subTest(name=name, kind=kind), self.assertRaises(ValueError):
                oci.verify_ancestry(candidate, accepted)

    def test_exact_content_addressed_oci_graph_is_required(self):
        with tempfile.TemporaryDirectory() as temporary:
            config = b'{"rootfs":{"type":"layers","diff_ids":[]}}'
            digest = "sha256:" + hashlib.sha256(config).hexdigest()
            manifest = json.dumps({"config": {"digest": digest}, "layers": []}).encode()
            manifest_digest = "sha256:" + hashlib.sha256(manifest).hexdigest()
            for tampered in (False, True):
                path = Path(temporary) / (str(tampered) + ".tar")
                with tarfile.open(path, "w") as archive:
                    for address, raw in ((digest, config + (b" " if tampered else b"")), (manifest_digest, manifest)):
                        info = tarfile.TarInfo("blobs/sha256/" + address[7:])
                        info.size = len(raw)
                        archive.addfile(info, io.BytesIO(raw))
                if tampered:
                    with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                        oci.OCI(path, manifest_digest)
                else:
                    parsed = oci.OCI(path, manifest_digest)
                    self.assertEqual(manifest_digest, parsed.manifest_digest)
                    parsed.archive.close()


class SignedCampaignTests(unittest.TestCase):
    """Synthetic suite records with a real temporary key; never production evidence."""
    def setUp(self):
        from test_lite_supply_chain import ManagedEvidenceTests
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        inputs = self.root / "hermes/acceptance"
        inputs.mkdir(parents=True)
        shutil.copyfile(ROOT / "acceptance/acceptance-protocol.json", inputs / "acceptance-protocol.json")
        self.private = self.root / "test-only-private.pem"
        self.node = shutil.which("node")
        self.assertIsNotNone(self.node)
        code = "const fs=require('node:fs'),c=require('node:crypto');const k=c.generateKeyPairSync('ed25519');fs.writeFileSync(process.argv[1],k.privateKey.export({format:'pem',type:'pkcs8'}),{mode:0o600});fs.writeFileSync(process.argv[2],JSON.stringify({schema_version:1,algorithm:'Ed25519',key_id:'test-only',key_origin:'local-operator',public_key_base64:Buffer.from(k.publicKey.export({format:'jwk'}).x,'base64url').toString('base64')}));"
        subprocess.run([self.node, "-e", code, str(self.private), str(inputs / "release-trust.json")], check=True, capture_output=True)
        self.fixture = ManagedEvidenceTests()
        self.fixture.acceptance_inputs = inputs
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        (self.fixture.evidence / "campaign-signature.json").unlink()
        helper_path = self.root / "hermes/scripts/campaign_signing.py"
        helper_path.parent.mkdir()
        shutil.copyfile(ROOT / "scripts/campaign_signing.py", helper_path)
        helper_location = patch.object(campaign_signing, "__file__", str(helper_path))
        helper_location.start()
        self.addCleanup(helper_location.stop)

    def test_real_signing_module_binds_complete_campaign_and_five_promotion_reports(self):
        path = campaign_signing.sign_completed_campaign(self.fixture.evidence, self.fixture.release, self.private)
        self.assertTrue(path.is_file())
        checked = subprocess.run([self.node, str(ROOT / "scripts/verify_campaign_signature.mjs"),
                                  str(self.fixture.root / verify.SHARE[1:] / "release-trust.json"), str(path)], capture_output=True)
        self.assertEqual(0, checked.returncode, checked.stderr)
        accepted = self.fixture.accept()
        with patch.object(verify.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"synthetic local rerun")):
            accepted["acceptance"]["promotion_reports"] = verify.recheck_promotion(accepted, self.fixture.root, ownership=False)
        verify.verify_release(accepted, self.fixture.lock, self.fixture.root, require_accepted=True, ownership=False)
        self.assertEqual(5, len(accepted["acceptance"]["promotion_reports"]))
        with self.assertRaisesRegex(ValueError, "overwrite"):
            campaign_signing.sign_completed_campaign(self.fixture.evidence, self.fixture.release, self.private)

    def test_failed_suite_is_rejected_before_private_key_signing(self):
        path = self.fixture.report_path("cm_bff_browser")
        report = verify.load_json(path)
        report["exit_code"] = 1
        path.write_text(json.dumps(report), encoding="utf-8")
        with patch.object(campaign_signing.subprocess, "run") as signer:
            with self.assertRaisesRegex(ValueError, "did not pass"):
                campaign_signing.sign_completed_campaign(self.fixture.evidence, self.fixture.release, self.private)
            signer.assert_not_called()
        self.assertFalse((self.fixture.evidence / "campaign-signature.json").exists())

    def test_signed_oci_acceptance_layer_is_verified_end_to_end(self):
        f = self.fixture
        policy = [(oci.SHARE + "/release.json", json.dumps(f.release).encode(), "file")]
        for name in ("acceptance-protocol.json", "release-trust.json", "verify_campaign_signature.mjs"):
            policy.append((oci.SHARE + "/" + name, (f.root / oci.SHARE / name).read_bytes(), "file"))
        base = layer(policy)
        candidate_path = self.root / "candidate.oci.tar"
        candidate_digest, config_digest = write_oci(candidate_path, [base])
        binding = verify.load_json(f.evidence / "acceptance-binding.json")
        binding.update(candidate_image_digest=candidate_digest, candidate_manifest_digest=candidate_digest, candidate_config_digest=config_digest)
        (f.evidence / "acceptance-binding.json").write_text(json.dumps(binding), encoding="utf-8")
        binding_hash = verify.sha256_file(f.evidence / "acceptance-binding.json")
        for phase in ("before", "after"):
            path = f.evidence / ("campaign-" + phase + ".json")
            value = verify.load_json(path)
            value.update(binding_sha256=binding_hash, runtime_image_digest=candidate_digest)
            path.write_text(json.dumps(value), encoding="utf-8")
        browser_path = f.evidence / "cm-bff-browser-result.json"
        browser = verify.load_json(browser_path)
        browser["binding_sha256"] = binding_hash
        browser_path.write_text(json.dumps(browser), encoding="utf-8")
        for suite in verify.SUITES:
            path = f.report_path(suite)
            report = verify.load_json(path)
            report.update(candidate_image_digest=candidate_digest, binding_sha256=binding_hash)
            if suite == "cm_bff_browser":
                report["browser_result"]["sha256"] = verify.sha256_file(browser_path)
            path.write_text(json.dumps(report), encoding="utf-8")
        campaign_signing.sign_completed_campaign(f.evidence, f.release, self.private)
        accepted_release = f.accept()
        with patch.object(verify.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"synthetic local rerun")):
            accepted_release["acceptance"]["promotion_reports"] = verify.recheck_promotion(accepted_release, f.root, ownership=False)
        entries = [(oci.SHARE + "/release.json", json.dumps(accepted_release).encode(), "file")]
        for path in f.evidence.rglob("*"):
            if path.is_file():
                entries.append((oci.SHARE + "/evidence/" + path.relative_to(f.evidence).as_posix(), path.read_bytes(), "file"))
        accepted_path = self.root / "accepted-fixture.oci.tar"
        accepted_digest, _ = write_oci(accepted_path, [base, layer(entries)])
        candidate, accepted = oci.OCI(candidate_path, candidate_digest), oci.OCI(accepted_path, accepted_digest)
        try:
            checked = oci.verify_ancestry(candidate, accepted)
            self.assertEqual("passed", checked["status"])
            self.assertEqual(1, checked["added_layers"])
            self.assertEqual(f.release["payload_sha256"], checked["payload_sha256"])
        finally:
            candidate.archive.close()
            accepted.archive.close()
        for change in ("payload", "extra-report"):
            broken = copy.deepcopy(accepted_release)
            if change == "payload":
                broken["acceptance"]["payload_sha256"] = "f" * 64
            else:
                broken["acceptance"]["reports"]["unexpected"] = broken["acceptance"]["reports"]["lite_image"]
            entries[0] = (oci.SHARE + "/release.json", json.dumps(broken).encode(), "file")
            bad_path = self.root / (change + ".oci.tar")
            bad_digest, _ = write_oci(bad_path, [base, layer(entries)])
            candidate, bad = oci.OCI(candidate_path, candidate_digest), oci.OCI(bad_path, bad_digest)
            try:
                with self.subTest(change=change), self.assertRaises(ValueError):
                    oci.verify_ancestry(candidate, bad)
            finally:
                candidate.archive.close()
                bad.archive.close()


if __name__ == "__main__":
    unittest.main()
