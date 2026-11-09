import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
if (pkg.version !== '2026.10.2') throw new Error(`unsupported 10.2 overlay target ${pkg.version}`);
const dist = path.join(root, 'dist');
const patching = process.argv.includes('--patch');
const helper = fs.readFileSync(fileURLToPath(new URL('./managed_proxy_policy.mjs', import.meta.url)));
function find(prefix, needle) {
  const files = fs.readdirSync(dist).filter(n => n.startsWith(prefix) && n.endsWith('.mjs'))
    .map(n => path.join(dist, n)).filter(f => fs.readFileSync(f, 'utf8').includes(needle));
  if (files.length !== 1) throw new Error(`ambiguous 10.2 contract ${prefix}: ${files.length}`);
  return files[0];
}
function replace(file, before, after, imports = '') {
  let source = fs.readFileSync(file, 'utf8');
  if (!source.includes(after)) {
    if (source.split(before).length !== 2) throw new Error(`10.2 contract changed: ${path.basename(file)}`);
    if (!patching) throw new Error(`unpatched 10.2 contract: ${path.basename(file)}`);
    source = imports + source.replace(before, after);
    fs.writeFileSync(file, source);
  }
  if (imports && !source.includes(imports)) throw new Error(`10.2 overlay imports changed: ${file}`);
}
const getter = find('io.runtime-', 'function getRuntimeConfig(options)');
replace(getter,
  'function getRuntimeConfig(options) {\n\treturn loadConfig(options);\n}',
  'function getRuntimeConfig(options) {\n\t// CLAWMANAGER_MANAGED_PROXY_POLICY_V1\n\tconst config = loadConfig(options);\n\treturn options?.pin === false ? config : clawmanagerManagedProxyPolicy(config);\n}',
  'import { withManagedProxyPolicy as clawmanagerManagedProxyPolicy } from "./clawmanager-managed-proxy-policy.mjs";\n');
const startup = find('server-runtime-config-', 'function resolveGatewayRuntimeConfig(params)');
replace(startup,
  'function resolveGatewayRuntimeConfig(params) {',
  'function resolveGatewayRuntimeConfig(params) {\n\t// CLAWMANAGER_MANAGED_PROXY_STARTUP_V1\n\tparams = { ...params, cfg: clawmanagerManagedStartupPolicy(params.cfg) };',
  'import { withManagedStartupPolicy as clawmanagerManagedStartupPolicy } from "./clawmanager-managed-proxy-policy.mjs";\n');

// Keep the exact native reader and its private ownership capability. Wrapping
// state.reader would break SharedGatewaySessionGenerationState.fromReader.
const start = find('server-start-', 'const sharedGatewaySessionGenerationState = new SharedGatewaySessionGenerationState');
const source = fs.readFileSync(start, 'utf8');
if (!source.includes('const connectionState = await startupTrace.measure("runtime.state", () => createGatewayConnectionState({')) throw new Error('10.2 connection registry owner changed');
const nativeModule = source.match(/from "(\.\/server-shared-auth-generation-[^"]+\.mjs)";/)?.[1];
if (!nativeModule) throw new Error('10.2 shared auth owner import changed');
const native = path.join(dist, nativeModule);
const nativeSource = fs.readFileSync(native, 'utf8');
if (!nativeSource.includes('disconnectStaleSharedGatewayAuthClients as n') || !nativeSource.includes('static fromReader(read)')) throw new Error('10.2 native auth owner exports changed');
replace(native,
  'this.#reader = () => this.#required === null ? this.#current : this.#required;',
  'this.#reader = () => {\n\t\t\t// CLAWMANAGER_MANAGED_SHARED_AUTH_READER_V1\n\t\t\tinitial.beforeRead?.();\n\t\t\treturn this.#required === null ? this.#current : this.#required;\n\t\t};');
replace(start,
  'const sharedGatewaySessionGenerationState = new SharedGatewaySessionGenerationState({\n\t\tcurrent: resolveCurrentSharedGatewaySessionGeneration(),\n\t\trequired: null\n\t});',
  'const sharedGatewaySessionGenerationState = new SharedGatewaySessionGenerationState({\n\t\tcurrent: resolveCurrentSharedGatewaySessionGeneration(),\n\t\trequired: null,\n\t\t// CLAWMANAGER_MANAGED_SHARED_AUTH_V1\n\t\tbeforeRead: () => clawmanagerSyncManagedAuth({ state: sharedGatewaySessionGenerationState, resolveGeneration: resolveCurrentSharedGatewaySessionGeneration, capture: s => s.capture(), replaceOwned: (s, owner, next) => s.replace(owner, next), disconnect: clawmanagerDisconnectStaleAuth, clients: connectionState.clients })\n\t});',
  `import { synchronizeManagedSharedAuth as clawmanagerSyncManagedAuth, withManagedProxyPolicy as clawmanagerAuthProxyPolicy } from "./clawmanager-managed-proxy-policy.mjs";\nimport { n as clawmanagerDisconnectStaleAuth } from "${nativeModule}";\n`);
replace(start, '}), config.gateway?.trustedProxies);', '}), clawmanagerAuthProxyPolicy(config).gateway?.trustedProxies);');
// Native 10.2 prefers its committed secrets snapshot over getRuntimeConfig.
// Overlay the read-only auth input at that boundary as well; never mutate the
// secrets manager's owned snapshot or persist an ephemeral proxy exception.
replace(start,
  'authConfig: getActiveSecretsRuntimeConfigSnapshot()?.config.gateway?.auth ?? getRuntimeConfig().gateway?.auth,',
  'authConfig: clawmanagerAuthProxyPolicy({ gateway: { auth: getActiveSecretsRuntimeConfigSnapshot()?.config.gateway?.auth ?? getRuntimeConfig().gateway?.auth } }).gateway?.auth,');
replace(start,
  'authConfig: config.gateway?.auth,',
  'authConfig: clawmanagerAuthProxyPolicy(config).gateway?.auth,');

// 10.2 also fences requests against the config reloader's committed snapshot,
// deliberately separate from getRuntimeConfig(). Apply the ephemeral policy at
// the auth capture boundary, not by replacing the reloader's writer-owned data.
const authPolicy = find('auth-policy-', 'function captureGatewayAuthPolicy(config, principal)');
replace(authPolicy,
  'function captureGatewayAuthPolicy(config, principal) {',
  'function captureGatewayAuthPolicy(config, principal) {\n\t// CLAWMANAGER_MANAGED_AUTH_POLICY_V1\n\tconfig = clawmanagerAuthProxyPolicy(config);',
  'import { withManagedProxyPolicy as clawmanagerAuthProxyPolicy } from "./clawmanager-managed-proxy-policy.mjs";\n');
// The native Browser refresh prefers the writer-owned source snapshot. In an
// isolated runtime that snapshot may lag the managed Browser configuration.
// Both service startup and route refresh already share this reader; retain the
// native lifecycle/fences and leave legacy source preference unchanged.
const browserContext = find('server-context-', 'function loadBrowserConfigForRuntimeRefresh()');
const browserNativeReader = 'function loadBrowserConfigForRuntimeRefresh() {\n\treturn getRuntimeConfigSourceSnapshot() ?? getRuntimeConfig();\n}';
const browserSnapshotReader = 'function loadBrowserConfigForRuntimeRefresh() {\n\t// CLAWMANAGER_MANAGED_BROWSER_SNAPSHOT_V1\n\treturn process.env.CLAWMANAGER_NETWORK_ISOLATED === "1" ? getRuntimeConfig() : getRuntimeConfigSourceSnapshot() ?? getRuntimeConfig();\n}';
// getRuntimeConfig() itself pins the active secrets runtime snapshot. The
// native pin:false reader is needed to observe manager-published disk changes.
// This browser-only reader never publishes or changes the auth-owned snapshot.
const browserFreshReader = 'function loadBrowserConfigForRuntimeRefresh() {\n\t// CLAWMANAGER_MANAGED_BROWSER_UNPINNED_V2\n\treturn process.env.CLAWMANAGER_NETWORK_ISOLATED === "1" ? getRuntimeConfig({ pin: false }) : getRuntimeConfigSourceSnapshot() ?? getRuntimeConfig();\n}';
replace(browserContext, fs.readFileSync(browserContext, 'utf8').includes(browserSnapshotReader) ? browserSnapshotReader : browserNativeReader, browserFreshReader);
// The native SSRF guard rejects link-local broker addresses even when their
// hostname is trusted. Derive a grant only for this exact CDP control endpoint;
// the shared Browser navigation policy must retain its original semantics.
const cdpPolicyModule = fs.readFileSync(browserContext, 'utf8').match(/from "(\.\/cdp-reachability-policy-[^"]+)";/)?.[1];
if (!cdpPolicyModule) throw new Error('10.2 Browser CDP policy owner changed');
replace(path.join(dist, cdpPolicyModule),
  'function resolveCdpReachabilityPolicy(profile, ssrfPolicy) {',
  'function resolveCdpReachabilityPolicy(profile, ssrfPolicy) {\n\t// CLAWMANAGER_MANAGED_CDP_BROKER_V1\n\tconst brokerHost = "clawmanager-local-services.invalid";\n\tif (process.env.CLAWMANAGER_NETWORK_ISOLATED === "1" && profile.cdpUrl === "http://clawmanager-local-services.invalid:31282" && profile.cdpHost === brokerHost && profile.cdpPort === 31282 && profile.driver === "openclaw" && profile.attachOnly === true && ssrfPolicy?.allowedHostnames?.includes(brokerHost)) {\n\t\tssrfPolicy = { ...ssrfPolicy, allowedHostnames: [brokerHost], hostnameAllowlist: [brokerHost], dangerouslyAllowPrivateNetwork: true };\n\t}');
const helperPath = path.join(dist, 'clawmanager-managed-proxy-policy.mjs');
if (patching) fs.writeFileSync(helperPath, helper);
if (!fs.readFileSync(helperPath).equals(helper)) throw new Error('10.2 managed policy helper differs');
if (process.argv.includes('--install-marker')) {
  if (!patching) throw new Error('marker requires verified patch');
  fs.mkdirSync('/usr/local/share/clawmanager', { recursive: true });
  fs.writeFileSync('/usr/local/share/clawmanager/openclaw-proxy-policy-v1', pkg.version + '\n', { mode: 0o644 });
}
console.log('OpenClaw 10.2 managed policy and native auth owner overlay verified');
