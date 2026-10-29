#!/bin/sh
# Execute as the intended unprivileged host identity, inside the actual Lite
# container/security context. A non-Linux host or missing bwrap is a failed gate.
set -eu

if [ "$(uname -s)" != Linux ]; then
  echo 'FAIL: sandbox acceptance requires a real Linux kernel' >&2
  exit 1
fi
mode=${1:-}
case "$mode" in
  '')
    if [ "$(id -u)" = 0 ] || [ "$(id -g)" = 0 ]; then
      echo 'FAIL: run as the intended non-root gateway UID/GID, or use --supervisor for the separate root control gate' >&2
      exit 1
    fi
    ;;
  --supervisor)
    if [ "$(id -u)" != 0 ]; then
      echo 'FAIL: --supervisor requires the actual root slot supervisor identity' >&2
      exit 1
    fi
    ;;
  *) echo 'FAIL: usage: acceptance-linux.sh [--supervisor]' >&2; exit 1 ;;
esac
for tool in bwrap mount unshare setsid; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: missing required acceptance tool: $tool" >&2
    exit 1
  fi
done

test_binary=${CM_SANDBOX_TEST_BINARY:-}
if [ -n "$test_binary" ]; then
  if [ ! -x "$test_binary" ]; then
    echo 'FAIL: CM_SANDBOX_TEST_BINARY must name an executable Linux test binary' >&2
    exit 1
  fi
elif ! command -v go >/dev/null 2>&1; then
  echo 'FAIL: provide CM_SANDBOX_TEST_BINARY in a slim image without Go' >&2
  exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -z "$test_binary" ]; then cd "$script_dir/.."; fi
if [ "$mode" = --supervisor ]; then
  if [ -n "$test_binary" ]; then
    CM_SANDBOX_SUPERVISOR_ACCEPTANCE=1 "$test_binary" -test.count=1 -test.timeout=90s -test.run='^TestLinuxSupervisorAcceptance$' -test.v
  else
    CM_SANDBOX_SUPERVISOR_ACCEPTANCE=1 go test -count=1 -timeout=90s ./sandbox -run '^TestLinuxSupervisorAcceptance$' -v
  fi
else
  if [ -n "$test_binary" ]; then
    CM_SANDBOX_ACCEPTANCE=1 "$test_binary" -test.count=1 -test.timeout=90s -test.run='^TestLinux(Acceptance|SeccompKernelGate)$' -test.v
  else
    CM_SANDBOX_ACCEPTANCE=1 go test -count=1 -timeout=90s ./sandbox -run '^TestLinux(Acceptance|SeccompKernelGate)$' -v
  fi
fi
