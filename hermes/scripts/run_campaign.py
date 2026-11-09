#!/usr/bin/env python3
"""Observe an isolated cluster, execute the fixed six suites, then sign evidence.

discover is read-only and works against the existing 172 deployment. execute
requires a separately provisioned CM candidate environment. It never edits CM,
sets accepted, provisions users, or publishes an image.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import ssl
import subprocess
import sys
from urllib.parse import urlsplit
from urllib.request import HTTPSHandler, HTTPRedirectHandler, build_opener

import campaign_evidence
import campaign_signing
import run_release_check
import verify_lite_release as verify

SHARE = verify.SHARE
HERE = Path(__file__).resolve().parent
ASSET_ROOT = "/usr/share/nginx/html/hermes-desktop-web"


class GateError(Exception):
    pass


def require(ok, category):
    if not ok:
        raise GateError(category)


def now():
    return datetime.now(timezone.utc).isoformat()


def encoded(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def write_new(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(value if isinstance(value, bytes) else encoded(value))


def private_directory(path):
    path.mkdir(parents=True, mode=0o700)
    if os.name == "nt":
        data = base64.b64encode(str(path.resolve()).encode()).decode()
        code = ("$ErrorActionPreference='Stop';$p=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + data + "'));"
                "$u=[Security.Principal.WindowsIdentity]::GetCurrent().User;"
                "$a=[IO.Directory]::GetAccessControl($p);$a.SetAccessRuleProtection($true,$false);"
                "foreach($r in @($a.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]))){$a.RemoveAccessRuleSpecific($r)};"
                "$r=New-Object Security.AccessControl.FileSystemAccessRule($u,'FullControl','ContainerInherit,ObjectInherit','None','Allow');"
                "$a.AddAccessRule($r);[IO.Directory]::SetAccessControl($p,$a)")
        exe = Path(os.environ.get("SystemRoot", "C:/Windows")) / "System32/WindowsPowerShell/v1.0/powershell.exe"
        command([str(exe), "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.b64encode(code.encode("utf-16le")).decode()])


def command(args, timeout=60):
    # No shell and no inherited Node/Python injection options. Child diagnostics
    # may contain credentials; only category codes reach public evidence.
    env = {k: v for k, v in os.environ.items() if k.upper() in {
        "PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE",
        "LOCALAPPDATA", "APPDATA", "DOCKER_CONFIG", "KUBECONFIG"}}
    p = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, env=env,
                       creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
    require(p.returncode == 0 and len(p.stdout) <= 8 * 1048576, "command_failed_or_oversized")
    return p.stdout


def browser_api():
    path = HERE.parent / "tests/smoke_lite_cm_bff_browser.py"
    if not path.exists():
        path = HERE / "tests/smoke_lite_cm_bff_browser.py"
    spec = importlib.util.spec_from_file_location("campaign_browser", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Cluster:
    def __init__(self, context, namespace):
        require(re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", context), "context_invalid")
        require(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", namespace), "namespace_invalid")
        self.context, self.namespace = context, namespace

    def call(self, *args):
        return command(["kubectl", "--context", self.context, "--namespace", self.namespace,
                        "--request-timeout=20s", *args])

    def get(self, resource, name=None):
        return json.loads(self.call("get", resource, *([name] if name else []), "-o", "json"))

    def exec(self, pod, container, *args):
        return self.call("exec", pod, "-c", container, "--", *args)


def select(labels, selector):
    require(not selector.get("matchExpressions"), "selector_expression_not_supported")
    return all(labels.get(k) == v for k, v in selector.get("matchLabels", {}).items())


def deployment_identity(cluster, name, container, count=None):
    deployment = cluster.get("deployment", name)
    selector = deployment["spec"]["selector"]
    pods = [p for p in cluster.get("pods")["items"] if select(p["metadata"].get("labels", {}), selector)]
    require(pods and (count is None or len(pods) == count), "replica_count_mismatch")
    records = []
    for pod in pods:
        require(not pod["metadata"].get("deletionTimestamp"), "terminating_replica")
        spec = next(c for c in pod["spec"]["containers"] if c["name"] == container)
        status = next(c for c in pod["status"]["containerStatuses"] if c["name"] == container)
        require(status.get("ready") is True and pod["status"]["phase"] == "Running", "replica_not_ready")
        require(re.search(r"@sha256:[0-9a-f]{64}$", spec["image"]), "immutable_image_required")
        digest = spec["image"].rsplit("@", 1)[1]
        require(status["imageID"].endswith("@" + digest) or status["imageID"] == digest,
                "running_image_differs_from_spec")
        records.append({"pod": pod["metadata"]["name"], "uid": pod["metadata"]["uid"],
                        "image_digest": digest, "restart_count": status["restartCount"]})
    require(len({r["image_digest"] for r in records}) == 1, "replica_image_mismatch")
    return {"deployment": name, "container": container, "replicas": sorted(records, key=lambda r: r["uid"]),
            "image_digest": records[0]["image_digest"], "spec_sha256": sha(encoded(deployment["spec"]))}


def asset_manifest(cluster, identity):
    command_text = "cd " + ASSET_ROOT + " && test -z \"$(find . -type l -print)\" && find . -type f -exec sha256sum '{}' +"
    manifests = []
    for pod in identity["replicas"]:
        rows = cluster.exec(pod["pod"], identity["container"], "sh", "-c", command_text).decode().splitlines()
        manifest = {}
        for row in rows:
            match = re.fullmatch(r"([0-9a-f]{64})  \./([A-Za-z0-9_./-]+)", row)
            require(match is not None, "renderer_manifest_invalid")
            digest, path = match.groups()
            require(all(p not in {"", ".", ".."} for p in path.split("/")) and path not in manifest,
                    "renderer_manifest_path_invalid")
            manifest[path] = digest
        require(3 <= len(manifest) <= 1024 and "build-info.json" in manifest and "index.html" in manifest,
                "renderer_manifest_incomplete")
        manifests.append(manifest)
    require(all(m == manifests[0] for m in manifests), "renderer_replica_mismatch")
    return manifests[0]


def cm_public_identity(cluster, identity):
    values = []
    for pod in identity["replicas"]:
        raw = cluster.exec(pod["pod"], identity["container"], "curl", "-fsS", "http://127.0.0.1:9001/api/v1/version")
        version = json.loads(raw)
        if isinstance(version.get("data"), dict):
            version = version["data"]
        build = json.loads(cluster.exec(pod["pod"], identity["container"], "cat", ASSET_ROOT + "/build-info.json"))
        values.append({"version": {k: version[k] for k in ("version", "commit", "build_time")}, "renderer": build})
    require(all(v == values[0] for v in values), "cm_build_replica_mismatch")
    return values[0]


def isolation(cluster, run_id, stub_uids):
    namespace = cluster.get("namespace", cluster.namespace)
    require(cluster.namespace.startswith("hermes-acceptance-") and
            namespace["metadata"].get("labels", {}).get("agentsruntime.io/campaign") == run_id,
            "isolated_campaign_namespace_required")
    pods = cluster.get("pods")["items"]
    policies = cluster.get("networkpolicies")["items"]
    require(pods and policies, "isolated_network_policy_required")
    require(stub_uids and set(stub_uids) <= {p["metadata"]["uid"] for p in pods}, "observed_stub_pods_required")
    # Kubernetes egress policies are additive. Examine EVERY selecting policy,
    # not merely the presence of a default-deny object.
    for pod in pods:
        spec = pod["spec"]
        require(not spec.get("hostNetwork") and not spec.get("hostPID") and not spec.get("hostIPC"), "host_namespace_forbidden")
        for volume in spec.get("volumes", []):
            sources = set(volume) - {"name"}
            require(len(sources) == 1 and sources <= {"emptyDir", "configMap", "secret", "projected", "downwardAPI"},
                    "ephemeral_test_volumes_required")
        for container in spec.get("containers", []) + spec.get("initContainers", []) + spec.get("ephemeralContainers", []):
            sc = container.get("securityContext", {})
            require(not sc.get("privileged") and not sc.get("capabilities", {}).get("add"), "privileged_test_container_forbidden")
        selected = [p["spec"] for p in policies if "Egress" in p["spec"].get("policyTypes", [])
                    and select(pod["metadata"].get("labels", {}), p["spec"].get("podSelector", {}))]
        require(selected, "unconfined_test_pod")
        is_stub = pod["metadata"]["uid"] in stub_uids
        for policy in selected:
            for rule in policy.get("egress", []):
                require(not is_stub, "model_stub_must_have_zero_egress")
                peers = rule.get("to", [])
                require(peers, "unrestricted_egress_forbidden")
                for peer in peers:
                    require(not peer.get("ipBlock") and "podSelector" in peer, "external_egress_forbidden")
                    select({}, peer["podSelector"])
                    ns = peer.get("namespaceSelector")
                    if ns is None:
                        continue
                    require(not ns.get("matchExpressions"), "external_egress_forbidden")
                    labels = ns.get("matchLabels", {})
                    if labels == {"kubernetes.io/metadata.name": cluster.namespace}:
                        continue
                    require(labels == {"kubernetes.io/metadata.name": "kube-system"}
                            and peer["podSelector"] == {"matchLabels": {"k8s-app": "kube-dns"}}
                            and rule.get("ports") and all(p.get("port") == 53 and p.get("protocol", "TCP") in {"UDP", "TCP"} for p in rule["ports"]),
                            "egress_only_same_namespace_and_dns")
    for service in cluster.get("services")["items"]:
        spec = service["spec"]
        require(spec.get("type", "ClusterIP") == "ClusterIP" and spec.get("selector") and not spec.get("externalIPs"), "isolated_selector_services_required")
    return sha(encoded({"pods": [{"uid": p["metadata"]["uid"], "spec": p["spec"]} for p in pods],
                       "policies": [p["spec"] for p in policies]}))


def runtime_launch_boundaries(cluster, identity):
    """The observed candidate must execute its own entrypoint and payload."""
    pods = {pod["metadata"]["uid"]: pod for pod in cluster.get("pods")["items"]}
    for replica in identity["replicas"]:
        require(replica["uid"] in pods, "runtime_pod_changed")
        spec = pods[replica["uid"]]["spec"]
        container = next(c for c in spec["containers"] if c["name"] == identity["container"])
        require(not container.get("command") and not container.get("args"), "runtime_image_entrypoint_required")
        require(not container.get("volumeDevices"), "runtime_payload_device_forbidden")
        volumes = {v["name"]: v for v in spec.get("volumes", [])}
        for mount in container.get("volumeMounts", []):
            require(not mount.get("subPath") and not mount.get("subPathExpr")
                    and mount.get("mountPropagation", "None") == "None", "runtime_mount_scope_invalid")
            volume = volumes.get(mount.get("name"), {})
            if mount.get("mountPath") == "/workspaces":
                require("emptyDir" in volume, "runtime_ephemeral_workspace_required")
                continue
            require(mount.get("mountPath") == "/var/run/secrets/kubernetes.io/serviceaccount"
                    and mount.get("readOnly") is True and "projected" in volume,
                    "runtime_payload_mount_forbidden")
            sources = volume["projected"].get("sources", [])
            require(sources and all(set(source) <= {"serviceAccountToken", "configMap", "downwardAPI"}
                                    and len(source) == 1 for source in sources), "runtime_serviceaccount_projection_invalid")


def stub_launch_boundaries(cluster, identity):
    """Only the candidate's fixed deterministic observer may attest model use."""
    pods = {pod["metadata"]["uid"]: pod for pod in cluster.get("pods")["items"]}
    for replica in identity["replicas"]:
        require(replica["uid"] in pods, "stub_pod_changed")
        spec = pods[replica["uid"]]["spec"]
        container = next(c for c in spec["containers"] if c["name"] == identity["container"])
        require(container.get("command") == ["python", "-I", "-B", SHARE + "/tests/acceptance_model_stub.py"]
                and container.get("args") == ["--config", "/run/hermes-acceptance/model.json"], "fixed_model_stub_command_required")
        require(not container.get("volumeDevices"), "model_stub_device_forbidden")
        volumes = {v["name"]: v for v in spec.get("volumes", [])}
        config_mounts = 0
        for mount in container.get("volumeMounts", []):
            require(mount.get("readOnly") is True and not mount.get("subPath") and not mount.get("subPathExpr")
                    and mount.get("mountPropagation", "None") == "None", "model_stub_mount_scope_invalid")
            volume = volumes.get(mount.get("name"), {})
            if mount.get("mountPath") == "/run/hermes-acceptance":
                require("secret" in volume, "model_stub_secret_config_required")
                config_mounts += 1
                continue
            require(mount.get("mountPath") == "/var/run/secrets/kubernetes.io/serviceaccount" and "projected" in volume,
                    "model_stub_payload_mount_forbidden")
            sources = volume["projected"].get("sources", [])
            require(sources and all(set(source) <= {"serviceAccountToken", "configMap", "downwardAPI"}
                                    and len(source) == 1 for source in sources), "model_stub_serviceaccount_projection_invalid")
        require(config_mounts == 1, "model_stub_secret_config_required")


def stub_observation(cluster, identity, run_id, phase, primary):
    # Read the fixed observer on the already verified Pod. Its credential never
    # leaves this Python process; no proxy or redirect can receive the header.
    code = """import json,urllib.request
config=json.load(open('/run/hermes-acceptance/model.json'))
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*args,**kwargs): raise RuntimeError('redirect forbidden')
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
request=urllib.request.Request('http://127.0.0.1:18080/__acceptance__/observation',headers={'Authorization':'Bearer '+config['observer_token']})
with opener.open(request,timeout=10) as response:
 assert response.status==200
 raw=response.read(65537)
 assert len(raw)<=65536
value=json.loads(raw)
fields=('schema_version','run_id','model_mode','request_count','external_requests','prompt_markers','tool_counts','instances','tool_schema_counts','tool_results_observed')
safe=json.dumps({key:value[key] for key in fields})
assert config['observer_token'] not in safe and all(item['token'] not in safe for item in config['instances'])
print(safe)
"""
    replica = identity["replicas"][0]
    observed = json.loads(cluster.exec(replica["pod"], identity["container"], "python", "-I", "-B", "-c", code))
    fields = {"schema_version", "run_id", "model_mode", "request_count", "external_requests", "prompt_markers",
              "tool_counts", "instances", "tool_schema_counts", "tool_results_observed"}
    require(isinstance(observed, dict) and set(observed) == fields and type(observed["schema_version"]) is int
            and observed["schema_version"] == 1 and observed["run_id"] == run_id
            and observed["model_mode"] == "deterministic-stub", "real_stub_identity_mismatch")
    for key in ("request_count", "external_requests", "tool_results_observed"):
        require(type(observed[key]) is int and 0 <= observed[key] <= 10000000, "real_stub_count_invalid")
    require(observed["external_requests"] == 0, "real_stub_external_request")
    for key in ("tool_counts", "tool_schema_counts"):
        require(isinstance(observed[key], dict) and all(isinstance(name, str) and re.fullmatch(r"[A-Za-z0-9_]{1,128}", name)
                    and type(count) is int and 0 <= count <= 10000000 for name, count in observed[key].items()), "real_stub_tool_count_invalid")
    require(isinstance(observed["instances"], list) and all(type(value) is int for value in observed["instances"])
            and isinstance(observed["prompt_markers"], list) and all(isinstance(value, str) for value in observed["prompt_markers"]), "real_stub_scope_invalid")
    if phase == "before":
        require(observed["request_count"] == 0 and observed["tool_results_observed"] == 0
                and not any(observed[key] for key in ("tool_counts", "tool_schema_counts", "instances", "prompt_markers")), "fresh_model_stub_required")
    else:
        require(phase == "after" and observed["request_count"] >= 5 and observed["tool_results_observed"] >= 3
                and observed["instances"] == [primary]
                and len(observed["prompt_markers"]) == 5
                and set(observed["prompt_markers"]) == {"ACCEPTANCE_" + run_id + "_" + kind for kind in ("TEXT", "SLOW", "ONCE", "DENY", "CLARIFY")}, "real_stub_execution_incomplete")
        require(all(observed[key].get("terminal", 0) > 0 and observed[key].get("clarify", 0) > 0
                    for key in ("tool_counts", "tool_schema_counts")), "real_stub_tools_not_observed")
        native = {"read_terminal", "close_terminal", "desktop_preview", "drive_preview", "annotate_preview", "read_window_below",
                  "focus_pane", "react_to_message", "setup_mcp", "tour", "tip", "desktop_project", "computer_use"}
        require(not native.intersection(observed["tool_schema_counts"]), "real_stub_native_tool_registered")
    return observed


def compare_browser_stub_observation(path, observed, run_id, binding_hash):
    browser = verify.load_json(path)
    require(isinstance(browser, dict) and browser.get("status") == "passed" and browser.get("run_id") == run_id
            and browser.get("binding_sha256") == binding_hash and isinstance(browser.get("counts"), dict), "browser_stub_result_identity_mismatch")
    for browser_key, observer_key in (("model_stub_requests", "request_count"), ("external_model_requests", "external_requests"),
                                      ("tool_results_observed", "tool_results_observed")):
        value = browser["counts"].get(browser_key)
        require(type(value) is int and value == observed[observer_key], "browser_real_stub_counts_mismatch")


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        raise GateError("identity_redirect_forbidden")


def served_identity(config, manifest, public):
    origin = config["cm_origin"]
    parsed = urlsplit(origin)
    require(parsed.scheme == "https" and not parsed.username and not parsed.path and not parsed.query and not parsed.fragment,
            "cm_https_origin_required")
    opener = build_opener(HTTPSHandler(context=ssl.create_default_context(cafile=config["ca_file"])), NoRedirect())
    def read(path):
        with opener.open(origin + path, timeout=30) as response:
            raw = response.read(8 * 1048576 + 1)
            require(response.status == 200 and len(raw) <= 8 * 1048576, "served_identity_invalid")
            return raw
    actual = json.loads(read("/api/v1/version"))
    actual = actual.get("data", actual)
    require({k: actual.get(k) for k in public["version"]} == public["version"], "served_cm_version_mismatch")
    for name, digest in manifest.items():
        require(sha(read("/hermes-desktop-web/" + name)) == digest, "served_renderer_asset_mismatch")


def runtime_state(cluster, identity, fixtures, run_id, phase, primary):
    pod = identity["replicas"][0]["pod"]
    for item in fixtures:
        require(set(item) == {"instance_id", "fixture_root"} and type(item["instance_id"]) is int and
                re.fullmatch(r"/workspaces/hermes/user-[1-9][0-9]*/instance-" + str(item["instance_id"]) + r"/\.acceptance-" + run_id, item["fixture_root"]),
                "fixture_path_scope_invalid")
    # Fixed code reads only release identity and disposable directory existence.
    code = """import json,os,pathlib,sys
f=json.loads(sys.argv[1]);r=json.load(open('/usr/local/share/hermes-lite/release.json'));o=[]
for x in f:
 p=pathlib.Path(x['fixture_root'])
 assert p.is_dir() and p.resolve()==p and all(not q.is_symlink() for q in (p,*p.parents))
 a=p/'allow-fixture';d=p/'deny-fixture'
 assert not a.is_symlink() and not d.is_symlink()
 assert not os.path.lexists(a) or a.is_dir()
 assert d.is_dir()
 o.append({'instance_id':x['instance_id'],'allow':os.path.lexists(a),'deny':d.is_dir()})
print(json.dumps({'payload_sha256':r['payload_sha256'],'accepted':r['desktop_web_accepted'],'fixtures':o}))
"""
    value = json.loads(cluster.exec(pod, identity["container"], "python", "-I", "-B", "-c", code, encoded(fixtures).decode()))
    require(value["accepted"] is False, "candidate_must_be_unaccepted")
    for item in value["fixtures"]:
        expected_allow = phase == "before" or item["instance_id"] != primary
        require(item["allow"] is expected_allow and item["deny"] is True, "real_approval_fixture_outcome_mismatch")
    return value


def tool_identity(config, api):
    runner = config["runner"]
    node = Path(runner["node_path"])
    require(Path(shutil.which("node") or "").resolve() == node.resolve(), "signer_node_path_mismatch")
    pw = Path(runner["playwright_package_path"])
    code = "const{chromium}=require(process.argv[1]);(async()=>{const b=await chromium.launch({headless:true,executablePath:process.argv[2]});process.stdout.write(b.version());await b.close()})().catch(()=>process.exit(1))"
    return {"node_sha256": api.digest(node), "browser_sha256": api.digest(runner["browser_path"]),
            "playwright_sha256": api.playwright_digest(pw),
            "node_version": command([str(node), "--print", "process.version"]).decode().strip(),
            "browser_version": command([str(node), "-e", code, str(pw), runner["browser_path"]]).decode().strip(),
            "playwright_version": json.loads((pw / "package.json").read_bytes())["version"]}


def snapshot(cluster, cfg, config, phase, expected=None):
    cm = deployment_identity(cluster, cfg["cm_deployment"], cfg["cm_container"], 3)
    runtime = deployment_identity(cluster, cfg["runtime_deployment"], cfg["runtime_container"], 1)
    stub = deployment_identity(cluster, cfg["stub_deployment"], cfg["stub_container"], 1)
    require(stub["image_digest"] == runtime["image_digest"], "stub_candidate_image_required")
    runtime_launch_boundaries(cluster, runtime)
    stub_launch_boundaries(cluster, stub)
    isolation_hash = isolation(cluster, cfg["run_id"], {pod["uid"] for pod in stub["replicas"]})
    manifest = asset_manifest(cluster, cm)
    public = cm_public_identity(cluster, cm)
    served_identity(config, manifest, public)
    state = runtime_state(cluster, runtime, cfg["fixtures"], cfg["run_id"], phase, config["instance_id"])
    model = stub_observation(cluster, stub, cfg["run_id"], phase, config["instance_id"])
    production = Cluster(cluster.context, "clawmanager-system").get("deployment", "clawmanager-app")
    result = {"cm": cm, "runtime": runtime, "stub": stub, "isolation_sha256": isolation_hash,
              "public": public, "assets": manifest, "payload_sha256": state["payload_sha256"],
              "production_cm_spec_sha256": sha(encoded(production["spec"]))}
    require(expected is None or result == {key: value for key, value in expected.items() if key != "stub_observation"}, "cluster_changed_during_campaign")
    return {**result, "stub_observation": model}


def observation(phase, binding, binding_hash):
    return {"schema_version": 1, "phase": phase, "status": "passed", "run_id": binding["run_id"],
            "binding_sha256": binding_hash, "runtime_image_digest": binding["candidate_manifest_digest"],
            "runtime_payload_sha256": binding["payload_sha256"], "cm_image_digest": binding["cm_image_digest"],
            "renderer_build_input_sha256": binding["renderer_build_input_sha256"],
            "renderer_asset_manifest_sha256": binding["renderer_asset_manifest_sha256"], "recorded_at": now(),
            "checks": {k: "passed" for k in campaign_evidence.OBSERVATION_CHECKS}}


def execute(args):
    api = browser_api()
    config, config_hash = api.read_json(args.execution_config.resolve(), True)
    cfg, cfg_hash = api.read_json(args.cluster_config.resolve(), True)
    api.restricted(args.private_key.resolve())
    require(set(cfg) == {"schema_version", "context", "run_id", "cm_deployment", "cm_container", "runtime_deployment", "runtime_container", "stub_deployment", "stub_container", "fixtures"}
            and cfg["schema_version"] == 1 and re.fullmatch(r"[0-9a-f]{32}", cfg["run_id"]), "cluster_config_schema_invalid")
    require(len(cfg["fixtures"]) == 2 and {i["instance_id"] for i in cfg["fixtures"]} == {config["instance_id"], config["other_instance_id"]}, "two_owned_test_fixtures_required")
    metadata = verify.load_json(args.metadata)
    candidate = metadata["containerimage.digest"]
    oci = run_release_check.candidate_identity(args.oci, candidate)
    image_id = command(["docker", "image", "inspect", args.image, "--format", "{{.Id}}"]).decode().strip()
    require(image_id in {candidate, oci["config_digest"]}, "candidate_local_identity_mismatch")
    release = json.loads(command(["docker", "run", "--rm", "--network", "none", "--entrypoint", "cat", image_id, SHARE + "/release.json"]))
    require(release.get("desktop_web_accepted") is False, "candidate_must_be_unaccepted")
    for filename in ("run_campaign.py", "run_release_check.py", "campaign_evidence.py", "campaign_signing.py", "verify_lite_release.py"):
        require(release["artifacts"].get(SHARE + "/" + filename) == verify.sha256_file(HERE / filename), "host_runner_differs_from_candidate")
    require(release["artifacts"].get(SHARE + "/tests/smoke_lite_cm_bff_browser.py") == verify.sha256_file(Path(api.__file__)), "host_browser_gate_differs_from_candidate")
    started = now()
    cluster = Cluster(cfg["context"], config["namespace"])
    before = snapshot(cluster, cfg, config, "before")
    require(before["runtime"]["image_digest"] == oci["manifest_digest"] and before["payload_sha256"] == release["payload_sha256"], "tested_runtime_candidate_mismatch")
    public = before["public"]
    build_hash = public["renderer"]["build_input_sha256"]
    require(public["version"] == {k: config["cm_identity"][k] for k in ("version", "commit", "build_time")}, "configured_cm_build_mismatch")
    provenance = {"schema_version": 1, "cluster_config_sha256": cfg_hash, "context": cluster.context,
                  "namespace": cluster.namespace, **{k: v for k, v in before.items() if k != "assets"}}
    write_new(args.output_dir / "cm-provenance.json", provenance)
    manifest_raw = Path(config["renderer_asset_manifest_file"]).read_bytes()
    require(json.loads(manifest_raw) == before["assets"], "configured_asset_inventory_mismatch")
    write_new(args.output_dir / "renderer-asset-manifest.json", manifest_raw)
    binding = {"schema_version": 1, "protocol": campaign_evidence.PROTOCOL, "scope": "real-cm-bff-browser",
               "run_id": cfg["run_id"], "started_at": started, "candidate_image_digest": candidate,
               "candidate_manifest_digest": oci["manifest_digest"], "candidate_config_digest": oci["config_digest"],
               "cm_image_digest": before["cm"]["image_digest"], "payload_sha256": release["payload_sha256"],
               "cm_provenance_sha256": sha(encoded(provenance)), "renderer_build_input_sha256": build_hash,
               "renderer_asset_manifest_sha256": sha(manifest_raw), "harness_sha256": release["artifacts"][SHARE + "/tests/cm_bff_browser.mjs"],
               "campaign_runner_sha256": verify.sha256_file(Path(__file__)), "execution_config_sha256": config_hash,
               **tool_identity(config, api)}
    campaign_evidence.validate_binding(binding, release, verify)
    api.validate(config, binding, config_hash, Path(api.__file__).parent)
    binding_path = args.output_dir / "acceptance-binding.json"
    write_new(binding_path, binding)
    api.restricted(binding_path.resolve())
    binding_hash = verify.sha256_file(binding_path)
    write_new(args.output_dir / "campaign-before.json", observation("before", binding, binding_hash))
    for suite in verify.SUITES:
        print(json.dumps({"stage": suite, "status": "running"}), flush=True)
        cmd = [sys.executable, "-I", "-B", str(HERE / "run_release_check.py"), "--image", args.image,
               "--metadata", str(args.metadata.resolve()), "--oci", str(args.oci.resolve()), "--suite", suite,
               "--output", str((args.output_dir / (suite + ".json")).resolve()), "--binding", str(binding_path.resolve())]
        # run_release_check imports fixed adjacent modules; -I excludes that
        # directory, so execute with -B and an explicit scrubbed environment.
        cmd.remove("-I")
        if suite == "cm_bff_browser":
            cmd.extend(["--execution-config", str(args.execution_config.resolve())])
        command(cmd, 1900)
    after = snapshot(cluster, cfg, config, "after", before)
    compare_browser_stub_observation(args.output_dir / "cm-bff-browser-result.json", after["stub_observation"], cfg["run_id"], binding_hash)
    after_tools = tool_identity(config, api)
    require(api.read_json(args.execution_config.resolve(), True)[1] == config_hash and
            api.read_json(args.cluster_config.resolve(), True)[1] == cfg_hash and
            after_tools == {k: binding[k] for k in after_tools}, "campaign_inputs_changed")
    for filename in ("run_campaign.py", "run_release_check.py", "campaign_evidence.py", "campaign_signing.py", "verify_lite_release.py"):
        require(release["artifacts"].get(SHARE + "/" + filename) == verify.sha256_file(HERE / filename), "host_runner_changed_during_campaign")
    write_new(args.output_dir / "campaign-after.json", observation("after", binding, binding_hash))
    campaign_signing.sign_completed_campaign(args.output_dir, release, args.private_key)
    print(json.dumps({"status": "passed", "signed": True, "accepted_image_published": False, "run_id": cfg["run_id"]}))


def discover(args):
    cluster = Cluster(args.context, args.namespace)
    cm = deployment_identity(cluster, args.cm_deployment, args.cm_container)
    runtime = deployment_identity(cluster, args.runtime_deployment, args.runtime_container)
    public = cm_public_identity(cluster, cm)
    manifest = asset_manifest(cluster, cm)
    namespaces = [n["metadata"]["name"] for n in cluster.get("namespaces")["items"] if n["metadata"]["name"].startswith("hermes-acceptance-")]
    deployment = cluster.get("deployment", args.cm_deployment)
    env_names = sorted({e["name"] for c in deployment["spec"]["template"]["spec"]["containers"] for e in c.get("env", [])})
    report = {"schema_version": 1, "recorded_at": now(), "mode": "read_only_discovery", "context": args.context,
              "namespace": args.namespace, "cm": cm, "runtime": runtime, "public": public,
              "cm_env_names": env_names, "isolated_candidate_namespaces": namespaces,
              "renderer_asset_manifest_sha256": sha(encoded(manifest)), "acceptance": "not_executed"}
    write_new(args.output_dir / "cluster-discovery.json", report)
    write_new(args.output_dir / "renderer-asset-manifest.json", manifest)
    print(json.dumps({"status": "discovered", "cm_image_digest": cm["image_digest"], "isolated_candidate_namespaces": namespaces, "acceptance": "not_executed"}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="mode", required=True)
    probe = sub.add_parser("discover")
    probe.add_argument("--context", default="172-cluster")
    probe.add_argument("--namespace", default="clawmanager-system")
    probe.add_argument("--cm-deployment", default="clawmanager-app")
    probe.add_argument("--cm-container", default="clawmanager-app")
    probe.add_argument("--runtime-deployment", default="hermes-runtime")
    probe.add_argument("--runtime-container", default="runtime")
    run = sub.add_parser("execute")
    for name in ("image", "metadata", "oci", "execution-config", "cluster-config", "private-key"):
        run.add_argument("--" + name, required=True, type=str if name == "image" else Path)
    for action in (probe, run):
        action.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()
    require(not args.output_dir.exists(), "new_output_directory_required")
    private_directory(args.output_dir)
    try:
        (discover if args.mode == "discover" else execute)(args)
        return 0
    except Exception as error:
        category = str(error) if isinstance(error, GateError) else "campaign_precondition_or_execution_failed"
        write_new(args.output_dir / "campaign-failure.json", {"schema_version": 1, "status": "failed", "category": category,
                  "recorded_at": now(), "signed": False, "accepted": False})
        print(json.dumps({"status": "failed", "category": category, "signed": False, "accepted": False}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
