"""Run on the cluster build node: real offline DSH startup, not only --version."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    parser.add_argument("--logs", type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    output = args.logs or Path(tempfile.mkdtemp(prefix="dsh-startup-"))
    output.mkdir(parents=True, exist_ok=True)
    name = "cm-dsh-startup-" + uuid.uuid4().hex[:12]
    report = {"image": args.image, "passed": False}

    def docker(*command, check=True):
        return subprocess.run(["docker", *command], capture_output=True, text=True,
                              timeout=25, check=check)

    def ready():
        return docker("exec", name, "curl", "--silent", "--fail", "--max-time", "3",
                      "-H", "X-ClawManager-Instance-Token:offline-fixture",
                      "http://127.0.0.1:21000/", check=False).returncode == 0

    created = False
    try:
        command = ["run", "-d", "--name", name, "--network", "none", "--cap-drop", "ALL",
                   "--security-opt", "no-new-privileges", "--user", "200099:200099",
                   "--memory", "1g", "--pids-limit", "256", "--tmpfs", "/tmp:rw,mode=1777"]
        for setting in ("HOME=/tmp/dsh-test", "CLAWMANAGER_GATEWAY_PORT=21000",
                        "CLAWMANAGER_INSTANCE_TOKEN=offline-fixture",
                        "CLAWMANAGER_LLM_BASE_URL=http://127.0.0.1:9/v1",
                        "CLAWMANAGER_LLM_API_KEY=offline-fixture",
                        "CLAWMANAGER_LLM_MODEL=offline-model"):
            command += ["-e", setting]
        docker(*command, "--entrypoint", "/usr/local/bin/start-deepseek-harness-gateway", args.image)
        created = True
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            state = json.loads(docker("inspect", name).stdout)[0]["State"]
            if not state["Running"]:
                raise AssertionError("DSH exited before readiness: " + str(state["ExitCode"]))
            if ready():
                break
            time.sleep(1)
        else:
            raise AssertionError("DSH startup deadline exceeded")
        # Observe plugin activation AND disposal after real watched edits.
        plugin = ("import fs from 'node:fs'; export default (ctx) => { "
                  "fs.writeFileSync('/tmp/dsh-test/hmr-applied', 'yes'); "
                  "ctx.effect(() => () => fs.writeFileSync('/tmp/dsh-test/hmr-removed', 'yes')); }")
        patch = ("- insert:\n  - id: hmr-acceptance\n"
                 "    name: /tmp/dsh-test/hmr-acceptance.mjs\n")
        docker("exec", name, "python3", "-c",
               "from pathlib import Path; "
               f"Path('/tmp/dsh-test/hmr-acceptance.mjs').write_text({plugin!r}); "
               f"Path('/tmp/dsh-test/.dsh/profiles/web/cordis.patch.yml').write_text({patch!r})")
        for marker in ("hmr-applied", "hmr-removed"):
            if marker == "hmr-removed":
                docker("exec", name, "python3", "-c", "from pathlib import Path; "
                       "Path('/tmp/dsh-test/.dsh/profiles/web/cordis.patch.yml').write_text('[]')")
            for _ in range(15):
                assert ready(), "DSH did not survive live patch reload"
                if docker("exec", name, "test", "-f", "/tmp/dsh-test/" + marker, check=False).returncode == 0:
                    break
                time.sleep(1)
            else:
                raise AssertionError("live patch change was not applied: " + marker)
        report["passed"] = True
        report["authenticated_http"] = 200
        report["live_patch_reload"] = True
    except Exception as error:
        report["error"] = str(error)
    finally:
        if created:
            if not report["passed"]:
                snapshot = docker("exec", name, "cat",
                                  "/tmp/dsh-test/.dsh/profiles/web/cordis.yml",
                                  "/tmp/dsh-test/.dsh/profiles/web/cordis.patch.yml", check=False)
                (output / "profile.private.yml").write_text(snapshot.stdout)
            logs = docker("logs", name, check=False)
            (output / "startup.private.log").write_text(logs.stdout + logs.stderr)
            report["hmr_missing"] = "user patch-layer watching requires the Cordis HMR service" in logs.stderr + logs.stdout
            docker("rm", "-f", name, check=False)
        (output / "result.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
