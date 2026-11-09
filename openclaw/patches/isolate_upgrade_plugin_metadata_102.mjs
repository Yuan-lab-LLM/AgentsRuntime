// Run only in an already cloned preflight HOME, never against the live instance.
import fs from 'node:fs';
import path from 'node:path';
const [sourceStateArg, probeStateArg] = process.argv.slice(2);
if (!sourceStateArg || !probeStateArg) throw Error('source and isolated state are required');
const sourceState = path.resolve(sourceStateArg), probeState = path.resolve(probeStateArg);
if (sourceState === probeState || fs.realpathSync(probeState) !== probeState ||
    path.resolve(process.env.OPENCLAW_STATE_DIR || '') !== probeState ||
    !path.basename(path.dirname(path.dirname(probeState))).startsWith('clawmanager-upgrade-'))
  throw Error('refusing to remap non-isolated plugin metadata');
const core = '/usr/local/lib/node_modules/openclaw';
if (JSON.parse(fs.readFileSync(path.join(core, 'package.json'))).version !== '2026.10.2') throw Error('unsupported probe metadata contract');
const { r: load } = await import(core + '/dist/installed-plugin-index-record-reader-DoGhN1Un.mjs');
const { a: write } = await import(core + '/dist/installed-plugin-index-records-CzJqH-fd.mjs');
const { i: withLease } = await import(core + '/dist/plugin-lifecycle-lease-5X2Of1QR.mjs');
const within = (root, value) => { const rel = path.relative(root, value); return rel === '' || (!rel.startsWith('..' + path.sep) && rel !== '..' && !path.isAbsolute(rel)); };
const records = await load({ env: process.env });
for (const record of Object.values(records)) {
  for (const key of ['installPath', 'sourcePath']) {
    const value = record[key];
    if (!value || !path.isAbsolute(value)) continue;
    if (within(sourceState, value)) record[key] = path.join(probeState, path.relative(sourceState, value));
    else if (!within(probeState, value) && !within('/defaults/.openclaw', value) && !within(core, value))
      throw Error('plugin metadata refers to a path outside the isolated or image-owned roots');
  }
}
const config = JSON.parse(fs.readFileSync(path.join(probeState, 'openclaw.json')));
await withLease({ env: process.env }, lease => write(records, { env: process.env, config, lease }));
console.log('Isolated plugin install records using the pinned core metadata API');
