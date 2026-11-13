import fs from 'node:fs/promises';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { nativeIdentity } from '../../opencode/team/native-client.mjs';

const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
// Native-generated context augments the current input; it is not another
// user/Team delivery. Keep this allowlist narrow: unknown sources retire tools.
const nativeContext = source => (['runtime-context', 'time-context', 'agent-instructions'].includes(source?.kind) || (source?.kind === 'clawmanager-team-context' && source?.origin === 'clawmanager-team-control'));
export function sessionIdentity(identity) {
  const h = hash([identity.teamId, identity.memberId, identity.taskId, identity.messageId]);
  return `session-${h.slice(0,8)}-${h.slice(8,12)}-${h.slice(12,16)}-${h.slice(16,20)}-${h.slice(20,32)}`;
}
export function turnFacts(events, input) {
  const facts = { consumed: false, failed: false, observed: {} };
  let owns = false;
  let candidate;
  for (const e of events) {
    if (e.type === 'agent/inbox/spliced' && e.data.inserted?.some(m => m.source?.kind === 'clawmanager-team' && m.source.inputId === input)) owns = true;
    if (e.type === 'user/message') {
      const source = e.data.source;
      if (source?.kind === 'clawmanager-team') owns = source.inputId === input;
      else if (!nativeContext(source)) owns = false;
      if (owns) facts.consumed = true;
    }
    if (!owns) continue;
    facts.observed = { ...facts.observed, sessionCursor: String(e.seq), mtimeMs: e.time,
      lastSessionEventAt: new Date(e.time).toISOString() };
    if (e.type === 'assistant/message') {
      facts.observed.lastActivityKind = 'model_output';
      const text = (e.data.message.content || []).filter(x => x.type === 'text').map(x => x.text).join('\n');
      candidate = text.trim() ? { text, recordId: e.data.message.id, at: new Date(e.time).toISOString() } : undefined;
    }
    if (e.type === 'tool/call') { candidate = undefined; Object.assign(facts.observed, { lastActivityKind: 'tool_call', pendingToolName: e.data.name || '' }); }
    if (e.type === 'tool/result') Object.assign(facts.observed, { lastActivityKind: 'tool_result', pendingToolName: '' });
    if (e.type === 'turn/end') {
      facts.consumed = true; // pre-request failures can occur before user/message
      facts.failed = e.data.reason?.kind !== 'completed';
      if (facts.failed) delete facts.publicText;
      else if (candidate) facts.publicText = candidate;
    }
  }
  return facts;
}

// Host-owned native identity journal. It is not a work queue or task authority.
// One native session per immutable transport input makes an old turn unable to
// acquire tools for a later assignment, even when the root is shared.
export class DshHost {
  constructor({ ctx, directory, freezeMessage, installModelSelection }) {
    Object.assign(this, { ctx, directory, freezeMessage, installModelSelection });
    this.handles = new Map(); this.current = null; this.admitting = false;
  }
  async events(id) {
    const observation = await this.ctx.sessionQuery.observeSession(id, { projectionMode: 'none' });
    try { return { header: observation.header, events: [...observation.events] }; }
    finally { observation[Symbol.dispose](); }
  }
  async journal(input, value) {
    await fs.mkdir(this.directory, { recursive: true, mode: 0o700 });
    const file = path.join(this.directory, input + '.json');
    if (value) {
      await fs.writeFile(file + '.tmp', JSON.stringify(value), { mode: 0o600 });
      await fs.rename(file + '.tmp', file);
    } else {
      try { return JSON.parse(await fs.readFile(file, 'utf8')); }
      catch (e) { if (e.code !== 'ENOENT') throw e; }
    }
  }
  async admit(identity, directory, text) {
    if (this.admitting) throw Error('DSH admission already in progress');
    this.admitting = true;
    try {
      if (!path.isAbsolute(directory || '') || typeof text !== 'string' || text.length > 512000) throw Error('Invalid DSH Team input');
      const ids = nativeIdentity(identity), id = sessionIdentity(identity), digest = hash([identity, directory, text]);
      const old = await this.journal(ids.message);
      if (old && (old.digest !== digest || old.generation !== identity.generation)) throw Error('Conflicting or retired DSH input');
      if (this.current?.agent?.status === 'running' && this.current.id !== id) throw Error('Previous DSH execution has not stopped');
      if (this.current && this.current.id !== id) {
        const previous = this.handles.get(this.current.id);
        if (previous) { await previous.agent.whenIdle(); await previous.dispose(); this.handles.delete(this.current.id); }
      }
      this.current = { identity, ids, id, digest };
      let saved;
      try { saved = await this.events(id); }
      catch (e) { if (e.code !== 'SESSION_QUERY_SESSION_NOT_FOUND') throw e; }
      if (saved && saved.header.cwd !== directory) throw Error('DSH session directory mismatch');
      const recordedInputs = (saved?.events || []).flatMap(e => e.type === 'agent/inbox/spliced' ? e.data.inserted || [] : e.type === 'user/message' ? [e.data] : []);
      for (const message of recordedInputs) if (message.source?.kind === 'clawmanager-team') {
        const source = message.source;
        if (source.inputId !== ids.message || source.teamId !== identity.teamId || source.memberId !== identity.memberId || source.generation !== identity.generation)
          throw Error('DSH native input provenance or generation mismatch');
      }
      const setup = async (agentCtx, agent) => {
        // Same supported preset composition as the native Web host, without
        // exposing browser credentials or classifying Team input as a human.
        const defaults = this.ctx.agentDefaultModel;
        this.installModelSelection(agentCtx, { get current() { return defaults.currentSelection(); }, consume() { return false; } });
        await this.ctx.agentPresets.mount(agentCtx, 'standard');
      };
      let handle = this.handles.get(id);
      if (!handle) {
        handle = saved ? await this.ctx.agents.resume({ resumeSessionId: id, setup })
          : await this.ctx.agents.create({ sessionId: id, meta: { cwd: directory, agentPreset: 'standard' }, setup });
        this.handles.set(id, handle);
      }
      this.current.agent = handle.agent;
      const hasInput = (saved?.events || []).some(e => e.type === 'agent/inbox/spliced' && e.data.inserted?.some(m => m.source?.inputId === ids.message))
        || (saved?.events || []).some(e => e.type === 'user/message' && e.data.source?.inputId === ids.message);
      if (hasInput) return ids;
      // A reservation surviving without a native append is ambiguous. Never
      // resubmit it or call createUserMessage again after response loss.
      if (old) throw Error('DSH input outcome is uncertain; original execution must be reconciled');
      await this.journal(ids.message, { digest, generation: identity.generation, sessionId: id, state: 'reserved' });
      const message = this.freezeMessage({ id: 'cm-' + ids.message, role: 'user', content: [{ type: 'text', text }],
        source: { kind: 'clawmanager-team', inputId: ids.message, teamId: identity.teamId, memberId: identity.memberId, generation: identity.generation } });
      handle.agent.followup(message);
      if (!await this.ctx.sessions.flush(handle.agent.session)) throw Error('DSH durable input checkpoint unavailable');
      await this.journal(ids.message, { digest, generation: identity.generation, sessionId: id, state: 'admitted' });
      return ids;
    } finally { this.admitting = false; }
  }
  async inspectTurn(identity) {
    const current = this.current;
    if (!current || current.id !== sessionIdentity(identity)) throw Error('DSH observation identity mismatch');
    return turnFacts((await this.events(current.id)).events, current.ids.message);
  }
  async steer(identity, text, noticeID) {
    const current=this.current;
    if(!current || current.id!==sessionIdentity(identity) || current.ids.message!==nativeIdentity(identity).message || current.agent?.status!=='running')return;
    const id='cm-context-'+hash([current.ids.message,noticeID]);
    const prior=(await this.events(current.id)).events.find(e=>e.type==='user/message'&&e.data.id===id);
    if(!prior && ![...(current.agent.inbox?.nextStep||[])].some(m=>m.id===id)) {
      // Native reserves runtime-context for its own generated snapshot and discards
      // caller text with that kind. Keep our signed/owned context distinct.
      current.agent.steer(this.freezeMessage({id,role:'user',content:[{type:'text',text}],source:{kind:'clawmanager-team-context',origin:'clawmanager-team-control',inputId:current.ids.message,contextMessageId:noticeID,teamId:identity.teamId,memberId:identity.memberId,generation:identity.generation}}));
      if(!await this.ctx.sessions.flush(current.agent.session))throw Error('DSH context checkpoint unavailable');
    }
    return {state:'submitted',nativeSessionId:current.id,nativeMessageId:id};
  }
  async observeContext(record, identity) {
    if(!this.current || this.current.id!==sessionIdentity(identity))return;
    const expected=record.nativeMessageId||'cm-context-'+hash([this.current.ids.message,record.envelope.messageId]);
    const consumed=(await this.events(this.current.id)).events.some(e=>e.type==='user/message'&&e.data.id===expected&&e.data.source?.origin==='clawmanager-team-control');
    if(consumed)return {state:'native_context_consumed',nativeSessionId:this.current.id,nativeMessageId:expected};
  }
  async api(resource) {
    const current = this.current;
    if (resource === '/api/session/active') return { data: current?.agent?.status === 'running' ? { [current.ids.session]: true } : {} };
    if (resource === `/api/session/${current?.ids.session}/inbox`) return { data: [...(current.agent?.inbox.nextTurn || []), ...(current.agent?.inbox.nextStep || [])] };
    throw Error('Unsupported DSH host observation');
  }
  async interrupt(identity) {
    if (!this.current || this.current.id !== sessionIdentity(identity)) throw Error('DSH stop identity mismatch');
    const agent = this.current.agent;
    if (agent) { agent.cancel({ kind: 'user' }); await agent.whenIdle(); if (agent.status !== 'idle') throw Error('DSH stop not confirmed'); }
  }
  async toolIdentity(exec) {
    const current = this.current;
    if (!current?.agent || exec.agent !== current.agent || this.ctx.agents.get(current.id) !== exec.agent || !exec.callId)
      throw Error('Team tools require the current native Team execution');
    const { events } = await this.events(current.id);
    const sources = events.filter(e => e.type === 'user/message' && !nativeContext(e.data.source));
    const source = sources.at(-1)?.data.source;
    if (source?.kind !== 'clawmanager-team' || source.inputId !== current.ids.message ||
        String(source.teamId) !== current.identity.teamId || source.memberId !== current.identity.memberId ||
        source.generation !== current.identity.generation) throw Error('Native input is not owned by Team');
    return { sessionID: current.ids.session, taskID: current.ids.message, generation: current.identity.generation, callID: exec.callId };
  }
  async close() {
    if (this.current) await this.interrupt(this.current.identity);
    for (const handle of this.handles.values()) await handle.dispose();
    this.handles.clear();
  }
}
