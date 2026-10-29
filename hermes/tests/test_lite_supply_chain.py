import asyncio
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
import sys
import subprocess
import tarfile
import base64
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    loaded = importlib.util.module_from_spec(spec)
    sys.modules[name] = loaded
    spec.loader.exec_module(loaded)
    return loaded


verify = module("verify_lite_release", ROOT / "scripts/verify_lite_release.py")
boundary = module("lite_gateway_boundary", ROOT / "patches/hermes-agent/lite_gateway_boundary.py")
runner = module("run_release_check", ROOT / "scripts/run_release_check.py")


class ReleaseTests(unittest.TestCase):
    def test_checked_in_lock_is_valid(self):
        lock = verify.load_lock(ROOT / "hermes-lite.lock.json")
        self.assertFalse(lock["desktop_web_accepted"])

    def test_mutable_ref_or_archive_is_rejected(self):
        source = json.loads((ROOT / "hermes-lite.lock.json").read_text())
        for field, value in [("tag", "main"), ("commit", "29112bef"), ("archive_url", "https://example.org/source")]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as temporary:
                lock = dict(source, **{field: value})
                path = Path(temporary) / "lock.json"
                path.write_text(json.dumps(lock))
                with self.assertRaises(ValueError):
                    verify.load_lock(path)

    def test_retargeted_upstream_tag_is_rejected(self):
        lock = verify.load_lock(ROOT / "hermes-lite.lock.json")
        output = f'{lock["tag_object"]}\trefs/tags/{lock["tag"]}\n' + "0" * 40 + f'\trefs/tags/{lock["tag"]}^{{}}\n'
        with patch.object(verify.subprocess, "check_output", return_value=output), self.assertRaises(ValueError):
            verify.verify_upstream(lock)

    def test_modified_source_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "pyproject.toml"
            path.write_text("modified")
            lock = verify.load_lock(ROOT / "hermes-lite.lock.json")
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                verify.verify_source(lock, temporary)

    def test_runtime_version_mismatch_is_rejected(self):
        lock = verify.load_lock(ROOT / "hermes-lite.lock.json")
        with patch.object(verify.subprocess, "check_output", return_value="Hermes v0.16.0"), self.assertRaises(ValueError):
            verify.verify_runtime(lock, "hermes", "node")

    def test_reviewed_patch_and_test_inputs_cannot_be_omitted_or_changed(self):
        lock = verify.load_lock(ROOT / "hermes-lite.lock.json")
        for key in ("patch_sha256", "test_sha256"):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as temporary:
                broken = copy.deepcopy(lock)
                broken[key].pop(next(iter(broken[key])))
                path = Path(temporary) / "lock.json"
                path.write_text(json.dumps(broken), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "incomplete reviewed"):
                    verify.load_lock(path)
                name = next(iter(lock[key]))
                (Path(temporary) / name).write_text("unreviewed mutation")
                with self.assertRaisesRegex(ValueError, "local input checksum mismatch"):
                    verify.verify_local_inputs(lock, temporary, key)


class ManagedEvidenceTests(unittest.TestCase):
    """Synthetic fixtures exercise validation only; they are never release evidence."""
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.lock = verify.load_lock(ROOT / "hermes-lite.lock.json")
        for tree in verify.ARTIFACT_TREES:
            (self.root / tree[1:]).mkdir(parents=True, exist_ok=True)
        for name in verify.MANDATORY:
            path = self.root / name[1:]
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("synthetic fixture only: " + name, encoding="utf-8")
        for name in ("acceptance-protocol.json", "release-trust.json"):
            (self.root / verify.SHARE[1:] / name).write_bytes((getattr(self, "acceptance_inputs", ROOT / "acceptance") / name).read_bytes())
        for name in ("verify_lite_release.py", "campaign_evidence.py", "campaign_signing.py", "verify_campaign_signature.mjs"):
            (self.root / verify.SHARE[1:] / name).write_bytes((ROOT / "scripts" / name).read_bytes())
        self.release = verify.generate_release(self.lock, self.root, ownership=False)
        self.candidate = "sha256:" + "a" * 64
        self.evidence = self.root / verify.SHARE[1:] / "evidence"
        self.evidence.mkdir()
        for filename in ("cm-provenance.json", "renderer-asset-manifest.json"):
            (self.evidence / filename).write_text('{"synthetic":"schema fixture only"}', encoding="utf-8")
        binding = {"schema_version": 1, "protocol": "hermes-lite-campaign-v1", "scope": "real-cm-bff-browser",
                   "run_id": "1" * 32, "started_at": "2026-01-01T00:00:00Z"}
        binding.update({key: "sha256:" + "a" * 64 for key in verify.campaign_evidence.DIGEST_FIELDS})
        binding.update({key: "a" * 64 for key in verify.campaign_evidence.HASH_FIELDS})
        binding.update({key: "1.0.0" for key in verify.campaign_evidence.VERSION_FIELDS})
        binding.update(payload_sha256=self.release["payload_sha256"],
                       harness_sha256=self.release["artifacts"][verify.SHARE + "/tests/cm_bff_browser.mjs"],
                       campaign_runner_sha256=self.release["artifacts"][verify.SHARE + "/run_campaign.py"],
                       cm_provenance_sha256=verify.sha256_file(self.evidence / "cm-provenance.json"),
                       renderer_asset_manifest_sha256=verify.sha256_file(self.evidence / "renderer-asset-manifest.json"))
        import shutil
        binding["node_sha256"] = verify.sha256_file(shutil.which("node"))
        self.binding = binding
        (self.evidence / "acceptance-binding.json").write_text(json.dumps(binding), encoding="utf-8")
        self.binding_hash = verify.sha256_file(self.evidence / "acceptance-binding.json")
        for phase, when in (("before", "2026-01-01T00:00:00Z"), ("after", "2026-01-01T00:00:02Z")):
            observation = {"schema_version": 1, "phase": phase, "status": "passed", "run_id": binding["run_id"],
                           "binding_sha256": self.binding_hash, "runtime_image_digest": binding["candidate_manifest_digest"],
                           "runtime_payload_sha256": binding["payload_sha256"], "cm_image_digest": binding["cm_image_digest"],
                           "renderer_build_input_sha256": binding["renderer_build_input_sha256"],
                           "renderer_asset_manifest_sha256": binding["renderer_asset_manifest_sha256"], "recorded_at": when,
                           "checks": {name: "passed" for name in verify.campaign_evidence.OBSERVATION_CHECKS}}
            (self.evidence / ("campaign-" + phase + ".json")).write_text(json.dumps(observation), encoding="utf-8")
        artifact = self.evidence / "browser-artifacts/surface-summary.json"
        artifact.parent.mkdir()
        artifact.write_text('{"synthetic":true}', encoding="utf-8")
        browser = {"schema_version": 1, "status": "passed", "run_id": binding["run_id"], "binding_sha256": self.binding_hash,
                   "cases": {name: "passed" for name in verify.campaign_evidence.BROWSER_CHECKS},
                   "artifacts": {"browser-artifacts/surface-summary.json": {"sha256": verify.sha256_file(artifact), "size": artifact.stat().st_size}}}
        (self.evidence / "cm-bff-browser-result.json").write_text(json.dumps(browser), encoding="utf-8")
        for suite, script in verify.SUITES.items():
            script_path = verify.SHARE + "/tests/" + script
            output_path = verify.SHARE + "/evidence/" + suite + ".log"
            output = self.root / output_path[1:]
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text("synthetic validation fixture; not an executed test", encoding="utf-8")
            self.write_report(suite, {
                "schema_version": 2, "phase": "campaign", "binding_sha256": self.binding_hash,
                "suite": suite, "status": "passed", "exit_code": 0,
                "candidate_image_digest": self.candidate, "payload_sha256": self.release["payload_sha256"],
                "script_path": script_path, "script_sha256": self.release["artifacts"][script_path],
                "runner_sha256": self.release["artifacts"][verify.SHARE + "/run_release_check.py"],
                "output_path": output_path, "output_sha256": verify.sha256_file(output),
                "started_at": "2026-01-01T00:00:00Z", "finished_at": "2026-01-01T00:00:01Z",
            })
        cm = verify.load_json(self.report_path("cm_bff_browser"))
        cm["browser_result"] = {"path": verify.SHARE + "/evidence/cm-bff-browser-result.json", "sha256": verify.sha256_file(self.evidence / "cm-bff-browser-result.json")}
        self.write_report("cm_bff_browser", cm)
        statement = {"binding_sha256": self.binding_hash,
                     "reports": {suite: {"sha256": verify.sha256_file(self.report_path(suite)), "output_sha256": verify.load_json(self.report_path(suite))["output_sha256"]} for suite in verify.SUITES},
                     "observations": {phase + "_sha256": verify.sha256_file(self.evidence / ("campaign-" + phase + ".json")) for phase in ("before", "after")}}
        signature = {"schema_version": 1, "algorithm": "Ed25519", "key_id": verify.load_json(self.root / verify.SHARE[1:] / "release-trust.json")["key_id"],
                     "payload_base64": base64.b64encode(json.dumps(statement).encode()).decode(), "signature_base64": base64.b64encode(bytes(64)).decode()}
        (self.evidence / "campaign-signature.json").write_text(json.dumps(signature), encoding="utf-8")
        # These are explicitly synthetic schema fixtures. Real Ed25519 success
        # and tampering tests run separately against Node's actual verifier.
        signature_check = patch.object(verify.campaign_evidence, "verify_signature")
        signature_check.start()
        self.addCleanup(signature_check.stop)

    def report_path(self, suite):
        return self.root / (verify.SHARE + "/evidence/" + suite + ".json")[1:]

    def write_report(self, suite, report):
        self.report_path(suite).write_text(json.dumps(report), encoding="utf-8")

    def accept(self, release=None):
        return verify.accept_release(release or self.release, self.lock, self.root, ownership=False)

    def validate(self, release, accepted=True):
        verify.verify_release(release, self.lock, self.root, require_accepted=accepted, ownership=False, require_promotion=False)

    def test_candidate_stays_unaccepted_despite_legacy_lock_boolean(self):
        self.lock["desktop_web_accepted"] = True
        candidate = verify.generate_release(self.lock, self.root, ownership=False)
        self.assertFalse(candidate["desktop_web_accepted"])
        self.assertNotIn("acceptance", candidate)
        self.validate(candidate, accepted=False)
        with self.assertRaisesRegex(ValueError, "not accepted"):
            self.validate(candidate)

    def test_complete_synthetic_evidence_passes_validator(self):
        accepted = self.accept()
        self.validate(accepted)
        self.assertEqual(set(verify.SUITES), set(accepted["acceptance"]["reports"]))
        self.assertEqual(self.release["payload_sha256"], accepted["payload_sha256"])

    def test_promotion_executes_every_fixed_suite_and_stops_on_failure(self):
        accepted = self.accept()
        # The records supplied above all claim success. Real execution status,
        # not those claims, still controls the separate promotion gate.
        local = [suite for suite in verify.SUITES if suite != "cm_bff_browser"]
        outcomes = [subprocess.CompletedProcess([], 0, b"fixture recheck") for _ in local]
        outcomes[-1] = subprocess.CompletedProcess([], 1, b"local suite failed")
        with patch.object(verify.subprocess, "run", side_effect=outcomes) as execute:
            with self.assertRaisesRegex(ValueError, "promotion suite failed: desktop_rpc"):
                verify.recheck_promotion(accepted, self.root, ownership=False)
            self.assertEqual(len(local), execute.call_count)
            for call, script in zip(execute.call_args_list, [verify.SUITES[suite] for suite in local]):
                self.assertEqual(str((self.root / (verify.SHARE + "/tests/" + script)[1:]).resolve()), call.args[0][1])
        failure = verify.load_json(self.root / (verify.SHARE + "/evidence/promotion-desktop_rpc.json")[1:])
        self.assertEqual("failed", failure["status"])
        self.assertEqual(1, failure["exit_code"])
        self.assertEqual("passed", verify.load_json(self.report_path("cm_bff_browser"))["status"])

    def test_all_successful_promotion_executions_preserve_original_reports(self):
        accepted = self.accept()
        originals = {suite: self.report_path(suite).read_bytes() for suite in verify.SUITES}
        with patch.object(verify.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"nondeterministic fixture output")):
            records = verify.recheck_promotion(accepted, self.root, ownership=False)
        self.assertEqual(set(verify.SUITES) - {"cm_bff_browser"}, set(records))
        accepted["acceptance"]["promotion_reports"] = records
        verify.verify_release(accepted, self.lock, self.root, require_accepted=True, ownership=False)
        for suite in verify.SUITES:
            self.assertEqual(originals[suite], self.report_path(suite).read_bytes())

    def test_promotion_reports_are_required_and_fully_checked(self):
        accepted = self.accept()
        with self.assertRaisesRegex(ValueError, "promotion reports"):
            verify.verify_release(accepted, self.lock, self.root, require_accepted=True, ownership=False)
        with patch.object(verify.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"fixture rerun")):
            accepted["acceptance"]["promotion_reports"] = verify.recheck_promotion(accepted, self.root, ownership=False)
        for suite in accepted["acceptance"]["promotion_reports"]:
            descriptor = accepted["acceptance"]["promotion_reports"][suite]
            path = self.root / descriptor["path"][1:]
            original = path.read_bytes()
            value = verify.load_json(path)
            path.chmod(0o600)
            for field in ("phase", "binding_sha256", "runner_sha256", "output_path", "exit_code"):
                broken = dict(value)
                del broken[field]
                path.write_text(json.dumps(broken), encoding="utf-8")
                descriptor["sha256"] = verify.sha256_file(path)
                with self.subTest(suite=suite, field=field), self.assertRaises(ValueError):
                    verify.verify_release(accepted, self.lock, self.root, require_accepted=True, ownership=False)
            path.write_bytes(original)
            descriptor["sha256"] = verify.sha256_file(path)

    def test_flipping_only_accepted_boolean_is_rejected(self):
        release = dict(self.release, desktop_web_accepted=True)
        with self.assertRaises(ValueError):
            self.validate(release)

    def test_every_required_suite_and_execution_field_is_mandatory(self):
        for suite in verify.SUITES:
            original = verify.load_json(self.report_path(suite))
            for field in original:
                with self.subTest(suite=suite, missing=field):
                    changed = dict(original)
                    del changed[field]
                    self.write_report(suite, changed)
                    with self.assertRaises(ValueError):
                        self.accept()
            self.write_report(suite, original)
            accepted = self.accept()
            del accepted["acceptance"]["reports"][suite]
            with self.assertRaises(ValueError):
                self.validate(accepted)

    def test_every_required_script_and_artifact_is_mandatory(self):
        for path in verify.MANDATORY:
            with self.subTest(path=path):
                changed = copy.deepcopy(self.release)
                del changed["artifacts"][path]
                changed["payload_sha256"] = verify.payload_hash(changed["artifacts"])
                with self.assertRaises(ValueError):
                    self.accept(changed)

    def test_actual_payload_or_output_tampering_rejected(self):
        accepted = self.accept()
        paths = [self.root / verify.MANDATORY[0][1:], self.root / (verify.SHARE + "/evidence/desktop_rpc.log")[1:]]
        for path in paths:
            original = path.read_bytes()
            path.write_bytes(original + b"tampered")
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "checksum mismatch"):
                self.validate(accepted)
            path.write_bytes(original)

    def test_wrong_candidate_or_failed_execution_rejected(self):
        original = verify.load_json(self.report_path("desktop_rpc"))
        mutations = {"candidate_image_digest": "sha256:" + "b" * 64,
                     "payload_sha256": "c" * 64, "script_sha256": "d" * 64,
                     "runner_sha256": "e" * 64, "status": "failed", "exit_code": 1,
                     "output_path": "/tmp/foreign.log", "finished_at": "2025-01-01T00:00:00Z"}
        for key, value in mutations.items():
            with self.subTest(key=key):
                self.write_report("desktop_rpc", dict(original, **{key: value}))
                with self.assertRaises(ValueError):
                    self.accept()
        self.write_report("desktop_rpc", original)

    def test_user_paths_malformed_hashes_and_duplicate_json_rejected(self):
        for path, value in [("/workspaces/forged.py", "a" * 64), ("/opt/hermes-agent/../forged.py", "a" * 64),
                            ("/opt/hermes-agent/forged.py", None)]:
            changed = copy.deepcopy(self.release)
            changed["artifacts"][path] = value
            if isinstance(value, str):
                changed["payload_sha256"] = verify.payload_hash(changed["artifacts"])
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.validate(changed, accepted=False)
        path = self.root / "duplicate.json"
        path.write_text('{"desktop_web_accepted":false,"desktop_web_accepted":true}')
        with self.assertRaisesRegex(ValueError, "duplicate"):
            verify.load_json(path)

    @unittest.skipIf(os.name == "nt", "POSIX ownership and symlink checks run in Linux CI")
    def test_writable_or_symlink_managed_files_rejected(self):
        path = self.root / verify.MANDATORY[0][1:]
        path.chmod(0o666)
        with self.assertRaisesRegex(ValueError, "ownership or permissions"):
            verify.managed_path(self.root, verify.MANDATORY[0])
        path.unlink()
        path.symlink_to("/etc/passwd")
        with self.assertRaisesRegex(ValueError, "symlink"):
            verify.managed_path(self.root, verify.MANDATORY[0], ownership=False)


class ExecutionRunnerTests(unittest.TestCase):
    def test_forged_metadata_config_cannot_relabel_another_image(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            metadata = root / "metadata.json"
            candidate, wrong_config = "sha256:" + "a" * 64, "sha256:" + "b" * 64
            metadata.write_text(json.dumps({"containerimage.digest": candidate, "containerimage.config.digest": wrong_config}))
            argv = ["run_release_check.py", "--image", "mutable-tag", "--metadata", str(metadata), "--oci", str(root / "candidate.oci.tar"),
                    "--suite", "lite_image", "--output", str(root / "lite_image.json")]
            with patch.object(sys, "argv", argv), patch.object(runner.subprocess, "check_output", return_value=wrong_config) as inspect, \
                 patch.object(runner, "candidate_config", return_value="sha256:" + "c" * 64), \
                 self.assertRaisesRegex(SystemExit, "does not match"):
                runner.main()
            self.assertEqual(1, inspect.call_count)
            self.assertFalse((root / "lite_image.json").exists())

    def test_classic_docker_config_is_resolved_from_verified_oci_graph(self):
        import hashlib
        config = "sha256:" + "c" * 64
        manifest = json.dumps({"config": {"digest": config}}).encode()
        manifest_digest = "sha256:" + hashlib.sha256(manifest).hexdigest()
        index = json.dumps({"manifests": [{"digest": manifest_digest, "platform": {"os": "linux", "architecture": "amd64"}}]}).encode()
        candidate = "sha256:" + hashlib.sha256(index).hexdigest()
        with tempfile.TemporaryDirectory() as temporary:
            archive_path = Path(temporary) / "candidate.oci.tar"
            for tamper in (False, True):
                with tarfile.open(archive_path, "w") as archive:
                    for digest, raw in ((candidate, index), (manifest_digest, manifest + (b"tamper" if tamper else b""))):
                        info = tarfile.TarInfo("blobs/sha256/" + digest[7:])
                        info.size = len(raw)
                        archive.addfile(info, io.BytesIO(raw))
                if tamper:
                    with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                        runner.candidate_config(archive_path, candidate)
                else:
                    self.assertEqual(config, runner.candidate_config(archive_path, candidate))

    def test_actual_exit_code_controls_report_and_output_is_hashed(self):
        for exit_code in (0, 7):
            with self.subTest(exit_code=exit_code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                metadata = root / "metadata.json"
                candidate = "sha256:" + "a" * 64
                metadata.write_text(json.dumps({"containerimage.digest": candidate}))
                script_path = runner.SHARE + "/tests/smoke_lite_image.py"
                release = {"payload_sha256": "b" * 64, "artifacts": {
                    runner.SHARE + "/run_release_check.py": verify.sha256_file(ROOT / "scripts/run_release_check.py"),
                    runner.SHARE + "/verify_lite_release.py": verify.sha256_file(ROOT / "scripts/verify_lite_release.py"),
                    runner.SHARE + "/campaign_evidence.py": verify.sha256_file(ROOT / "scripts/campaign_evidence.py"),
                    script_path: "c" * 64}}
                output = root / "lite_image.json"
                argv = ["run_release_check.py", "--image", "fixture-only", "--metadata", str(metadata),
                        "--oci", str(root / "fixture.oci.tar"), "--suite", "lite_image", "--output", str(output)]
                with patch.object(sys, "argv", argv), patch.object(runner.subprocess, "check_output", side_effect=[candidate, json.dumps(release).encode()]) as inspect, \
                     patch.object(runner, "candidate_config", return_value="sha256:" + "d" * 64), \
                     patch.object(runner.subprocess, "run", side_effect=[subprocess.CompletedProcess([], exit_code, b"fixture subprocess output"), subprocess.CompletedProcess([], 0)]) as run:
                    self.assertEqual(0 if exit_code == 0 else 1, runner.main())
                    self.assertIn(script_path, run.call_args_list[0].args[0])
                    self.assertIn("none", run.call_args_list[0].args[0])
                    self.assertIn(candidate, run.call_args_list[0].args[0])
                    self.assertNotIn("fixture-only", run.call_args_list[0].args[0])
                    self.assertIn(candidate, inspect.call_args_list[1].args[0])
                    self.assertNotIn("fixture-only", inspect.call_args_list[1].args[0])
                report = verify.load_json(output)
                self.assertEqual(exit_code, report["exit_code"])
                self.assertEqual("passed" if exit_code == 0 else "failed", report["status"])
                self.assertEqual(verify.sha256_file(output.with_suffix(".log")), report["output_sha256"])
                with patch.object(sys, "argv", argv), self.assertRaisesRegex(SystemExit, "overwrite"):
                    runner.main()


class BoundaryTests(unittest.TestCase):
    def test_managed_lease_rotation_expiry_and_namespace_fail_closed(self):
        with tempfile.TemporaryDirectory() as root:
            file = Path(root) / "policy.json"
            with patch.dict(os.environ, {"CLAWMANAGER_PROXY_POLICY_FILE": str(file), "CLAWMANAGER_PROXY_POLICY_NAMESPACE": "tenant"}):
                guard = boundary.LiteGatewayBoundary(None)
                def publish(addresses, namespace="tenant", expires=160000):
                    file.write_text(json.dumps({"contract": 1, "namespace": namespace, "proxies": addresses, "expires_at_ms": expires}))
                with patch.object(boundary.time, "time", return_value=100):
                    self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope()))
                    publish(["10.23.4.8"])
                    self.assertIsNone(guard.denial(self.scope()))
                    publish(["10.23.4.9"])
                    self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope()))
                    self.assertIsNone(guard.denial(self.scope(peer="10.23.4.9")))
                    for addresses, namespace, expires in [(["10.23.4.8"], "other", 160000), (["10.23.4.8"], "tenant", 100000), (["10.23.4.0/24"], "tenant", 160000), (["10.23.4.8", 1], "tenant", 160000)]:
                        publish(addresses, namespace, expires)
                        self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope()))
                    file.write_text("{broken")
                    self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope()))
                    self.assertIsNone(guard.denial(self.scope(peer="127.0.0.1", origin=None, method="GET")))
                    publish(["10.23.4.8"])
                    self.assertIsNone(guard.denial(self.scope()))

    def setUp(self):
        self.env = patch.dict(os.environ, {
            "CLAWMANAGER_CONTROL_UI_ORIGIN": "https://manager.example",
            "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "10.23.4.0/24",
        })
        self.env.start()
        self.addCleanup(self.env.stop)
        self.guard = boundary.LiteGatewayBoundary(None)

    def scope(self, peer="10.23.4.8", origin="https://manager.example", kind="http", method="POST", extra=()):
        headers = [(b"origin", origin.encode())] if origin is not None else []
        return {"type": kind, "method": method, "client": (peer, 1234), "headers": headers + list(extra)}

    def test_private_relay_requires_valid_policy_and_isolated_mode(self):
        forwarded = [(b"x-forwarded-host", b"manager.example"), (b"x-forwarded-proto", b"https")]
        with tempfile.TemporaryDirectory() as temporary:
            file = Path(temporary) / "policy.json"
            env = {"CLAWMANAGER_PROXY_POLICY_FILE": str(file), "CLAWMANAGER_PROXY_POLICY_NAMESPACE": "tenant", "CLAWMANAGER_NETWORK_ISOLATED": "1"}
            with patch.dict(os.environ, env), patch.object(boundary.time, "time", return_value=100):
                guard = boundary.LiteGatewayBoundary(None)
                def publish(expires=160000, namespace="tenant"):
                    file.write_text(json.dumps({"contract":1, "namespace":namespace, "proxies":["10.23.4.8"], "expires_at_ms":expires}))
                publish()
                for peer in ("127.0.0.1", "::1"):
                    self.assertIsNone(guard.denial(self.scope(peer=peer, extra=forwarded)))
                self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope(extra=forwarded)))
                publish(expires=100000)
                self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope(peer="127.0.0.1", extra=forwarded)))
                publish(namespace="other")
                self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope(peer="127.0.0.1", extra=forwarded)))
                file.write_text("{broken")
                self.assertEqual("untrusted_gateway_peer", guard.denial(self.scope(peer="127.0.0.1", extra=forwarded)))
                publish()
                with patch.dict(os.environ, {"CLAWMANAGER_NETWORK_ISOLATED":"0"}):
                    legacy=boundary.LiteGatewayBoundary(None)
                    self.assertEqual("untrusted_gateway_peer", legacy.denial(self.scope(peer="127.0.0.1", extra=forwarded)))
                    self.assertIsNone(legacy.denial(self.scope(extra=forwarded)))
        with patch.dict(os.environ, {"CLAWMANAGER_NETWORK_ISOLATED":"1", "CLAWMANAGER_PROXY_POLICY_FILE":""}), self.assertRaises(RuntimeError):
            boundary.LiteGatewayBoundary(None)

    def test_trusted_proxy_and_local_probe_are_accepted(self):
        self.assertIsNone(self.guard.denial(self.scope()))
        self.assertIsNone(self.guard.denial(self.scope(peer="127.0.0.1")))
        self.assertIsNone(self.guard.denial(self.scope(origin="https://manager.example:443", kind="websocket")))

    def test_cross_origin_and_scheme_port_changes_are_rejected(self):
        for origin in ["https://evil.example", "http://manager.example", "https://manager.example:444", "https://manager.example:0", "null", "https://manager.example@evil.example", "https://manager.example/path", "\x00https://manager.example", "https://@manager.example"]:
            self.assertEqual("websocket_origin_rejected", self.guard.denial(self.scope(origin=origin, kind="websocket")))

    def test_missing_or_duplicate_origin_is_rejected(self):
        self.assertEqual("origin_required", self.guard.denial(self.scope(origin=None)))
        self.assertEqual("origin_required", self.guard.denial(self.scope(origin=None, kind="websocket")))
        self.assertEqual("origin_rejected", self.guard.denial(self.scope(extra=[(b"origin", b"https://evil.example")])))
        self.assertIsNone(self.guard.denial(self.scope(origin=None, method="GET")))

    def test_spoofed_forwarder_cannot_change_socket_peer(self):
        extra = [(b"x-forwarded-for", b"10.23.4.8")]
        self.assertEqual("untrusted_gateway_peer", self.guard.denial(self.scope(peer="10.99.8.2", extra=extra)))
        self.assertEqual("untrusted_gateway_peer", self.guard.denial(self.scope(peer="127.0.0.1", extra=extra)))
        self.assertIsNone(self.guard.denial(self.scope(extra=extra)))

    def test_boundary_configuration_fails_closed(self):
        for cidr in ["", "*", "0.0.0.0/0", "::/0"]:
            with patch.dict(os.environ, {"CLAWMANAGER_TRUSTED_PROXY_CIDRS": cidr}), self.assertRaises(RuntimeError):
                boundary.LiteGatewayBoundary(None)
        with patch.dict(os.environ, {"CLAWMANAGER_CONTROL_UI_ORIGIN": "https://*.example"}), self.assertRaises(RuntimeError):
            boundary.LiteGatewayBoundary(None)

    def test_native_desktop_apis_are_rejected(self):
        for path in ["/api/console", "/api/ssh/ownership", "/api/cloud/connect", "/api/hermes/update"]:
            scope = self.scope()
            scope["path"] = path
            self.assertEqual("unsupported_native_desktop_api", self.guard.denial(scope))

    def test_chat_accepts_only_known_parameters_and_current_profile(self):
        scope = dict(self.scope(), path="/api/pty", query_string=b"channel=chat-1&profile=current")
        self.assertIsNone(self.guard.denial(scope))
        for query in [b"command=bash", b"profile=other", b"shell=1"]:
            scope["query_string"] = query
            self.assertEqual("unsupported_chat_parameters", self.guard.denial(scope))

    def test_url_credentials_are_rejected(self):
        scope = dict(self.scope(), query_string=b"internal=test-secret")
        self.assertEqual("credential_query_rejected", self.guard.denial(scope))

    def test_internal_protocol_requires_verified_loopback_credential(self):
        headers = [(b"sec-websocket-protocol", b"hermes-gateway-v1,hermes-internal.test")]
        scope = self.scope(peer="127.0.0.1", origin=None, kind="websocket", extra=headers)
        with patch.object(boundary, "verified_internal_peer", return_value=False):
            self.assertEqual("invalid_internal_websocket_peer", self.guard.denial(scope))
        with patch.object(boundary, "verified_internal_peer", return_value=True):
            self.assertIsNone(self.guard.denial(scope))
            self.assertTrue(scope["hermes_lite_internal"])
            self.assertIn((b"origin", b"https://manager.example"), scope["headers"])

    def test_denial_does_not_call_auth_or_leak_header_values(self):
        async def inner(*args):
            self.fail("denied request reached Hermes")
        guard = boundary.LiteGatewayBoundary(inner)
        events = []
        async def send(event):
            events.append(event)
        scope = self.scope(origin="https://evil.example", extra=[(b"cookie", b"secret-cookie")])
        asyncio.run(guard(scope, None, send))
        self.assertEqual(403, events[0]["status"])
        self.assertNotIn("secret-cookie", str(events))


if __name__ == "__main__":
    unittest.main()
