# DSH M0 native contract (2026-11-05)

Target: Team `codex/team-personal-integration`. Personal source is read-only.
DSH is fixed at 0.2.1-rc.3, with the complete dependency graph/integrities in
`deepseek-harness/native/package-lock.json`; Node is fixed by digest in source-lock.
This replaces neither the Team modular Agent nor the isolated-slot architecture.

The actual fixed Linux installation passed `tests/native-contract.mjs` with a
local HTTP model fixture and no external network/model request:

- Web session create/prompt, native tool registration and public final output.
- Tool context supplies the actual Agent/session and native call identity.
- Agent exposes followup/cancel/whenIdle/inject. A pending model request is
  actually cancelled and the native session reaches idle.
- A custom `clawmanager-team` non-human source persists in the real event log.
  This version's source union is extensible; there is no generic `plugin` kind.
- The same session resumes after native Web process restart, retaining its tool
  result and public conversation. Five fixture model requests were observed.

This proves the extension contract only. It does not install a Team consumer,
register production Team tools, validate Redis dispatch, or claim real-model
Team acceptance. Native idle remains distinct from a formal business receipt.
The future adapter must persist input identity before an uncertain submission;
`createUserMessage` allocates a fresh ID, so blindly calling it again is not retry.

The new sandbox original SHA256 is recorded in source-lock. Its reviewed Linux
bwrap function still creates tmpfs only for workspace-write; the Landlock
allowlist still grants that workspace and /tmp. M1 must preserve the outer
instance-private persistent /tmp without weakening read-only profiles or other
instance boundaries. The old 0.1.2 artifact hash must not be accepted as evidence
for this new package. Cordis4.0.5/HMR0.2.1/Loader1.0.6 form the actual new graph;
do not force the old pinned4.0.2/1.0.17/1.0.3 family over it.

Run the contract in a task-owned Linux install of the locked native package:
`node deepseek-harness/tests/native-contract.mjs /path/to/native-install`.
The directory must be writable for the test-only host plugin. Nothing from
`tests/` is installed into the production Runtime image.
