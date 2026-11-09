import fs from "node:fs";
import path from "node:path";

const expectedVersion = "2026.8.1";
const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
if (packageJson.version === "2026.10.2") {
  await import("./patch_browser_proxy_navigation_102.mjs");
  process.exit(0);
}
if (packageJson.version !== expectedVersion) {
  throw new Error(`refusing to patch OpenClaw ${packageJson.version}; expected ${expectedVersion}`);
}

const distDir = path.join(packageRoot, "dist");
const patching = process.argv.includes("--patch");

function singleModule(pattern, needle, label) {
  const candidates = fs.readdirSync(distDir)
    .filter((name) => pattern.test(name))
    .map((name) => path.join(distDir, name))
    .filter((file) => fs.readFileSync(file, "utf8").includes(needle));
  if (candidates.length !== 1) throw new Error(`expected one OpenClaw ${label} module, found ${candidates.length}`);
  return candidates[0];
}

function replaceOnce(source, needle, replacement, label) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences !== 1) throw new Error(`expected one ${label}, found ${occurrences}`);
  return source.replace(needle, replacement);
}

function replaceEvery(source, needle, replacement, label, expectedCount) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences !== expectedCount) throw new Error(`expected ${expectedCount} ${label}, found ${occurrences}`);
  return source.split(needle).join(replacement);
}

function requireAll(source, fragments, label) {
  for (const fragment of fragments) {
    if (!source.includes(fragment)) throw new Error(`${label} patch is incomplete: ${fragment}`);
  }
}

const chromeMarker = "CLAWMANAGER_MANAGED_PREVIEW_PROXY_DNS";
const chromeTarget = singleModule(/^chrome-.*\.js$/, "async function assertBrowserNavigationAllowed(opts)", "browser navigation");
let chromeSource = fs.readFileSync(chromeTarget, "utf8");
const dnsCall = "\tawait resolvePinnedHostnameWithPolicy(parsed.hostname, {";
if (patching && !chromeSource.includes(chromeMarker)) {
  chromeSource = replaceOnce(chromeSource, dnsCall, [
    `\t// ${chromeMarker}: the operator-managed explicit proxy resolves only`,
    "\t// ClawManager's signature-derived Preview origin. All other hosts and",
    "\t// direct profiles keep the upstream DNS and redirect checks.",
    "\tif (opts.browserProxyMode === \"explicit-browser-proxy\" &&",
    "\t\tisPrivateNetworkAllowedByPolicy(opts.ssrfPolicy) &&",
    "\t\t/^p-[a-z0-9_-]{16}\\.clawmanager-team-preview\\.invalid$/.test(normalizeHostname(parsed.hostname))) return;",
    dnsCall,
  ].join("\n"), "browser navigation DNS call");
  fs.writeFileSync(chromeTarget, chromeSource);
}
chromeSource = fs.readFileSync(chromeTarget, "utf8");
requireAll(chromeSource, [chromeMarker, 'opts.browserProxyMode === "explicit-browser-proxy"', "isPrivateNetworkAllowedByPolicy(opts.ssrfPolicy)", "/^p-[a-z0-9_-]{16}\\.clawmanager-team-preview\\.invalid$/", dnsCall], "managed Preview DNS");

const routeMarker = "CLAWMANAGER_NATIVE_BROWSER_PROXY_MODE_SNAPSHOTS";
const routeTarget = singleModule(/^routes-.*\.js$/, "function browserNavigationPolicyForProfile(ctx, profileCtx)", "browser routes");
let routeSource = fs.readFileSync(routeTarget, "utf8");
const routeReplacements = [
  [[
    "\t\t\t\t\t\tconst snap = await pw.snapshotRoleViaPlaywright({",
    "\t\t\t\t\t\t\tcdpUrl,",
    "\t\t\t\t\t\t\ttargetId: tab.targetId,",
    "\t\t\t\t\t\t\tssrfPolicy: ctx.state().resolved.ssrfPolicy",
    "\t\t\t\t\t\t});",
  ].join("\n"), [
    `\t\t\t\t\t\t// ${routeMarker}`,
    "\t\t\t\t\t\tconst snap = await pw.snapshotRoleViaPlaywright({",
    "\t\t\t\t\t\t\tcdpUrl,",
    "\t\t\t\t\t\t\ttargetId: tab.targetId,",
    "\t\t\t\t\t\t\t...browserNavigationPolicyForProfile(ctx, profileCtx)",
    "\t\t\t\t\t\t});",
  ].join("\n"), "labeled screenshot snapshot"],
  [[
    "\t\t\t\t\t\t\trefsMode: plan.refsMode,",
    "\t\t\t\t\t\t\tssrfPolicy: ctx.state().resolved.ssrfPolicy,",
    "\t\t\t\t\t\t\turls: plan.urls,",
  ].join("\n"), [
    "\t\t\t\t\t\t\trefsMode: plan.refsMode,",
    "\t\t\t\t\t\t\t...browserNavigationPolicyForProfile(ctx, profileCtx),",
    "\t\t\t\t\t\t\turls: plan.urls,",
  ].join("\n"), "role snapshot policy"],
  [[
    "\t\t\t\t\t\t\ttargetId: tab.targetId,",
    "\t\t\t\t\t\t\tssrfPolicy: ctx.state().resolved.ssrfPolicy,",
    "\t\t\t\t\t\t\turls: plan.urls,",
  ].join("\n"), [
    "\t\t\t\t\t\t\ttargetId: tab.targetId,",
    "\t\t\t\t\t\t\t...browserNavigationPolicyForProfile(ctx, profileCtx),",
    "\t\t\t\t\t\t\turls: plan.urls,",
  ].join("\n"), "AI snapshot policy"],
  [[
    "\t\t\t\t\t\t\t\ttimeoutMs: plan.timeoutMs,",
    "\t\t\t\t\t\t\t\tssrfPolicy: ctx.state().resolved.ssrfPolicy",
  ].join("\n"), [
    "\t\t\t\t\t\t\t\ttimeoutMs: plan.timeoutMs,",
    "\t\t\t\t\t\t\t\t...browserNavigationPolicyForProfile(ctx, profileCtx)",
  ].join("\n"), "ARIA snapshot policy"],
];
if (patching && !routeSource.includes(routeMarker)) {
  for (const [needle, replacement, label] of routeReplacements) routeSource = replaceOnce(routeSource, needle, replacement, label);
  fs.writeFileSync(routeTarget, routeSource);
}
routeSource = fs.readFileSync(routeTarget, "utf8");
requireAll(routeSource, [routeMarker, "...browserNavigationPolicyForProfile(ctx, profileCtx)", "function browserNavigationPolicyForProfile(ctx, profileCtx)"], "native browser proxy route");

const playwrightMarker = "CLAWMANAGER_NATIVE_BROWSER_PROXY_MODE_SNAPSHOT_GUARD";
const playwrightTarget = singleModule(/^pw-ai-.*\.js$/, "async function prepareSnapshotPageViaPlaywright(opts)", "Playwright snapshots");
let playwrightSource = fs.readFileSync(playwrightTarget, "utf8");
if (patching && !playwrightSource.includes(playwrightMarker)) {
  const snapshotStart = playwrightSource.indexOf("async function prepareSnapshotPageViaPlaywright(opts)");
  const snapshotEnd = playwrightSource.indexOf("async function navigateViaPlaywright(opts)", snapshotStart);
  if (snapshotStart < 0 || snapshotEnd <= snapshotStart) throw new Error("could not isolate Playwright snapshot functions");
  const prefix = playwrightSource.slice(0, snapshotStart);
  let snapshotSource = playwrightSource.slice(snapshotStart, snapshotEnd);
  snapshotSource = replaceOnce(snapshotSource,
    "async function prepareSnapshotPageViaPlaywright(opts) {",
    `// ${playwrightMarker}\nasync function prepareSnapshotPageViaPlaywright(opts) {`,
    "snapshot marker");
  snapshotSource = replaceOnce(snapshotSource, [
    "\t\tssrfPolicy: opts.ssrfPolicy,",
    "\t\ttargetId: opts.targetId",
  ].join("\n"), [
    "\t\tssrfPolicy: opts.ssrfPolicy,",
    "\t\tbrowserProxyMode: opts.browserProxyMode,",
    "\t\ttargetId: opts.targetId",
  ].join("\n"), "snapshot completed-navigation guard");
  snapshotSource = replaceEvery(snapshotSource, [
    "\t\tssrfPolicy: opts.ssrfPolicy",
    "\t});",
  ].join("\n"), [
    "\t\tssrfPolicy: opts.ssrfPolicy,",
    "\t\tbrowserProxyMode: opts.browserProxyMode",
    "\t});",
  ].join("\n"), "snapshot proxy mode forwarding", 3);
  playwrightSource = prefix + snapshotSource + playwrightSource.slice(snapshotEnd);
  fs.writeFileSync(playwrightTarget, playwrightSource);
}
playwrightSource = fs.readFileSync(playwrightTarget, "utf8");
requireAll(playwrightSource, [playwrightMarker, "browserProxyMode: opts.browserProxyMode", "async function snapshotAiViaPlaywright(opts)", "async function snapshotRoleViaPlaywright(opts)", "async function snapshotAriaViaPlaywright(opts)"], "native browser proxy Playwright snapshot");

const navigationFailureMarker = "CLAWMANAGER_CONTAIN_DOCUMENT_DNS_FAILURE";
const playwrightSessionTarget = singleModule(/^pw-session-.*\.js$/, "async function gotoPageWithNavigationGuard(opts)", "Playwright navigation session");
let playwrightSessionSource = fs.readFileSync(playwrightSessionTarget, "utf8");
const uncontainedNavigationFailure = [
  "\t\t} catch (err) {",
  "\t\t\tif (isPolicyDenyNavigationError(err)) {",
  "\t\t\t\tif (requestKind === \"top-level\") blockedError = err;",
  "\t\t\t\tawait route.abort().catch(() => {});",
  "\t\t\t\treturn;",
  "\t\t\t}",
  "\t\t\tthrow err;",
  "\t\t}",
].join("\n");
const containedNavigationFailure = [
  "\t\t} catch (err) {",
  `\t\t\t// ${navigationFailureMarker}: DNS and transport lookup failures are`,
  "\t\t\t// request failures, not process failures. Preserve policy denials and",
  "\t\t\t// surface top-level failures to the browser tool while quietly aborting",
  "\t\t\t// failed subframes so a third-party document cannot kill the Gateway.",
  "\t\t\tif (requestKind === \"top-level\") blockedError = err;",
  "\t\t\tawait route.abort().catch(() => {});",
  "\t\t\treturn;",
  "\t\t}",
].join("\n");
if (patching && !playwrightSessionSource.includes(navigationFailureMarker)) {
  const navigationStart = playwrightSessionSource.indexOf("async function gotoPageWithNavigationGuard(opts)");
  const navigationEnd = playwrightSessionSource.indexOf("/** Resolve a browser snapshot ref", navigationStart);
  if (navigationStart < 0 || navigationEnd <= navigationStart) throw new Error("could not isolate Playwright guarded navigation");
  const prefix = playwrightSessionSource.slice(0, navigationStart);
  let navigationSource = playwrightSessionSource.slice(navigationStart, navigationEnd);
  navigationSource = replaceOnce(navigationSource, uncontainedNavigationFailure, containedNavigationFailure, "uncontained document DNS failure");
  playwrightSessionSource = prefix + navigationSource + playwrightSessionSource.slice(navigationEnd);
  fs.writeFileSync(playwrightSessionTarget, playwrightSessionSource);
}
playwrightSessionSource = fs.readFileSync(playwrightSessionTarget, "utf8");
requireAll(playwrightSessionSource, [navigationFailureMarker, 'if (requestKind === "top-level") blockedError = err;', "await route.abort().catch(() => {});", "async function gotoPageWithNavigationGuard(opts)"], "Playwright document DNS containment");

for (const [label, source] of [["routes", routeSource], ["Playwright", playwrightSource], ["Playwright session", playwrightSessionSource]]) {
  if (source.includes("__clawmanagerBrowserProxyMode")) throw new Error(`${label} still contains the retired hidden proxy marker`);
}

process.stdout.write(`OpenClaw native Browser proxy patch verified in ${path.basename(chromeTarget)}, ${path.basename(routeTarget)}, ${path.basename(playwrightTarget)}, and ${path.basename(playwrightSessionTarget)}\n`);
