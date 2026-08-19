# DeepSeek Harness Runtime

This directory owns the ClawManager runtime images for DeepSeek Harness.

- `Dockerfile.lite` builds the pooled, headless Lite runtime. The shared
  `clawmanager-agent` starts one isolated `dsh web` process per instance.
- `Dockerfile.pro` builds the dedicated Webtop Pro runtime. DeepSeek Harness
  runs on loopback port `3080` and opens automatically in the desktop browser.

Both images pin `@deepseek-ai/dsh` and apply the managed Cordis provider patch
from `config/managed.cordis.patch.yml`. ClawManager supplies the OpenAI-compatible
base URL, instance credential, and allowed model list at runtime.

Build both images from the repository root:

```bash
docker build -f deepseek-harness/Dockerfile.lite -t deepseek-harness-lite:local .
docker build -f deepseek-harness/Dockerfile.pro -t deepseek-harness:local .
```

## CI images

Pushes to `main` that change DeepSeek Harness or either shared agent dependency
automatically publish multi-architecture `deepseek-harness` and
`deepseek-harness-lite` images through `.github/workflows/docker-ghcr.yml`.
Each run updates `latest` and the current Shanghai-date tag.
