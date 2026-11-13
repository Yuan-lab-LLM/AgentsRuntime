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
  process.stdout.write(target + '\n');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
