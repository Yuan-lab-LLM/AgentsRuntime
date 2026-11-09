"""Synthetic campaign gates: no cluster, browser, image, or release is changed."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import run_campaign as campaign

RUN_ID = "ab" * 16
NAMESPACE = "hermes-acceptance-unit"


class FakeCluster:
    namespace = NAMESPACE

    def __init__(self):
        self.resources = {
            "namespace": {"metadata": {"labels": {"agentsruntime.io/campaign": RUN_ID}}},
            "pods": {"items": [
                {"metadata": {"uid": "runtime-uid", "labels": {"role": "runtime"}},
                 "spec": {"containers": [{"name": "hermes"}], "volumes": []}},
                {"metadata": {"uid": "stub-uid", "labels": {"app": "label-differs-from-deployment", "role": "stub"}},
                 "spec": {"containers": [{"name": "stub"}], "volumes": []}},
            ]},
            "networkpolicies": {"items": [{"spec": {"podSelector": {}, "policyTypes": ["Egress"], "egress": []}}]},
            "services": {"items": [{"spec": {"type": "ClusterIP", "selector": {"role": "runtime"}}}]},
        }

    def get(self, resource, _name=None):
        return self.resources[resource]

    def add_egress(self, role, rule):
        self.resources["networkpolicies"]["items"].append({"spec": {
            "podSelector": {"matchLabels": {"role": role}}, "policyTypes": ["Egress"], "egress": [rule]}})


class IsolationTests(unittest.TestCase):
    def check(self, cluster):
        return campaign.isolation(cluster, RUN_ID, {"stub-uid"})

    def test_scoped_namespace_with_default_deny_is_valid(self):
        self.assertRegex(self.check(FakeCluster()), r"^[a-f0-9]{64}$")

    def test_same_namespace_and_exact_dns_peers_are_valid(self):
        cluster = FakeCluster()
        cluster.add_egress("runtime", {"to": [{"podSelector": {"matchLabels": {"role": "stub"}}}]})
        cluster.add_egress("runtime", {"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}},
                                                "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}}}],
                                           "ports": [{"port": 53, "protocol": "UDP"}, {"port": 53, "protocol": "TCP"}]})
        self.check(cluster)

    def test_namespace_and_campaign_label_are_both_required(self):
        for namespace, label in (("clawmanager-system", RUN_ID), (NAMESPACE, "cd" * 16)):
            cluster = FakeCluster()
            cluster.namespace = namespace
            cluster.resources["namespace"]["metadata"]["labels"]["agentsruntime.io/campaign"] = label
            with self.subTest(namespace=namespace, label=label), self.assertRaises(campaign.GateError):
                self.check(cluster)

    def test_additive_policy_cannot_override_default_deny(self):
        for rule in ({}, {"to": [{}]}, {"to": [{"ipBlock": {"cidr": "0.0.0.0/0"}}]},
                     {"to": [{"podSelector": {}, "namespaceSelector": {}}]},
                     {"to": [{"podSelector": {}, "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "clawmanager-system"}}}]}):
            cluster = FakeCluster()
            cluster.add_egress("runtime", rule)
            with self.subTest(rule=rule), self.assertRaises(campaign.GateError):
                self.check(cluster)

    def test_stub_zero_egress_uses_observed_uid_not_app_label(self):
        cluster = FakeCluster()
        cluster.add_egress("stub", {"to": [{"podSelector": {}}]})
        with self.assertRaisesRegex(campaign.GateError, "model_stub_must_have_zero_egress"):
            self.check(cluster)
        with self.assertRaisesRegex(campaign.GateError, "observed_stub_pods_required"):
            campaign.isolation(FakeCluster(), RUN_ID, {"unknown-uid"})

    def test_privileged_ephemeral_container_and_shared_volume_are_rejected(self):
        for change in (
            {"ephemeralContainers": [{"securityContext": {"privileged": True}}]},
            {"ephemeralContainers": [{"securityContext": {"capabilities": {"add": ["NET_ADMIN"]}}}]},
            {"volumes": [{"name": "disk", "iscsi": {"targetPortal": "fixture.invalid"}}]},
            {"volumes": [{"name": "disk", "ephemeral": {"volumeClaimTemplate": {}}}]},
            {"volumes": [{"name": "disk", "persistentVolumeClaim": {"claimName": "production"}}]},
        ):
            cluster = FakeCluster()
            cluster.resources["pods"]["items"][0]["spec"].update(change)
            with self.subTest(change=change), self.assertRaises(campaign.GateError):
                self.check(cluster)

    def test_runtime_entrypoint_and_payload_mounts_are_fixed(self):
        identity = {"container": "hermes", "replicas": [{"uid": "runtime-uid"}]}
        cluster = FakeCluster()
        spec = cluster.resources["pods"]["items"][0]["spec"]
        spec["volumes"] = [{"name": "workspace", "emptyDir": {}}, {"name": "api", "projected": {"sources": [{"serviceAccountToken": {"path": "token"}}]}}]
        base = {"name": "hermes", "volumeMounts": [
            {"name": "workspace", "mountPath": "/workspaces"},
            {"name": "api", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": True}]}
        spec["containers"] = [copy.deepcopy(base)]
        campaign.runtime_launch_boundaries(cluster, identity)
        for field, value in (("command", ["python"]), ("args", ["fake.py"]),
                             ("volumeDevices", [{"name": "workspace", "devicePath": "/dev/fake"}]),
                             ("volumeMounts", [{"name": "workspace", "mountPath": "/opt/hermes-agent"}]),
                             ("volumeMounts", [{"name": "workspace", "mountPath": "/workspaces", "subPath": "override"}])):
            spec["containers"] = [dict(copy.deepcopy(base), **{field: value})]
            with self.subTest(field=field, value=value), self.assertRaises(campaign.GateError):
                campaign.runtime_launch_boundaries(cluster, identity)

    def test_stub_uses_exact_candidate_observer_and_readonly_secret(self):
        cluster = FakeCluster()
        identity = {"container": "stub", "replicas": [{"uid": "stub-uid"}]}
        spec = cluster.resources["pods"]["items"][1]["spec"]
        spec["volumes"] = [{"name": "model", "secret": {"secretName": "test-model"}}]
        base = {"name": "stub", "command": ["python", "-I", "-B", campaign.SHARE + "/tests/acceptance_model_stub.py"],
                "args": ["--config", "/run/hermes-acceptance/model.json"],
                "volumeMounts": [{"name": "model", "mountPath": "/run/hermes-acceptance", "readOnly": True}]}
        spec["containers"] = [copy.deepcopy(base)]
        campaign.stub_launch_boundaries(cluster, identity)
        for field, value in (("command", ["python", "-c", "print('passed')"]),
                             ("args", ["--config", "/run/hermes-acceptance/other.json"]),
                             ("volumeMounts", []),
                             ("volumeMounts", [{"name": "model", "mountPath": "/opt/hermes-agent", "readOnly": True}]),
                             ("volumeMounts", [{"name": "model", "mountPath": "/run/hermes-acceptance", "readOnly": False}])):
            spec["containers"] = [dict(copy.deepcopy(base), **{field: value})]
            with self.subTest(field=field, value=value), self.assertRaises(campaign.GateError):
                campaign.stub_launch_boundaries(cluster, identity)


class RuntimeFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.fixtures = [{"instance_id": number, "fixture_root": f"/workspaces/hermes/user-7/instance-{number}/.acceptance-{RUN_ID}"} for number in (10, 11)]
        self.paths = {}
        for fixture in self.fixtures:
            directory = self.root / str(fixture["instance_id"])
            directory.mkdir()
            (directory / "allow-fixture").mkdir()
            (directory / "deny-fixture").mkdir()
            self.paths[fixture["fixture_root"]] = directory
        self.release = {"payload_sha256": "e" * 64, "desktop_web_accepted": False}
        self.identity = {"container": "hermes", "replicas": [{"pod": "fixture-pod"}]}
        owner = self

        class FixtureCluster:
            def exec(self, _pod, _container, *args):
                # Execute the actual fixed remote Python, mapping only the two
                # scoped fixture roots into this test's disposable directory.
                real_path = Path
                real_open = open

                def fixture_path(value):
                    return owner.paths.get(value, real_path(value))

                def release_open(value, *pos, **kw):
                    if value == "/usr/local/share/hermes-lite/release.json":
                        return io.StringIO(json.dumps(owner.release))
                    return real_open(value, *pos, **kw)

                offset = args.index("-c")
                output = io.StringIO()
                with patch("pathlib.Path", fixture_path), patch("builtins.open", release_open), patch.object(sys, "argv", ["-c", *args[offset + 2:]]), contextlib.redirect_stdout(output):
                    exec(compile(args[offset + 1], "<fixed-runtime-check>", "exec"), {})
                return output.getvalue().encode()

        self.cluster = FixtureCluster()

    def check(self, phase):
        return campaign.runtime_state(self.cluster, self.identity, self.fixtures, RUN_ID, phase, 10)

    def test_real_release_schema_and_only_approved_directory_removal(self):
        self.check("before")
        shutil.rmtree(self.root / "10/allow-fixture")
        self.check("after")

    def test_wrong_release_state_or_fixture_path_never_pass(self):
        self.release["desktop_web_accepted"] = True
        with self.assertRaises(campaign.GateError):
            self.check("before")
        self.release["desktop_web_accepted"] = False
        self.fixtures[0]["fixture_root"] = "/workspaces/hermes/user-7/instance-10/../production"
        with self.assertRaisesRegex(campaign.GateError, "fixture_path_scope_invalid"):
            self.check("before")

    def test_denial_and_cross_instance_directories_must_remain(self):
        shutil.rmtree(self.root / "10/allow-fixture")
        shutil.rmtree(self.root / "11/allow-fixture")
        with self.assertRaises(campaign.GateError):
            self.check("after")
        (self.root / "11/allow-fixture").mkdir()
        shutil.rmtree(self.root / "10/deny-fixture")
        with self.assertRaises((AssertionError, campaign.GateError)):
            self.check("after")

    def test_file_substitution_does_not_count_as_approved_removal(self):
        shutil.rmtree(self.root / "10/allow-fixture")
        (self.root / "10/allow-fixture").write_text("still present")
        with self.assertRaises((AssertionError, campaign.GateError)):
            self.check("after")

    def test_child_symlink_cannot_impersonate_real_fixture(self):
        target = self.root / "outside-fixture"
        target.mkdir()
        shutil.rmtree(self.root / "10/allow-fixture")
        try:
            os.symlink(target, self.root / "10/allow-fixture", target_is_directory=True)
        except OSError as error:
            self.skipTest(f"symlink unavailable: {error.__class__.__name__}")
        with self.assertRaises((AssertionError, campaign.GateError)):
            self.check("before")
        target.rmdir()
        with self.assertRaises((AssertionError, campaign.GateError)):
            self.check("after")


class SignatureSequenceTests(unittest.TestCase):
    def execute_fixture(self, failure=None):
        """Only external operations are mocked; execute's gate ordering is real."""
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            manifest = root / "assets.json"
            manifest.write_text("{}", encoding="utf-8")
            output = root / "evidence"
            output.mkdir()
            args = SimpleNamespace(execution_config=root / "config.json", cluster_config=root / "cluster.json",
                                   private_key=root / "fixture-key", metadata=root / "metadata.json", oci=root / "fixture.oci",
                                   image="fixture-only", output_dir=output)
            digest = "sha256:" + "a" * 64
            payload = "b" * 64
            version = {"version": "fixture", "commit": "fixture", "build_time": "fixture"}
            config = {"namespace": NAMESPACE, "instance_id": 10, "other_instance_id": 11,
                      "cm_identity": version, "renderer_asset_manifest_file": str(manifest)}
            cfg = {"schema_version": 1, "context": "fixture-context", "run_id": RUN_ID,
                   "cm_deployment": "cm", "cm_container": "cm", "runtime_deployment": "runtime", "runtime_container": "hermes",
                   "stub_deployment": "stub", "stub_container": "stub", "fixtures": [{"instance_id": 10}, {"instance_id": 11}]}
            before = {"runtime": {"image_digest": digest}, "cm": {"image_digest": digest}, "payload_sha256": payload,
                      "public": {"version": version, "renderer": {"build_input_sha256": "c" * 64}}, "assets": {}}
            scripts = ("run_campaign.py", "run_release_check.py", "campaign_evidence.py", "campaign_signing.py", "verify_lite_release.py")
            artifacts = {campaign.SHARE + "/" + name: campaign.verify.sha256_file(ROOT / "scripts" / name) for name in scripts}
            browser_file = ROOT / "tests/smoke_lite_cm_bff_browser.py"
            artifacts[campaign.SHARE + "/tests/smoke_lite_cm_bff_browser.py"] = campaign.verify.sha256_file(browser_file)
            artifacts[campaign.SHARE + "/tests/cm_bff_browser.mjs"] = "d" * 64
            release = {"desktop_web_accepted": False, "payload_sha256": payload, "artifacts": artifacts}
            sequence = []

            def read_config(path, _private):
                changed = failure == "changed_inputs" and "after" in sequence
                return (config, "changed" if changed else "config-hash") if path == args.execution_config else (cfg, "cluster-hash")

            api = SimpleNamespace(__file__=str(browser_file), read_json=read_config,
                                  restricted=lambda _path: None, validate=lambda *_args: None)

            def command(command_args, _timeout=60):
                if command_args[:3] == ["docker", "image", "inspect"]:
                    return digest.encode()
                if command_args[0] == "docker":
                    return json.dumps(release).encode()
                suite = command_args[command_args.index("--suite") + 1]
                sequence.append(suite)
                if failure == suite:
                    raise campaign.GateError("fixture_suite_failed")
                if suite == "cm_bff_browser":
                    campaign.write_new(output / "cm-bff-browser-result.json", {
                        "status": "passed", "run_id": RUN_ID, "binding_sha256": campaign.verify.sha256_file(output / "acceptance-binding.json"),
                        "counts": {"model_stub_requests": 5, "external_model_requests": 0, "tool_results_observed": 3}})
                return b"fixture process exit zero"

            def snapshot(_cluster, _cfg, _config, phase, expected=None):
                sequence.append(phase)
                if failure == phase:
                    raise campaign.GateError("fixture_snapshot_failed")
                if phase == "after":
                    self.assertEqual(expected, before)
                    return {**before, "stub_observation": {"request_count": 999 if failure == "observer_mismatch" else 5,
                                                           "external_requests": 0, "tool_results_observed": 3}}
                return before

            def sign(*_args):
                self.assertTrue((output / "campaign-before.json").is_file())
                self.assertTrue((output / "campaign-after.json").is_file())
                sequence.append("sign")

            with patch.object(campaign, "browser_api", return_value=api), patch.object(campaign, "command", side_effect=command), \
                    patch.object(campaign, "snapshot", side_effect=snapshot), patch.object(campaign, "tool_identity", return_value={}), \
                    patch.object(campaign.verify, "load_json", side_effect=lambda p: {"containerimage.digest": digest} if p == args.metadata else json.loads(Path(p).read_bytes())), \
                    patch.object(campaign.run_release_check, "candidate_identity", return_value={"config_digest": digest, "manifest_digest": digest}), \
                    patch.object(campaign.campaign_evidence, "validate_binding"), \
                    patch.object(campaign.campaign_signing, "sign_completed_campaign", side_effect=sign), contextlib.redirect_stdout(io.StringIO()):
                if failure:
                    with self.assertRaises(campaign.GateError):
                        campaign.execute(args)
                    self.assertNotIn("sign", sequence)
                    self.assertFalse((output / "campaign-after.json").exists())
                else:
                    campaign.execute(args)
                    self.assertEqual(["before", *campaign.verify.SUITES, "after", "sign"], sequence)

    def test_signature_requires_all_six_executions_then_postflight(self):
        self.execute_fixture()

    def test_suite_postflight_or_input_failure_never_signs(self):
        for failure in ("before", *campaign.verify.SUITES, "after", "changed_inputs", "observer_mismatch"):
            with self.subTest(failure=failure):
                self.execute_fixture(failure)


class StubObservationTests(unittest.TestCase):
    def observed(self, after=False):
        return {"schema_version": 1, "run_id": RUN_ID, "model_mode": "deterministic-stub", "request_count": 8 if after else 0,
                "external_requests": 0, "tool_results_observed": 3 if after else 0,
                "prompt_markers": ["ACCEPTANCE_" + RUN_ID + "_" + kind for kind in ("TEXT", "SLOW", "ONCE", "DENY", "CLARIFY")] if after else [],
                "instances": [10] if after else [], "tool_counts": {"terminal": 2, "clarify": 1} if after else {},
                "tool_schema_counts": {"terminal": 8, "clarify": 8} if after else {}}

    def check(self, value, phase):
        cluster = SimpleNamespace(exec=lambda *_args: json.dumps(value).encode())
        return campaign.stub_observation(cluster, {"container": "stub", "replicas": [{"pod": "verified-stub"}]}, RUN_ID, phase, 10)

    def test_fresh_before_and_real_tools_after(self):
        self.check(self.observed(), "before")
        self.check(self.observed(True), "after")

    def test_wrong_or_incomplete_real_pod_statistics_fail(self):
        for key, value in (("run_id", "cd" * 16), ("external_requests", 1), ("request_count", 4),
                           ("tool_results_observed", 2), ("instances", [10, 11]), ("prompt_markers", []),
                           ("tool_counts", {"terminal": 2}), ("tool_schema_counts", {"terminal": 8, "clarify": 8, "computer_use": 1})):
            data = dict(self.observed(True), **{key: value})
            with self.subTest(key=key), self.assertRaises(campaign.GateError):
                self.check(data, "after")
        with self.assertRaisesRegex(campaign.GateError, "fresh_model_stub_required"):
            self.check(self.observed(True), "before")

    def test_browser_counts_must_equal_actual_pod_not_self_report(self):
        data = self.observed(True)
        with tempfile.TemporaryDirectory() as temporary:
            result = Path(temporary) / "browser.json"
            record = {"status": "passed", "run_id": RUN_ID, "binding_sha256": "c" * 64,
                      "counts": {"model_stub_requests": 8, "external_model_requests": 0, "tool_results_observed": 3}}
            result.write_text(json.dumps(record), encoding="utf-8")
            campaign.compare_browser_stub_observation(result, data, RUN_ID, "c" * 64)
            record["counts"]["model_stub_requests"] = 999
            result.write_text(json.dumps(record), encoding="utf-8")
            with self.assertRaisesRegex(campaign.GateError, "browser_real_stub_counts_mismatch"):
                campaign.compare_browser_stub_observation(result, data, RUN_ID, "c" * 64)

    def test_fixed_remote_observer_reads_loopback_and_never_prints_credentials(self):
        owner = self
        observed = self.observed(True)
        config = {"observer_token": "fixture-observer-" + "x" * 32, "instances": [{"token": "fixture-instance-" + "y" * 32}]}

        class LoopbackCluster:
            def exec(self, _pod, _container, *args):
                output = io.StringIO()

                class Response:
                    status = 200
                    def __enter__(self): return self
                    def __exit__(self, *_args): return None
                    def read(self, limit): return json.dumps(observed).encode()[:limit]

                class Opener:
                    def open(self, request, timeout):
                        owner.assertEqual("http://127.0.0.1:18080/__acceptance__/observation", request.full_url)
                        owner.assertTrue(request.get_header("Authorization").endswith(config["observer_token"]))
                        return Response()

                with patch("builtins.open", return_value=io.StringIO(json.dumps(config))), \
                        patch("urllib.request.build_opener", return_value=Opener()), contextlib.redirect_stdout(output):
                    exec(compile(args[args.index("-c") + 1], "<fixed-stub-observer>", "exec"), {})
                raw = output.getvalue()
                owner.assertFalse(config["observer_token"] in raw)
                owner.assertFalse(config["instances"][0]["token"] in raw)
                return raw.encode()

        campaign.stub_observation(LoopbackCluster(), {"container": "stub", "replicas": [{"pod": "verified-stub"}]}, RUN_ID, "after", 10)
        observed["prompt_markers"] = [config["observer_token"]]
        with self.assertRaises(AssertionError):
            campaign.stub_observation(LoopbackCluster(), {"container": "stub", "replicas": [{"pod": "verified-stub"}]}, RUN_ID, "after", 10)


if __name__ == "__main__":
    unittest.main()
