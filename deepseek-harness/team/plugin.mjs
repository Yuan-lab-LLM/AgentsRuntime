import { Service } from '@deepseek-ai/cordis';
import { defineTool } from '@deepseek-ai/dsh-tools';
import { freezeMessage } from '@deepseek-ai/dsh-llm';
import { installModelSelection } from '@deepseek-ai/dsh-agent';
import fs from 'node:fs/promises';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { createBridge } from '../../opencode/team/bridge.mjs';
import { DshHost } from './host.mjs';
import { nativeParameters } from './schema.mjs';
import * as protocol from './protocol.mjs';

export default class TeamHost {
  static inject = ['tools', 'agents', 'sessions', 'sessionQuery', 'sessionPersistence', 'agentPresets', 'agentDefaultModel'];
  constructor(ctx) { this.ctx = ctx; }
  async *[Service.init]() {
    if (process.env.CLAWMANAGER_TEAM_ENABLED !== 'true') return;
    if (process.env.CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION !== 'dsh-team-v1') throw Error('DSH Team image capability is unavailable');
    const cfg = protocol.readChannelConfig({}), state = protocol.runtimeStateDir(cfg);
    await fs.mkdir(state, { recursive: true, mode: 0o700 });
    const lockFile = path.join(state, 'dsh-native-host.json'), nonce = randomUUID();
    const processStart = async pid => { try { return (await fs.readFile(`/proc/${pid}/stat`, 'utf8')).split(') ')[1].split(' ')[19]; } catch (e) { if (e.code !== 'ENOENT') throw e; } };
    let locked = false, bridge;
    const host = new DshHost({ ctx: this.ctx, directory: path.join(state, 'dsh-inputs'), freezeMessage, installModelSelection });
    // The old consumer and native execution must finish before the lock clears.
    yield async () => {
      await bridge?.stop(); await host.close();
      if (locked && JSON.parse(await fs.readFile(lockFile, 'utf8')).nonce === nonce) await fs.unlink(lockFile);
    };
    try {
      await fs.writeFile(lockFile, JSON.stringify({ pid: process.pid, start: await processStart(process.pid), nonce }), { flag: 'wx', mode: 0o600 }); locked = true;
    } catch (e) {
      if (e.code !== 'EEXIST') throw e;
      const old = JSON.parse(await fs.readFile(lockFile, 'utf8'));
      if (await processStart(old.pid) === old.start) throw Error('Previous DSH Team host still owns execution');
      await fs.unlink(lockFile);
      await fs.writeFile(lockFile, JSON.stringify({ pid: process.pid, start: await processStart(process.pid), nonce }), { flag: 'wx', mode: 0o600 }); locked = true;
    }
    // DSH inherits its trusted cwd from the gateway supervisor. Unlike OpenCode
    // it does not require a product-specific project environment variable.
    bridge = await createBridge({ protocol, native: host, runtimeName: 'DeepSeek Harness',
      environment: { ...process.env, CLAWMANAGER_DEFAULT_PROJECT_PATH: process.cwd() } });
    const definitions = {
      team_send: [protocol.teamSendParameters, 'Send a real assignment or handoff using to and text. Reuse already running or accepted work.'],
      team_status: [protocol.teamStatusParameters, 'Read current accepted results and workflow facts. Task files are dispatch snapshots; do not repeat accepted work.'],
      team_update_progress: [protocol.progressParameters, 'Report actual progress. Current assignment identity is inherited automatically.'],
      team_complete_task: [protocol.completeParameters, 'Submit status=succeeded, failed or cancelled and summary. Put the full answer in optional resultMarkdown and real files in artifactRefs. Text answers need no file. Identity is inherited; a Worker cannot close the Leader root.'],
      team_artifact_write: [protocol.artifactWriteParameters, 'Atomically write path and content in this Team. A simple filename uses the active assignment; Leader final reports use scope=team, kind=final. Reuse returned canonical paths.'],
      team_artifact_read: [protocol.artifactReadParameters, 'Read a UTF-8 Team file using path.'],
      team_artifact_list: [protocol.artifactListParameters, 'List this Team files using optional path.'],
      team_artifact_mkdir: [protocol.artifactMkdirParameters, 'Create a member-scoped Team directory using path.'],
      team_artifact_preview: [protocol.artifactPreviewParameters, 'Get a signed read-only Team preview. Do not start preview servers.'],
    };
    for (const [name, [schema, description]] of Object.entries(definitions)) {
      const parameters = nativeParameters(schema);
      if (name === 'team_complete_task') {
        parameters.status.enum = [...parameters.status.enum, 'completed','done','success'];
        parameters.resultMarkdown.description = 'Full public result, optional for a short answer; preserve complete content for the Leader.';
      }
      this.ctx.tools.register(defineTool({ name, description, parameters,
        output: { schema: { type: 'string' }, render: (_args, value) => [{ type: 'text', text: value }] },
        execute: async (args, exec) => {
          const identity = await host.toolIdentity(exec);
          const response = await fetch(`http://127.0.0.1:${bridge.port}/tool`, { method: 'POST', redirect: 'error',
            signal: AbortSignal.any([exec.signal || new AbortController().signal, AbortSignal.timeout(30000)]),
            headers: { Authorization: `Bearer ${bridge.token}`, 'Content-Type': 'application/json' },
            body: JSON.stringify({ ...identity, name, arguments: args }) });
          const value = await response.json();
          if (!response.ok) throw Error(response.status === 422 ? value.error || 'Invalid Team path or arguments; correct this call using the active assignment' : 'Team result is unconfirmed; inspect original work before retrying');
          return JSON.stringify(value);
        },
      }));
    }
    await bridge.start();
  }
}
