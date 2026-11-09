# Hermes Desktop runtime compatibility contract 2

The authenticated Runtime control health response describes two separate facts:
the installed runtime supports the reviewed Desktop protocol, and whether that
image has completed signed end-to-end release acceptance.

```json
{
  "hermes_desktop_web": {
    "contract_version": 2,
    "enabled": true,
    "hermes_ref": "v2026.8.31",
    "hermes_commit": "29112bef099274229cadff79cdff7bf7b99c4b77",
    "rpc_protocol": "hermes-jsonrpc-v1",
    "backend_mode": "dashboard",
    "auth_mode": "password-cookie",
    "artifacts_verified": true,
    "release_accepted": false,
    "payload_sha256": "<64 lowercase hex characters>"
  }
}
```

Capability contract 2 does not alter the image's `release.json` schema 2 or its
protocol `contract_version: 1`. `release_accepted` is copied from the verified
`desktop_web_accepted` field. An unaccepted image must explicitly contain the
boolean `false` and no acceptance record. A claimed accepted image must still
pass all six reports, browser observations, operator signature and five promotion
reports; malformed or forged evidence suppresses the entire capability.

Both states require the same fixed source identity, protocol/backend/auth modes,
mandatory artifact inventory and payload hash. Startup verifies every listed
image-owned file's actual SHA-256, bounded size, ownership, permissions, path and
executable bits. The deployment must retain its managed launcher, authenticated
control/report channels, bounded trusted proxy CIDRs, internal Service endpoints
and separate instance/state paths. `CLAWMANAGER_RUNTIME_IMAGE_REF` is reported as
deployment metadata and is not treated as proof of the running image's identity;
tags and digests are both accepted. Verification runs once at startup and health
reads return a cloned immutable snapshot.

CM can use this verified protocol capability to open the authorized user's
Desktop without waiting for a browser campaign that itself requires the Desktop
to open. Existing contract 1 accepted capabilities remain compatible. CM must
strictly require the contract 2 integrity flag and payload format, and must not
interpret `release_accepted: false` as a completed signed acceptance campaign.
All CM login, ownership, instance generation, Origin, scoped cookie, ticket replay
and logout checks remain necessary at their existing boundaries.

Validation consists of Linux capability tests covering both acceptance states,
every existing forged-signature rejection, explicit boolean presence and
modified/missing/writable/symlink/non-root/non-executable candidate artifacts.
The fixed `smoke_lite_agent.py` uses the real restrictive entrypoint, a disposable
internal-Service control stub, and checks the complete capability against the
actual image release record. Five local suites and real CM browser checks remain
separate evidence; none is inferred from the capability itself.
