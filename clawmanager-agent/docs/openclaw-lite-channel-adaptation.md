# OpenClaw Lite Channel 适配指南

本文记录 ClawManager 在 OpenClaw Lite 多实例运行时中适配 channel 插件的约束、实现位置、验证方法和常见故障。新增或升级钉钉、飞书、企业微信等 channel 时，应先阅读本文。

## 1. 适用范围

本文只描述 OpenClaw Lite：一个 runtime Pod 内由 `clawmanager-agent` 启动多个、使用不同 UID/GID、workspace 和端口的 OpenClaw gateway 进程。

它与普通单实例 OpenClaw 镜像有两个关键区别：

- runtime Pod 内的 gateway 不是镜像默认用户，而是实例专属 UID，例如 `200000 + instanceID`。
- 批量创建时不能为每个实例完整复制或在线安装一份 `node_modules`，否则启动时间、磁盘和元数据操作会随实例数线性增长。

Lite 的默认策略是“镜像预装、共享只读包、实例目录可覆盖”。

## 2. 当前目录模型

| 用途 | 路径 | 所有权/写入策略 |
| --- | --- | --- |
| 镜像插件模板 | `/defaults/.openclaw/npm` | 镜像构建期生成，运行时所有实例只读 |
| 全局 OpenClaw 包 | `/usr/local/lib/node_modules/openclaw` | 镜像提供，只读 |
| 实例 OpenClaw 状态 | `<workspace>/home/.openclaw` | 实例 UID/GID 所有，可写 |
| 实例 npm 根目录 | `<workspace>/home/.openclaw/npm` | 实例所有；默认只链接镜像包 |
| 插件注册表 | `<workspace>/home/.openclaw/plugins/installs.json` | 实例所有，路径在启动前重写 |

`CLAWMANAGER_OPENCLAW_NPM_RUNTIME_MODE` 控制 npm 初始化方式：

- `shared`：默认。创建实例自己的 scope 目录，将镜像中的 package 链接进去。
- `copy`：兼容模式。首次创建时完整复制 npm 目录，启动更慢、空间占用更大。

已有实例 npm 目录不会被覆盖。OpenClaw repair 或人工复制产生的实例包会成为实例级 override，后续更新镜像中的共享包也不会替换它。

## 3. gateway 启动顺序

Lite 创建 gateway 时，`WriteGatewayConfig` 在启动进程前完成以下步骤：

1. 校验实例 workspace、`HOME` 和持久化目录。
2. 修复 `/defaults/.openclaw` 父目录和 npm 根目录的可读、可遍历权限。
3. 根据 `shared` 或 `copy` 模式初始化实例 npm 目录。
4. 准备 channel 所需的 peer package。
5. 复制实例可写的 `plugins`、`extensions` 和注册表，并重写 `/defaults` 路径。
6. 合并 `CLAWMANAGER_OPENCLAW_CHANNELS_JSON`，同步启用对应 plugin。
7. 将实例目录和配置归属到请求中的 UID/GID。
8. 启动已经指定端口的 `openclaw` 进程并执行 gateway 健康检查。

相关实现：

- `internal/runtime/openclaw/config_writer.go`
- `internal/runtime/openclaw/plugin_runtime.go`
- `internal/runtime/openclaw/plugin_runtime_test.go`
- `scripts/clawmanager-agent-run`
- `../../openclaw/Dockerfile.openclaw`

## 4. 新增 channel 的适配步骤

### 4.1 明确 channel ID、plugin ID 和 npm package

这三个名称可能不同，不能根据产品名称猜测。例如钉钉当前使用：

- channel ID：`dingtalk-connector`
- plugin ID：`dingtalk-connector`
- npm package：`@dingtalk-real-ai/dingtalk-connector`

需要同时检查插件的 `openclaw.plugin.json` 和 `package.json`。ClawManager 下发的 channel key 必须能被 OpenClaw 识别，`openClawEnvManagedChannelPlugins` 必须把该 key 映射到正确的 plugin ID。

新增映射时同时添加配置写入测试，至少覆盖：

- 配置该 channel 时只启用对应 plugin。
- 未配置时保持默认禁用。
- 切换 channel 后禁用旧的受管 plugin。
- 非法 JSON 和非对象 payload 返回明确错误。

### 4.2 在镜像中预装完整依赖

channel package 必须在镜像构建期安装到 `/defaults`，不能把 `npm install` 放到实例启动路径中：

```dockerfile
HOME=/defaults openclaw plugins install <package-spec>
```

构建后要检查插件目录内的生产依赖是否完整。只复制插件源码或只复制 `dist` 目录可能遗漏 `axios` 等运行时依赖，表现为插件已发现但加载时报 `ERR_MODULE_NOT_FOUND`。

不要依赖 `openclaw plugins repair` 完成首次启动。repair 可能把共享插件复制进实例目录，使单个实例暂时恢复，却会形成不可由新镜像更新的 override，并重新引入批量复制成本。

### 4.3 处理 Node peer dependency 和 realpath

如果 channel 的 `peerDependencies` 包含 `openclaw`，只在实例 npm 根目录创建 peer 链接并不总是有效。

Node 会先解析插件符号链接的真实路径。OpenClaw 7.1 的共享插件真实路径位于隔离 project：

```text
/defaults/.openclaw/npm/projects/<project-id>/node_modules/<channel-package>
```

为兼容 5.4 工作区中已经持久化的绝对符号链接，新镜像还必须提供旧入口：

```text
/defaults/.openclaw/npm/node_modules/<channel-package>
  -> /defaults/.openclaw/npm/projects/<project-id>/node_modules/<channel-package>
```

已有真实目录不得覆盖；构建时必须检查软连接最终解析为目录。这样旧实例不需要复制插件，也不需要修改自己的 `~/.openclaw/npm/node_modules`。

插件从 project 的真实路径向上解析依赖，不会回到实例 npm 根目录。每个 project 内应包含自己的 `openclaw` peer 链接，同时共享 npm 根目录继续保留：

```text
/defaults/.openclaw/npm/node_modules/openclaw
  -> /usr/local/lib/node_modules/openclaw
```

实例 npm 根目录也保留同名 peer 链接，以兼容实例 override 或 copy 模式。

当前 `ensureDingTalkOpenClawPeer` 只在检测到钉钉 connector 时创建该链接。适配其他具有 `openclaw` peer dependency 的 channel 时，应扩展或泛化这段逻辑，并增加并发幂等测试；不能只修改 Dockerfile。

### 4.4 满足跨 UID 权限和 OpenClaw 信任检查

所有实例 UID 都必须能够读取并遍历共享 package，但不应拥有共享目录。镜像至少保证：

- `/defaults` 和 `/defaults/.openclaw` 可遍历。
- `/defaults/.openclaw/npm` 下文件对其他 UID 可读、目录可遍历。
- 共享的第三方 channel package 使用受信任的 root 所有权。
- 符号链接本身和目标路径均可访问。
- 5.4 legacy package 路径能够解析到 7.1 project package。

新增预装 channel 后，要把它的 package 目录加入 Dockerfile 的 root ownership 处理。不要把共享目录 `chown` 给某个实例 UID；那会让其他实例失败，也会造成实例间所有权争用。

镜像基础初始化可能在容器启动时重新设置 `/defaults` 权限，所以 `ensureOpenClawDefaultsTraversal` 是 gateway 创建前的最后一道兜底，不能只依赖 Dockerfile 中的 `chmod`。

### 4.5 区分 gateway Available 与 channel Ready

ClawManager Lite 在 gateway HTTP 健康检查通过后即可报告实例可用。配置写入、plugin registry 和模块解析必须在进程启动前完成；channel 的网络连接和握手可以在 gateway 启动后异步进行。

因此：

- `Available` 表示用户可以进入 OpenClaw，通常目标是创建后约 5 秒。
- `Available` 不承诺钉钉、飞书等外部 channel 已完成长连接握手。
- 若产品需要展示 channel 可用性，应单独上报 channel 状态，不要延长 gateway 的可用时间。
- 自动化端到端测试发送首条消息时应允许短暂重试，并分别记录 gateway ready 和 channel ready 时间。

## 5. 验证清单

### 5.1 镜像级验证

- package、manifest、入口文件和生产依赖存在。
- `/defaults` 权限允许任意实例 UID 读取和遍历。
- 共享 package 为受信任所有者。
- 所需 peer 链接目标存在。
- 每个预装 channel 的 `/defaults/.openclaw/npm/node_modules/<channel-package>` 是可解析的兼容软连接。
- `openclaw plugins list` 不出现 stale、ownership、permission 或 module resolution 错误。

### 5.2 单实例验证

以实例 UID 运行模块解析，而不是以 root 验证：

```bash
readlink -f <instance-home>/.openclaw/npm/node_modules/<channel-package>
readlink /defaults/.openclaw/npm/node_modules/openclaw
```

检查配置时只输出 channel key 和 enabled 状态，不输出 client secret、token 或消息正文。

端到端验证至少覆盖：

1. channel 启动成功。
2. 外部平台消息进入实例。
3. Agent 创建或更新会话。
4. 回复成功发送到原 channel。
5. 日志中没有 plugin load、peer dependency 或权限错误。

### 5.3 批量验证

建议依次创建 1、10、100 个实例，记录：

- gateway ready 的平均值、P95 和最大值。
- 成功、失败、重复端口和缺失进程数量。
- runtime Pod 间的实例分布。
- 共享目录是否保持稳定所有权和权限。
- 是否发生实例级完整 npm 复制或在线安装。

## 6. 常见故障

| 现象 | 常见原因 | 处理方向 |
| --- | --- | --- |
| `plugin not found (stale config entry)` | registry 仍指向旧路径，或实例 UID 无法遍历 `/defaults` | 检查 registry 重写与父目录权限 |
| ownership/permission blocked | 共享 package 所有者或模式不满足信任检查 | 修复镜像 ownership，并保留启动前权限兜底 |
| `Cannot find module 'openclaw/plugin-sdk/core'` | peer 链接只在实例目录，Node realpath 从共享目录解析 | 在共享 npm 根目录提供 `openclaw` peer |
| `Cannot find package 'axios'` 等 | package 生产依赖不完整，或 repair/copy 只复制了部分内容 | 检查镜像安装结果和实例 override |
| 配置了钉钉但启动了企业微信 | ClawManager channel key、plugin 映射或保存 payload 错误 | 只检查配置 key，核对前后端映射和 `openClawEnvManagedChannelPlugins` |
| 只有 repair 后可用 | repair 创建了实例副本，掩盖共享路径问题 | 删除测试实例 override，修复镜像和 runtime 初始化 |
| 少量实例成功、批量失败 | 每实例复制/安装导致 I/O 放大，或共享目录被实例 UID 改写 | 使用 shared 模式，保持共享目录只读和 root 所有 |

## 7. 回归测试位置

- `internal/runtime/openclaw/config_writer_test.go`：channel key 与 plugin 启停映射。
- `internal/runtime/openclaw/plugin_runtime_test.go`：shared/copy 初始化、peer 链接、权限与并发幂等。
- `../../openclaw/dockerfile_permissions_test.go`：镜像中的共享目录权限、所有权和 peer 链接。

每次升级 OpenClaw 或 channel package 都应重新执行这些测试，并使用一个没有历史 workspace 的新实例做端到端验证。历史实例可能存在 repair 生成的 override，不能代表新镜像的首次启动行为。
