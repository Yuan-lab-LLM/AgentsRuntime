import { createHash, createHmac } from "node:crypto";

export class NativeRequestError extends Error {
  constructor(status) { super(`OpenCode native request failed (${status})`); this.status = status; }
}

// Identity is supplied by the authenticated Team transport, never by model tools.
export function nativeIdentity({ teamId, memberId, taskId, messageId, generation }) {
  if (![teamId, memberId, taskId, messageId].every(v => typeof v === "string" && v.length > 0 && v.length <= 512) || !Number.isSafeInteger(generation) || generation < 0)
    throw new Error("Invalid Team execution identity");
  const hash = value => createHash("sha256").update(JSON.stringify(value)).digest("hex");
  return {
    session: `ses_${hash([teamId, memberId, taskId])}`,
    message: `msg_${hash([teamId, memberId, taskId, messageId])}`,
  };
}

export class NativeClient {
  constructor({ port, password, username = "opencode", request = fetch }) {
    if (!Number.isInteger(port) || port < 1 || port > 65535 || !password || /[\r\n]/.test(username)) throw new Error("Invalid OpenCode native connection");
    this.origin = `http://127.0.0.1:${port}`;
    this.auth = `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`;
    this.request = request;
  }

  async api(resource, { body, signal, method } = {}) {
    const response = await this.request(this.origin + resource, {
      method: method ?? (body === undefined ? "GET" : "POST"), redirect: "error",
      headers: { Authorization: this.auth, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(15000)]) : AbortSignal.timeout(15000),
    });
    if (!response.ok) { await response.body?.cancel(); throw new NativeRequestError(response.status); }
    if (response.status === 204) return null;
    const reader = response.body.getReader();
    let size = 0; const chunks = [];
    try {
      while (true) {
        const { done, value } = await reader.read(); if (done) break;
        size += value.byteLength;
        if (size > 4 * 1024 * 1024) throw new Error("OpenCode native response too large");
        chunks.push(value);
      }
      return JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
  }

  async ensureSession(identity, directory, signal) {
    if (typeof directory !== "string" || !directory.startsWith("/") || directory.includes("\0")) throw new Error("Invalid Team project directory");
    const ids = nativeIdentity(identity);
    const check = record => {
      if (record?.data?.id !== ids.session || record.data.location?.directory !== directory || record.data.parentID || record.data.metadata?.["clawmanager.team"] !== identity.teamId || record.data.metadata?.["clawmanager.member"] !== identity.memberId)
        throw new Error("OpenCode Team session ownership mismatch");
      return ids;
    };
    try { return check(await this.api(`/api/session/${ids.session}`, { signal })); }
    catch (error) { if (!(error instanceof NativeRequestError) || error.status !== 404) throw error; }
    // On a lost response, a retry observes this same deterministic session.
    try {
      return check(await this.api("/api/session", { signal, body: {
        id: ids.session, title: "Team task", location: { directory },
        metadata: { "clawmanager.team": identity.teamId, "clawmanager.member": identity.memberId },
      } }));
    } catch (error) {
      if (!(error instanceof NativeRequestError) || error.status !== 409) throw error;
      return check(await this.api(`/api/session/${ids.session}`, { signal }));
    }
  }

  // Upstream 2.0.27 Bus.configured() defaults persist=false. Its historical
  // SSE endpoint can return only log.synced, so use the materialized native
  // messages for this exact admitted input without enabling unbounded logging.
  async inspectTurn(identity, signal) {
    const ids=nativeIdentity(identity);
    let input;
    try { input=(await this.api(`/api/session/${ids.session}/message/${ids.message}`,{signal})).data; }
    catch(error){if(error instanceof NativeRequestError && error.status===404)return {consumed:false};throw error;}
    if(input?.id!==ids.message || input.type!=="user")throw Error("Native input ownership mismatch");
    let cursor=Buffer.from(JSON.stringify({id:ids.message,order:"asc",direction:"next"})).toString("base64url");
    const facts={consumed:true,failed:false,observed:{}};
    const seen=new Set();
    for(let page=0;page<10;page++){
      const result=await this.api(`/api/session/${ids.session}/message?limit=100&cursor=${encodeURIComponent(cursor)}`,{signal});
      if(!Array.isArray(result.data))throw Error("Invalid native message projection");
      for(const message of result.data){
        if(seen.has(message.id))throw Error("Repeated native message cursor");
        seen.add(message.id);
        if(message.type==="user") {
          if(this.isOwnedContext(message, ids))continue;
          return facts; // never cross another admitted business input
        }
        const at=Number(message.time?.completed || message.time?.streamed || message.time?.created || 0);
        if(at)Object.assign(facts.observed,{mtimeMs:at,lastSessionEventAt:new Date(at).toISOString(),sessionCursor:message.id});
        if(message.type==="idle") { facts.failed=message.outcome!=="succeeded";facts.outcome=message.outcome;return facts; }
        if(message.type!=="assistant")continue;
        facts.observed.sessionCursor=message.id+":"+(message.content||[]).map(p=>p.type+":"+String(p.text||"").length+":"+String(p.state?.status||"")).join("|");
        facts.observed.lastActivityKind="model_output";
        if(message.error)facts.failed=true;
        for(const part of message.content||[]){
          if(part.type==="tool")Object.assign(facts.observed,{lastActivityKind:part.state?.status==="running"?"tool_call":"tool_result",lastToolName:part.name,pendingToolName:part.state?.status==="running"?part.name:"",lastToolFailed:part.state?.status==="error"});
        }
        // Final public text only; intermediate prose, reasoning and raw tool
        // arguments/results never become a Team delivery or observation body.
        if(message.finish==="stop" && message.time?.completed){
          const text=(message.content||[]).filter(p=>p.type==="text").map(p=>String(p.text||"")).join("\n");
          if(text.trim())facts.publicText={text,recordId:message.id,at:new Date(at).toISOString()};
        }
      }
      if(result.data.length<100)return facts;
      const next=result.cursor?.next;
      if(!next || next===cursor)throw Error("Native message continuation unavailable");
      cursor=next;
    }
    throw Error("Native input observation window exceeded");
  }

  async readLog(identity, after, observe, signal) {
    const ids = nativeIdentity(identity);
    const response = await this.request(`${this.origin}/api/experimental/session/${ids.session}/log?after=${after}&follow=false`, {
      redirect: "error", headers: { Authorization: this.auth, Accept: "text/event-stream" },
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(15000)]) : AbortSignal.timeout(15000),
    });
    if (!response.ok) { await response.body?.cancel(); throw new NativeRequestError(response.status); }
    if (!response.headers.get("content-type")?.includes("text/event-stream") || !response.body) throw new Error("Invalid native event log");
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let pending = "", cursor = after;
    try {
      while (true) {
        const { done, value } = await reader.read();
        pending += done ? decoder.decode() : decoder.decode(value, { stream: true });
        pending = pending.replaceAll("\r\n", "\n");
        let boundary;
        while ((boundary = pending.indexOf("\n\n")) >= 0) {
          const frame = pending.slice(0, boundary); pending = pending.slice(boundary + 2);
          if (frame.length > 4 * 1024 * 1024) throw new Error("Native event exceeds limit");
          if (frame.split("\n").some(line => line.startsWith("event:") && line.includes("failure"))) throw new Error("Native event stream failed");
          const data = frame.split("\n").filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n");
          if (!data) continue;
          const event = JSON.parse(data);
          if (event.type === "log.synced") continue;
          if (event.data?.sessionID !== ids.session || event.durable?.aggregateID !== ids.session || !Number.isSafeInteger(event.durable?.seq)) throw new Error("Native event ownership mismatch");
          if (event.durable.seq <= cursor) continue;
          await observe(event);
          cursor = event.durable.seq;
        }
        if (pending.length > 4 * 1024 * 1024) throw new Error("Native event exceeds limit");
        if (done) { if (pending.trim()) throw new Error("Incomplete native event"); break; }
      }
      return cursor;
    } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
  }

  async pendingInteraction(identity, signal) {
    const { session } = nativeIdentity(identity);
    const [permissions, forms] = await Promise.all([
      this.api(`/api/session/${session}/permission`, { signal }),
      this.api("/api/form", { signal }),
    ]);
    const own = value => Array.isArray(value?.data) ? value.data.filter(item => item.sessionID === session) : [];
    const requests = [...own(permissions).map(item => ({ id: item.id, kind: "permission" })),
      ...own(forms).map(item => ({ id: item.id, kind: item.metadata?.kind === "websearch.provider" ? "search_provider" : "question" }))];
    return { pending: requests.length > 0, kinds: [...new Set(requests.map(item => item.kind))].sort(), ids: requests.map(item => String(item.id)).sort() };
  }

  async admit(identity, directory, text, signal) {
    if (typeof text !== "string" || text.length > 512000) throw new Error("Invalid Team input");
    const ids = await this.ensureSession(identity, directory, signal);
    // v2 durably admits caller-supplied input IDs and rejects conflicting replays.
    await this.api(`/api/session/${ids.session}/prompt`, { signal, body: {
      id: ids.message, text, delivery: "queue",
      metadata: { "clawmanager.team": identity.teamId, "clawmanager.member": identity.memberId, "clawmanager.task": identity.taskId, "clawmanager.message": identity.messageId },
    } });
    return ids;
  }

  isOwnedContext(message, ids) {
    const meta=message?.metadata, parent=meta?.["clawmanager.contextFor"], notice=meta?.["clawmanager.contextMessageId"];
    if(!this.contextKey || message?.type!=="user" || parent!==ids.message || typeof notice!=="string" || !notice)return false;
    const signature=createHmac("sha256",this.contextKey).update(JSON.stringify([ids.session,message.id,parent,Number(meta["clawmanager.contextGeneration"]),notice])).digest("hex");
    return meta["clawmanager.contextSignature"]===signature;
  }

  async steer(identity, text, noticeID, signal) {
    const ids=nativeIdentity(identity);
    const state=await this.api("/api/session/active",{signal});
    if(!state?.data?.[ids.session])return;
    const notice=nativeIdentity({...identity,messageId:"context:"+noticeID});
    const signature=createHmac("sha256",this.contextKey).update(JSON.stringify([ids.session,notice.message,ids.message,identity.generation,noticeID])).digest("hex");
    await this.api(`/api/session/${ids.session}/prompt`,{signal,body:{id:notice.message,text,delivery:"steer",metadata:{"clawmanager.team":identity.teamId,"clawmanager.member":identity.memberId,"clawmanager.contextFor":ids.message,"clawmanager.contextMessageId":noticeID,"clawmanager.contextGeneration":identity.generation,"clawmanager.contextSignature":signature}}});
    return {state:"submitted",nativeSessionId:ids.session,nativeMessageId:notice.message};
  }

  async observeContext(record, identity) {
    const ids=nativeIdentity(identity),notice=nativeIdentity({...identity,messageId:"context:"+record.envelope.messageId});
    try {
      const input=(await this.api(`/api/session/${ids.session}/message/${notice.message}`)).data;
      if(this.isOwnedContext(input,ids))return {state:"native_context_consumed",nativeSessionId:ids.session,nativeMessageId:notice.message};
    }catch(error){if(!(error instanceof NativeRequestError)||error.status!==404)throw error;}
  }

  async interrupt(identity, signal) {
    const ids = nativeIdentity(identity);
    await this.api(`/api/session/${ids.session}/interrupt?resume=false`, { signal, body: {} });
    // A successful HTTP response alone does not establish idle. Re-read native state.
    const state = await this.api("/api/session/active", { signal });
    if (state?.data?.[ids.session]) throw new Error("OpenCode Team execution stop is unconfirmed");
    return ids;
  }
}
