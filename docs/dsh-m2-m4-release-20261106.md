# DeepSeek Harness Team integration — 2026-11-06

## Scope and architecture

Both Team repositories remain on `codex/team-personal-integration`. Personal
repositories were read-only sources. This release adds DSH **Lite Worker** support;
OpenClaw Lite remains the only Leader, and ordinary DSH Pro remains available.
CM, northbound, owner portal and custom templates use the same admission rules.
The existing 2–6 member policy, owner boundaries, serial Team consumer, work-item
revisions, dependencies, formal receipts and Leader root closure are retained.
No new Worker argument is required.

The DSH Cordis host reuses the existing Team bridge and authoritative shared tool
schemas, including all optional result/body/artifact fields. It derives identity
from authenticated transport and native tool context. A persisted admission journal,
native source provenance and generation fences prevent duplicate or stale execution.
Only completed public assistant text can supplement a formal receipt; reasoning,
intermediate tool prose and ordinary browser/subagent sessions cannot do so.
HMR/shutdown first stops admission and confirms its owned native execution exits.

## Source and artifacts

| Component | Source / artifact |
| --- | --- |
| App implementation | `677f9f1a`, `2699a226`, compatibility correction `630d2b37`; final member projection `b3586758572afc56d6802b80170fb0279d8acc04` |
| Runtime host | `dbac27e4`, native restart check `96bf5f6` |
| Trusted native working directory | `ea2b4c07` |
| Native instruction provenance | `a4aaad23e1b9c1a18abb7bff3526a78bca3a1873`; fixture Web readiness correction `50cfefc` |
| Additive Agent heartbeat capacity field | `494debd7b99211f3b742d14e88eb9e054685bf7b` |
| Final App | `10.130.14.23:5000/clawmanager-hxc-app@sha256:cc14c91211ab1f77b9c65ff605654817276b1a140c5c0d0a27918a8d31ed14b2` |
| Final DSH Lite | `10.130.14.23:5000/agentsruntime/deepseek-harness-lite@sha256:ab576d3f22de8cff2ea61fe1892959ff10a9216a9635aeb29338fbcae70dabcf` |
| Native / capability | `0.2.1-rc.3` / image-owned `dsh-team-v1` |

The final Runtime contains a newly compiled current Agent. The image is built with
`deepseek-harness/Dockerfile.team`, using the pinned accepted M1 base and explicit
`agent-binaries` context. Pro and other Runtime images were not rebuilt or replaced.

## Necessary validation

- Focused host, schema, shared bridge/outbox/recovery and DSH Go checks passed.
- The real pinned native process plus disposable Redis passed tool schema,
  status/summary-only receipt, artifact, public supplement, duplicate input and actual
  process restart checks under an unprivileged UID, dropped capabilities and noexec
  temporary filesystem. Its model and acknowledgement are fixtures; this is **not**
  the production model/completion-gate acceptance.
- App backend Linux checks passed. Failed packages from the initial incomplete
  mount were rerun with the complete repository and Runtime mounted; Windows-only
  POSIX path/hardlink failures were not worked around by weakening security.
  An actual Hermes error-message compatibility regression was fixed in `630d2b37`.
- Frontend Team creation checks and production build passed. No schema migration
  or change to the stable scheduling algorithm was introduced.
- Final member-state checks reproduced an old failed root's retained `running`
  card overriding a newer successful assignment. `b3586758` excludes only cards
  whose root is independently confirmed terminal from the operational projection;
  historical ledger rows remain unchanged, unknown roots are retained, and current
  work/Leader ownership is protected. Focused state regressions and the affected
  Linux services suite passed (41.125 seconds) for this final change; unrelated frontend/full
  backend matrices are not repeated.

## Deployment and operational findings

Target is only `10.130.14.23:32543`, namespaces
`clawmanager-hxc-peer-system` and `clawmanager-hxc-peer-user-1`.
The App was applied from `/home/hxc` with `TENANT_SUFFIX=-hxc-peer`,
`NODE_PORT=32543`. Initial App tag was `team-dsh-20261106-v1`; the final member
projection correction uses `APP_IMAGE=10.130.14.23:5000/clawmanager-hxc-app:team-dsh-20261106-v2`.
Snapshots and restricted deployment evidence are in
`/home/hxc/team-dsh-m4-20261106/`; they contain operational configuration and must
not be copied into Git.

The site still uses the compatibility process-pool contract. A separate
`deepseek-harness-team-fixed-runtime` was introduced, leaving existing Runtime pools,
ordinary DSH Lite #589 and Pro #590 intact. This is not a global native-slot
migration. The Agent limit is 3 gateways per new Pod; the existing Core's configured
capacity policy takes precedence in its database and reports 100. The new additive
heartbeat field preserves the actual Agent value and the old `max_gateways` field;
this release does not change that pre-existing Core policy.

The first real dispatch exposed a project-directory variable supplied by OpenCode
but absent in DSH. It failed **before** native model/session admission. The DSH host
now obtains the managed process working directory itself. The regression fixture
explicitly removes both project-path variables and passes. A second real-only
difference was native `agent-instructions` context inserted after the Team input.
The provenance guard now recognizes that exact native context alongside time/runtime
context while still rejecting human, unknown, foreign Team and retired-generation
inputs. The real native regression loads AGENTS.md and independently checks that its
persisted context was exercised. Initial images v1–v3 are superseded by v4 above.

Test Teams are CM #178 and owner custom #179. Original CM root #383 was formally
failed by its Leader after the initial adapter fault and remains failed; it is not
rewritten as success. The final-image CM root is #386. Northbound root #385 retained
its original calculation work item #471/revision 1, then OpenCode verified it in
#472/revision 1 and its Leader formally closed the root with #473. No SQL task status
was edited. A removed process-pool Pod is recovered by the existing
independently confirmed stale-Pod failover, whose threshold is three heartbeat
timeouts; it is not safe to assume a six-minute heartbeat suspicion is immediate
failover. The subsequent v4 transition first prepared a compatible replacement pool,
paused admission on only the two superseded test Pods, used the existing fenced
restart API for our two DSH members, and confirmed the old pool had no bindings
before scaling it to zero. Ordinary pool #369 and its data were retained.
CM #383's remaining native turn blocked queued root #386 after formal failure;
only after confirming #383 failed and #386 had no assigned work, the two owned
CM members were restarted through the existing lifecycle API to retire that turn.
This was controlled acceptance recovery, not a new automatic interruption policy.
After northbound root #385 and every work item were terminal, the owned DSH member
#594 was also restarted through the fenced lifecycle API to retire an old
post-result native approval from fault recovery. Accepted results, native history
and shared files were retained; this did not create or rerun a business work item.

## Real acceptance outcome

Both real roots succeeded through the production completion gate:

| Entry | Real result |
| --- | --- |
| CM Team #178 / root #386 | DSH work #474, `assign-386-compute`, revision 1; Leader synthesis #475; formal root closure |
| Northbound custom Team #179 / root #385 | DSH work #471, `a-calc-17x29`, revision 1; OpenCode validation #472 targeting revision 1; Leader synthesis #473; formal root closure |

Each Worker has an explicit member-only receipt, a unique accepted work item and
readable Markdown containing the calculation result 493. No Worker closed a root.
Actual file readback through the corresponding CM/owner API established:

- CM DSH artifact: 200 bytes,
  `443999229688f4da9157c8c6368bb1fd832069e8634333896a71c1e32fda6edd`.
- CM final report: 1445 bytes,
  `9bc59c51c4c55c9eca4bd43c7504286114bf94feeef1dc62116761f2a1134a52`.
- Northbound DSH artifact: 1308 bytes,
  `b1b30f63199f87dde1fb63f00a6987e1a603e76a476f28c2f7ef69e94a6f9eda`.
- Northbound OpenCode review: 4823 bytes,
  `83baf6fbdebeafbf3307d8a6667e4ea6b6e029903bd8d061f00f45f0813a6a4e`.
- Northbound final report: 3139 bytes,
  `dba5cf481f914c8d447226cf8199fba6c5c02fe8785de1378eb445ea72f6b654`.

The two DSH native entry/re-entry checks returned HTTP 200 and public native records
showed authenticated Team sources, real tool calls and completed turns, with no
human inputs. The native history scan for owner member #594 produced no
`question_v4` records, while the existing human-only parser regression passed;
automated Team instructions did not become human questions/XP.

Live CM and northbound custom requests rejected DSH Leader and DSH Pro with 422;
cross-owner Team/files and CM-resource-through-owner requests returned 404. The
invalid requests left the owner Team list unchanged. Owner ordinary #589 Lite and
#590 Pro remained running. All original 29 Runtime Pod UIDs/images and all original
Pro Pod UIDs were retained. Existing Team175/task381's report remains 3166 bytes,
`1f942bcccfa6ff3c266e5e7ea622fc8c0caa91c202f2e698056561d3b4d28de5`.
After final App deployment, CM #178's two members converged to idle, and all three
northbound #179 member business statuses were idle. Historical failed-root work
#470 remains unchanged and no longer reopens the member's operational state.

After both roots succeeded, Core's protected-image configuration and the Lite image
catalog were aligned with the final v4 digest. Actual deployed server/frontend,
Agent and host hashes were compared with the release artifacts; the native
dependency verifier reported `teamEnabled=true`, `teamProtocol=dsh-team-v1`.
Sensitive credentials, access URLs, native reasoning and deployment snapshots are
not in this document or Git. Local ignored evidence is under
`ClawManager-2/team-optimization-docs/dsh-m2-20261106/`, including
`real-acceptance.json`, `live-policy-checks.json`, `team-regression.json`,
`native-public-proof.json` and `final-deployed-artifacts.log`.

The apply script's last **server-originated** callback to port 32556 timed out
(HTTP 000 / exit 1) after applying the deployments. It was not blindly rerun.
Both deployments were independently observed Ready, and fresh client-originated
northbound challenge/JWE login, owner Team queries, artifact/native re-entry and
the App version endpoint passed. Final App reports version `team-dsh-20261106-v2`,
commit `b3586758572afc56d6802b80170fb0279d8acc04`. This separates the callback
failure from the verified deployment outcome; it does not claim the apply script
exited successfully. Server `final-verified.json` retains that distinction.

## Rollback

Stop new DSH Team admission/placements first and preserve existing Team IDs,
members, work items, sessions, shared files and receipts. Confirm active native
execution has stopped before changing a binding or image. Keep an App that knows
DSH Team resources; do not downgrade to an App that misclassifies them as OpenClaw.
Restore only reviewed deployment differences from the restricted snapshots, not an
entire stale manifest over later changes. New native session formats require an
isolated compatibility check before returning to an older DSH package. Never erase
NFS/session directories to make rollback appear successful.

## Follow-on maintenance

The deployment remains a compatibility process pool; enabling global native slots
is a separate migration, not a hidden outstanding prerequisite for DSH Team.
The real first-run faults showed that a minimal fixture must include the actual
supervisor cwd and native AGENTS.md behavior. Both are now exercised in one pinned
image test. Future changes should run that first, then one small real Team path;
do not repeat this release's avoidable image iterations or treat fixture success
as proof that production wiring is correct. Initial failed acceptance records are
kept to make the repair verifiable, rather than rewriting failures as success.
