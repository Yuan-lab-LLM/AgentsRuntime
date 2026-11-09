// Pinned10.2 adaptation: source captures are per-process scratch, not user data.
import fs from 'node:fs';
import path from 'node:path';
const root=process.env.OPENCLAW_PACKAGE_ROOT||'/usr/local/lib/node_modules/openclaw';
if(JSON.parse(fs.readFileSync(root+'/package.json')).version!=='2026.10.2')throw Error('unsupported plugin scratch contract');
const file=root+'/dist/plugin-source-capture-path-Dqh0vrxU.mjs';
const marker='CLAWMANAGER_UPGRADE_PLUGIN_SCRATCH_V1';
let code=fs.readFileSync(file,'utf8');
const original='function resolvePluginSourceCapturesDirectory(stateDir) {\n\treturn path.join(stateDir, "tmp", "plugin-captures");\n}';
const replacement=`function resolvePluginSourceCapturesDirectory(stateDir) {
 // ${marker}: only an owned, local, no-follow temporary root is admitted.
 const scratch = process.env.CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT;
 if (scratch) {
  if (!new RegExp("^/tmp/clawmanager-upgrade-plugin-[A-Za-z0-9-]+$").test(scratch)) throw Error("Invalid upgrade plugin scratch root");
  const stat = clawmanagerScratchFs.lstatSync(scratch);
  if (!stat.isDirectory() || stat.isSymbolicLink() || clawmanagerScratchFs.realpathSync(scratch) !== scratch ||
      stat.uid !== process.getuid() || (stat.mode & 511) !== 448 || clawmanagerScratchFs.statfsSync(scratch).type === 0x6969)
    throw Error("Upgrade plugin scratch must be owned, private and local");
  return scratch;
 }
 return path.join(stateDir, "tmp", "plugin-captures");
}`;
if(!code.includes(marker)) {
 if(!process.argv.includes('--patch'))throw Error('upgrade plugin scratch overlay is missing');
 if(code.split(original).length!==2)throw Error('plugin scratch source drift');
 code='import clawmanagerScratchFs from "node:fs";\n'+code.replace(original,replacement);
 fs.writeFileSync(file,code);
}
if(!code.includes(replacement))throw Error('plugin scratch overlay drift');
console.log('Verified owned local scratch for10.2 upgrade plugin captures; durable state path unchanged');
