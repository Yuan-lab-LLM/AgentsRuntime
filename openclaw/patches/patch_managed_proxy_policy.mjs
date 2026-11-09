import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
if (process.argv.includes('--allow-legacy') && /^2026\.7\./.test(pkg.version)) {
  console.log(`Legacy OpenClaw ${pkg.version}: unchanged proxy contract`);
  process.exit(0);
}
if (pkg.version === '2026.10.2') {
  await import('./patch_managed_proxy_policy_102.mjs');
  process.exit(0);
}
if (pkg.version !== '2026.8.1') throw new Error(`unsupported OpenClaw ${pkg.version}`);
const marker = 'CLAWMANAGER_MANAGED_PROXY_POLICY_V1';
const original = 'function getRuntimeConfig(options) {\n\treturn loadConfig(options);\n}';
const replacement = `function getRuntimeConfig(options) {\n\t// ${marker}\n\tconst config = loadConfig(options);\n\treturn options?.pin === false ? config : clawmanagerManagedProxyPolicy(config);\n}`;
const targets = [];
const startupTargets = [];
const authTargets = [];
const authMarker = 'CLAWMANAGER_MANAGED_SHARED_AUTH_V1';
const authOriginal = 'getRequiredSharedGatewaySessionGeneration: () => getRequiredSharedGatewaySessionGeneration(sharedGatewaySessionGenerationState),';
const authReplacement = `getRequiredSharedGatewaySessionGeneration: () => {\n\t\t\t// ${authMarker}\n\t\t\tclawmanagerSyncManagedAuth({ state: sharedGatewaySessionGenerationState, resolveGeneration: () => resolveSharedGatewaySessionGeneration(getResolvedAuth(), getRuntimeConfig().gateway?.trustedProxies), capture: clawmanagerCaptureAuthOwnership, replaceOwned: clawmanagerReplaceOwnedAuth, disconnect: clawmanagerDisconnectStaleAuth, clients });\n\t\t\treturn getRequiredSharedGatewaySessionGeneration(sharedGatewaySessionGenerationState);\n\t\t},`;
const configGenerationOriginal = '}), config.gateway?.trustedProxies);';
const configGenerationReplacement = '}), clawmanagerAuthProxyPolicy(config).gateway?.trustedProxies);';
const startupOriginal = 'async function resolveGatewayRuntimeConfig(params) {';
const startupMarker = 'CLAWMANAGER_MANAGED_PROXY_STARTUP_V1';
const startupReplacement = `${startupOriginal}\n\t// ${startupMarker}\n\tparams = { ...params, cfg: clawmanagerManagedStartupPolicy(params.cfg) };`;
function visit(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const file = path.join(dir, entry.name);
    if (entry.isDirectory()) visit(file);
    else if (/\.(mjs|js)$/.test(file)) {
      const source = fs.readFileSync(file, 'utf8');
      if (source.includes(original) || source.includes(marker)) targets.push({ file, source });
      if (source.includes(startupOriginal) || source.includes(startupMarker)) startupTargets.push({ file, source });
      if (source.includes(authOriginal) || source.includes(authMarker)) authTargets.push({ file, source });
    }
  }
}
visit(path.join(root, 'dist'));
if (!targets.some(t => path.basename(t.file).startsWith('io.runtime-'))) throw new Error('runtime config contract changed');
if (!startupTargets.some(t => path.basename(t.file).startsWith('server-runtime-config-'))) throw new Error('startup config contract changed');
if (!authTargets.some(t => path.basename(t.file).startsWith('server-start-'))) throw new Error('shared auth generation contract changed');
for (const { file, source } of targets) {
  const helper = path.join(path.dirname(file), 'clawmanager-managed-proxy-policy.mjs');
  const expected = fs.readFileSync(fileURLToPath(new URL('./managed_proxy_policy.mjs', import.meta.url)));
  if (source.includes(marker)) {
    if (!source.includes(replacement) || !source.includes('import { withManagedProxyPolicy as clawmanagerManagedProxyPolicy }')) {
      throw new Error(`managed getter contract changed: ${file}`);
    }
    if (process.argv.includes('--patch')) fs.writeFileSync(helper, expected);
    if (!fs.readFileSync(helper).equals(expected)) throw new Error(`managed helper mismatch: ${file}`);
    continue;
  }
  if (source.split(original).length !== 2) throw new Error(`ambiguous getter: ${file}`);
  if (!process.argv.includes('--patch')) throw new Error(`unpatched getter: ${file}`);
  fs.writeFileSync(helper, expected);
  fs.writeFileSync(file, `import { withManagedProxyPolicy as clawmanagerManagedProxyPolicy } from "./clawmanager-managed-proxy-policy.mjs";\n` + source.replace(original, replacement));
}
for (const { file, source } of startupTargets) {
  const helper = path.join(path.dirname(file), 'clawmanager-managed-proxy-policy.mjs');
  const expected = fs.readFileSync(fileURLToPath(new URL('./managed_proxy_policy.mjs', import.meta.url)));
  if (source.includes(startupMarker)) {
    if (!source.includes(startupReplacement) || !source.includes('import { withManagedStartupPolicy as clawmanagerManagedStartupPolicy }')) throw new Error(`startup policy contract changed: ${file}`);
  } else {
    if (source.split(startupOriginal).length !== 2) throw new Error(`ambiguous startup resolver: ${file}`);
    if (!process.argv.includes('--patch')) throw new Error(`unpatched startup resolver: ${file}`);
    fs.writeFileSync(file, `import { withManagedStartupPolicy as clawmanagerManagedStartupPolicy } from "./clawmanager-managed-proxy-policy.mjs";\n` + source.replace(startupOriginal, startupReplacement));
  }
  if (process.argv.includes('--patch')) fs.writeFileSync(helper, expected);
  if (!fs.readFileSync(helper).equals(expected)) throw new Error(`startup helper mismatch: ${file}`);
}
for (const { file, source } of authTargets) {
  const helper = path.join(path.dirname(file), 'clawmanager-managed-proxy-policy.mjs');
  const expected = fs.readFileSync(fileURLToPath(new URL('./managed_proxy_policy.mjs', import.meta.url)));
  const nativeModule = source.match(/from "(\.\/server-shared-auth-generation-[^"]+\.js)";/)?.[1];
  if (!nativeModule) throw new Error(`native auth generation exports changed: ${file}`);
  const nativeSource = fs.readFileSync(path.join(path.dirname(file), nativeModule), 'utf8');
  for (const contract of ['captureSharedGatewaySessionGenerationOwnership as t', 'replaceOwnedSharedGatewaySessionGenerationState as l', 'disconnectStaleSharedGatewayAuthClients as i']) {
    if (!nativeSource.includes(contract)) throw new Error(`native auth ownership contract changed: ${file}`);
  }
  if (source.includes(authMarker)) {
    if (!source.includes(authReplacement) || !source.includes(configGenerationReplacement)) throw new Error(`managed auth contract changed: ${file}`);
  } else {
    if (source.split(authOriginal).length !== 2 || source.split(configGenerationOriginal).length !== 2) throw new Error(`ambiguous auth generation contract: ${file}`);
    if (!process.argv.includes('--patch')) throw new Error(`unpatched shared auth generation: ${file}`);
    const imports = `import { synchronizeManagedSharedAuth as clawmanagerSyncManagedAuth, withManagedProxyPolicy as clawmanagerAuthProxyPolicy } from "./clawmanager-managed-proxy-policy.mjs";\nimport { t as clawmanagerCaptureAuthOwnership, l as clawmanagerReplaceOwnedAuth, i as clawmanagerDisconnectStaleAuth } from "${nativeModule}";\n`;
    fs.writeFileSync(file, imports + source.replace(authOriginal, authReplacement).replace(configGenerationOriginal, configGenerationReplacement));
  }
  if (process.argv.includes('--patch')) fs.writeFileSync(helper, expected);
  if (!fs.readFileSync(helper).equals(expected)) throw new Error(`managed helper mismatch: ${file}`);
}
if (process.argv.includes('--install-marker')) {
  if (!process.argv.includes('--patch')) throw new Error('marker installation requires patch verification');
  fs.mkdirSync('/usr/local/share/clawmanager', { recursive: true });
  fs.writeFileSync('/usr/local/share/clawmanager/openclaw-proxy-policy-v1', '2026.8.1\n', { mode: 0o644 });
}
console.log(`Managed proxy overlay verified: ${targets.length} config getters`);
