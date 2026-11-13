# Pinned Hermes installer

`install.sh` is an unmodified copy of the upstream installer from
[NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/scripts/install.sh)
at commit `29112bef099274229cadff79cdff7bf7b99c4b77`, the default Pro source revision in `hermes/Dockerfile`.

- Upstream Git blob: `6f717b011b9bbc3e8df2e54f3463a6a81cbb677b`
- SHA-256: `85ef536d455e51ab67aa74d79272efd49fe717597dbaadfd3cca179a905f4706`
- License: MIT; the original notice is preserved in `LICENSE` and copied into the image.

The Pro Dockerfile copies and verifies this installer before executing it, so bootstrap does not depend on a separate raw-content download during each architecture build. The installer still fetches the Hermes source and dependencies; the Dockerfile retains its source commit verification.

When changing the pinned Hermes source revision, review the matching installer, replace this file from that revision, update the Dockerfile checksum and this provenance record, and retain the upstream license.
