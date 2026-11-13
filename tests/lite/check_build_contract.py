#!/usr/bin/env python3
"""Offline structural gate for the four official Lite images (stdlib only).

Run from any directory. This cannot replace Docker builds or Linux smoke tests.
"""
from pathlib import Path
import json
import re
import shlex
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
RUNTIMES = ("openclaw", "hermes", "opencode", "deepseek-harness")


def check():
    errors = []

    def require(condition, message):
        if not condition:
            errors.append(message)

    workflow = (ROOT / ".github/workflows/docker-ghcr.yml").read_text()
    mappings = re.findall(r'"([^"|]+)-lite\|([^|]+)\|([^|]+)\|[^"|]+"', workflow)
    for runtime in RUNTIMES:
        module_vendor = ROOT / runtime / "vendor"
        require(not module_vendor.exists() or (module_vendor / "modules.txt").is_file(),
                f"{runtime}: vendor is reserved for Go dependencies and requires vendor/modules.txt; put bootstrap assets elsewhere")
        selected = [(context, dockerfile) for name, context, dockerfile in mappings if name == runtime]
        require(bool(selected), f"{runtime}: missing Lite publish mapping")
        for context, dockerfile in selected:
            require(context == "." and dockerfile == f"./{runtime}/Dockerfile.lite",
                    f"{runtime}: Lite must publish its dedicated Dockerfile with root context; got {dockerfile}")
        path = ROOT / runtime / "Dockerfile.lite"
        require(path.is_file(), f"{runtime}: missing Dockerfile.lite")
        if not path.is_file():
            continue
        content = path.read_text()
        # Join Docker continuation lines before parsing COPY and FROM.
        instructions = re.sub(r"\\\n\s*", " ", content).splitlines()
        stages = []
        for line in instructions:
            words = shlex.split(line, comments=True)
            if not words:
                continue
            if words[0].upper() == "FROM":
                args = [word for word in words[1:] if not word.startswith("--")]
                base = args[0]
                require(not re.search(r"webtop|(?:^|[-/:])pro(?:$|[-/:])", base, re.I),
                        f"{runtime}: non-Lite image ancestor {base}")
                stages.append((base, args[2] if len(args) > 2 and args[1].upper() == "AS" else ""))
            if words[0].upper() == "COPY" and not any(word.startswith("--from=") for word in words):
                sources = [word for word in words[1:] if not word.startswith("--")][:-1]
                for source in sources:
                    require(bool(list(ROOT.glob(source))), f"{runtime}: missing root-context COPY source {source}")
                require(f"{runtime}/rootfs/" not in sources,
                        f"{runtime}: must copy Lite scripts explicitly, not Pro rootfs/autostart services")
            if words[0].upper() == "RUN":
                syntax = subprocess.run(["sh", "-n"], input=line[4:], text=True, capture_output=True)
                require(syntax.returncode == 0, f"{runtime}: invalid RUN shell syntax: {syntax.stderr.strip()}")
        require(bool(stages), f"{runtime}: missing stages")
        if stages:
            ancestors = dict((alias, base) for base, alias in stages if alias)
            base = stages[-1][0]
            seen = set()
            while base in ancestors and base not in seen:
                seen.add(base)
                base = ancestors[base]
            defaults = dict(re.findall(r"(?m)^ARG ([A-Z_]+)=(.+)$", content))
            base = re.sub(r"\$\{([A-Z_]+)\}", lambda m: defaults.get(m[1], m[0]), base)
            require(bool(re.search(r"-slim[^\s]*@sha256:[0-9a-f]{64}$", base)),
                    f"{runtime}: final stage must descend from a digest-pinned slim image; got {base}")
        require(f"./cmd/{runtime}-lite-agent" in content,
                f"{runtime}: must build its own injected Lite command")
        require("-o /out/clawmanager-agent" in content and
                "/out/clawmanager-agent /usr/local/bin/clawmanager-agent" in content,
                f"{runtime}: missing operational agent binary")
        final_stage = re.split(r"(?im)^FROM .*\n", content)[-1]
        for package in ("bubblewrap", "util-linux", "tini", "bash", "git", "ripgrep", "curl", "openssh-client", "build-essential"):
            require(bool(re.search(r"\b" + re.escape(package) + r"\b", final_stage)),
                    f"{runtime}: missing sandbox/runtime tool {package} in final stage")
        require('ENTRYPOINT ["/usr/bin/tini", "--",' in content,
                f"{runtime}: missing init/reaper entrypoint")
        for forbidden in ("/etc/services.d/", "/etc/s6-overlay/", "start-opencode-pro-web", "wrapped-chromium"):
            require(forbidden not in content, f"{runtime}: Pro desktop payload {forbidden}")
        if runtime == "openclaw":
            lock = json.loads((ROOT / runtime / "openclaw102.lock.json").read_text())
            require(lock["nodeImage"] in content and "install-pinned-openclaw.mjs" in content,
                    "openclaw: must verify the locked core and Node version")
            for plugin, version in lock["channelPlugins"].items():
                require(f"{plugin}@{version} --force --accept-capabilities" in content,
                        f"openclaw: missing locked channel {plugin}")
            for patch in ("patch_managed_proxy_policy", "patch_workspace_publication_102",
                          "patch_upgrade_plugin_scratch_102", "patch_memory_core_startup_migration",
                          "patch_browser_proxy_navigation"):
                require(f"{patch}.mjs --patch" in content, f"openclaw: missing managed patch {patch}")
            for payload in ("plugins/openclaw-redis-team/", "openclaw/defaults-template/.openclaw",
                            "test_managed_plugin_payload_102.mjs --write-manifest", "chromium"):
                require(payload in content, f"openclaw: missing runtime payload {payload}")
        elif runtime == "hermes":
            lock = json.loads((ROOT / runtime / "hermes-lite.lock.json").read_text())
            for key, arg in (("package_version", "HERMES_VERSION"), ("tag", "HERMES_GIT_REF"),
                             ("commit", "HERMES_GIT_COMMIT")):
                require(f"ARG {arg}={lock[key]}" in content, f"hermes: changed locked {arg}")
            for payload in ("--generate-release --verify-release", "plugins/hermes-redis-team/",
                            "start-hermes-lite-runtime", "hermes_cli/tui_dist/entry.js"):
                require(payload in content, f"hermes: missing runtime payload {payload}")
        elif runtime == "opencode":
            require("ARG OPENCODE_VERSION=2.0.27" in content, "opencode: changed locked runtime version")
            require("start-opencode-web" in content, "opencode: missing native Web/PTY server launcher")
            # Both default publish entries must install the reviewed native tools;
            # copying only the transport beside a stock CLI silently loses Team.
            for flavor in ("Dockerfile", "Dockerfile.lite"):
                published = (ROOT / "opencode" / flavor).read_text()
                require("AS native-team-builder" in published and
                        "RUN bash /governance/build-native.sh" in published and
                        re.search(r"COPY .*--from=native-team-builder /out/opencode /usr/local/bin/opencode", published),
                        f"opencode/{flavor}: default release omits the native Team build")
                require("CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=opencode-team-v1" in published and
                        "opencode/team/" in published and "build-protocol.mjs" in published,
                        f"opencode/{flavor}: native tools and Team bridge must ship together")
        else:
            # Native DSH is installed by npm ci from reviewed lockfiles, not
            # Docker ARGs. Validate the actual version/integrity authority.
            native = ROOT / runtime / "native"
            manifest = json.loads((native / "package.json").read_text())
            dependency_lock = json.loads((native / "package-lock.json").read_text())
            source_lock = json.loads((native / "source-lock.json").read_text())
            dependencies = manifest["dependencies"]
            packages = dependency_lock["packages"]
            require(packages[""]["dependencies"] == dependencies,
                    "deepseek-harness: manifest and dependency lock disagree")
            for name in ("@deepseek-ai/dsh", "pnpm"):
                version = dependencies.get(name, "")
                locked = packages.get("node_modules/" + name, {})
                require(bool(re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version)),
                        f"deepseek-harness: {name} must use an exact native version")
                require(locked.get("version") == version and bool(locked.get("integrity")),
                        f"deepseek-harness: missing locked native dependency {name}")
            require(dependencies.get("@deepseek-ai/dsh") == source_lock.get("dshVersion") and
                    bool(source_lock.get("dshIntegrity")) and
                    packages.get("node_modules/@deepseek-ai/dsh", {}).get("integrity") == source_lock.get("dshIntegrity"),
                    "deepseek-harness: native source version/integrity drift")
            require(source_lock.get("nodeImage", "missing-node-image") in content,
                    "deepseek-harness: native Node image must match its source lock")
            for payload in ("COPY deepseek-harness/native/package.json deepseek-harness/native/package-lock.json",
                            "npm ci --prefix /opt/clawmanager-dsh --ignore-scripts",
                            "deepseek-harness/native/source-lock.json deepseek-harness/scripts/verify-native.mjs",
                            "node /opt/clawmanager-dsh/verify-native.mjs /opt/clawmanager-dsh/native-manifest.json",
                            "managed.cordis.patch.yml", "prepare-deepseek-harness-resources",
                            "start-deepseek-harness-gateway", "/usr/local/libexec/clawmanager-agent-image"):
                require(payload in content, f"deepseek-harness: missing locked runtime payload {payload}")
    return errors


if __name__ == "__main__":
    errors = check()
    for error in errors:
        print("FAIL:", error)
    print(f"{'FAIL' if errors else 'PASS'}: four Lite image build contracts ({len(errors)} failures)")
    sys.exit(bool(errors))
