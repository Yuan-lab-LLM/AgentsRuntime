// Managed runtime protocol, deliberately independent of OpenCode UI and SDKs.
// No prompt text, provider credentials, or model access lists are logged.
export class ModelUnavailable extends Error {
  constructor() { super("当前模型不可用，本轮任务已中止，请重新选择模型"); this.code = "model_unavailable" }
}

export class GovernanceUnavailable extends Error {
  constructor() { super("模型授权检查暂时不可用，已停止当前任务以避免越权调用"); this.code = "model_authorization_unavailable" }
}

export function createGovernance({ env = process.env, fetcher = globalThis.fetch, now = Date.now } = {}) {
  const base = (env.CLAWMANAGER_LLM_BASE_URL || "").replace(/\/+$/, "")
  const key = env.CLAWMANAGER_LLM_API_KEY || ""
  const enabled = Boolean(base && key)
  const generation = Number(env.CLAWMANAGER_GATEWAY_GENERATION || "0")
  const tasks = new Map()
  const currentTasks = new Map()
  const receipts = new Map()
  let receiptFlush
  let catalogCache, catalogAt = 0, catalogPending
  async function request(path, body) {
    let response
    try {
      response = await fetcher(`${base}/runtime/${path}`, {
        method: body === undefined ? "GET" : "POST", redirect: "error",
        headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(2500),
      })
    } catch { throw new GovernanceUnavailable() }
    if (response.status === 403) throw new ModelUnavailable()
    if (!response.ok) throw new GovernanceUnavailable()
    try { return await response.json() } catch { throw new GovernanceUnavailable() }
  }
  const identity = (task) => ({ generation, session_id: task.sessionID, task_id: task.taskID })
  const taskKey = (sessionID, taskID) => JSON.stringify([sessionID, taskID])
  async function check(task) {
    if (!enabled || !task) return
    if (task.revoked) throw new ModelUnavailable()
    try { await request("tasks/check", identity(task)) }
    catch (error) { if (error instanceof ModelUnavailable) task.revoked = true; throw error }
  }
  async function begin(sessionID, messages) {
    if (!enabled) return undefined
    // Compaction may remove the original user message from projected context.
    // Only reuse this execution's already authenticated task, never a guessed ID.
    const taskID = [...messages].reverse().find((message) => message.type === "user")?.id || currentTasks.get(sessionID)?.taskID
    // Never guess a session's task from prompt text or the accounting alias main.
    if (!taskID || !sessionID || !Number.isSafeInteger(generation) || generation < 0) throw new GovernanceUnavailable()
    const id = taskKey(sessionID, taskID)
    const task = tasks.get(id) || { sessionID, taskID, revoked: false }
    tasks.set(id, task)
    currentTasks.set(sessionID, task)
    await check(task)
    return task
  }
  function headers(task) {
    return task ? {
      "X-ClawManager-Session-ID": task.sessionID,
      "X-ClawManager-Task-ID": task.taskID,
      "X-ClawManager-Runtime-Generation": String(generation),
    } : {}
  }
  async function finish(task, cancelled = false) {
    if (!enabled || !task) return
    await request("tasks/finish", { ...identity(task), cancelled })
    tasks.delete(taskKey(task.sessionID, task.taskID))
    if (currentTasks.get(task.sessionID) === task) currentTasks.delete(task.sessionID)
  }
  async function finishSession(sessionID) {
    currentTasks.delete(sessionID)
    for (const task of [...tasks.values()]) {
      if (task.sessionID !== sessionID) continue
      // Called only after the execution scope has closed and tools have joined.
      // An unreachable backend leaves the receipt unconfirmed, never successful.
      receipts.set(taskKey(task.sessionID, task.taskID), task)
    }
    await flushReceipts()
  }
  async function flushReceipts() {
    if (receiptFlush) return receiptFlush
    receiptFlush = (async () => {
      for (const [id,task] of [...receipts]) {
        try {
          try { await check(task) } catch (error) { if (!(error instanceof ModelUnavailable)) throw error }
          await finish(task, task.revoked)
          receipts.delete(id)
        } catch { /* Retain unconfirmed receipts for the next catalog tick. */ }
      }
    })().finally(() => { receiptFlush = undefined })
    return receiptFlush
  }
  async function catalog() {
    if (!enabled) return undefined
    void flushReceipts()
    if (catalogCache && now() - catalogAt < 3000) return catalogCache
    if (!catalogPending) catalogPending = request("catalog").then((value) => {
      if (!Array.isArray(value.models)) throw new GovernanceUnavailable()
      for (const item of value.models) if (!item || typeof item.id !== "string" || !item.id) throw new GovernanceUnavailable()
      catalogCache = value.models; catalogAt = now(); return catalogCache
    }).finally(() => { catalogPending = undefined })
    return catalogPending
  }
  // Start from a valid projected model to preserve provider API settings. Every
  // returned model ID and capability comes from the current authorized catalog.
  async function projectCatalog(existing) {
    if (!enabled) return existing
    const unmanaged = existing.filter((item) => item.providerID !== "clawmanager")
    const managed = existing.filter((item) => item.providerID === "clawmanager")
    const template = managed.find((item) => item.id === "auto") || managed[0]
    if (!template) return unmanaged
    let entries
    try { entries = await catalog() } catch { return unmanaged } // Never serve stale grants on failure.
    return [...unmanaged, ...entries.map((entry) => {
      const old = managed.find((item) => item.id === entry.id) || template
      return { ...old, id: entry.id, name: entry.id, enabled: true,
        api: { ...old.api, id: entry.id },
        capabilities: { ...old.capabilities, tools: entry.tools },
        limit: { ...old.limit, context: entry.context_window > 0 ? entry.context_window : old.limit.context },
      }
    })]
  }
  return { enabled, begin, check, headers, finish, finishSession, flushReceipts, catalog, projectCatalog }
}

export const governance = createGovernance()
