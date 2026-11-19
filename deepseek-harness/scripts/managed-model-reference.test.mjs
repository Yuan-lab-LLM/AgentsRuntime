import { test, beforeEach, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { execFileSync } from 'node:child_process';
import { resolveManagedSelection, filterManagedModels, readCatalog, withManagedTurn, managedRequestHeaders, createHarnessFailStop, verifiedHarnessGroup, terminateHarnessRuntime } from './managed-model-reference.mjs';
import { transform, install, contracts } from './patch-model-reference.mjs';

class LlmError extends Error { constructor(message, code) { super(message); this.code = code; } }
const base = 'https://gateway.test/llm';
const key = 'test-instance-token';
const model = { id: 'new-model', stable_id: 'cm-deployment-1', aliases: ['old-model', 'namespace/old-model'],
  provider_refs: [{ provider_id: 'old-provider', model_id: 'old-model' }, { provider_id: 'new-provider', model_id: 'new-model' }] };
let items, requests, savedFetch, savedEnv;
beforeEach(() => {
  items = [{ id: 'auto', provider_refs: [{ provider_id: 'auto', model_id: 'auto' }] }, model];
  requests = [];
  savedFetch = globalThis.fetch;
  savedEnv = { ...process.env };
  process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL = '1';
  process.env.CLAWMANAGER_LLM_BASE_URL = base;
  process.env.CLAWMANAGER_LLM_API_KEY = key;
  globalThis.fetch = async (url, options) => { requests.push({ url, options }); return Response.json({ models: items }); };
});
afterEach(() => { globalThis.fetch = savedFetch; process.env = savedEnv; });

function registry(routes = { 'new-provider': ['new-model', 'auto'] }) {
  return { adapters: new Map(Object.entries(routes).map(([provider, ids]) => [provider, {
    provider: { id: provider }, adapter: { clawmanagerModelProfile: async () => ({ baseURL: base, apiKey: key, models: ids.map(id => ({ id })) }) },
  }])) };
}

function turnMachine(session = 'session-one') {
  return { loopCtx: { llm: registry() }, options: { provider: 'new-provider' },
    session: { id: session }, phase: { kind: 'running', turn: 4, abort: new AbortController() },
    cancel(cause) { this.phase.abort.abort(cause); } };
}

test('Harness force-stop is explicit, single-shot, admission-closing and bounded', () => {
  let scheduled, milliseconds, exits = 0;
  const stop = createHarnessFailStop({ terminate: () => exits++, schedule: (fn, ms) => { scheduled = fn; milliseconds = ms; } });
  stop.arm();
  assert.equal(scheduled, undefined);
  process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL = '1';
  process.env.CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE = '1';
  process.env.CLAWMANAGER_TEAM_ENABLED = 'true';
  stop.arm();
  assert.equal(scheduled, undefined);
  process.env.CLAWMANAGER_TEAM_ENABLED = '';
  stop.arm();
  assert.equal(milliseconds, 10000);
  assert.throws(() => stop.assertAdmission(), { code: 'MODEL_UNAVAILABLE' });
  process.env.CLAWMANAGER_HARNESS_CANCEL_GRACE_SECONDS = '1';
  stop.arm();
  assert.equal(milliseconds, 10000);
  assert.equal(exits, 0);
  scheduled();
  assert.equal(exits, 1);
});

test('Harness Pro verifies process identity, group and session before group stop', () => {
  const stat = '123 (node ) tricky name) S 1 500 500 0 0';
  assert.equal(verifiedHarnessGroup(stat, '500', 123), 500);
  for (const [text, group, pid] of [
    [stat, '501', 123], [stat, '500', 124], [stat, '1', 123],
    ['123 (node) S 1 500 501 0', '500', 123], ['bad', '500', 123],
  ]) assert.equal(verifiedHarnessGroup(text, group, pid), undefined);
  process.env.CLAWMANAGER_HARNESS_FAIL_STOP_PGID = '500';
  const signals = [], exits = [];
  terminateHarnessRuntime({ platform:'linux', pid:123, read:()=>stat,
    kill:(...args)=>signals.push(args), exit:code=>exits.push(code) });
  assert.deepEqual(signals, [[-500, 'SIGKILL']]);
  assert.deepEqual(exits, [78]);
  signals.length = 0;
  terminateHarnessRuntime({ platform:'linux', pid:124, read:()=>stat,
    kill:(...args)=>signals.push(args), exit:code=>exits.push(code) });
  assert.equal(signals.length, 0);
  assert.deepEqual(exits, [78,78]);
});

test('Harness Pro private session launcher is explicit and excludes shared/Team modes', () => {
  const source = readFileSync(new URL('./start-deepseek-harness-pro', import.meta.url), 'utf8');
  for (const text of ['CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL:-',
    'CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE:-', '"$shared_runtime" == "false"',
    '1|true|yes|on|enabled) ;;', 'exec setsid --wait /bin/bash -c',
    'CLAWMANAGER_HARNESS_FAIL_STOP_PGID="$$"']) assert.ok(source.includes(text), text);
  assert.ok(source.indexOf('shared_runtime=false') < source.indexOf('unset RUNTIME_AGENT_CONTROL_TOKEN'));
});

test('real isolated child exits only for confirmed task revocation', () => {
  for (const confirmed of [true, false]) {
    const code = [
      'import { withManagedTurn } from ' + JSON.stringify(new URL('./managed-model-reference.mjs', import.meta.url).href) + ';',
      'const base = "https://gateway.test/llm", key = "child-test-token";',
      'Object.assign(process.env, {CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL:"1", CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE:"1", CLAWMANAGER_HARNESS_CANCEL_GRACE_SECONDS:"1", CLAWMANAGER_GATEWAY_GENERATION:"3", CLAWMANAGER_LLM_BASE_URL:base, CLAWMANAGER_LLM_API_KEY:key, CLAWMANAGER_TEAM_ENABLED:""});',
      'let count=0; globalThis.fetch=async()=> ++count===1 ? Response.json({}) : new Response("",{status:403,headers:' + JSON.stringify(confirmed ? {'X-ClawManager-Task-State':'revoked'} : {}) + '});',
      'const machine={session:{id:"child-session"},options:{provider:"managed"},phase:{kind:"running",turn:0,abort:new AbortController()},cancel(){this.phase.abort.abort();},loopCtx:{llm:{adapters:new Map([["managed",{adapter:{clawmanagerModelProfile:async()=>({baseURL:base,apiKey:key,models:[]})}}]])}}};',
      confirmed
        ? 'await withManagedTurn(machine,()=>new Promise(()=>{}));'
        : 'try { await withManagedTurn(machine,()=>new Promise(resolve=>machine.phase.abort.signal.addEventListener("abort",resolve,{once:true}))); } catch {} await new Promise(resolve=>setTimeout(resolve,1500));',
    ].join('\n');
    if (confirmed) assert.throws(() => execFileSync(process.execPath, ['--input-type=module','-e',code], {timeout:8000}), error => error.status===78);
    else execFileSync(process.execPath, ['--input-type=module','-e',code], {timeout:8000});
  }
});

test('Harness task bridge preserves native identity and ends only ordinary completion', async () => {
  process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL = '1';
  process.env.CLAWMANAGER_GATEWAY_GENERATION = '3';
  let escaped;
  await withManagedTurn(turnMachine(), async () => {
    const input = { 'x-clawmanager-task-id': 'spoofed', other: 'kept' };
    const headers = managedRequestHeaders(base, key, input);
    assert.equal(headers['x-clawmanager-task-id'], 'turn-5');
    assert.equal(headers['x-clawmanager-session-id'], 'session-one');
    assert.equal(headers['x-clawmanager-runtime-generation'], '3');
    assert.equal(input['x-clawmanager-task-id'], 'spoofed');
    escaped = () => managedRequestHeaders(base, key);
  });
  assert.deepEqual(requests.map(r => r.url), [base + '/runtime/tasks/check', base + '/runtime/tasks/check', base + '/runtime/tasks/finish']);
  assert.equal(JSON.parse(requests.at(-1).options.body).cancelled, false);
  assert.throws(escaped, { code: 'MODEL_UNAVAILABLE' });
});

test('Harness task bridge cancels native work on revocation and sends no false completion', async () => {
  process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL = '1';
  process.env.CLAWMANAGER_GATEWAY_GENERATION = '3';
  const machine = turnMachine();
  let count = 0;
  globalThis.fetch = async () => ++count === 1 ? Response.json({}) : new Response('', { status: 403 });
  await assert.rejects(withManagedTurn(machine, () => new Promise(resolve => {
    machine.phase.abort.signal.addEventListener('abort', resolve, { once: true });
  })), { code: 'MODEL_UNAVAILABLE' });
  assert.equal(machine.phase.abort.signal.aborted, true);
  assert.equal(count, 2);
});

test('Harness protocol is opt-in, excludes Team, and rejects missing task generation', async () => {
  let ran = 0;
  await withManagedTurn(turnMachine(), () => ran++);
  assert.equal(requests.length, 0);
  process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL = '1';
  delete process.env.CLAWMANAGER_GATEWAY_GENERATION;
  await assert.rejects(withManagedTurn(turnMachine(), () => ran++), { code: 'MODEL_UNAVAILABLE' });
  process.env.CLAWMANAGER_TEAM_ENABLED = 'true';
  await withManagedTurn(turnMachine(), () => ran++);
  assert.equal(ran, 2);
  assert.equal(requests.length, 0);
});

test('Harness concurrent sessions never share task headers', async () => {
  process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL = '1';
  process.env.CLAWMANAGER_GATEWAY_GENERATION = '3';
  const actual = await Promise.all(['session-A', 'session-B'].map(session =>
    withManagedTurn(turnMachine(session), async () => {
      await Promise.resolve();
      return managedRequestHeaders(base, key)['x-clawmanager-session-id'];
    })));
  assert.deepEqual(actual, ['session-A', 'session-B']);
});
const resolve = (reg, selection, signal) => resolveManagedSelection(reg, selection, signal, LlmError);
test('historical full pair recovers same deployment without mutating frozen selection', async () => {
  const selection = Object.freeze({ provider: 'old-provider', model: 'old-model', reasoningEffort: 'low' });
  assert.deepEqual(await resolve(registry(), selection), { provider: 'new-provider', model: 'new-model', reasoningEffort: 'low' });
  assert.equal(selection.model, 'old-model');
  assert.equal(requests[0].url, base + '/runtime/catalog');
  assert.equal(requests[0].options.headers.Authorization, `Bearer ${key}`);
  assert.equal(requests[0].options.redirect, 'error');
});
test('canonical stable ID, old alias and slash-containing model name are accepted', async () => {
  for (const name of ['cm-deployment-1', 'old-model', 'namespace/old-model']) {
    assert.equal((await resolve(registry(), { provider: 'clawmanager', model: name })).model, 'new-model');
  }
});
test('unknown provider cannot borrow an available model by leaf name', async () => {
  await assert.rejects(resolve(registry(), { provider: 'wrong-provider', model: 'new-model' }), { code: 'MODEL_UNAVAILABLE' });
});
test('revocation is observed on the next lookup, not retained by a grant cache', async () => {
  const reg = registry();
  const selected = { provider: 'new-provider', model: 'new-model' };
  assert.equal(await resolve(reg, selected), selected);
  items = [];
  await assert.rejects(resolve(reg, selected), { code: 'MODEL_UNAVAILABLE' });
  assert.equal(requests.length, 2);
});
test('ambiguous historical identity and ambiguous bare wire identity both reject', async () => {
  items.push({ id: 'second', stable_id: 'cm-deployment-2', provider_refs: model.provider_refs });
  await assert.rejects(resolve(registry(), { provider: 'old-provider', model: 'old-model' }), { code: 'MODEL_UNAVAILABLE' });
  items = [model, { id: 'second', stable_id: 'cm-deployment-2', aliases: ['new-model'] }];
  await assert.rejects(resolve(registry(), { provider: 'old-provider', model: 'old-model' }), { code: 'MODEL_UNAVAILABLE' });
});
test('custom route takes precedence even when its provider has a historical managed name', async () => {
  for (const profile of [undefined, { baseURL: 'https://custom.test', apiKey: key, models: [] }, { baseURL: base, apiKey: 'other-key', models: [] }]) {
    const reg = registry({ 'old-provider': ['old-model'] });
    reg.adapters.get('old-provider').adapter.clawmanagerModelProfile = async () => profile;
    const selected = { provider: 'old-provider', model: 'old-model' };
    assert.equal(await resolve(reg, selected), selected);
  }
  assert.equal(requests.length, 0);
});
test('cannot redirect old deployment into a foreign target provider', async () => {
  const reg = registry();
  reg.adapters.get('new-provider').adapter.clawmanagerModelProfile = async () => undefined;
  await assert.rejects(resolve(reg, { provider: 'old-provider', model: 'old-model' }), { code: 'MODEL_UNAVAILABLE' });
});
test('protocol disabled leaves old image behavior and custom routes alone', async () => {
  delete process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL;
  const selected = { provider: 'old-provider', model: 'old-model' };
  assert.equal(await resolve(registry(), selected), selected);
  assert.equal(requests.length, 0);
});
test('visible catalog hides revoked models and lookup-only aliases', async () => {
  const reg = registry({ 'new-provider': ['new-model', 'old-model', 'deleted', 'auto'] });
  const list = ['new-model', 'old-model', 'deleted', 'auto'].map(id => ({ id, provider: 'new-provider' }));
  assert.deepEqual((await filterManagedModels(reg, 'new-provider', list, LlmError)).map(v => v.id), ['new-model', 'auto']);
  assert.equal(list.length, 4);
});

test('recreated names cannot restore an old selection but the new stable identity is visible', async () => {
  items = [{id:'same-name',stable_id:'cm-deploy-v1-new',legacy_name_unavailable:true}];
  const reg = registry({clawmanager:['same-name','cm-deploy-v1-new']});
  await assert.rejects(resolve(reg,{provider:'clawmanager',model:'same-name'}), /当前模型不可用/);
  assert.equal((await resolve(reg,{provider:'clawmanager',model:'cm-deploy-v1-new'})).model,'cm-deploy-v1-new');
  const list = await filterManagedModels(reg,'clawmanager',[{id:'same-name'},{id:'cm-deploy-v1-new'}],LlmError);
  assert.deepEqual(list.map(item=>item.id),['cm-deploy-v1-new']);
});
test('directory failures, malformed metadata and oversized responses fail closed without secrets', async () => {
  for (const response of [new Response('private-key', { status: 403 }), Response.json({ models: [{ id: 'missing-stable-id' }] }),
    Response.json({ models: [{ ...model, provider_refs: [{}] }] }), new Response('x'.repeat(1024 * 1024 + 1))]) {
    globalThis.fetch = async () => response;
    await assert.rejects(readCatalog(undefined, LlmError), error => error.code === 'MODEL_CATALOG_UNAVAILABLE' && !error.message.includes('private-key'));
  }
  globalThis.fetch = async () => { throw new Error('secret-url-and-token'); };
  await assert.rejects(readCatalog(undefined, LlmError), error => !error.message.includes('secret-url'));
});
test('caller cancellation reaches fetch and remains cancellation, not a fallback', async () => {
  const controller = new AbortController();
  globalThis.fetch = async (_url, { signal }) => { controller.abort(); signal.throwIfAborted(); };
  await assert.rejects(readCatalog(controller.signal, LlmError), { code: 'ABORTED' });
});
test('unreviewed native code is rejected', () => {
  for (const name of Object.keys(contracts)) assert.throws(() => transform(name, 'unknown code'), /Unreviewed/);
});

const nativeRoot = process.env.DSH_NATIVE_TEST_ROOT;
const nativeFixture = process.env.DSH_NATIVE_FIXTURE_DIR;
function sourceOf(name) {
  return readFileSync(nativeRoot ? createRequire(join(nativeRoot, 'package.json')).resolve('@deepseek-ai/' + name) : join(nativeFixture, name + '.js'), 'utf8');
}
function method(source, signature, indent = '\t\t') {
  const start = source.indexOf(indent + signature);
  assert.notEqual(start, -1, signature);
  const end = source.indexOf('\n' + indent + '}\n', start);
  assert.notEqual(end, -1, signature);
  return source.slice(start, end + indent.length + 2);
}
test('real pinned native modules: patch, syntax, idempotency and installation', { skip: !nativeRoot && !nativeFixture }, () => {
  const root = mkdtempSync(join(tmpdir(), 'cm-dsh-model-'));
  try {
    writeFileSync(join(root, 'package.json'), JSON.stringify({ name: '@deepseek-ai/dsh', version: '0.2.1-rc.3', type: 'module' }));
    for (const name of Object.keys(contracts)) {
      const source = sourceOf(name);
      const result = transform(name, source);
      assert.equal(transform(name, result), result);
      const directory = join(root, 'node_modules/@deepseek-ai', name, 'lib');
      mkdirSync(directory, { recursive: true });
      writeFileSync(join(directory, '../package.json'), JSON.stringify({ name: '@deepseek-ai/' + name, type: 'module', main: 'lib/index.js' }));
      writeFileSync(join(directory, 'index.js'), source);
      execFileSync(process.execPath, ['--input-type=module', '--check'], { input: result });
    }
    assert.throws(() => install(root, true), /absent/);
    install(root);
    install(root, true);
    install(root);
    install(root, true);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
test('native prepare/query/stream entry points resolve before adapter dispatch', { skip: !nativeRoot && !nativeFixture }, async () => {
  const source = transform('dsh-llm', sourceOf('dsh-llm'));
  const methods = ['async resolveManagedModelReference(selection, signal)', 'async resolveModelInfo(provider, model, signal)',
    'async resolveCallConfig(config, signal)', 'async prepareCall(config, signal, configure)', 'async *adapterStream(options, prepared)']
    .map(signature => method(source, signature + ' {')).join('\n');
  const Native = new Function('resolveManagedSelection', 'LlmError', 'deepFreeze', 'adapterFailureChunk',
    `return class { ${methods} }`)(resolveManagedSelection, LlmError, v => v, error => ({ type: 'finish', reason: { kind: 'error', failure: { code: error.code } } }));
  const instance = new Native();
  Object.assign(instance, registry());
  const calls = [];
  instance.registration = provider => { calls.push(provider); return instance.adapters.get(provider); };
  instance.resolveModelInfoFor = async (registration, model) => ({ provider: registration.provider.id, id: model });
  instance.resolveCallFor = async (_registration, config) => ({ config });
  instance.resolveCallWithInfo = config => ({ config });
  instance.normalizeModelInfo = (_registration, _model, info) => info;
  instance.adapters.get('new-provider').adapter.prepareCall = async (provider, model) => ({ model: { provider, id: model } });
  assert.deepEqual(await instance.resolveModelInfo('old-provider', 'old-model'), { provider: 'new-provider', id: 'new-model' });
  assert.equal((await instance.resolveCallConfig({ provider: 'old-provider', model: 'old-model' })).model, 'new-model');
  const prepared = await instance.prepareCall(Object.freeze({ provider: 'old-provider', model: 'old-model' }));
  assert.deepEqual(prepared.config, { provider: 'new-provider', model: 'new-model' });
  assert.ok(calls.every(provider => provider === 'new-provider'));
  calls.length = 0;
  items = [];
  await assert.rejects(instance.prepareCall({ provider: 'old-provider', model: 'old-model' }), { code: 'MODEL_UNAVAILABLE' });
  const chunks = [];
  for await (const chunk of instance.adapterStream({ provider: 'old-provider', model: 'old-model' })) chunks.push(chunk);
  assert.equal(chunks[0].reason.failure.code, 'MODEL_UNAVAILABLE');
  assert.equal(calls.length, 0);
  // Prepared calls must not retain the authorization from prepare time.
  let dispatched = false;
  const retained = { registration: instance.adapters.get('new-provider'),
    config: prepared.config, dispatch: () => { dispatched = true; } };
  const revokedPrepared = [];
  for await (const chunk of instance.adapterStream({ ...prepared.config, messages: [] }, retained)) revokedPrepared.push(chunk);
  assert.equal(revokedPrepared[0].reason.failure.code, 'MODEL_UNAVAILABLE');
  assert.equal(dispatched, false);
  items = [model];
  instance.adapters.set('new-provider', { ...retained.registration });
  const replacedRegistration = [];
  for await (const chunk of instance.adapterStream({ ...prepared.config, messages: [] }, retained)) replacedRegistration.push(chunk);
  assert.equal(replacedRegistration[0].reason.failure.code, 'INVALID_PREPARED_CALL');
  assert.equal(dispatched, false);
});
test('native tool prepare cancellation never dispatches; Team and legacy retain native behavior', { skip: !nativeRoot && !nativeFixture }, async () => {
  const source = transform('dsh-agent-loop', sourceOf('dsh-agent-loop'));
  const scheduler = Symbol('scheduler');
  const run = new Function('TOOL_RUNTIME_SCHEDULER', 'appendToolCall', 'appendToolResult', 'appendSkippedToolCall',
    method(source, 'async function runGroup(ctx, turn, step, group, mode, signal, acceptContext) {', '') + '; return runGroup;'
  )(scheduler, () => 1, () => {}, () => {});
  for (const kind of ['managed', 'legacy', 'team']) {
    process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL = kind === 'legacy' ? '' : '1';
    process.env.CLAWMANAGER_TEAM_ENABLED = kind === 'team' ? 'true' : '';
    const controller = new AbortController();
    let dispatches = 0;
    const marker = new Error('native-dispatch');
    const tools = {
      prepare: async exec => { controller.abort(); return { kind: 'dispatch', exec }; },
      dispatch: async () => { dispatches++; throw marker; },
    };
    const ctx = { agents: { requireInitiator: () => ({ session: {} }) },
      agentLoop: { config: { maxParallelToolCalls: { get: () => 1 } } },
      tools: { [scheduler]: tools } };
    const operation = run(ctx, 1, 1, [{ block: { id: 'tool-1' }, exec: {} }], 'exclusive', controller.signal, () => {});
    if (kind === 'managed') {
      await assert.rejects(operation, { name: 'AbortError' });
      assert.equal(dispatches, 0);
    } else {
      await assert.rejects(operation, error => error === marker);
      assert.equal(dispatches, 1);
    }
  }
});

test('native tool cancellation waits for already dispatched tools before rejecting', { skip: !nativeRoot && !nativeFixture }, async () => {
  const source = transform('dsh-agent-loop', sourceOf('dsh-agent-loop'));
  const scheduler = Symbol('scheduler');
  const run = new Function('TOOL_RUNTIME_SCHEDULER', 'appendToolCall', 'appendToolResult', 'appendSkippedToolCall',
    method(source, 'async function runGroup(ctx, turn, step, group, mode, signal, acceptContext) {', '') + '; return runGroup;'
  )(scheduler, () => 1, () => {}, () => {});
  process.env.CLAWMANAGER_TEAM_ENABLED = '';
  const controller = new AbortController();
  let release, prepared = 0, dispatches = 0, settled = false;
  let signalSecond;
  const secondPrepared = new Promise(resolve => { signalSecond = resolve; });
  const running = new Promise(resolve => { release = resolve; });
  const tools = {
    prepare: async exec => {
      if (++prepared === 2) { controller.abort(); signalSecond(); }
      return { kind: 'dispatch', exec };
    },
    dispatch: async () => { dispatches++; await running; return { kind: 'final-result', result: {} }; },
  };
  const ctx = { agents: { requireInitiator: () => ({ session: {} }) },
    agentLoop: { config: { maxParallelToolCalls: { get: () => 2 } } },
    tools: { [scheduler]: tools, executionMode: () => ({ kind: 'parallel' }) } };
  const operation = run(ctx, 1, 1, [1, 2].map(id => ({ block: { id }, exec: {} })), 'parallel', controller.signal, () => {});
  const observed = operation.then(() => { settled = true; }, error => { settled = true; throw error; });
  const rejected = assert.rejects(observed, { name: 'AbortError' });
  await secondPrepared;
  await Promise.resolve();
  assert.equal(settled, false, 'scheduler returned while the first tool was still running');
  assert.equal(dispatches, 1, 'second tool dispatched after cancellation');
  release();
  await rejected;
  assert.equal(settled, true);
});

test('pinned native turn entry binds and closes the actual durable turn identity', { skip: !nativeRoot && !nativeFixture }, async () => {
  process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL = '1';
  process.env.CLAWMANAGER_GATEWAY_GENERATION = '3';
  const source = transform('dsh-agent-loop', sourceOf('dsh-agent-loop'));
  const methods = ['async turn()', 'async clawmanagerTurn()'].map(signature => method(source, signature + ' {', '\t')).join('\n');
  const Native = new Function('withManagedTurn', 'return class { ' + methods + ' }')(withManagedTurn);
  const machine = Object.assign(new Native(), turnMachine());
  const events = [];
  machine.session.append = (name, data) => events.push({ name, data });
  machine.preStep = async () => {
    assert.equal(managedRequestHeaders(base, key)['x-clawmanager-task-id'], 'turn-5');
    return { kind: 'reject' };
  };
  assert.equal(await machine.turn(), false);
  assert.equal(machine.phase.turn, 5);
  assert.deepEqual(events.map(e => e.name), ['turn/start', 'turn/end']);
  assert.equal(JSON.parse(requests.at(-1).options.body).task_id, 'turn-5');
  assert.equal(JSON.parse(requests.at(-1).options.body).cancelled, false);
});

test('native GUI selection lookup uses canonical route, errors are explicit', { skip: !nativeRoot && !nativeFixture }, async () => {
  const source = transform('dsh-api-session-controller', sourceOf('dsh-api-session-controller'));
  const functionSource = method(source, 'async function modelAvailable(ctx, selection) {', '');
  class RemoteError extends Error { constructor(code, message) { super(message); this.code = code; } }
  const available = new Function('RemoteError', `${functionSource}; return modelAvailable;`)(RemoteError);
  const reg = registry();
  const ctx = { llm: { resolveManagedModelReference: selected => resolve(reg, selected),
    listProviders: () => [{ id: 'new-provider' }], listModels: async provider => { assert.equal(provider, 'new-provider'); return [{ id: 'new-model' }]; } } };
  assert.equal(await available(ctx, { provider: 'old-provider', model: 'old-model' }), true);
  items = [];
  await assert.rejects(available(ctx, { provider: 'old-provider', model: 'old-model' }), { code: 'session/model-unavailable' });
});
