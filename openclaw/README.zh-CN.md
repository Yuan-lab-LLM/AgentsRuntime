# ClawManager OpenClaw Image

> 为多实例 Lite runtime 新增或升级 channel 插件前，请同时阅读
> [OpenClaw Lite Channel 适配指南](../clawmanager-agent/docs/openclaw-lite-channel-adaptation.md)。

<p align="center">
  <img src="docs//assets/openclaw_logo.jpg" alt="ClawManager" width="100%" />
</p>

<p align="center">
  <strong>Languages:</strong>
  <a href="./README.md">English</a> |
  <a href="./README.zh-CN.md">中文</a>
</p>

本指南帮助您为 **ClawManager** 控制面构建 **OpenClaw** 基础镜像，支持 **自动化配置注入**（API Key、Base URL 等）与多租户场景下的 **持久化目录布局**。

镜像现在也内置了 **OpenClaw Agent** 服务。它会在容器内自动启动，负责向 ClawManager 注册、发送心跳、轮询命令、托管 OpenClaw 进程，并提供基于 Gin 的本地健康检查接口。

**发布镜像请在仓库根目录使用 `openclaw/Dockerfile.openclaw`。** 它通过[版本锁](openclaw102.lock.json)固定 OpenClaw **2026.10.2** 和 Node **24.21.0**，校验包完整性及实际安装版本。添加工具请参考下文的 **可复现的定制构建**。


## 项目概览

在 ClawManager 批量管理场景下，逐台手动配置容器不可扩展。本项目提供：

* **锁定运行环境**：Node.js 24.21.0、OpenClaw 2026.10.2 及受管 Channel 版本，不使用无版本约束的 `openclaw@latest` 安装。
* **Agent 驱动启动**：独立／Pro 模式下，`openclaw-agent`（Go 实现）准备 `/config/.openclaw`、规范化 channels 与文件归属，并以 `abc` 启动 OpenClaw。共享池 Lite 则使用分配给每个实例的 HOME 和 Linux 身份。
* **动态注入**：用环境变量更新 `openclaw.json`，无需在桌面会话里手改文件。

---

## 快速开始（推荐）

标准镜像使用固定摘要的 `lscr.io/linuxserver/webtop:ubuntu-kde-version-cc2e18a4` 基础镜像，安装锁定的 Node.js／OpenClaw、准备 `/defaults/.openclaw` 并提供 Agent 服务。独立／Pro 模式配置位于 **`/config/.openclaw`**；共享池 Lite 使用 ClawManager 分配的每实例 HOME 与 workspace，不能让所有用户共用一份 `/config`。启动后核对 Agent 日志与实际实例健康。

`openclaw` 与 `openclaw-lite` 都使用这一 Dockerfile。Team UI 和文件编辑来自配套的 ClawManager App，Runtime 适配器提供成员执行与交付协议；只更新镜像不会更新界面，也不能安全迁移已有数据。存量实例应使用 ClawManager 数据安全升级流程，并验收真实源版本到目标版本的迁移路径。`openclaw-shell` 是独立构建，不随此版本锁升级。

### 构建

**Bash**

请在仓库根目录执行，这样 Docker 才能包含公共的 `clawmanager-agent/` 组件。

```
docker build -f openclaw/Dockerfile.openclaw -t openclaw:local .
```

### 运行

> **共享内存**：运行 WebTop 时必须传入 `--shm-size="1gb"`（至少 1GB），否则浏览器/桌面栈可能崩溃或行为异常。

**Bash**

```
docker run -d \
  --name=webtop-openclaw \
  --shm-size="1gb" \
  --restart unless-stopped \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=Asia/Shanghai \
  -e CLAWMANAGER_LLM_BASE_URL=https://your-gateway/v1 \
  -e CLAWMANAGER_LLM_API_KEY=your-sk-key \
  -e CLAWMANAGER_LLM_MODEL=gpt-4o \
  -p 3000:3000 \
  -p 3001:3001 \
  openclaw:local
```

按需调整端口与占位符。

---

## 环境变量

在 ClawManager 或 `docker run` 中设置以下变量，以写入 `openclaw.json`：

| 变量                         | 配置路径                                 | 说明                                                                      |
| ---------------------------- | ---------------------------------------- | ------------------------------------------------------------------------- |
| `CLAWMANAGER_LLM_BASE_URL` | `models.providers.auto.baseUrl`        | 网关或上游 Base URL                                                       |
| `CLAWMANAGER_LLM_API_KEY`  | `apiKey`                               | 模型 API 密钥                                                             |
| `CLAWMANAGER_LLM_MODEL`    | `primary` / `agents.defaults.models` | 模型 ID 替换；支持单值或 JSON 数组；由 agent 的配置规范化器统一处理 |
| `CLAWMANAGER_OPENCLAW_CHANNELS_JSON` | `channels`（合并）                     | JSON 对象，可含一个或多个通道键（`feishu`、`slack` 等）；与已有 `channels` 做浅合并；解析失败时容器初始化失败 |
| `OPENCLAW_AGENT_INSTANCE_ID` | agent 启动参数                           | 必填。实例唯一 ID，用于 `/api/v1/agent/register` |
| `OPENCLAW_AGENT_BOOTSTRAP_TOKEN` | agent 启动参数                      | 必填。agent 注册使用的 bootstrap token |
| `OPENCLAW_AGENT_CONTROL_PLANE_BASE_URL` | agent 启动参数             | 必填。ClawManager 基础地址 |
| `OPENCLAW_AGENT_INITIAL_CONFIG_REVISION_ID` | agent 启动参数      | 可选。初始配置 revision id |
| `OPENCLAW_AGENT_OPENCLAW_COMMAND` | 进程托管参数                  | 可选。默认是 `openclaw gateway run` |
| `OPENCLAW_AGENT_OPENCLAW_DOCTOR_COMMAND` | 启动修复参数          | 可选。默认是 `openclaw doctor --fix` |
| `OPENCLAW_AGENT_OPENCLAW_DOCTOR_POLICY` | 启动修复策略          | 可选。`auto`（默认）、`always` 或 `never`；`auto` 不会在首次拉起前执行 doctor，只会在 gateway 拉起失败、启动阶段退出或健康检查未就绪时自动修复 |
| `OPENCLAW_AGENT_OPENCLAW_STARTUP_HEALTH_TIMEOUT` | 进程托管参数 | 可选。默认 `90s`；gateway 健康检查未就绪时，等待多久后触发自动 doctor |
| `OPENCLAW_AGENT_STARTUP_NOTIFICATION_MESSAGE` | 桌面通知文案      | 可选。默认是 `正在启动龙虾` |
| `OPENCLAW_AGENT_STARTUP_REPAIR_NOTIFICATION_MESSAGE` | 桌面通知文案 | 可选。默认是 `正在修复启动环境，可能需要 1-2 分钟` |

agent 默认配置位于 `/etc/openclaw-agent/config.yaml`，初次启动时由 `/defaults/openclaw-agent/config.yaml` 填充；本地健康与调试接口默认监听 `:18080`。

---

## GitHub Actions 与 GHCR（可选）

工作流 [`.github/workflows/docker-ghcr.yml`](../.github/workflows/docker-ghcr.yml) 在匹配源码路径的更改合入 `main` 时，或在默认分支手动运行时构建。自动运行还要求 `main` 是仓库默认分支。推送开发分支、`v*` 标签或仅修改 README 不会触发发布；当前路径过滤也未包含 `plugins/**`，仅修改插件时需在合并后手动运行。

**简要步骤**

1. 将源码合入 `main`，在 **Actions** 中确认 **Build and push runtime images** 的双架构构建及最终 manifest 发布均成功。
2. 默认镜像地址是 `ghcr.io/<owner>/<repo>/openclaw` 和 `ghcr.io/<owner>/<repo>/openclaw-lite`，owner／repo 小写；自己的 fork 不会更新上游仓库的包。
3. 私有包需先执行 `docker login ghcr.io`；若希望匿名 `docker pull`，请将 Package 设为 **Public**。

**Bash**

```
docker pull ghcr.io/<owner>/<repo>/openclaw:latest

docker run -d \
  --name=webtop-openclaw \
  --shm-size="1gb" \
  --restart unless-stopped \
  -e PUID=1000 -e PGID=1000 -e TZ=Asia/Shanghai \
  -e CLAWMANAGER_LLM_BASE_URL=https://your-gateway/v1 \
  -e CLAWMANAGER_LLM_API_KEY=your-sk-key \
  -e CLAWMANAGER_LLM_MODEL=gpt-4o \
  -e CLAWMANAGER_OPENCLAW_CHANNELS_JSON='{"feishu":{"enabled":true,"accounts":{"main":{"appId":"cli_xxx","appSecret":"your-secret"}}}}' \
  -p 3000:3000 -p 3001:3001 \
  ghcr.io/<owner>/<repo>/openclaw:latest
```

成功发布后生成 `latest` 与按 Asia/Shanghai 日期命名的 `vYYYY.M.D` 标签。两者都可能移动；可复现部署应记录并使用已核对的 `@sha256` 摘要。即使 OpenClaw 版本都是 2026.10.2，新的 CI 构建也不一定与之前测试镜像的摘要相同。

不启动 Gateway、不挂载用户数据即可核对镜像中安装的实际版本：

```bash
docker run --rm --network none --entrypoint node <image>@sha256:<verified-digest> \
  -p 'require("/usr/local/lib/node_modules/openclaw/package.json").version'
```

---

## 可复现的定制构建

从已验证摘要的 Runtime 镜像派生一个纳入版本管理的 Dockerfile，添加工具后重新构建并验收。不要安装浮动版本的 Node／OpenClaw，不要用 `docker commit` 打包用户 HOME 或凭据，也不要删除持久化状态来强制初始化。旧版交互安装／commit 方法不属于正式发布或迁移流程。

```dockerfile
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
# 在此添加明确版本的工具；保留 Runtime 与 Agent 服务。
```

Overlay Dockerfile 只更新选定组件，核心版本由基础镜像决定。请按已核对的摘要选择基础镜像并重新执行相关兼容性测试；overlay 构建成功不代表已经升级 OpenClaw 到 10.2。

---

## 说明

* **权限**：保留 Agent 分配的文件归属与实例身份。独立／Pro 和共享池 Lite 的 HOME、UID 边界不同，不能把共享 Lite 工作区递归 chown 成 `abc`。
* **Docker Compose**：将 `image` 指向您构建的标签，或在仓库根目录使用 `build`，设置 `context: .` 和 `dockerfile: openclaw/Dockerfile.openclaw`。不要仅依赖官方 `webtop` 镜像，其中不包含本仓库的模板与 agent 服务。
* **单独使用 WebTop**：桌面启动成功不代表共享池 Lite 隔离、Team 交付或数据迁移已经通过验收，应分别验证托管模式。

---

## 链接

* [ClawManager - 管理AI代理最友好的方式](https://github.com/Yuan-lab-LLM/ClawManager)
