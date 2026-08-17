import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const patchScript = path.resolve(import.meta.dirname, "patch_browser_proxy_navigation.mjs");
const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-browser-proxy-patch-"));
try {
  fs.mkdirSync(path.join(fixtureRoot, "dist"), { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, "package.json"), JSON.stringify({ version: "2026.7.1-2", type: "module" }));
  fs.writeFileSync(
    path.join(fixtureRoot, "dist", "chrome-fixture.js"),
    [
      "const lookups = [];",
      "function normalizeHostname(value) { return String(value || '').trim().toLowerCase().replace(/\\.$/, ''); }",
      "function isPrivateNetworkAllowedByPolicy(policy) { return policy?.dangerouslyAllowPrivateNetwork === true || policy?.allowPrivateNetwork === true; }",
      "async function resolvePinnedHostnameWithPolicy(hostname) { lookups.push(normalizeHostname(hostname)); }",
      "async function assertBrowserNavigationAllowed(opts) {",
      "\tconst parsed = new URL(opts.url);",
      "\tawait resolvePinnedHostnameWithPolicy(parsed.hostname, {",
      "\t\tlookupFn: opts.lookupFn,",
      "\t\tpolicy: opts.ssrfPolicy",
      "\t});",
      "}",
      "export async function check(url, browserProxyMode = 'explicit-browser-proxy', allowPrivate = true) {",
      "\tconst before = lookups.length;",
      "\tawait assertBrowserNavigationAllowed({ url, browserProxyMode, ssrfPolicy: { dangerouslyAllowPrivateNetwork: allowPrivate } });",
      "\treturn lookups.length - before;",
      "}",
    ].join("\n"),
  );
  fs.writeFileSync(
    path.join(fixtureRoot, "dist", "routes-fixture.js"),
    [
      "function withBrowserNavigationPolicy(ssrfPolicy, extra = {}) { return { ssrfPolicy, ...extra }; }",
      "function resolveBrowserNavigationProxyMode({ profile }) { return profile.proxyMode || 'direct'; }",
      "function browserNavigationPolicyForProfile(ctx, profileCtx) {",
      "\treturn withBrowserNavigationPolicy(ctx.state().resolved.ssrfPolicy, { browserProxyMode: resolveBrowserNavigationProxyMode({",
      "\t\tresolved: ctx.state().resolved,",
      "\t\tprofile: profileCtx.profile",
      "\t}) });",
      "}",
      "function actPolicy(ctx, profileCtx) {",
      "\t\t\t\tconst ssrfPolicy = ctx.state().resolved.ssrfPolicy;",
      "\treturn ssrfPolicy;",
      "}",
      "function routeContext(ctx) {",
      "\treturn {",
      "\t\t\trun: async ({ cdpUrl, tab, pw, resolveTabUrl }) => {",
      "\t\t\t\tconst call = { ssrfPolicy: ctx.state().resolved.ssrfPolicy };",
      "\t\t\t\treturn { cdpUrl, tab, pw, resolveTabUrl, call };",
      "\t\t\t},",
      "\t};",
      "}",
      "export function policyFor(proxyMode) {",
      "\tconst ctx = { state: () => ({ resolved: { ssrfPolicy: { dangerouslyAllowPrivateNetwork: true } } }) };",
      "\treturn actPolicy(ctx, { profile: { proxyMode } });",
      "}",
    ].join("\n"),
  );
  fs.writeFileSync(
    path.join(fixtureRoot, "dist", "pw-ai-fixture.js"),
    [
      "const lookups = [];",
      "function withBrowserNavigationPolicy(ssrfPolicy, extra = {}) { return { ssrfPolicy, ...extra }; }",
      "async function assertBrowserNavigationResultAllowed(opts) {",
      "\tconst host = new URL(opts.url).hostname;",
      "\tif (opts.browserProxyMode === 'explicit-browser-proxy' && /^p-[a-z0-9_-]{16}\\.clawmanager-team-preview\\.invalid$/.test(host)) return;",
      "\tlookups.push(host);",
      "}",
      "async function assertSubframeNavigationAllowed(frameUrl, ssrfPolicy) {",
      "\tif (!ssrfPolicy || !frameUrl.startsWith(\"http://\") && !frameUrl.startsWith(\"https://\")) return;",
      "\tawait assertBrowserNavigationResultAllowed({",
      "\t\turl: frameUrl,",
      "\t\t...withBrowserNavigationPolicy(ssrfPolicy)",
      "\t});",
      "}",
      "/** Validate a completed page navigation and quarantine policy-denied targets. */",
      "async function assertPageNavigationCompletedSafely(opts) {",
      "\tconst navigationPolicy = withBrowserNavigationPolicy(opts.ssrfPolicy, { browserProxyMode: opts.browserProxyMode });",
      "\tawait assertBrowserNavigationResultAllowed({ url: opts.page.url(), ...navigationPolicy });",
      "}",
      "async function executeActViaPlaywright(opts) {",
      "\tconst download = { url: opts.url };",
      "\tif (download.url) await assertBrowserNavigationResultAllowed({",
      "\t\t\turl: download.url,",
      "\t\t\t...withBrowserNavigationPolicy(opts.ssrfPolicy)",
      "\t\t});",
      "}",
      "export async function checkPage(url, ssrfPolicy, browserProxyMode) {",
      "\tconst before = lookups.length;",
      "\tawait assertPageNavigationCompletedSafely({ page: { url: () => url }, ssrfPolicy, browserProxyMode });",
      "\treturn lookups.length - before;",
      "}",
      "export async function checkSubframe(url, ssrfPolicy) {",
      "\tconst before = lookups.length; await assertSubframeNavigationAllowed(url, ssrfPolicy); return lookups.length - before;",
      "}",
      "export async function checkDownload(url, ssrfPolicy) {",
      "\tconst before = lookups.length; await executeActViaPlaywright({ url, ssrfPolicy }); return lookups.length - before;",
      "}",
    ].join("\n"),
  );

  const run = (mode) => spawnSync(process.execPath, [patchScript, mode], {
    env: { ...process.env, OPENCLAW_PACKAGE_ROOT: fixtureRoot },
    encoding: "utf8",
  });
  const patched = run("--patch");
  assert.equal(patched.status, 0, patched.stderr || patched.stdout);
  const verified = run("--verify");
  assert.equal(verified.status, 0, verified.stderr || verified.stdout);

  const chrome = await import(pathToFileURL(path.join(fixtureRoot, "dist", "chrome-fixture.js")).href);
  const routes = await import(pathToFileURL(path.join(fixtureRoot, "dist", "routes-fixture.js")).href);
  const playwright = await import(pathToFileURL(path.join(fixtureRoot, "dist", "pw-ai-fixture.js")).href);
  const previewUrl = "http://p-abcdefghijklmnop.clawmanager-team-preview.invalid/v2/interactive/x";
  const managedPolicy = routes.policyFor("explicit-browser-proxy");
  const directPolicy = routes.policyFor("direct");

  assert.equal(await chrome.check("https://example.com/"), 1, "ordinary public hosts retain upstream DNS pinning");
  assert.equal(await chrome.check(previewUrl), 0, "exact managed Preview host delegates DNS to the proxy");
  assert.equal(await chrome.check(previewUrl, "direct"), 1, "direct profiles do not receive the Preview exception");
  assert.equal(await chrome.check(previewUrl, "explicit-browser-proxy", false), 1, "strict private-network policy does not receive the exception");
  assert.equal(await playwright.checkPage(previewUrl, managedPolicy), 0, "post-open page guards retain managed proxy mode");
  assert.equal(await playwright.checkSubframe(previewUrl, managedPolicy), 0, "subframe guards retain managed proxy mode");
  assert.equal(await playwright.checkDownload(previewUrl, managedPolicy), 0, "download guards retain managed proxy mode");
  assert.equal(await playwright.checkPage(previewUrl, directPolicy), 1, "direct Playwright calls retain local policy checks");
  assert.equal(await playwright.checkPage("https://example.com/", managedPolicy), 1, "ordinary public Playwright targets retain policy checks");

  for (const hostname of [
    "clawmanager-team-preview.invalid",
    "p-short.clawmanager-team-preview.invalid",
    "p-abcdefghijklmnop.evil.clawmanager-team-preview.invalid",
    "xp-abcdefghijklmnop.clawmanager-team-preview.invalid",
    "p-abcdefghijklmnop.clawmanager-team-preview.invalid.evil.example",
  ]) {
    assert.equal(await chrome.check(`http://${hostname}/v2/interactive/x`), 1, `lookalike host retains upstream validation: ${hostname}`);
  }
} finally {
  fs.rmSync(fixtureRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}

process.stdout.write("OpenClaw Browser proxy navigation patch test passed\n");
