// Source and target cores run this in separate disposable containers sharing
// only a task-owned volume. The official SDK writes/reads real SQLite history;
// no direct SQL edits, version-marker edits, real model calls or live data.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
const workspace = '/workspaces/openclaw/user-1/instance-'+(process.env.UPGRADE_INSTANCE_ID || '910');
const home = path.join(workspace, 'home'), state = path.join(home, '.openclaw');
process.env.HOME = home;
process.env.OPENCLAW_STATE_DIR = state;
process.env.OPENCLAW_CONFIG_PATH = path.join(state, 'openclaw.json');
const pkg = '/usr/local/lib/node_modules/openclaw';
const version = JSON.parse(fs.readFileSync(pkg + '/package.json')).version;
const mode = process.argv[2];
const identities = ['main', 'researcher'].map(agentId => ({
  agentId, sessionKey: `agent:${agentId}:redis-team:group:910`,
  sessionId: `${agentId}-preserved-history`,
  storePath: path.join(state, 'stores', agentId, 'sessions.json'),
}));
try {
  if (mode === 'seed') {
    assert.equal(version, '2026.8.1');
    fs.mkdirSync(state, { recursive: true });
    fs.writeFileSync(process.env.OPENCLAW_CONFIG_PATH, JSON.stringify({
      gateway: { mode: 'local', auth: { mode: 'token', token: 'local-fixture-only' } },
      agents: { ownership: 'explicit', defaults: { workspace: path.join(state, 'workspace') }, entries: Object.fromEntries(identities.map(({agentId}) => [agentId, {
        agentDir: path.join(state, 'custom-agent-dirs', agentId),
      }])) },
      session: { store: path.join(state, 'stores', '{agentId}', 'sessions.json') },
      update: { checkOnStart: false },
    }));
    fs.mkdirSync(path.join(state, 'workspace'), { recursive: true });
    fs.writeFileSync(path.join(state, 'workspace', 'report.md'), 'Prior report with verified sources A and B.');
    fs.writeFileSync(path.join(state, 'user-owned.txt'), 'Must survive migration and restore.');
  }
  const store = await import(pkg + '/dist/plugin-sdk/session-store-runtime.js');
  const transcript = await import(pkg + '/dist/plugin-sdk/session-transcript-runtime.js');
  for (const scope of identities) {
    if (mode === 'seed') {
      await store.upsertSessionEntry({ ...scope, entry: { sessionId: scope.sessionId, updatedAt: 1700000000000, chatType: 'group', channel: 'redis-team' } });
      const result = await transcript.appendAssistantMirrorMessageByIdentity({ ...scope, text: `Preserved work by ${scope.agentId}: verified source A and source B.`, idempotencyKey: `history-${scope.agentId}` });
      assert.equal(result.ok, true, JSON.stringify(result));
    }
    const entry = store.getSessionEntry(scope);
    assert.equal(entry.sessionId, scope.sessionId);
    const events = await transcript.readSessionTranscriptEvents(scope);
    assert.match(JSON.stringify(events), new RegExp(`Preserved work by ${scope.agentId}`));
    assert.equal(store.listSessionEntries({ agentId: scope.agentId, storePath: scope.storePath }).length, 1);
  }
  assert.equal(fs.readFileSync(path.join(state, 'workspace', 'report.md'), 'utf8'), 'Prior report with verified sources A and B.');
  assert.equal(fs.readFileSync(path.join(state, 'user-owned.txt'), 'utf8'), 'Must survive migration and restore.');
  const sqlite = [];
  const walk = dir => { for (const e of fs.readdirSync(dir, {withFileTypes:true})) {const f=path.join(dir,e.name); if(e.isDirectory())walk(f); else if(e.name.endsWith('.sqlite'))sqlite.push({path:path.relative(state,f),sha256:createHash('sha256').update(fs.readFileSync(f)).digest('hex')});} };
  walk(state);
  fs.writeFileSync(path.join(workspace, `${mode}-${version}.json`), JSON.stringify({version,sqlite}));
  console.log(`PASS: ${mode} real ${version} multi-Agent custom-store history and user artifacts`);
} catch (error) { console.error(error); process.exit(1); }
process.exit(0);
