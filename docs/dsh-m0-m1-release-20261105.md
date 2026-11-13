# DeepSeek Harness M0–M1 发布交接（2026-11-05）

本轮是两次开发中的第一次，仅完成 M0 原生契约验证和 M1 普通实例版本同步。
开发仓库为团队 `AgentsRuntime`，分支 `codex/team-personal-integration`。
个人仓库只读取已提交的 `ce30810d`，没有修改其工作区。

## 已交付与明确边界

- Lite、Pro 原生 DSH 统一到 `0.2.1-rc.3`，Node `24.21.0`；完整 npm lock、
  integrity、原件及补丁 SHA 随源码保存。实际依赖为 Cordis 4.0.5、HMR 0.2.1-rc.3、
  Loader 1.0.6，未把旧 Cordis 兼容族强制覆盖到新版。
- 保留团队模块化 Agent、isolated slots、受控模型出口、凭据处理和目录权限。
  按语义移植个人版的模型窗口/vision、组合 preset、浏览 Cookie 提前续期和资源限制修复。
- 私有 Cookie 刷新合并并发请求；401 不自动重放已经可能产生副作用的请求。
- 启动脚本 source 资源准备脚本，实际 native child 继承 nofile 65536。
  受限容器禁用 NARB 的临时原生模块缓存，直接加载镜像内已验证模块，未放宽 noexec。
- Pro 继续使用原桌面监督器；共享 Agent 的模式选择补齐 DSH，不改变其他产品模式。
- **未安装 DSH Team 宿主、消费者或生产 `team_*` 工具。** `native-manifest.json`
  的 `teamEnabled` 仍为 false；CM/北向 Team 准入没有新增 DSH，也没有增加 Worker 必填参数。

M0 契约细节见 [dsh-native-contract-20261105.md](dsh-native-contract-20261105.md)。
工具原生上下文、持久化非人工 source、取消/idle 和同会话恢复均由真实固定原生包验证，
模型侧使用受控 fixture；这不是 Team 生产模型闭环的验收。

## 现场运行方式：保留实际部署契约

32543 开工时没有 DSH 实例或运行池。现场其他 Lite 产品仍使用兼容进程池，
没有开启全局 native slots。此次从该现场契约新增独立 `deepseek-harness-runtime`
池，容量 2，并继承现有 NFS、控制/报告 Secret 引用；没有修改其他产品运行池。

镜像仍支持团队 native slots。只有可信 supervisor 的 `IsolatedLifecycle=true`
才生成 `CLAWMANAGER_DSH_PRIVATE_TMP=1`，使内层 workspace-write 使用外层实例私有
持久化 `/tmp`；兼容进程池强制为 0，保留原生内层 tmpfs。请求提供的同名环境变量
不能切换该边界。read-only profile 未修改。Pro 保持原始 sandbox，不套 Lite 私有 tmp 补丁。

这不是对全局沙箱迁移完成的声明。第二次开发如需改变现场运行方式，必须先验证
Core 与各产品控制客户端、TLS、UID、工作目录和存量池的共同兼容性，不能仅打开全局开关。

## 镜像与源码

最终镜像业务源码为 `01b8077`；`73cbfe6` 只加强验收断言，之后的交接提交不改变二进制。
阶段提交为 `396af99`（M0）、`1523f11`（M1）、`21e5d05`/`cd6f79f`
（可核实的构建输入）、`01b8077`（低权限与现场契约修复）、`73cbfe6`（验收修正）。

| 镜像 | 最终不可变引用 |
| --- | --- |
| Lite | `10.130.14.23:5000/agentsruntime/deepseek-harness-lite@sha256:a356a00e15da7e9e8807cdcdf913acfa617ed5d9c17ac3f4828a9ed22b28f431` |
| Pro | `10.130.14.23:5000/agentsruntime/deepseek-harness@sha256:6dd7f1a01a4b7644fec6fe6f6621b36b7f9b5495555cd15fa0500e07fdebe371` |

两者发布 tag 都是 `team-dsh-021-m1-20261105-v2`。运行中 image config 分别为
`ce9b813e8f3503c6c2c5b06f6d093cf2e83d7e8095279426b70ce50a1d58493a` 和
`aaa82f77084e41155b626061abfcc7832370c1f44ea02b6a18d70fc5216eb8ae`。
Agent 二进制 SHA256 分别为
`2c4ffe6d7145256c37aa36f697f3ecda41336351957f4c6de128d9b83b7bd6e7` 和
`0f44536b3d3326a89f43495099fe59ca0b4036e996ba3e0945a23e2bd1ca10a2`，现场已读回匹配。

原生 sandbox 原件 SHA256 为 `be6e1107871cf8d94c040090b13c405bc833822e2f51ed5280e93eececf762be`；
Lite 条件补丁后为 `e4dbfdae3e8aba28e17ea55d2242c59f6b246a1cc5a12e449a86a281fdd259ea`。
现场分别运行 `verify-native.mjs --patched`（Lite）及 `verify-native.mjs`（Pro）通过。

构建使用固定 Node digest（source-lock）和已验证 Go 1.26.8：
`golang:1.26.8-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107`。
Go 模块下载使用显式代理，校验和验证保留。Pro Webtop 基础固定为
`10.130.14.23:5000/agentsruntime/openclaw@sha256:44fdecc39f0dd0c66d30bfd7478d58e5a00ea4ded286163de8ec02de21311e02`。
这不升级任何已运行 OpenClaw。维护 Dockerfile 只复用本次已构建基础层、替换核实的产物，
避免为修正启动脚本重复下载整套桌面；完整新构建路径仍保留在两份常规 Dockerfile 中。

## 必要验收结果

1. DSH Go 模块与共享 Agent app 测试通过（Windows、固定 Go 的 Linux 构建）；
   模型配置 3 项测试通过，可信 tmp 标志与 read-only/兼容模式回归通过。
2. 最终 Lite 镜像在非特权用户、cap-drop ALL、no-new-privileges、noexec tmpfs、
   无外网条件启动通过；HTTP、HMR 装卸和实际 native child nofile 均核实。
3. 原生 Web HTTP/assets/plugin batch/WebSocket/认证、重启、模型 catalog、受控流式
   模型与读文件工具通过；续期及不重放行为由代理测试验证。
4. 真正旧 `0.1.2-alpha.3` 创建的会话由新版恢复；用户 profile、技能和文件 SHA 未变。
   没有把错误挂载导致的测试失败计入通过。
5. 现有外层 slots 的持久 tmp 源→其他实例隔离→目标恢复测试通过，验证内容/权限/
   UID/GID/mtime。这是受控 Linux 共享机制测试，不冒称现场 K8s DSH 迁移。
6. 32543 专用普通实例：Lite **#589**、Pro **#590**，owner 为
   `portal-accept-20260923@example.com`，均 running。Lite 独立域名原生页和 Pro 桌面 HTTP 200；
   两个真实受管模型会话均产生公开 assistant 文本并完成。仅查询原会话确认结果，未重复发送。
7. 北向仍为 Team Lite、Claw Leader、自定义 2–6 人，DSH 不在 Team Worker 候选中；
   两个普通实例可见。原有 29 个 Runtime Pod 的 UID/镜像未变；Team175/任务381报告仍为
   3166 bytes，SHA256 `1f942bcccfa6ff3c266e5e7ea622fc8c0caa91c202f2e698056561d3b4d28de5`。

本轮 App 业务代码与 schema 未修改，故复用已验收 App 镜像，不重复后端全量/前端构建。
本地详细证据保存在忽略的 `ClawManager-2/team-optimization-docs/dsh-m0-20261105/`，
包含每项结果与发布日志；凭据、Cookie、访问 token、模型思考和完整原生页不入库。

## 部署、保留数据与回退

32543 的 Core 追加
`CLAWMANAGER_DEEPSEEK_HARNESS_PUBLIC_URL_TEMPLATE=https://deepseek-harness-{instance_id}.10-130-14-23.nip.io:32543/`。
沿用 `clawmanager-hxc-app:team-portal-sync-20261105-v2`，在 `/home/hxc` 按用户指定
`TENANT_SUFFIX=-hxc-peer NODE_PORT=32543` 执行 `clawmanager-apply.sh`，退出码 0。
仅把目录中的 DSH 默认镜像 id7/id8 更新为上述 digest，旧值已先保存；没有删实例/数据/旧镜像。
专用 #589/#590 保留供验收，不是 Team 成员。

服务端原 Deployment、原池模板、原 Pod 清单和最终镜像清单保存在
`/home/hxc/team-dsh-m1-20261105/`（受限权限）。本地 `images-before.json` 保留原目录值。
若需要停止新增 DSH，可先恢复这两个目录项并禁止向新池分配，核对现有绑定后按受控停止
流程处理 #589/#590；不得直接删除新池或其 NFS 目录。需要撤回 Core 配置时仅撤回本次新增
环境项，不能整份覆盖同事后来修改的 Deployment。已有新版会话写入后，不能承诺直接降级
旧 native 包可读；先封存新版数据，在隔离副本验证后再选择回退方式。

## 第二次开发接续点（M2–M4）

尚未实施 DSH Team 原生宿主、派发身份绑定、正式结果/正文补充、presence/执行观察、
能力标记及池过滤、CM/北向/自定义统一准入、共享产物/预览与 DSH Team 真实任务闭环。
按 App 的 `docs/deepseek-harness-team-integration-plan-20261105.md` 从 M2 开始，
继续保留稳定串行调度和最小完成参数；原生 turn completed 不能直接关闭 Team 工作项。

本轮效率复盘：先固定原生契约，再实现可运行普通实例；现场模式不符合预想时只适配
真实契约，不扩展成全局调度改造。镜像分层复用有效；SSH 隧道推送出现长时间无进展后，
改为直连复用已上传层完成发布。验收客户端的系统代理、无状态 Cookie、命令参数和
旧数据挂载错误属于验收设施问题，修正后重验受影响项，未为通过验收放宽产品约束。
