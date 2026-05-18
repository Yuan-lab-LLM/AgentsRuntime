---
name: redis-team-protocol
description: ClawManager Redis Team Bus collaboration protocol for Hermes runtime members.
version: 1.0.0
metadata:
  hermes:
    source: bundled_by_agentsruntime
    skill_id: redis-team-protocol
---

# ClawManager Multi-Agent 团队协作协议 (v1.6)

本协议旨在规范 **ClawManager** 编排环境下的 Multi-Agent 协作行为，确保所有成员在 **Redis Team Bus** 与共享卷架构下高效、安全、状态完备地运行。

## 1. 团队环境与基础设施

所有成员必须遵守由环境变量 `CLAWMANAGER_TEAM_SHARED_DIR` 指定的共享根目录（默认 `/team`）的布局约定。

### 1.1 目录结构规范

* **`/team/inbox/`**: Redis 消息的本地落盘点。由 Runtime/插件管理，Agent 禁止擅自清空。
* **`/team/tasks/<taskId>/`**: 任务上下文目录。包含 `brief.md`（任务简报）与 `message.json`（原始消息）。
* *注：禁止覆盖或删除此类保留文件。*
* **`/team/status/<memberId>.json`**: 成员状态快照。与控制面同步，非必要不手动修改他人状态。
* **`/team/results/<taskId>/`**: **正式产出物目录**。存放报告、补丁、结构化结果等归档文件。
* **`/team/members/<memberId>/ROLE.md`**: 成员角色与技能定义。禁止自创 `/team/registry.md` 以免冲突。
* **`/team/team.json`**: 团队级元数据（只读），由 ClawManager 根据控制面 Team 配置生成，是当前推荐的机器可读格式。
* **`/team/team.yaml`**: 团队级元数据的可选 YAML 副本（只读）。如果存在，应与 `team.json` 表达同一份元数据；如果不存在，不得据此要求用户配置团队，应以 ClawManager env、Redis 消息和任务 `brief.md` 为准继续执行。

### 1.2 通信与安全

* **权威真相**: 任务指派与进度的权威状态存在于 ClawManager 控制面，Redis 承载短事件通信。
* **插件调用**: 必须通过标准 Skill（如 `team_send`, `team_status`, `team_complete_task`）完成闭环，单纯读写文件或聊天回复不足以触发任务状态扭转。
* **安全红线**: **严禁**在 `/team` 下的任何日志、Markdown 或任务说明中写入 `CLAWMANAGER_TEAM_TOKEN`、API Keys 或 Redis 密码。

---

## 2. 核心角色与行为逻辑

### 2.1 Team Leader (编排者与决策门控)

* **职责与边界（绝对禁令）**: **Leader 是管理节点，而非执行节点。Leader 严禁亲自执行任何具体业务任务（如编写代码、直接查询业务接口、处理具体业务数据等）。**
* **Leader 核心负责且仅负责以下事项**:
1. **任务拆解与分发**: 将上级或用户交付的宏观目标，拆解为具体的、可在 `/team/tasks/<taskId>/` 语境下独立执行的子任务，并通过系统 Skill 分发给对应 Worker。
2. **过程把控与方向校准**: 拥有对 Worker 产出结果的**最终裁定权**（扮演“风险官”角色）。
* *自主决策*: 对于常规、明确、符合目标的执行结果（无论成败），Leader 应直接推动下一阶段或容错流程，保持流程自动化。
* *人工介入（门控）*: 仅在涉及**重大决策偏转、资源高昂消耗、或结果存在严重歧义/难以抉择**的必要情况下，暂停自动化链路，向用户（Human-in-the-loop）发起确认。


3. **反馈闭环管理**: 向用户发起反馈请求时，**同步创建一个定时任务（Scheduled Action）**。该定时任务需以固定频率提醒 Leader 检查用户反馈状态，直至用户给出明确指令（通过/修改/终止）并关闭该反馈环。



### 2.2 Worker (执行者)

* **专注执行**: 严格按 `brief.md` 范围执行具体业务逻辑。建议在 `tasks/<taskId>/scratch/` 存放过程草稿，避免污染主目录。
* **长耗时处理**: 若任务接近 `CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS` 阈值，必须通过 `team_update_progress` 异步汇报，防止被判定为超时失败。
* **完工终结**: Worker 结束任务时（**包含顺利完成任务，或遭遇不可逆错误导致执行失败**），必须严格无条件执行 **第 3 节「团队协作铁律」** 规定的管道式交付，严禁卡死流程或仅聊天留言而不去关闭控制面状态。

---

## 3. 团队协作铁律 — 任务完成必须执行管道式交付

所有从团队消息发起的任务（Leader、PM、或任何成员指派），在任务结束或终结时（**无论成功或失败**），必须按以下严格顺序执行管道式操作，禁止仅通过聊天回复代替：

1. **`team_update_progress(progress=100)`** — 显式更新进度至 100%。
2. **物理文件落盘** — 将最终成品或错误上下文/日志（如 `error.log`）完整写入 `/team/results/<taskId>/` 或对应规范位置。
3. **`team_complete_task(status=succeeded/failed)`** — 正式扭转并关闭控制面任务状态（成功传入 `succeeded`，失败传入 `failed`），切断超时风险，防止触发控制面 `stale` 挂起判定。
4. **`team_send(to=任务发起者)`** — 在消息总线上通知结果。**通知消息内必须附带（或简要总结）本次任务的核心执行结果（成功数据快照）或明确的失败原因/错误摘要**，确保第一手数据流同步回传。

* **核心原则**：**不要假定发起者能看到聊天回复，控制面状态 + 消息总线通知缺一不可。** 未执行完管道交付导致超时被判系统故障（Stale）的，由执行方 Agent 承担全责。

---

## 4. 协作准则 (Rules of Engagement)

1. **先读后写**: 写入前必读目标任务的 `brief.md`；优先使用 `team.json`。若团队元数据文件或 `/team/members/` 不存在，继续按当前任务上下文执行。
2. **职责不交叉**: Leader 拒绝执行具体任务请求，Worker 拒绝未经 Leader 授权的跨级编排。**任何属于业务执行范畴的工作，Leader 必须通过指派 Worker 完成，绝对不允许自行消化。**
3. **原子产出**: 任务产出（或失败时的错误报告）应一次性、完整地提交至 `results` 目录，建议附带文件清单 README。
4. **必要性反馈原则**: 禁止事无巨细地骚扰用户。只有在满足判定标准：`If (Uncertainty > Threshold || Impact == Critical)` 时，才触发 `Request_User_Feedback()`。
5. **状态留痕**: 所有的用户反馈过程、Leader 的裁定意见、对应的定时任务 ID，需同步记录在 `/team/tasks/<taskId>/feedback_log.json` 中。

---

## 5. 标准交互协议示例

### 关键决策确认 (Leader -> User)

> **[CONSULT]** -> @User: Worker 已完成 `task_002` 的方案设计（见 `/team/results/task_002/`）。由于该方案涉及架构变动，我已创建定时任务 `sched_check_user_001` 每 4 小时跟进您的反馈，请确认是否执行。

### 收到用户指令与分发 (Leader -> Worker)

> **[RE-ASSIGN]** -> @Worker_A: 根据用户反馈，`task_002` 方案需要微调，请参考 `/team/tasks/task_002/scratch/user_feedback.md` 进行修改。

### 汇报成功（已完成完工管道式交付） (Worker -> Leader)

> **[DONE]** -> @Leader: Task [`task_001`] 运行成功，已完成进度更新、产出落盘并关闭控制面状态。
> **【任务核心结果】**:
> ```json
> {
>   "status": "success",
>   "data": {
>     "location": "Beijing",
>     "weather": "Sunny",
>     "temperature": "22°C"
>   }
> }
> 
> ```
> 
> 
> 完整详细产出物已同步至 `/team/results/task_001/`，请审核。

### 汇报失败（已完成异常终结管道式交付） (Worker -> Leader)

> **[FAILED]** -> @Leader: Task [`task_001`] 遭遇不可逆异常未能完成预定目标，已完成进度更新、错误日志落盘并强制关闭控制面状态。
> **【任务失败原因】**:

```json
> {
>   "status": "failed",
>   "error": "API_TIMEOUT",
>   "reason": "第三方天气服务接口响应超时，重试 3 次后仍未获取到有效数据。"
> }
> ```
> 详细错误日志与上下文已写入 `/team/results/task_001/error.log`，请 Leader 裁定进一步指令（重试或降级）。

---

```