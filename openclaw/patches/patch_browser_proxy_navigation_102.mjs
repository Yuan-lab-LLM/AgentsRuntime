import fs from 'node:fs';
import path from 'node:path';
const root = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
if (JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8')).version !== '2026.10.2') throw new Error('10.2 browser overlay requires the exact tested release');
const dist = path.join(root, 'dist');
const patching = process.argv.includes('--patch');
function find(prefix, needle) {
  const files = fs.readdirSync(dist).filter(n => n.startsWith(prefix) && n.endsWith('.mjs')).map(n => path.join(dist,n)).filter(f => fs.readFileSync(f,'utf8').includes(needle));
  if (files.length !== 1) throw new Error(`ambiguous browser contract ${prefix}`);
  return files[0];
}
function change(file, before, after) {
  let source = fs.readFileSync(file,'utf8');
  if (!source.includes(after)) {
    if (!patching || source.split(before).length !== 2) throw new Error(`browser contract changed: ${file}`);
    source = source.replace(before,after);
    fs.writeFileSync(file,source);
  }
}
const chrome = find('chrome-','async function assertBrowserNavigationAllowed(opts)');
change(chrome,'\tawait resolvePinnedHostnameWithPolicy(parsed.hostname, {',
  '\t// CLAWMANAGER_MANAGED_PREVIEW_PROXY_DNS\n\tif (opts.browserProxyMode === "explicit-browser-proxy" &&\n\t\tisPrivateNetworkAllowedByPolicy(opts.ssrfPolicy) &&\n\t\t/^p-[a-z0-9_-]{16}\\.clawmanager-team-preview\\.invalid$/.test(normalizeHostname(parsed.hostname))) return;\n\tawait resolvePinnedHostnameWithPolicy(parsed.hostname, {');

const routes = find('routes-','function browserNavigationPolicyForProfile(ctx, profileCtx)');
let source = fs.readFileSync(routes,'utf8');
const snapshot = /((?:snapshotRoleViaPlaywright|snapshotAiViaPlaywright|snapshotAriaViaPlaywright)\(\{[\s\S]{0,1200}?)ssrfPolicy: ctx\.state\(\)\.resolved\.ssrfPolicy/g;
if (!source.includes('CLAWMANAGER_NATIVE_BROWSER_PROXY_MODE_SNAPSHOTS')) {
  let count = 0;
  let changed = source.replace(snapshot, (_, prefix) => { count++; return prefix + '...browserNavigationPolicyForProfile(ctx, profileCtx)'; });
  changed = changed.replace(/(const roleSnapshotArgs = \{[\s\S]{0,1200}?)ssrfPolicy: ctx\.state\(\)\.resolved\.ssrfPolicy/g, (_, prefix) => { count++; return prefix + '...browserNavigationPolicyForProfile(ctx, profileCtx)'; });
  if (!patching || count !== 4) throw new Error(`10.2 snapshot route contract changed: ${count}`);
  source = '// CLAWMANAGER_NATIVE_BROWSER_PROXY_MODE_SNAPSHOTS\n' + changed;
  fs.writeFileSync(routes,source);
}
if (snapshot.test(source)) throw new Error('snapshot route still loses proxy mode');

const pw = find('pw-ai-','async function prepareSnapshotPageViaPlaywright(opts)');
change(pw,'\t\tssrfPolicy: opts.ssrfPolicy,\n\t\ttargetId: opts.targetId\n\t});\n\treturn page;',
  '\t\tssrfPolicy: opts.ssrfPolicy,\n\t\tbrowserProxyMode: opts.browserProxyMode,\n\t\ttargetId: opts.targetId\n\t});\n\treturn page;');
let pwSource = fs.readFileSync(pw,'utf8');
if (!pwSource.includes('CLAWMANAGER_NATIVE_BROWSER_PROXY_MODE_SNAPSHOT_GUARD')) {
  // 10.2 consolidates the old AI snapshot into role capture; role and ARIA
  // are the two native callers of this preparation boundary.
  const call = /(prepareSnapshotPageViaPlaywright\(\{[\s\S]{0,300}?ssrfPolicy: opts\.ssrfPolicy)(\n\t\}\);)/g;
  let count = 0;
  pwSource = pwSource.replace(call, (_, a,b) => { count++; return a + ',\n\t\tbrowserProxyMode: opts.browserProxyMode' + b; });
  if (!patching || count !== 2) throw new Error(`10.2 snapshot preparation contract changed: ${count}`);
  fs.writeFileSync(pw,'// CLAWMANAGER_NATIVE_BROWSER_PROXY_MODE_SNAPSHOT_GUARD\n' + pwSource);
}

const navigation = find('pw-tools-core.interactions.navigation-','async function gotoPageWithNavigationGuard(opts)');
const before = '\t\t} catch (err) {\n\t\t\tif (isPolicyDenyNavigationError(err)) {\n\t\t\t\tif (requestKind === "top-level") blockedError = err;\n\t\t\t\tawait route.abort().catch(() => {});\n\t\t\t\treturn;\n\t\t\t}\n\t\t\tthrow err;\n\t\t}';
const after = '\t\t} catch (err) {\n\t\t\t// CLAWMANAGER_CONTAIN_DOCUMENT_DNS_FAILURE\n\t\t\tif (requestKind === "top-level") blockedError = err;\n\t\t\tawait route.abort().catch(() => {});\n\t\t\treturn;\n\t\t}';
change(navigation,before,after);
console.log('OpenClaw 10.2 scoped Preview DNS, snapshot proxy mode and navigation failure overlay verified');
