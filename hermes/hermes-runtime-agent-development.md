# Hermes Runtime Image and Agent Development Guide

本文面向 Hermes 镜像开发端，说明如何基于 Webtop 构建 ClawManager 可管理的 Hermes runtime 镜像，以及 Hermes 如何通过共享 `clawmanager-agent` 接入 ClawManager 的 Agent Control Plane，使 Hermes 像 OpenClaw 一样具备实时状态上报、健康信息上报、skill inventory 同步、skill 包上传和命令轮询能力。

## 目标

Hermes 镜像需要同时满足两层要求：

- 桌面访问层：沿用 Webtop/KasmVNC 运行方式，ClawManager 通过实例 Service 的 `3001` 端口访问桌面。
- Runtime agent 层：镜像内常驻一个 Hermes agent，向 ClawManager 控制面注册、心跳、上报状态、同步 skill，并执行平台下发的命令。

当前 Hermes runtime 在 ClawManager 中按 Webtop 基线接入：

- 端口：`3001`
- 持久化目录：`/config/.hermes`
- 默认标题：`Hermes Runtime`
- 代理路径：由 ClawManager 在创建实例时把 `SUBFOLDER` 改写为 `/api/v1/instances/{instance_id}/proxy/`

不要在镜像里改用其他端口或持久化目录，否则实例代理、PVC 挂载和用户数据持久化都会偏离平台预期。

## 镜像构建要求

建议以 LinuxServer Webtop 镜像作为基础镜像，例如：

```dockerfile
FROM lscr.io/linuxserver/webtop:ubuntu-xfce

USER root

# 1. 安装 Hermes runtime 依赖。
# 这里保留为示例，具体安装方式由 Hermes 项目维护。
# RUN apt-get update && apt-get install -y ... && rm -rf /var/lib/apt/lists/*

# 2. 安装 Hermes 本体。
# COPY hermes /opt/hermes

# 3. 安装共享 ClawManager agent。
COPY clawmanager-agent /usr/local/bin/clawmanager-agent
RUN chmod +x /usr/local/bin/clawmanager-agent

# 4. 注册 s6 longrun 服务，让 agent 随 Webtop 容器启动。
COPY root/ /

ENV TITLE="Hermes Runtime"
ENV SUBFOLDER="/"

EXPOSE 3001
```

Webtop 使用 s6 overlay 管理服务。Hermes agent 可以以 longrun 服务形式启动：

```text
root/
  etc/
    s6-overlay/
      s6-rc.d/
        hermes-agent/
          type
          run
        user/
          contents.d/
            hermes-agent
```

`type`：

```text
longrun
```

`run`：

```bash
#!/usr/bin/with-contenv bash
set -euo pipefail

if [ "${CLAWMANAGER_AGENT_ENABLED:-false}" != "true" ]; then
  echo "ClawManager Hermes agent disabled"
  sleep infinity
fi

exec /usr/local/bin/clawmanager-agent
```

Hermes agent 不要占用 `3001`。`3001` 是 Webtop 桌面访问入口，agent 只需要作为后台进程向 ClawManager 发起出站 HTTP 请求。

## ClawManager 注入的环境变量

Hermes 镜像必须从环境变量读取配置，不能把 ClawManager 地址、实例 ID、token 或路径写死。

基础 Webtop 变量：

| 变量 | 说明 |
| --- | --- |
| `TITLE` | 桌面标题，Hermes 默认为 `Hermes Runtime` |
| `SUBFOLDER` | 反向代理子路径，运行时由 ClawManager 改写 |
| `HTTP_PROXY` / `HTTPS_PROXY` | 如平台启用 egress proxy，会自动注入 |
| `NO_PROXY` | 平台内服务和 localhost 会自动加入 |

Agent 控制面变量：

| 变量 | 说明 |
| --- | --- |
| `CLAWMANAGER_AGENT_ENABLED` | 为 `true` 时启动 Hermes agent |
| `CLAWMANAGER_AGENT_BASE_URL` | ClawManager Agent API 根地址，不带 `/api/v1/agent` 后缀 |
| `CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN` | 首次注册用的一次性 bootstrap token |
| `CLAWMANAGER_AGENT_INSTANCE_ID` | 当前实例 ID |
| `CLAWMANAGER_AGENT_PROTOCOL_VERSION` | 当前为 `v1` |
| `CLAWMANAGER_AGENT_PERSISTENT_DIR` | 持久化目录，Hermes 为 `/config/.hermes` |
| `CLAWMANAGER_AGENT_DISK_LIMIT_BYTES` | 实例磁盘配额字节数 |

资源注入变量：

| 变量 | 说明 |
| --- | --- |
| `CLAWMANAGER_HERMES_CHANNELS_JSON` | 创建实例时注入的 Channel 配置 |
| `CLAWMANAGER_HERMES_SKILLS_JSON` | 创建实例时注入的 Skill 配置 |
| `CLAWMANAGER_HERMES_BOOTSTRAP_MANIFEST_JSON` | 本次注入的资源清单 |
| `CLAWMANAGER_RUNTIME_CHANNELS_JSON` | Runtime 通用 Channel 配置别名 |
| `CLAWMANAGER_RUNTIME_SKILLS_JSON` | Runtime 通用 Skill 配置别名 |
| `CLAWMANAGER_RUNTIME_BOOTSTRAP_MANIFEST_JSON` | Runtime 通用资源清单别名 |

为了兼容现有 OpenClaw 资源中心，ClawManager 也会保留 `CLAWMANAGER_OPENCLAW_*` 原始变量。Hermes agent 优先读取 `CLAWMANAGER_HERMES_*`，如果不存在再回退到 `CLAWMANAGER_RUNTIME_*` 或 `CLAWMANAGER_OPENCLAW_*`。

平台侧注意：当前 ClawManager 代码里的 Agent Control Plane 仍保留部分 OpenClaw 历史字段名。在协议字段重命名前，Hermes agent 先复用 `openclaw_status`、`openclaw_pid`、`openclaw_version` 这些兼容字段来表达 Hermes runtime 状态。

## Channel 和 Skill 注入消费规范

Hermes agent 需要同时处理两类注入：

- Runtime bootstrap 注入：创建实例时选择的 Channel、配置型 Skill、Session Template、Agent、Scheduled Task 等资源会通过环境变量注入容器。
- 平台 Skill 安装注入：创建实例时勾选的可复用 skill 会先绑定到实例，随后由 ClawManager 下发 `install_skill` 命令，agent 需要下载并安装对应 skill 包。

### 读取顺序

启动时按下面优先级读取配置，读取到第一个非空值即可：

| 资源 | 优先读取 | 回退读取 |
| --- | --- | --- |
| Manifest | `CLAWMANAGER_HERMES_BOOTSTRAP_MANIFEST_JSON` | `CLAWMANAGER_RUNTIME_BOOTSTRAP_MANIFEST_JSON`，`CLAWMANAGER_OPENCLAW_BOOTSTRAP_MANIFEST_JSON` |
| Channels | `CLAWMANAGER_HERMES_CHANNELS_JSON` | `CLAWMANAGER_RUNTIME_CHANNELS_JSON`，`CLAWMANAGER_OPENCLAW_CHANNELS_JSON` |
| Config Skills | `CLAWMANAGER_HERMES_SKILLS_JSON` | `CLAWMANAGER_RUNTIME_SKILLS_JSON`，`CLAWMANAGER_OPENCLAW_SKILLS_JSON` |
| Session Templates | `CLAWMANAGER_HERMES_SESSION_TEMPLATES_JSON` | `CLAWMANAGER_RUNTIME_SESSION_TEMPLATES_JSON`，`CLAWMANAGER_OPENCLAW_SESSION_TEMPLATES_JSON` |
| Agents | `CLAWMANAGER_HERMES_AGENTS_JSON` | `CLAWMANAGER_RUNTIME_AGENTS_JSON`，`CLAWMANAGER_OPENCLAW_AGENTS_JSON` |
| Scheduled Tasks | `CLAWMANAGER_HERMES_SCHEDULED_TASKS_JSON` | `CLAWMANAGER_RUNTIME_SCHEDULED_TASKS_JSON`，`CLAWMANAGER_OPENCLAW_SCHEDULED_TASKS_JSON` |

Scheduled Task 使用 OpenClaw cron 标杆（`schedule` + `payload` + `delivery`，format=`task/openclaw-cron@v1`）。Hermes 启动时会把该 JSON **翻译**为原生 `~/.hermes/cron/jobs.json`（托管 id `cm-st-*`），由 gateway 内置 cron 执行。`announce` 映射为 Hermes `deliver`；`webhook` 映射为 `deliver=local` + prompt 内必填 POST 说明，并写入 `cron/webhooks/cm-st-*.url`。

补充说明：

- apply 为 soft-fail：解析/翻译失败写入 bootstrap `scheduled-tasks` 状态，不中断启动。
- 相同 payload sha256 且 managed job 数量一致时跳过重写 `jobs.json`。
- OpenClaw 字段 `wakeMode` / `sessionTarget` 在 Hermes 侧故意忽略（`ignored_fields`）；Hermes cron 始终使用全新 agent 会话。平台资源仍保留这些字段以兼容 OpenClaw。

如果某个变量不存在或为空，按空配置处理，不要让 agent 启动失败。只有变量存在但 JSON 无法解析时，agent 应记录清晰错误，并在 state report 的 `health.bootstrap_config` 或 `health.config_loader` 中标记为 `error`。

建议 agent 把本次读取到的原始 JSON、解析后的配置和 hash 写入：

```text
/config/.hermes/hermes-agent/bootstrap/
  manifest.json
  channels.json
  skills.json
  applied-state.json
```

日志中不要打印 Channel token、secret、webhook、bootstrap token 或 Gateway API key。

### Channel 注入

`CLAWMANAGER_HERMES_CHANNELS_JSON` 是一个以 resource key 为键的 JSON object。示例：

```json
{
  "feishu": {
    "enabled": true,
    "domain": "feishu",
    "defaultAccount": "main",
    "accounts": {
      "main": {
        "appId": "cli_xxx",
        "appSecret": "secret",
        "enabled": true
      }
    },
    "requireMention": true
  },
  "telegram": {
    "enabled": true,
    "botToken": "123456:xxx",
    "dmPolicy": "open",
    "allowFrom": ["*"]
  }
}
```

Hermes agent 应该：

1. 以顶层 key 作为 channel ID，例如 `feishu`、`telegram`、`slack`、`dingtalk-connector`。
2. 跳过 `enabled=false` 的 channel，但保留配置文件，方便后续热更新。
3. 将 channel 配置转换为 Hermes 自己的通知/消息通道配置，推荐落盘到 `/config/.hermes/channels.json` 或 Hermes 原生配置目录。
4. 保留未知字段，避免 ClawManager 后续扩展字段时 agent 丢配置。
5. 如果 Hermes 暂不支持某类 channel，把该 channel 标记为 unsupported，并在 state report 的 `health.channels` 中说明，不要影响 agent 注册和心跳。

### 配置型 Skill 注入

`CLAWMANAGER_HERMES_SKILLS_JSON` 是配置资源列表，不是 zip 包。它用于把资源中心里的配置型 skill 随实例创建注入给 runtime。示例：

```json
{
  "schemaVersion": 1,
  "items": [
    {
      "id": 5,
      "type": "skill",
      "key": "support-bot",
      "name": "Support Bot",
      "version": 1,
      "tags": ["skill"],
      "content": {
        "schemaVersion": 1,
        "kind": "skill",
        "format": "skill/custom@v1",
        "dependsOn": [],
        "config": {
          "prompt": "help"
        }
      }
    }
  ]
}
```

Hermes agent 应该：

1. 遍历 `items`，以 `key` 作为稳定 skill 标识。
2. 读取 `content.config`，转换为 Hermes 可执行的 skill 配置。
3. 如果 Hermes 把 skill 以目录管理，建议写入 `/config/.hermes/skills/{key}/skill.json`，并把原始 `content` 一起保存，便于排查。
4. 对已落盘的注入 skill 计算 `content_md5`，在后续 `skills/inventory` 中上报。
5. 对这类通过 bootstrap 配置生成的 skill，inventory 的 `source` 建议填 `injected_by_clawmanager` 或 `bootstrap_config`；如果填 `injected_by_clawmanager` 且 `skill_id` 使用平台外部 ID，格式应为 `skill-{id}`，例如 `skill-5`。

配置型 Skill 和平台 Skill 包安装是两条路径：

- `CLAWMANAGER_HERMES_SKILLS_JSON`：启动时读取并应用，不需要先等命令。
- `install_skill` 命令：运行期下载平台上传的 zip skill 包，并安装到 `/config/.hermes/skills`。

### Bootstrap manifest

`CLAWMANAGER_HERMES_BOOTSTRAP_MANIFEST_JSON` 描述本次注入了哪些 payload。示例：

```json
{
  "schemaVersion": 1,
  "mode": "manual",
  "resources": [
    { "id": 5, "type": "skill", "key": "support-bot", "name": "Support Bot", "version": 1 }
  ],
  "payloads": [
    { "env": "CLAWMANAGER_HERMES_CHANNELS_JSON", "count": 1 },
    { "env": "CLAWMANAGER_HERMES_SKILLS_JSON", "count": 1 }
  ]
}
```

Agent 可用 manifest 做幂等判断：如果 manifest hash 没变化，可以跳过重复应用；如果 hash 变化，需要重新应用 channel 和配置型 skill，然后补发一次 state report 和一次 full skill inventory。

### 创建实例时勾选的 Skill

创建 Hermes 实例时，如果用户勾选了平台已有 skill，ClawManager 会把 skill 先绑定到实例，并创建 `install_skill` 命令。Hermes agent 必须实现这个命令，否则用户在创建页看到的 Skill Injection 只会停留在平台记录里，实例内不会真正安装。

`install_skill` 命令 payload 示例：

```json
{
  "skill_id": "skill-12",
  "skill_version": "skill-version-34",
  "target_name": "weather-tool",
  "content_md5": "d41d8cd98f00b204e9800998ecf8427e"
}
```

处理流程：

1. 收到 `install_skill` 后，调用下载接口：

```http
GET {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/skills/versions/{skill_version}/download
Authorization: Bearer {session_token}
```

2. 校验 zip，拒绝绝对路径、`../`、多顶层目录和超出 `/config/.hermes/skills` 的解压路径。
3. 解压到 `/config/.hermes/skills/{target_name}`，必要时用临时目录加原子替换。
4. 重新计算目录 `content_md5`，与 payload 中的 `content_md5` 比对；不一致时 finish 为 `failed`。
5. 发送一次 `skills/inventory`，对应 skill 的 `source` 填 `injected_by_clawmanager`，`skill_id` 填 payload 里的 `skill_id`。
6. 调用 command finish，结果中带 `install_path`、`skill_id`、`skill_version`、`content_md5`。

## Agent 生命周期

Hermes agent 启动后按以下顺序工作：

1. 读取 `CLAWMANAGER_AGENT_*` 环境变量。
2. 如果没有本地可用 session token，调用注册接口。
3. 将返回的 session token 保存到 `/config/.hermes/hermes-agent/session.json`。
4. 按服务端返回的心跳间隔发送 heartbeat。
5. 按服务端返回的命令轮询间隔拉取命令；如果 heartbeat 返回 `has_pending_command=true`，立即拉取一次。
6. 定期发送完整 state report 和 skill inventory。
7. session token 过期或接口返回 401 时，使用 bootstrap token 重新注册。

建议本地状态目录：

```text
/config/.hermes/hermes-agent/
  session.json
  state.json
  logs/
  cache/
```

## 注册

请求：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/register
Authorization: Bearer {CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN}
Content-Type: application/json
```

Body 示例：

```json
{
  "instance_id": 123,
  "agent_id": "hermes-123-main",
  "agent_version": "0.1.0",
  "protocol_version": "v1",
  "capabilities": [
    "runtime.status",
    "runtime.health",
    "metrics.report",
    "skills.inventory",
    "skills.upload",
    "commands.poll"
  ],
  "host_info": {
    "runtime": "hermes",
    "desktop_base": "webtop",
    "persistent_dir": "/config/.hermes",
    "port": 3001,
    "arch": "amd64"
  }
}
```

响应中的 `data.session_token` 是后续所有 Agent API 的认证 token。Agent 应缓存它，但不要写入日志。

## 心跳

请求：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/heartbeat
Authorization: Bearer {session_token}
Content-Type: application/json
```

Body 示例：

```json
{
  "agent_id": "hermes-123-main",
  "timestamp": "2026-04-27T14:30:00Z",
  "openclaw_status": "running",
  "summary": {
    "runtime": "hermes",
    "hermes_status": "running",
    "hermes_pid": 245,
    "skill_count": 8,
    "active_skill_count": 8,
    "disk_used_bytes": 2147483648,
    "disk_limit_bytes": 10737418240
  }
}
```

兼容要求：

- `openclaw_status` 当前仍是平台字段名，Hermes agent 先填 Hermes 主进程状态。
- 状态建议使用 `starting`、`running`、`stopped`、`error`、`unknown`。
- 心跳默认约每 15 秒一次，以注册响应里的 `heartbeat_interval_seconds` 为准。
- ClawManager 45 秒内收到心跳显示为 online，45 到 120 秒显示 stale，超过 120 秒显示 offline。

## 完整状态上报

Heartbeat 用于轻量在线状态。更完整的运行时状态、系统信息和健康信息通过 state report 上报。

请求：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/state/report
Authorization: Bearer {session_token}
Content-Type: application/json
```

Body 示例：

```json
{
  "agent_id": "hermes-123-main",
  "reported_at": "2026-04-27T14:30:00Z",
  "runtime": {
    "openclaw_status": "running",
    "openclaw_pid": 245,
    "openclaw_version": "hermes-0.4.0"
  },
  "system_info": {
    "runtime": "hermes",
    "os": "ubuntu",
    "desktop_base": "webtop",
    "sampled_at": "2026-04-27T14:30:00Z",
    "cpu": {
      "cores": 2,
      "load": {
        "1m": 0.64,
        "5m": 0.52,
        "15m": 0.40
      }
    },
    "memory": {
      "mem_total_bytes": 4294967296,
      "mem_available_bytes": 2147483648
    },
    "disk": {
      "mount_path": "/config/.hermes",
      "root_total_bytes": 10737418240,
      "root_free_bytes": 8589934592
    },
    "network": {
      "interfaces": [
        {
          "name": "eth0",
          "status": "up",
          "addresses": ["10.42.0.12"],
          "rx_bytes": 123456789,
          "tx_bytes": 98765432
        }
      ]
    }
  },
  "health": {
    "hermes_process": "ok",
    "desktop": "ok",
    "agent": "ok",
    "metrics_collector": "ok",
    "metrics_sample_interval_seconds": 5,
    "last_skill_scan_at": "2026-04-27T14:29:30Z"
  }
}
```

上报建议：

- heartbeat：按服务端间隔执行。
- state report：启动后立即上报一次，之后每 5 到 10 秒上报一次；如果只需要在线状态，heartbeat 仍按服务端间隔执行。
- Hermes 主进程状态变化、skill inventory 变化、命令执行完成后立即补发一次 state report。

## 监测数据上报规范

ClawManager 不单独提供 CPU、内存、磁盘、网络的专用上报接口。Hermes agent 需要把每次采样结果放进 state report 的 `system_info` 字段。后端会原样保存这段 JSON，前端实例详情页会按下面的字段读取并绘制 5 分钟窗口内的趋势。

### 必填字段

| 路径 | 类型 | 单位 | 说明 |
| --- | --- | --- | --- |
| `system_info.sampled_at` | string | ISO 8601 UTC | agent 采样时间 |
| `system_info.cpu.cores` | number | 核数 | 当前容器可用 CPU 核数 |
| `system_info.cpu.load.1m` | number | load average | 1 分钟 load average |
| `system_info.cpu.load.5m` | number | load average | 5 分钟 load average |
| `system_info.cpu.load.15m` | number | load average | 15 分钟 load average |
| `system_info.memory.mem_total_bytes` | number | bytes | 容器内存上限或系统总内存 |
| `system_info.memory.mem_available_bytes` | number | bytes | 当前可用内存 |
| `system_info.disk.root_total_bytes` | number | bytes | `/config/.hermes` 所在文件系统总容量 |
| `system_info.disk.root_free_bytes` | number | bytes | `/config/.hermes` 所在文件系统剩余容量 |
| `system_info.network.interfaces[].name` | string | 无 | 网卡名称，例如 `eth0` |
| `system_info.network.interfaces[].status` | string | 无 | 建议填 `up` 或 `down` |
| `system_info.network.interfaces[].rx_bytes` | number | bytes | 网卡累计接收字节数 |
| `system_info.network.interfaces[].tx_bytes` | number | bytes | 网卡累计发送字节数 |

CPU 展示百分比由前端按 `load.1m / cores * 100` 计算并限制在 0 到 100。Hermes agent 不需要额外上报 `cpu_percent`。

内存展示百分比由前端按 `(mem_total_bytes - mem_available_bytes) / mem_total_bytes * 100` 计算。磁盘展示百分比由前端按 `(root_total_bytes - root_free_bytes) / root_total_bytes * 100` 计算。

网络速率由前端对 `rx_bytes`、`tx_bytes` 做相邻两次采样的差值计算。Hermes agent 应上报单调递增的累计 counter，不要把瞬时速率填到 `rx_bytes` 或 `tx_bytes`。如果容器重启导致 counter 归零，前端会从下一次有效采样重新计算。

### 采样来源建议

- CPU load：读取 `/proc/loadavg` 的前三个值。
- CPU cores：优先读取 cgroup CPU 配额。cgroup v2 可用 `/sys/fs/cgroup/cpu.max`，没有配额时使用 `/proc/cpuinfo` 或语言运行时返回的 CPU 数。
- 内存：优先读取 cgroup 内存限制。cgroup v2 可用 `/sys/fs/cgroup/memory.max` 和 `/sys/fs/cgroup/memory.current`，其中 `mem_available_bytes = memory.max - memory.current`，需要限制为不小于 0。如果没有 cgroup 限制，使用 `/proc/meminfo` 的 `MemTotal` 和 `MemAvailable`。
- 磁盘：对 `/config/.hermes` 调用 `statvfs`。字段名仍使用 `root_total_bytes`、`root_free_bytes`，但语义是 Hermes 持久化目录所在文件系统。
- 网络：读取 `/proc/net/dev`。建议默认排除 `lo`，并保留 `eth0` 等业务网卡的累计 `rx_bytes`、`tx_bytes`。

### 上报频率

- 启动成功后立即发送一次包含完整 `system_info` 的 state report。
- 正常运行时每 5 秒采样并上报一次，和 ClawManager 实例详情页的轮询节奏一致。
- 如果担心开销，可以放宽到每 10 秒一次；采样间隔不要短于 2 秒，避免制造无意义的写入压力。
- 收到 `collect_system_info` 或 `health_check` 命令时，立即采样一次，先发送 state report，再 finish 命令。

### 命令结果建议

`collect_system_info` 的 finish `result` 可以复用同一份快照，便于排查：

```json
{
  "agent_id": "hermes-123-main",
  "status": "succeeded",
  "finished_at": "2026-04-27T14:31:05Z",
  "result": {
    "sampled_at": "2026-04-27T14:31:05Z",
    "system_info": {
      "cpu": {
        "cores": 2,
        "load": {
          "1m": 0.70,
          "5m": 0.55,
          "15m": 0.42
        }
      },
      "memory": {
        "mem_total_bytes": 4294967296,
        "mem_available_bytes": 2013265920
      },
      "disk": {
        "mount_path": "/config/.hermes",
        "root_total_bytes": 10737418240,
        "root_free_bytes": 8589934592
      },
      "network": {
        "interfaces": [
          {
            "name": "eth0",
            "status": "up",
            "rx_bytes": 124000000,
            "tx_bytes": 99000000
          }
        ]
      }
    }
  },
  "error_message": ""
}
```

`health_check` 的 finish `result` 建议包含 `health` 和 `system_info.sampled_at`，同时通过 state report 更新 `health.metrics_collector`。如果采样失败，state report 仍应发送可用字段，并在 `health.metrics_collector` 填 `error`，在 `health.metrics_error` 填简短错误原因。

### 验收方式

1. Hermes agent 连续发送两次 state report，间隔约 5 秒。
2. 调用 `GET /api/v1/instances/{instance_id}/runtime`，确认响应里的 `data.runtime.system_info.cpu`、`memory`、`disk`、`network` 都存在。
3. 打开 ClawManager 实例详情页，CPU、Memory、Disk、Network 指标在 10 秒内开始显示并持续刷新。
4. 制造一次网络流量或磁盘写入，确认对应曲线在后续采样中变化。

## Skill Inventory 同步

Hermes agent 需要能发现当前实例内安装的 skills，并把 inventory 上报给 ClawManager。

建议镜像约定 skill 根目录：

```text
/config/.hermes/skills
```

也可以支持环境变量：

```text
HERMES_SKILL_DIRS=/config/.hermes/skills
```

每个 skill 应作为一个目录管理。Agent 需要为每个 skill 计算 `content_md5`，并上报：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/skills/inventory
Authorization: Bearer {session_token}
Content-Type: application/json
```

Body 示例：

```json
{
  "agent_id": "hermes-123-main",
  "reported_at": "2026-04-27T14:30:00Z",
  "mode": "full",
  "trigger": "startup",
  "skills": [
    {
      "skill_id": "hermes-weather",
      "skill_version": "1.2.0",
      "identifier": "hermes-weather",
      "install_path": "/config/.hermes/skills/hermes-weather",
      "content_md5": "d41d8cd98f00b204e9800998ecf8427e",
      "source": "discovered_in_instance",
      "type": "hermes-skill",
      "size_bytes": 20480,
      "file_count": 12,
      "metadata": {
        "runtime": "hermes",
        "manifest": "skill.json"
      }
    }
  ]
}
```

`mode` 语义：

- `full`：全量上报。ClawManager 会把本次未出现的实例 skills 标记为 removed。
- `incremental`：增量上报。只更新本次出现的 skills，不移除缺失项。

建议：

- agent 启动后执行一次 `full`。
- 后续文件监听或周期扫描可用 `incremental`。
- 当平台下发 `sync_skill_inventory` 或 `refresh_skill_inventory` 命令时执行 `full`。

### content_md5 计算规则

ClawManager 的 `content_md5` 不是压缩包 MD5，而是 skill 目录内容指纹。完整规范见 [Skill Content MD5 Calculation Spec](skill-content-md5-spec.md)。

Hermes agent 侧最容易出错的是顶层目录处理：

- inventory 上报时，对 `/config/.hermes/skills/{skill_name}` 目录内部内容计算。
- 上传 zip 时，zip 必须包含一个顶层目录 `{skill_name}/`。
- ClawManager 上传校验时只剥掉 zip 顶层目录 `{skill_name}/` 一次。
- 如果 skill 内部只有一个 `src/`、`lib/`、`dist/` 等目录，不要再剥掉这个内部目录。

例如本地文件是 `/config/.hermes/skills/weather/src/main.py`，参与 MD5 的相对路径必须是 `src/main.py`，不是 `weather/src/main.py`，也不是 `main.py`。

Agent 侧必须在 inventory 阶段和 `collect_skill_package` 上传阶段使用同一份目录内容、同一套算法。否则平台会返回 `skill package md5 mismatch`。

## Skill 包上传

当 ClawManager 发现某个 skill 的 blob 缺少对象内容时，会下发 `collect_skill_package` 命令。Hermes agent 应把对应 skill 目录打包成 zip 并上传。

请求：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/skills/upload
Authorization: Bearer {session_token}
Content-Type: multipart/form-data
```

表单字段：

| 字段 | 说明 |
| --- | --- |
| `file` | zip 包，必须包含且只包含一个顶层 skill 目录 |
| `agent_id` | 当前 agent ID |
| `skill_id` | inventory 中的 skill ID |
| `skill_version` | inventory 中的 skill version |
| `identifier` | skill 名称或 key |
| `content_md5` | inventory 上报的目录指纹 |
| `source` | 通常为 `discovered_in_instance` |

zip 结构示例：

```text
hermes-weather/
  skill.json
  main.py
  README.md
```

不要上传多个顶层目录，也不要把文件散放在 zip 根目录。ClawManager 会拒绝这两种格式。

## 命令轮询与执行

Hermes agent 使用 session token 拉取命令：

```http
GET {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/commands/next
Authorization: Bearer {session_token}
```

如果响应中的 `data.command` 为 `null`，表示没有待执行命令。如果有命令，agent 应：

1. 调用 start 接口标记开始。
2. 执行命令。
3. 调用 finish 接口上报结果。
4. 执行失败也必须 finish，状态填 `failed`。

Start：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/commands/{id}/start
Authorization: Bearer {session_token}
Content-Type: application/json
```

```json
{
  "agent_id": "hermes-123-main",
  "started_at": "2026-04-27T14:31:00Z"
}
```

Finish：

```http
POST {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/commands/{id}/finish
Authorization: Bearer {session_token}
Content-Type: application/json
```

```json
{
  "agent_id": "hermes-123-main",
  "status": "succeeded",
  "finished_at": "2026-04-27T14:31:05Z",
  "result": {
    "message": "skill inventory refreshed",
    "skill_count": 8
  },
  "error_message": ""
}
```

当前平台支持的命令类型包括：

- `collect_system_info`
- `health_check`
- `sync_skill_inventory`
- `refresh_skill_inventory`
- `collect_skill_package`
- `install_skill`
- `update_skill`
- `uninstall_skill`
- `remove_skill`
- `disable_skill`
- `quarantine_skill`
- `handle_skill_risk`
- `start_openclaw`
- `stop_openclaw`
- `restart_openclaw`
- `apply_config_revision`
- `reload_config`

Hermes agent 初期至少实现：

- `collect_system_info`
- `health_check`
- `sync_skill_inventory`
- `refresh_skill_inventory`
- `collect_skill_package`

带 OpenClaw 名称的命令目前属于兼容历史。Hermes agent 可以暂时忽略 `start_openclaw`、`stop_openclaw`、`restart_openclaw`，或在平台侧增加 Hermes 专属命令后再处理。

## Skill 安装和版本下载

如果 Hermes 要支持平台向实例安装 skill，命令 payload 可以携带 skill version 标识。Agent 可通过：

```http
GET {CLAWMANAGER_AGENT_BASE_URL}/api/v1/agent/skills/versions/{external_version_id}/download
Authorization: Bearer {session_token}
```

下载 zip 包，然后解压到 Hermes skill 目录。安装完成后：

1. 重新计算 content_md5。
2. 上报 `skills/inventory`。
3. finish 命令，结果里带上安装路径、skill ID、版本、content_md5。

## 本地开发调试

可以用下面的环境变量在本地启动 agent：

```bash
export CLAWMANAGER_AGENT_ENABLED=true
export CLAWMANAGER_AGENT_BASE_URL=http://127.0.0.1:8080
export CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN=agt_boot_xxx
export CLAWMANAGER_AGENT_INSTANCE_ID=123
export CLAWMANAGER_AGENT_PROTOCOL_VERSION=v1
export CLAWMANAGER_AGENT_PERSISTENT_DIR=/config/.hermes
export CLAWMANAGER_AGENT_DISK_LIMIT_BYTES=10737418240
```

本地调试时不要把真实 token 提交到镜像、仓库或日志。Agent 日志里最多打印 token 前缀和末尾少量字符。

## 验收标准

Hermes 镜像交付前至少通过以下检查：

- 实例创建后，Webtop 桌面可通过 ClawManager 代理访问。
- 容器内 `/config/.hermes` 挂载正常，重启后 agent session 和 Hermes 用户数据仍存在。
- Agent 在 30 秒内完成注册并开始 heartbeat。
- 实例详情页能看到 agent online、runtime running、最后上报时间更新。
- 修改 Hermes skill 目录后，inventory 能同步到 ClawManager。
- 对缺少对象内容的 discovered skill，平台下发 `collect_skill_package` 后，agent 能上传 zip。
- 命令执行会调用 start 和 finish，失败时有清晰 `error_message`。
- 断网、ClawManager 重启、session 过期后，agent 能自动重试并重新注册。

## 平台侧配套清单

为了让 Hermes agent 完整生效，ClawManager 侧需要保持以下能力：

- 在 instance 创建和启动时，对 `hermes` 注入 `CLAWMANAGER_AGENT_*` 环境变量。
- 在 Agent 注册服务中允许 `hermes` 类型实例注册。
- 给 `hermes` 注入 `CLAWMANAGER_LLM_*` 和 OpenAI-compatible 环境变量，使 Hermes 通过 ClawManager AI Gateway 访问模型。
- 后续协议版本中，把 `openclaw_status`、`openclaw_pid`、`openclaw_version` 抽象为 runtime 通用字段；在此之前 Hermes agent 按兼容字段上报。
- 如需要 Hermes 专属 runtime 控制命令，新增 `start_hermes`、`stop_hermes`、`restart_hermes` 或通用 `start_runtime`、`stop_runtime`、`restart_runtime`。
