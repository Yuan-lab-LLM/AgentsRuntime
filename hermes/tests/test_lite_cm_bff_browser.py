"""Fail-closed input/result tests. These never claim CM/browser acceptance."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("browser_gate", Path(__file__).with_name("smoke_lite_cm_bff_browser.py"))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class BrowserGateTests(unittest.TestCase):
    def report(self):
        value = gate.result()
        value.update(run_id="a" * 32, binding_sha256="b" * 64)
        return value

    def test_all_eighteen_cases_are_required(self):
        value = self.report()
        value["status"] = "passed"
        value["cases"] = {name: "passed" for name in gate.CASES}
        gate.validate_result(value)
        for name in gate.CASES:
            bad = copy.deepcopy(value)
            bad["cases"][name] = "not_run"
            with self.assertRaisesRegex(gate.GateError, "browser_incomplete_acceptance"):
                gate.validate_result(bad)
        del value["cases"][gate.CASES[-1]]
        with self.assertRaisesRegex(gate.GateError, "browser_result_cases_invalid"):
            gate.validate_result(value)

    def test_duplicate_json_and_fake_pass_fields_rejected(self):
        with self.assertRaisesRegex(gate.GateError, "duplicate_json_key"):
            json.loads('{"status":"failed","status":"passed"}', object_pairs_hook=gate.unique_object)
        value = self.report()
        value["accepted"] = True
        with self.assertRaises(gate.GateError):
            gate.validate_result(value)

    def test_arbitrary_browser_output_is_never_forwarded(self):
        for key, payload in (("message", "private-password"), ("stdout", "cookie=value"), ("trace", "https://host/ws?ticket=secret")):
            value = self.report()
            value[key] = payload
            with self.assertRaises(gate.GateError):
                gate.validate_result(value)
        for detail in ({"identity": {"cm_image_digest": "credential"}}, {"counts": {"text": "cookie"}},
                       {"artifacts": {"../../credentials": "a" * 64}}):
            value = self.report()
            value.update(detail)
            with self.assertRaises(gate.GateError):
                gate.validate_result(value)

    def test_production_namespace_fails_before_browser_or_credentials(self):
        config = {k: None for k in ("schema_version", "namespace", "cm_origin", "cm_identity", "candidate_image_digest",
                  "candidate_payload_sha256", "instance_id", "other_instance_id", "credentials_file", "secret_canaries_file",
                  "ca_file", "runner", "stub_observation_url", "stub_observer_credentials_file", "renderer_asset_manifest_file")}
        config.update(schema_version=1, namespace="clawmanager-system")
        with patch.object(gate.subprocess, "run") as run:
            with self.assertRaisesRegex(gate.GateError, "isolated_namespace_required"):
                gate.validate(config, {}, "", Path(__file__).parent)
            run.assert_not_called()
        config["command"] = ["echo", "pass"]
        with self.assertRaisesRegex(gate.GateError, "config_schema_invalid"):
            gate.validate(config, {}, "", Path(__file__).parent)

    def test_tree_hash_binds_transitive_playwright_core_bytes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ("playwright", "playwright-core"):
                (root / name).mkdir()
                (root / name / "index.mjs").write_text("export const version = 1", encoding="utf-8")
            before = gate.playwright_digest(root / "playwright")
            (root / "playwright-core" / "index.mjs").write_text("export const version = 2", encoding="utf-8")
            self.assertNotEqual(before, gate.playwright_digest(root / "playwright"))
            with self.assertRaises(gate.GateError):
                gate.playwright_digest(root / "playwright-core")

    def test_validated_fixture_and_binding_cannot_change_driver_or_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            def file(name, value):
                p = root / name
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(json.dumps(value), encoding="utf-8")
                p.chmod(0o600)
                return str(p)
            for name in ("playwright", "playwright-core"):
                file(name + "/index.mjs", "fixture-only")
                file(name + "/package.json", {"version": "1.62.1"})
            node = file("node", "not-executed-fixture")
            browser = file("browser", "not-executed-fixture")
            manifest = file("assets.json", {"index.html": "a" * 64, "assets/main.js": "b" * 64, "assets/main.css": "c" * 64})
            identity = dict(image_digest="sha256:" + "a" * 64, version="candidate", commit="source-dirty", build_time="2026-09-08T00:00:00Z",
                            renderer_build_input_sha256="b" * 64, renderer_asset_manifest_sha256=gate.digest(manifest))
            config = dict(schema_version=1, namespace="clawmanager-acceptance", cm_origin="https://acceptance.invalid", cm_identity=identity,
                          candidate_image_digest="sha256:" + "c" * 64, candidate_payload_sha256="d" * 64, instance_id=1, other_instance_id=2,
                          credentials_file=file("credentials.json", {"username": "fixture", "password": "fixture-password"}),
                          secret_canaries_file=file("canaries.json", {"values": ["private-canary-12345", "private-canary-67890"]}),
                          ca_file=file("ca.pem", "fixture-only"), renderer_asset_manifest_file=manifest,
                          stub_observation_url="http://127.0.0.1:1234/__acceptance__/observation",
                          stub_observer_credentials_file=file("observer.json", {"token": "private-observer-12345"}),
                          runner=dict(node_path=node, browser_path=browser, playwright_package_path=str(root / "playwright")))
            binding = dict(schema_version=1, protocol="hermes-lite-campaign-v1", scope="real-cm-bff-browser", run_id="e" * 32,
                           execution_config_sha256="f" * 64, candidate_image_digest=config["candidate_image_digest"],
                           candidate_manifest_digest="sha256:" + "c" * 64, candidate_config_digest="sha256:" + "d" * 64,
                           payload_sha256=config["candidate_payload_sha256"], cm_image_digest=identity["image_digest"],
                           renderer_build_input_sha256=identity["renderer_build_input_sha256"], renderer_asset_manifest_sha256=gate.digest(manifest),
                           harness_sha256=gate.digest(Path(__file__).with_name("cm_bff_browser.mjs")), node_sha256=gate.digest(node), browser_sha256=gate.digest(browser),
                           playwright_sha256=gate.playwright_digest(root / "playwright"), node_version="v22.22.0", browser_version="151.0.7922.174", playwright_version="1.62.1")
            # Files above are synthetic input fixtures; permission policy has a
            # separate OS-level test. No process, HTTP server or browser starts.
            with patch.object(gate, "restricted", side_effect=Path):
                gate.validate(config, binding, "f" * 64, Path(__file__).parent)
                for field, value in (("harness_sha256", "0" * 64), ("candidate_manifest_digest", None), ("playwright_sha256", "0" * 64), ("node_sha256", "0" * 64)):
                    bad = dict(binding, **{field: value})
                    with self.assertRaises(gate.GateError):
                        gate.validate(config, bad, "f" * 64, Path(__file__).parent)
                with self.assertRaisesRegex(gate.GateError, "execution_config_binding_mismatch"):
                    gate.validate(config, binding, "0" * 64, Path(__file__).parent)

    @unittest.skipUnless(shutil.which("node"), "Node is optional for Python-only fixture tests")
    def test_real_node_provenance_hash_and_observed_replica_uid_binding(self):
        # Run the production validator with real on-disk bytes; no HTTP,
        # Kubernetes or browser response is fabricated as acceptance evidence.
        with tempfile.TemporaryDirectory(prefix="provenance-#-") as tmp:
            root = Path(tmp)
            harness = Path(__file__).with_name("cm_bff_browser.mjs").resolve().as_uri()
            driver = root / "check.mjs"
            driver.write_text("import {readCMProvenance,assertReplicaProvenance} from " + json.dumps(harness) + ";\n" + r'''
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
const dir=path.dirname(process.argv[1]), filename=path.join(dir,'cm-provenance.json');
const digest='sha256:'+'a'.repeat(64), ids=['11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','33333333-3333-3333-3333-333333333333'];
const value={schema_version:1,context:'fixture',namespace:'acceptance-fixture',cm:{image_digest:digest,replicas:ids.map(uid=>({uid,image_digest:digest}))}};
const raw=Buffer.from(JSON.stringify(value));fs.writeFileSync(filename,raw);
const binding={cm_image_digest:digest,cm_provenance_sha256:createHash('sha256').update(raw).digest('hex')};
const load=expected=>readCMProvenance(path.join(dir,'binding.json'),expected,'acceptance-fixture');
const observed=load(binding);assertReplicaProvenance(observed,[ids[2],ids[0],ids[1]]);
assert.throws(()=>load({...binding,cm_provenance_sha256:'0'.repeat(64)}),/cm_provenance_hash_mismatch/);
assert.throws(()=>load({...binding,cm_image_digest:'sha256:'+'b'.repeat(64)}),/cm_provenance_identity_mismatch/);
assert.throws(()=>assertReplicaProvenance(observed,[ids[0],ids[1],'44444444-4444-4444-4444-444444444444']),/candidate_admission_replica_identity_mismatch/);
assert.throws(()=>assertReplicaProvenance(observed,[ids[0],ids[0],ids[1]]),/three_real_replicas_required/);
fs.writeFileSync(filename,Buffer.alloc(1048577));assert.throws(()=>load(binding),/cm_provenance_file_invalid/);
if(process.platform!=='win32'){
  fs.unlinkSync(filename);fs.writeFileSync(path.join(dir,'target.json'),raw);fs.symlinkSync('target.json',filename);
  assert.throws(()=>load(binding),/cm_provenance_file_invalid/);
}
process.stdout.write('provenance validators passed\n');
''', encoding="utf-8")
            run = subprocess.run([shutil.which("node"), str(driver)], capture_output=True, timeout=15)
            self.assertEqual(run.returncode, 0, run.stderr.decode())
            self.assertEqual(run.stdout, b"provenance validators passed\n")

    @unittest.skipUnless(shutil.which("node"), "Node is optional for Python-only fixture tests")
    def test_real_node_http_contract_rejects_resolved_errors_and_bad_shapes(self):
        with tempfile.TemporaryDirectory() as tmp:
            driver = Path(tmp) / "check.mjs"
            harness = Path(__file__).with_name("cm_bff_browser.mjs").resolve().as_uri()
            driver.write_text("import {assertHTTPContract as verify} from " + json.dumps(harness) + ";\n" + r'''
import assert from 'node:assert/strict';
const cases=[['/api/status',{version:'fixture',active_sessions:0}],
 ['/api/model/info',{model:'stub',provider:'custom',effective_context_length:4096}],
 ['/api/config',{}],['/api/config/defaults',{display:{skin:'default'}}],
 ['/api/model/options?explicit_only=true',{model:'stub',provider:'custom',providers:[]}],
 ['/api/sessions?limit=10',{total:0,sessions:[]}],
 ['/api/profiles/sessions/sidebar?recents_profile=all',Object.fromEntries(['recents','cron','messaging'].map(k=>[k,{sessions:[],profiles_truncated:{default:false}}]))],
 ['/api/sessions/fixture/messages',{session_id:'fixture',messages:[{role:'assistant',content:'fixture'}]}]];
for(const [route,value]of cases){
 assert.equal(verify(route,value),value);
 for(const failure of [{error:'failed'},{ok:false},{success:false},null,[],{status:500},'login HTML'])assert.throws(()=>verify(route,failure));
}
assert.throws(()=>verify('/api/config',{dashboard:{password:'fixture'}}),/http_contract_response_shape/);
assert.throws(()=>verify('/api/sessions',{total:0,sessions:[{}]}),/http_contract_response_shape/);
process.stdout.write('HTTP validators passed\n');
''', encoding="utf-8")
            run = subprocess.run([shutil.which("node"), str(driver)], capture_output=True, timeout=15)
            self.assertEqual(run.returncode, 0, run.stderr.decode())
            self.assertEqual(run.stdout, b"HTTP validators passed\n")

    @unittest.skipUnless(shutil.which("node"), "Node is optional for Python-only fixture tests")
    def test_real_driver_preflight_never_outputs_input_secret_or_fake_pass(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = root / "config.json"
            config.write_text('{"do_not_print":"private-fixture-sentinel"}', encoding="utf-8")
            binding = root / "binding.json"
            harness = Path(__file__).with_name("cm_bff_browser.mjs")
            binding.write_text(json.dumps(dict(run_id="a" * 32, execution_config_sha256=gate.digest(config),
                                                harness_sha256=gate.digest(harness), node_version="v0.0.0")), encoding="utf-8")
            p = subprocess.run([shutil.which("node"), str(harness), "--config", str(config), "--binding", str(binding)],
                               capture_output=True, timeout=15)
            self.assertNotEqual(p.returncode, 0)
            self.assertNotIn(b"private-fixture-sentinel", p.stdout + p.stderr)
            value = json.loads(p.stdout)
            self.assertEqual(value["category"], "node_version_mismatch")
            self.assertTrue(all(v == "not_run" for v in value["cases"].values()))

    @unittest.skipIf(os.name == "nt", "POSIX permission assertion; Windows uses checked DACLs")
    def test_secret_file_permissions_and_symlinks(self):
        with tempfile.TemporaryDirectory() as tmp:
            secret = Path(tmp) / "credentials.json"
            secret.write_text('{"username":"test"}', encoding="utf-8")
            secret.chmod(0o644)
            with self.assertRaisesRegex(gate.GateError, "restricted_file_permissions"):
                gate.read_json(secret, True)
            secret.chmod(0o600)
            gate.read_json(secret, True)
            link = Path(tmp) / "link.json"
            link.symlink_to(secret)
            with self.assertRaises(gate.GateError):
                gate.read_json(link, True)

    @unittest.skipUnless(os.name == "nt", "Windows DACL test")
    def test_windows_dacl_rejects_everyone_even_when_mode_bits_look_private(self):
        import re
        identity = subprocess.check_output(["whoami", "/user", "/fo", "csv", "/nh"], text=True)
        sid = re.search(r"S-1-[0-9-]+", identity).group(0)
        with tempfile.TemporaryDirectory() as tmp:
            secret = Path(tmp) / "credentials.json"
            secret.write_text('{"fixture":"not-a-real-secret"}', encoding="utf-8")
            subprocess.run(["icacls", str(secret), "/inheritance:r", "/grant:r", "*" + sid + ":F", "*S-1-5-18:F", "*S-1-5-32-544:F"],
                           check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            gate.read_json(secret, True)
            subprocess.run(["icacls", str(secret), "/grant", "*S-1-1-0:R"], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            secret.chmod(0o600)
            with self.assertRaisesRegex(gate.GateError, "restricted_file_permissions"):
                gate.read_json(secret, True)

    def test_javascript_manifest_exactly_matches_python_cases(self):
        source = Path(__file__).with_name("cm_bff_browser.mjs").read_text(encoding="utf-8")
        cases = source.split("const CASES = [", 1)[1].split("];", 1)[0]
        import re
        self.assertEqual(list(gate.CASES), re.findall(r"'([a-z_]+)'", cases))
        self.assertNotIn("route.fulfill", source)
        self.assertNotIn("ignoreHTTPSErrors:true", source)
        self.assertIn("await editor.press('Enter')", source)


if __name__ == "__main__":
    unittest.main()
