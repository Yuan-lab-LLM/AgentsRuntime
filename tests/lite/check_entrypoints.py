#!/usr/bin/env python3
"""Run real Lite wrappers with only exec replaced, so no daemon is started."""
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def main():
    with tempfile.TemporaryDirectory(prefix="lite-entrypoints-") as temporary:
        for runtime in ("openclaw", "hermes", "opencode", "deepseek-harness"):
            script = ROOT / runtime / "scripts" / f"{runtime}-lite-entrypoint"
            assert script.is_file(), f"{runtime}: missing Lite entrypoint"
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith(("CLAWMANAGER_", "RUNTIME_"))}
            environment.update(RUNTIME_AGENT_DATA_DIR=temporary, RUNTIME_WORKSPACE_ROOT=temporary)

            def run(**values):
                return subprocess.run(["bash", "-c", 'exec() { printf "EXEC %s\\n" "$*"; }; source "$1"',
                                       "entrypoint-check", str(script)], env=dict(environment, **values),
                                      text=True, capture_output=True)

            for role in ("slot", "pool"):
                result = run(RUNTIME_AGENT_ROLE=role)
                assert result.returncode == 64 and "EXEC" not in result.stdout, (runtime, role, result)
            result = run(RUNTIME_AGENT_ROLE="slot", RUNTIME_AGENT_CONTROL_TOKEN="fixture-control")
            assert result.returncode == 0 and "EXEC /usr/local/bin/clawmanager-agent" in result.stdout, (runtime, result)
            result = run(RUNTIME_AGENT_ROLE="pool", RUNTIME_AGENT_CONTROL_TOKEN="fixture-control")
            assert result.returncode == 64 and "EXEC" not in result.stdout, (runtime, result)
            result = run(RUNTIME_AGENT_ROLE="pool", RUNTIME_AGENT_CONTROL_TOKEN="fixture-control",
                         RUNTIME_AGENT_REPORT_TOKEN="fixture-report")
            assert result.returncode == 0 and "EXEC /usr/local/bin/clawmanager-agent" in result.stdout, (runtime, result)
            result = run(RUNTIME_AGENT_ROLE="unexpected", RUNTIME_AGENT_CONTROL_TOKEN="fixture-control")
            assert result.returncode == 64 and "EXEC" not in result.stdout, (runtime, result)
            for enabled in ("true", "TRUE", " true "):
                result = run(RUNTIME_AGENT_ROLE="slot", RUNTIME_AGENT_CONTROL_TOKEN="fixture-control",
                             CLAWMANAGER_AGENT_ENABLED=enabled)
                assert result.returncode == 64 and "EXEC" not in result.stdout, (runtime, result)
    print("PASS: four Lite entrypoints enforce pool/slot credentials and reject Pro mode")


if __name__ == "__main__":
    main()
