# CM Lite 容器槽位与精简镜像设计

状态：设计草案，待评审。本文确定两个仓库的改造边界，不表示相关功能已经实现。

## 1. 已确认的要求

- Lite 使用一个 Pod 预置多个 container 槽位，单个槽位同时只承载一个实例。
- 实例由对应 runtime 的基础镜像、实例配置和持久数据组成。实例数量与运行槽位数量分开管理。
- 实例配置采用 ConfigMap，敏感字段只保存 Secret 引用。
- 用户代码能够执行任意 Shell 时，也只能访问基础镜像文件、自己的持久数据和明确授权的共享目录。
- 支持跨节点迁移。迁移允许先停止源 gateway，再在目标槽位启动，不要求进程内存、连接或打开的文件句柄随实例迁移。
- 本地 PVC 仅适用于单节点部署；多节点部署强制使用 Longhorn。
- OpenClaw、Hermes、OpenCode、DeepSeek Harness 四类 Lite 镜像全部从 slim 基础镜像重新构建，每类只有一个用于正式构建的 `Dockerfile.lite`。
- 精简不能通过删除既有功能达成；只保留必要工具，但用户任务实际依赖的工具也属于功能基线。
- 基于现有 `clawmanager-agent` 改造。公共功能留在 `clawmanager-agent/`，runtime 专属功能放到各自顶层目录。

## 2. 当前实现与改造范围

现有 Lite runtime 列表来自 ClawManager 的 `NormalizeV2RuntimeType`，四类正式镜像由 AgentsRuntime 的 `.github/workflows/docker-ghcr.yml` 构建。

OpenClaw Lite 当前与 Pro 共用 `openclaw/Dockerfile.openclaw`；OpenCode Lite 与 Pro 共用 `opencode/Dockerfile`。两者最终基础镜像都是 Webtop。Hermes 和 DeepSeek Harness 已有独立 Lite Dockerfile，仍按本方案审查和重新构建。

公共 Agent 当前通过 `internal/runtime/<type>` 注册 profile，公共 gateway 中也含有具体 runtime 的默认值、配置与生命周期分支。命令入口直接引用 DeepSeek 专属代理，LLM 配置命令引用 OpenCode 渲染器。这些代码需要按实际职责迁移，不能只移动 profile 目录。

本次面向四类 Lite。现有 Pro 和 OpenClaw Shell 的功能、镜像用途和存量实例需要保持兼容。公共库调整导致的构建接线变化必须同步处理，不能将旧的通用 Agent 直接覆盖到新槽位镜像。

## 3. 镜像与代码组织

### 3.1 构建入口

| Runtime | 唯一 Lite Dockerfile | 最终基础镜像方向 | 必须保留的运行能力 |
| --- | --- | --- | --- |
| OpenClaw | `openclaw/Dockerfile.lite` | 固定版本、digest 的 Node Debian slim | gateway、渠道插件、Team、工具执行、浏览器及现有升级兼容能力 |
| Hermes | `hermes/Dockerfile.lite` | 固定版本、digest 的 Python Debian slim，包含必要 Node 运行组件 | Web/RPC、会话 worker、Team、技能与工具执行 |
| OpenCode | `opencode/Dockerfile.lite` | 固定版本、digest 的 Debian slim | 官方 Web 服务、终端、项目搜索、模型配置及必要开发工具 |
| DeepSeek Harness | `deepseek-harness/Dockerfile.lite` | 固定版本、digest 的 Node Debian slim | Web 服务、托管鉴权代理、插件安装、Team 及工具执行 |

版本先沿用已有锁定版本。更换基础镜像与升级上游版本分开验证。构建上下文统一为 AgentsRuntime 仓库根目录，以便包含公共 Agent 和对应 runtime。

镜像采用多阶段构建。最终层包含必要系统库、证书、进程回收组件、隔离启动所需组件、runtime 和管理程序。构建工具、构建缓存、开发依赖仅在确实不被运行功能使用时移出最终层。不得因为体积大就移除用户任务需要的 Shell、Git、SSH、编译或包管理能力。

所有 Lite 镜像不引入 Webtop、KDE、X11、桌面串流服务或 Pro 专属自启动程序。浏览器功能按现有 Lite 行为保留，是否改为按需启动以功能回归和启动时延验证为依据。

### 3.2 目标目录

以下为职责布局，内部文件只在实际需要时创建：

```text
AgentsRuntime/
  clawmanager-agent/
    go.mod
    agent/                         # 可供其他模块调用的公共启动与注入入口
    runtime/                       # 公共 profile 契约和跨模块数据类型
    internal/
      agent/                       # Pod 注册、心跳、报告与恢复
      control/                     # 通用控制协议、认证和请求校验
      gateway/                     # 逐步清除具体 runtime 分支的生命周期核心
      llmconfig/                   # 与具体 runtime 无关的模型配置
      slot/                        # 槽位状态与本地控制
      sandbox/                     # Linux 隔离环境、挂载与进程回收
  openclaw/
    Dockerfile.lite
    cmd/openclaw-lite-agent/
    internal/lite/                 # OpenClaw profile、配置、插件与健康适配
    scripts/
  hermes/
    Dockerfile.lite
    cmd/hermes-lite-agent/
    internal/lite/                 # Hermes profile、配置、RPC 与能力适配
    scripts/
  opencode/
    Dockerfile.lite
    cmd/opencode-lite-agent/
    internal/lite/                 # OpenCode profile、配置与项目路径适配
    scripts/
  deepseek-harness/
    Dockerfile.lite
    cmd/deepseek-harness-lite-agent/
    internal/lite/                 # DSH profile、资源准备与托管代理
    scripts/
```

OpenClaw 和 Hermes 复用自己的现有 Go 模块；OpenCode 和 DeepSeek Harness 增加最小 Go 模块。各模块通过仓库内相对路径 `replace` 引用公共模块，Docker 构建保留相应路径布局。构建和 CI 使用满足公共模块要求的 Go 工具链。

### 3.3 公共与专属代码的依赖边界

公共模块公开必要的 Agent 启动接口、现有 RuntimeProfile 契约及其公共数据类型，避免各 runtime 跨模块导入 Go `internal` 包。优先沿用现有 profile 能力，按槽位机制补齐必要参数，不创建另一套重复接口。

依赖方向为：runtime 的命令入口导入公共库和自己的适配器，再将适配器传给公共启动入口。公共核心不导入 `openclaw/`、`hermes/`、`opencode/` 或 `deepseek-harness/`，也不通过 runtime 类型分支实现专属行为。

公共目录负责槽位状态机、配置校验框架、隔离与挂载、资源统计、进程退出确认、控制协议、Pod 注册、心跳、上报、代次和恢复机制。专属目录负责原生配置格式、启动参数、环境转换、插件约束、健康探测和版本迁移钩子。

例如 DSH Web 代理位于 DeepSeek Harness 目录；通用模型字段解析保留在公共目录，OpenCode 配置序列化位于 OpenCode 目录。专属测试随实现迁移，公共安全机制只有一份实现。

每类入口编译为镜像内的 `/usr/local/bin/clawmanager-agent`，保留运维路径；四个二进制共用同一套核心源码，但只组装自己的 runtime 适配器。编译期组装即可，不引入动态 Go 插件系统。

## 4. Pod 与槽位管理

每个池 Pod 运行一个 Pod Agent 和固定数量的 runtime 槽位容器，Pod 内的槽位承载同一种 runtime。Pod Agent 聚合注册、心跳和状态；槽位内仅保留必要的控制和隔离启动逻辑，不各自重复运行完整的上报循环。

Pod Agent 和槽位控制程序使用公共 Agent 代码的不同运行角色。Pod Agent 根据 ConfigMap 中的期望配置协调槽位，通过每槽位独立的控制通道下发经过验证的配置。槽位控制通道、管理凭据和 Kubernetes API 凭据不暴露给 gateway。

槽位固定端口与控制端点需要唯一分配；同一 Pod 的容器共享网络，业务请求和管理请求都必须经过鉴权。现有 runtime 的多端口能力必须完整保留。

基本状态为 `idle -> preparing -> starting -> running -> draining -> cleaning -> idle`。停止、卸载、隔离清理或身份确认失败时进入 `error`，槽位不得重新分配。重试同一个实例和代次必须幂等，旧代次请求必须拒绝。

槽位数和资源预算按 runtime 分别配置，以实测容量为依据，不能沿用进程池容量作为容器槽位容量的证明。容器级 cgroup 限制覆盖实例及其子进程，空闲槽位不启动业务 gateway。

## 5. 文件系统隔离

### 5.1 信任边界

可信管理面包含 ClawManager、Pod Agent、槽位控制程序和宿主机容器运行时。实例包括 gateway、工具调用、Shell、插件及其全部子进程，按不可信代码处理。

目标是让实例只能访问镜像系统文件、自己的持久目录和明确授权的共享目录。基础镜像只读内容可共享；可写状态、缓存、临时文件和配置必须按实例隔离。

槽位控制程序可以持有管理挂载的能力，gateway 必须去除这些能力。不能把 `privileged` 或 `CAP_SYS_ADMIN` 直接授予用户代码，不能仅凭 UID、目录权限或 `chroot` 宣称完成隔离。

### 5.2 分配与运行

槽位控制程序在启动用户代码前建立独立 mount/PID namespace，隔离挂载传播，只保留实例授权挂载，提供与 PID namespace 对应的 `/proc`。新 namespace 会继承原有挂载，必须明确移除 PVC 源目录、控制目录、Secret、ServiceAccount、宿主设备等未授权入口。

在执行 gateway 前关闭未授权目录和 namespace 文件描述符，重设工作目录，切换到非特权身份，清除可恢复的 capabilities，设置 `no_new_privs` 并采用限制后的系统调用策略。所用运行内核、安全策略或隔离工具不满足要求时，槽位失败关闭，禁止退回仅 UID 隔离。

Linux 启动顺序与允许的挂载操作先以原型验证，复用可审计的隔离组件和内核接口，不自行构建通用容器运行时。只允许可信配置选择实例身份；挂载源目录由管理程序从身份推导，不接受用户提供任意绝对路径。对目录替换、符号链接和文件描述符穿透做负向验证。

若槽位控制程序在自己的容器内完成挂载，挂载保持私有，不需要向宿主机双向传播。管理程序所需能力与 gateway 的能力分别设置；特别是不能把容器级 `CAP_SYS_ADMIN` 与容器级 `allowPrivilegeEscalation: false` 当作可同时生效的配置。

### 5.3 回收

停止接入后先终止整个实例隔离环境中的进程，确认子进程、监听端口和文件句柄已释放，再卸载和销毁隔离环境。禁止以 lazy unmount 成功代替进程和写入权回收确认。临时数据、缓存和凭据在重新分配前清理。

该设计建立文件系统访问边界；同 Pod 共享的网络仍需端口与鉴权控制。对内核或可信控制程序漏洞的防护不以目录隔离机制作为绝对保证。

## 6. 存储与跨节点迁移

### 6.1 部署约束

单节点可使用本地 PVC 或 Longhorn。多节点配置必须使用 Longhorn，运行时池工作区要求 RWX，以支持同一运行时池跨节点共享实例数据。不能只验证 StorageClass 名称，需要验证实际 provisioner、PVC 访问模式、绑定状态和可用性。

同一运行时池的 Pod 在同一 namespace 引用同一工作区 PVC，PVC 内按实例身份分配目录。PVC 以池为粒度管理，不要求为每个实例创建一个 PVC；底层共享不改变 gateway 可见目录的隔离要求。授权的 Team 目录作为单独的授权挂载处理。

本地 PVC 的池 Pod 必须遵守 PV 节点约束。单节点场景处理节点内迁移；多节点不支持本地 PVC，也不引入节点间目录复制作为默认迁移路径。

### 6.2 迁移协议

1. 控制端持久化迁移任务并预留目标槽位；目标必须使用匹配的 runtime、镜像版本和同一工作区 PVC。
2. 源端收到 draining 期望配置，停止接入，结束 gateway 及全部子进程，完成应用关闭和数据落盘。
3. 源端撤销实例挂载，报告包含实例、源 Pod UID、槽位和代次的停止及卸载证据。
4. 控制端核对证据并以比较更新提交新的唯一归属和代次，投射目标期望配置。
5. 目标建立新的隔离环境，挂载原数据目录，启动 gateway。健康检查成功后更新业务路由，再回收源槽位。

同一目录始终只能有一个有效写入实例。目标起动失败时，控制端先确认目标没有继续写入，再决定回到源端；不能只恢复旧 ConfigMap。

源端失联、超时或租约过期不等于旧进程停止。Longhorn RWX 也不自动提供实例级写入隔离。未获得停止确认时，必须先执行可验证的节点或存储写入隔离，才能在其他节点激活目标；否则迁移保持等待或失败，不能冒险启动第二个写入端。

## 7. 配置与控制端职责

ConfigMap 保存实例身份、配置版本、期望状态、runtime 类型、目标 Pod/槽位、代次、workspace 标识、授权共享目录、gateway 参数和资源预算。Secret 引用仅由管理面解析，并只向实例提供其需要的业务凭据。

公共配置与 runtime 原生配置分开：公共核心校验身份、归属和权限；对应适配器生成实例目录内的原生配置。用户不能编辑自己的挂载授权、目标槽位或代次。

ConfigMap 是期望状态的投射；ClawManager 的持久记录保存唯一归属、实际状态、操作幂等标识和迁移进度。数据库提交和 Kubernetes API 写入之间通过重试协调，ConfigMap 更新成功不能作为执行成功的依据。

动态分配使用管理面的 API watch 与重新同步，避免把所有实例 ConfigMap 或 Secret 挂载进每个槽位。watch 中断或重连需要从当前状态恢复，拒绝过期配置。Pod Agent 重启后先核对控制端归属和现有槽位，确认前不得自动恢复未知写入实例。

ClawManager 需要改造 runtime Pod 构建、槽位库存与预留、代理路由、ConfigMap/Secret 下发、PVC 验证、迁移协调和升级流程。新后端以明确的协议版本接入，旧进程池不因新配置而被静默改造；迁移存量实例时先验证同一份持久数据与版本兼容性。

## 8. 开发顺序

1. 固定四类 runtime 的现有功能清单和资源基线，确定需要保留的工具与依赖。
2. 将公共 Agent 的扩展契约改为可跨模块使用，把 Lite 专属 profile、配置、命令和测试移入各自目录，保持现有行为可验证。
3. 验证公共槽位隔离启动、进程回收和挂载撤销原型，通过后实现公共槽位生命周期与配置协议。
4. 完成四个 slim Dockerfile 和各自入口，统一正式构建矩阵，每个 runtime 独立构建、验证和发布。
5. 接入 ClawManager 多容器 Pod、容量调度、配置投射、路由与跨节点迁移；加入单节点和多节点存储配置验证。
6. 完成四类 runtime 的功能、安全、迁移和密度验收后，再执行存量实例升级。

以上是实施分阶段边界；具体文件级任务、命令与回滚操作在设计评审后编写。

## 9. 验收标准

- 四类 Lite 各有一个正式 Dockerfile，最终基础镜像为 slim，构建结果不依赖 Pro 镜像或 Webtop。
- 公共 Agent 核心不导入具体 runtime 实现；每个 runtime 的入口仅组装自己的适配器。四类以及受公共库变更影响的旧入口均可编译。
- 原有聊天、流式输出、会话恢复、模型配置、终端与工具、技能、渠道、Team、浏览器、文件操作和版本升级能力按适用 runtime 完成功能对照。
- 从实例内尝试其他实例路径、父路径、符号链接、`/proc`、管理通道、管理凭据与重新挂载，均不能获得未授权目录；授权 Team 共享仍可使用。
- 跨节点迁移保持数据和业务配置一致，其他槽位继续服务；停止失败、卸载失败、目标启动失败、重复请求、旧代次、控制程序重启和源节点失联均不会产生双写或错误复用。
- 多节点本地 PVC、非 Longhorn 工作区、以及不满足共享迁移要求的 RWO 配置在启用新池前被拒绝。
- 记录镜像大小、空闲槽位开销、业务空闲与负载下内存/CPU、启动及迁移耗时、节点可承载的活跃实例数量。与相同版本、相同功能、相同负载基线对照，不以镜像缩小代替运行资源验证。
- 优化后的四类 runtime 均提供测量结果；存在不可避免的安全隔离开销时单独列出，未测量前不承诺资源降幅。

## 10. 技术依据

- [Kubernetes Pod 更新限制](https://kubernetes.io/docs/concepts/workloads/pods/#pod-update-and-replacement)
- [Kubernetes 卷与本地存储](https://kubernetes.io/docs/concepts/storage/volumes/)
- [Kubernetes PVC 访问模式](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes)
- [Longhorn RWX 存储](https://longhorn.io/docs/1.13.0/nodes-and-volumes/volumes/rwx-volumes/)
- [Linux mount namespace](https://man7.org/linux/man-pages/man7/mount_namespaces.7.html)
- [Kubernetes 安全上下文](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/)

本设计针对 Lite 的代码职责划分替代旧的“具体 profile 全部放在公共 Agent 的 `internal/runtime/<type>`”组织约定；已确认的接口兼容与功能要求继续保留。
