import { execFileSync } from 'node:child_process';
import { mkdirSync, realpathSync, renameSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Operate on the composed tree, including user presets. Never rewrite the user's
// profile or copy upstream preset definitions into the runtime repository.
export function managedPresetPatches(entries) {
  if (!Array.isArray(entries)) throw new Error('DSH config dump must be an entry list');
  const patches = [];
  let presets = 0;
  function disableWebTools(value) {
    if (!value || typeof value !== 'object') return;
    if (value.name === '@deepseek-ai/dsh-tool-web') value.disabled = true;
    for (const child of Object.values(value)) disableWebTools(child);
  }
  function visit(rows) {
    for (const row of rows) {
      if (row.name === '@deepseek-ai/dsh-agent-preset') {
        presets++;
        if (!row.id || !Array.isArray(row.config?.plugins)) {
          throw new Error('DSH preset has no stable id or declarative plugin list');
        }
        disableWebTools(row.config.plugins);
        patches.push({ id: row.id, config: row.config });
      } else if (row.group && Array.isArray(row.config)) {
        visit(row.config);
      }
    }
  }
  visit(entries);
  if (!presets) throw new Error('DSH Web config contains no agent presets');
  return patches;
}

export function startupCatalogReceipt(env) {
  if (env.CLAWMANAGER_MODEL_CATALOG_SYNC !== '1' || !env.CLAWMANAGER_MODEL_CATALOG_JSON) return null;
  const snapshot = JSON.parse(env.CLAWMANAGER_MODEL_CATALOG_JSON);
  const canonical = JSON.parse(env.CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON || '{}');
  const instance = Number(env.CLAWMANAGER_INSTANCE_ID), owner = Number(env.CLAWMANAGER_USER_ID), generation = Number(env.CLAWMANAGER_GATEWAY_GENERATION);
  if (!Number.isSafeInteger(instance) || instance <= 0 || !Number.isSafeInteger(owner) || owner <= 0 || !Number.isSafeInteger(generation) || generation < 0 || snapshot.complete !== true || snapshot.catalog_protocol !== 2 || snapshot.generation !== generation || snapshot.owner_user_id !== owner || !/^[a-f0-9]{64}$/.test(snapshot.revision) || !Array.isArray(snapshot.models) || canonical.catalog_synced !== true || !Array.isArray(canonical.model_ids)) throw new Error('Managed startup catalog receipt mismatch');
  const expected = snapshot.models.map(model => model.id === 'auto' ? 'auto' : model.stable_id);
  if (expected.some(id => typeof id !== 'string' || (id !== 'auto' && !id.startsWith('cm-deploy-v1-'))) || new Set(expected).size !== expected.length || JSON.stringify([...expected].sort()) !== JSON.stringify([...canonical.model_ids].sort())) throw new Error('Managed startup catalog models mismatch');
  return { instance_id: instance, owner_user_id: owner, generation, revision: snapshot.revision, model_count: expected.filter(id => id !== 'auto').length };
}

function main() {
  const basePatch = process.argv[2];
  if (!basePatch || !process.env.DSH_HOME) throw new Error('managed patch and DSH_HOME are required');
  // Resolve the parser from the pinned DSH installation, not a mutable workspace.
  // The optional JS entrypoint also permits smoke tests with a local npm install.
  const executable = realpathSync(process.argv[3] || execFileSync('which', ['dsh'], { encoding: 'utf8' }).trim());
  const yaml = createRequire(executable)('js-yaml');
  class Expression { constructor(source) { this.source = source; } }
  const schema = yaml.DEFAULT_SCHEMA.extend([new yaml.Type('tag:yaml.org,2002:js', {
    kind: 'scalar', construct: source => new Expression(source),
    instanceOf: Expression, represent: expression => expression.source,
  })]);
  const dump = execFileSync(process.execPath, [executable, 'web', '--patch', basePatch, '--dump-config'], {
    encoding: 'utf8', maxBuffer: 16 * 1024 * 1024, stdio: ['ignore', 'pipe', 'inherit'],
  });
  const patches = managedPresetPatches(yaml.load(dump, { schema }));
  const target = join(process.env.DSH_HOME, '.clawmanager', 'presets.patch.yml');
  mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
  const temporary = `${target}.${process.pid}.tmp`;
  writeFileSync(temporary, yaml.dump(patches, { schema, noRefs: true, lineWidth: -1 }), { mode: 0o600 });
  renameSync(temporary, target);
  const receipt = startupCatalogReceipt(process.env);
  if (receipt) {
    const receiptTarget = join(dirname(target), 'catalog-sync.json');
    const receiptTemporary = `${receiptTarget}.${process.pid}.tmp`;
    writeFileSync(receiptTemporary, JSON.stringify(receipt), { mode: 0o600 });
    renameSync(receiptTemporary, receiptTarget);
  }
  process.stdout.write(target + '\n');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
