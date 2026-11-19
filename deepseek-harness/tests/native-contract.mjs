// Run with Node 24 in a task-owned Linux container. No external model requests.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { setTimeout as sleep } from 'node:timers/promises';
import { randomUUID, createHash } from 'node:crypto';

const install = path.resolve(process.argv[2] || '/work');
const entry = path.join(install, 'node_modules/@deepseek-ai/dsh/lib/bin.js');
const pkg = JSON.parse(await fs.readFile(path.join(install, 'node_modules/@deepseek-ai/dsh/package.json')));
assert.equal(pkg.version, '0.2.1-rc.3');
const root = await fs.mkdtemp(path.join(os.tmpdir(), 'cm-dsh-native-contract-'));
const requests = [];
let holdObserved = false;
let heldRequestClosed = false;
const model = createServer(async (req, res) => {
  const chunks = []; for await (const chunk of req) chunks.push(chunk);
  const body = JSON.parse(Buffer.concat(chunks)); requests.push(body);
  assert.equal(req.headers.authorization, 'Bearer contract-fixture');
  assert.equal(req.url, '/v1/chat/completions');
  if (JSON.stringify(body.messages.at(-1)).includes('M0_HOLD')) {
    holdObserved = true; res.on('close', () => { heldRequestClosed = true; }); return;
  }
  const usedTool = body.messages.some(message => message.role === 'tool');
  const delta = usedTool || body.messages.some(m => JSON.stringify(m).includes('M0_RESUME'))
    ? { role: 'assistant', content: 'native-contract-ok' }
    : { role: 'assistant', tool_calls: [{ index: 0, id: 'native-contract-call', type: 'function',
      function: { name: 'm0_contract_probe', arguments: '{"value":"ok"}' } }] };
  if (body.stream) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    for (const [value, finish] of [[delta, null], [{}, delta.tool_calls ? 'tool_calls' : 'stop']])
      res.write('data: ' + JSON.stringify({ id: 'fixture', object: 'chat.completion.chunk', created: 1,
        model: body.model, choices: [{ index: 0, delta: value, finish_reason: finish }] }) + '\n\n');
    res.end('data: [DONE]\n\n');
  } else {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ id: 'fixture', model: body.model, choices: [{ index: 0, message: delta,
      finish_reason: delta.tool_calls ? 'tool_calls' : 'stop' }], usage: { prompt_tokens: 5, completion_tokens: 2 } }));
  }
});
await new Promise(resolve => model.listen(0, '127.0.0.1', resolve));
const plugin = path.join(install, 'native-contract-plugin.mjs');
await fs.copyFile(new URL('./native-contract-plugin.mjs', import.meta.url), plugin);
const patch = path.join(root, 'contract.patch.yml');
// Contract fixture is independent of production configuration migration.
const base = `- id: agent-default-model
  config:
    provider: clawmanager
    model: auto
- id: llm-pi-ai
  config:
    providers:
      clawmanager:
        apiKeyEnv: CLAWMANAGER_LLM_API_KEY
        baseURL: !!js JSON.parse(process.env.CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON).base_url
        api: openai-completions
        models:
          - id: auto
            contextWindow: 64000
            input: [text]
`;
await fs.writeFile(patch, base + '\n- insert:\n  - id: cm-native-contract\n    name: ' + plugin + '\n');
const env = { ...process.env, HOME: root, DSH_HOME: path.join(root, '.dsh'),
  DSH_TELEMETRY_DISABLED: '1', M0_CONTRACT_DIR: root, CLAWMANAGER_LLM_API_KEY: 'contract-fixture',
  CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON: JSON.stringify({ default_provider: 'clawmanager', default_model: 'auto',
    providers: [{ provider_id: 'clawmanager', model_ids: ['auto'] }], api_key_env: 'CLAWMANAGER_LLM_API_KEY',
    base_url: `http://127.0.0.1:${model.address().port}/v1` }) };
let child, raw = '', origin, cookie;
async function start() {
  raw = '';
  child = spawn(process.execPath, [entry, 'web', '--patch', patch, '--host', '127.0.0.1', '--port', '0', '--no-open'],
    { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'] });
  child.stdout.on('data', b => { raw += b; }); child.stderr.on('data', b => { raw += b; });
  const until = Date.now() + 90000;
  while (Date.now() < until) {
    if (child.exitCode !== null) throw new Error('Native startup failed: ' + raw.replace(/\?token=\S+/g, '?token=[withheld]').slice(-2500));
    const match = raw.match(/http:\/\/127\.0\.0\.1:\d+\/\?token=[A-Za-z0-9_%.-]+/);
    if (match) {
      const response = await fetch(match[0], { redirect: 'manual', signal: AbortSignal.timeout(5000) });
      assert.equal(response.status, 303); cookie = response.headers.getSetCookie().map(v => v.split(';')[0]).join('; ');
      assert.ok(cookie.includes('dsh-auth-')); origin = new URL(match[0]).origin; return;
    }
    await sleep(100);
  }
  throw new Error('Native startup timeout');
}
async function stop() {
  if (!child || child.exitCode !== null) return;
  const closed = new Promise(resolve => child.once('exit', resolve)); child.kill('SIGTERM');
  await Promise.race([closed, sleep(15000, undefined, { ref: false }).then(() => { throw new Error('Native stop not confirmed'); })]);
}
async function rpc(method, args = {}) {
  const parameters = method === 'session/list' ? { _request: args } : { request: args };
  const response = await fetch(origin + '/api/' + method, { method: 'POST',
    headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json' },
    body: JSON.stringify({ type: 'client-request', rpcId: randomUUID(), method, payload: { args: parameters } }),
    signal: AbortSignal.timeout(10000) });
  assert.equal(response.status, 200, method);
  const result = (await response.json()).result; assert.ok(result.ok, method + ': ' + JSON.stringify(result)); return result.value;
}
async function idle(id) {
  const until = Date.now() + 30000;
  while (Date.now() < until) {
    const list = await rpc('session/list');
    if (list.items.some(item => item.sessionId === id && !item.running)) return;
    await sleep(100);
  }
  throw new Error('Native did not reach idle');
}
async function page(id) {
  const baseline = await rpc('session/projections', { sessionId: id });
  return rpc('session/page', { address: { kind: 'session', sessionId: id }, throughSeq: baseline.asOfSeq });
}
const prompt = (id, text) => rpc('session/prompt', { sessionId: id, requestId: randomUUID(), mode: 'queue', content: [{ type: 'text', text }] });
try {
  await start();
  const created = await rpc('session/create', { cwd: root, agentPreset: 'standard' });
  await prompt(created.sessionId, 'M0_PROBE');
  const until = Date.now() + 30000;
  while (!requests.some(body => body.messages.some(m => m.role === 'tool')) && Date.now() < until) await sleep(100);
  await idle(created.sessionId);
  const tool = JSON.parse(await fs.readFile(path.join(root, 'tool-context.json')));
  assert.equal(tool.agent, created.sessionId); assert.equal(tool.session, created.sessionId);
  for (const name of ['followup', 'cancel', 'whenIdle', 'inject']) assert.equal(tool.methods[name], 'function');
  const first = JSON.stringify(await page(created.sessionId)); assert.ok(first.includes('native-contract-ok'));
  assert.ok(first.includes('clawmanager-team'), 'Custom non-human source must persist');
  const before = requests.length;
  await prompt(created.sessionId, 'M0_HOLD');
  while (!holdObserved && Date.now() < until + 30000) await sleep(100);
  assert.ok(holdObserved);
  assert.ok((await rpc('session/list')).items.some(item => item.sessionId === created.sessionId && item.running), 'Cancel must target an actually running model request');
  await rpc('session/cancel', { sessionId: created.sessionId }); await idle(created.sessionId);
  assert.ok(heldRequestClosed, 'Cancellation must close the held model request');
  assert.ok(requests.length > before);
  await stop(); await start();
  await prompt(created.sessionId, 'M0_RESUME'); await sleep(500); await idle(created.sessionId);
  const resumed = JSON.stringify(await page(created.sessionId));
  assert.ok(resumed.includes('native-tool-ok')); assert.ok(resumed.includes('M0_RESUME'));
  const sandbox = await fs.readFile(path.join(install, 'node_modules/@deepseek-ai/dsh-sandbox-local/lib/index.js'));
  console.log(JSON.stringify({ native: pkg.version, native_tool_attribution: true, nonhuman_source_persisted: true,
    cancelled_and_idle: true, same_session_resumed: true, public_answer_persisted: true,
    sandbox_sha256: createHash('sha256').update(sandbox).digest('hex'), requests: requests.length }));
} finally {
  await stop(); model.closeAllConnections(); await new Promise(resolve => model.close(resolve));
  await fs.rm(root, { recursive: true, force: true });
}
