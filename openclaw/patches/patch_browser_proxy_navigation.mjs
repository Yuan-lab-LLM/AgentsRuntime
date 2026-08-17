import fs from "node:fs";
import path from "node:path";

const expectedVersion = "2026.7.1-2";
const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
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
  if (candidates.length !== 1) {
    throw new Error(`expected one OpenClaw ${label} module, found ${candidates.length}`);
  }
  return candidates[0];
}

function replaceOnce(source, needle, replacement, label) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences !== 1) throw new Error(`expected one ${label}, found ${occurrences}`);
  return source.replace(needle, replacement);
}

function replaceEvery(source, needle, replacement, label) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences < 1) throw new Error(`expected at least one ${label}, found ${occurrences}`);
  return source.split(needle).join(replacement);
}

function verify(source, requirements, label) {
  for (const required of requirements) {
    if (!source.includes(required)) throw new Error(`${label} patch is incomplete: ${required}`);
  }
}

const chromeMarker = "CLAWMANAGER_MANAGED_PREVIEW_PROXY_DNS";
const chromeTarget = singleModule(/^chrome-.*\.js$/, "async function assertBrowserNavigationAllowed(opts)", "browser navigation");
let chromeSource = fs.readFileSync(chromeTarget, "utf8");
const dnsCall = "\tawait resolvePinnedHostnameWithPolicy(parsed.hostname, {";
if (patching && !chromeSource.includes(chromeMarker)) {
  chromeSource = replaceOnce(chromeSource, dnsCall, [
    `\t// ${chromeMarker}: Chromium is already forced through the operator-managed`,
    "\t// forward proxy. Interactive Team previews use a signature-derived,",
    "\t// non-resolving origin. Delegate only that exact reserved host shape;",
    "\t// direct profiles and every ordinary destination retain upstream checks.",
    "\tif (opts.browserProxyMode === \"explicit-browser-proxy\" &&",
    "\t\tisPrivateNetworkAllowedByPolicy(opts.ssrfPolicy) &&",
    "\t\t/^p-[a-z0-9_-]{16}\\.clawmanager-team-preview\\.invalid$/.test(normalizeHostname(parsed.hostname))) return;",
    dnsCall,
  ].join("\n"), "navigation DNS call");
  fs.writeFileSync(chromeTarget, chromeSource);
}
chromeSource = fs.readFileSync(chromeTarget, "utf8");
verify(chromeSource, [
  chromeMarker,
  'opts.browserProxyMode === "explicit-browser-proxy"',
  "isPrivateNetworkAllowedByPolicy(opts.ssrfPolicy)",
  "/^p-[a-z0-9_-]{16}\\.clawmanager-team-preview\\.invalid$/",
  dnsCall,
], "OpenClaw managed Preview Browser DNS");

const contextMarker = "CLAWMANAGER_MANAGED_PREVIEW_PROXY_CONTEXT";
const routeTarget = singleModule(/^routes-.*\.js$/, "function browserNavigationPolicyForProfile(ctx, profileCtx)", "browser routes");
let routeSource = fs.readFileSync(routeTarget, "utf8");
const policyFunction = [
  "function browserNavigationPolicyForProfile(ctx, profileCtx) {",
  "\treturn withBrowserNavigationPolicy(ctx.state().resolved.ssrfPolicy, { browserProxyMode: resolveBrowserNavigationProxyMode({",
  "\t\tresolved: ctx.state().resolved,",
  "\t\tprofile: profileCtx.profile",
  "\t}) });",
  "}",
].join("\n");
const patchedPolicyFunction = [
  "function browserNavigationPolicyForProfile(ctx, profileCtx) {",
  `\t// ${contextMarker}: keep the resolved proxy mode attached to the`,
  "\t// per-call SSRF policy so nested Playwright helpers cannot silently",
  "\t// fall back to local DNS after a managed-proxy navigation.",
  "\tconst policy = withBrowserNavigationPolicy(ctx.state().resolved.ssrfPolicy, { browserProxyMode: resolveBrowserNavigationProxyMode({",
  "\t\tresolved: ctx.state().resolved,",
  "\t\tprofile: profileCtx.profile",
  "\t}) });",
  "\tif (policy.ssrfPolicy && policy.browserProxyMode) policy.ssrfPolicy = {",
  "\t\t...policy.ssrfPolicy,",
  "\t\t__clawmanagerBrowserProxyMode: policy.browserProxyMode",
  "\t};",
  "\treturn policy;",
  "}",
].join("\n");
if (patching && !routeSource.includes(contextMarker)) {
  routeSource = replaceOnce(routeSource, policyFunction, patchedPolicyFunction, "profile navigation policy function");
  routeSource = replaceOnce(
    routeSource,
    "\t\t\t\tconst ssrfPolicy = ctx.state().resolved.ssrfPolicy;",
    "\t\t\t\tconst ssrfPolicy = browserNavigationPolicyForProfile(ctx, profileCtx).ssrfPolicy;",
    "Playwright act SSRF policy binding",
  );
}
if (patching && routeSource.includes("ssrfPolicy: ctx.state().resolved.ssrfPolicy")) {
  if (routeSource.includes("\t\t\trun: async ({ cdpUrl, tab, pw, resolveTabUrl }) => {")) {
    routeSource = replaceEvery(
      routeSource,
      "\t\t\trun: async ({ cdpUrl, tab, pw, resolveTabUrl }) => {",
      "\t\t\trun: async ({ profileCtx, cdpUrl, tab, pw, resolveTabUrl }) => {",
      "Playwright route profile context binding",
    );
  }
  routeSource = replaceEvery(
    routeSource,
    "ssrfPolicy: ctx.state().resolved.ssrfPolicy",
    "ssrfPolicy: browserNavigationPolicyForProfile(ctx, profileCtx).ssrfPolicy",
    "profile-scoped Browser SSRF policy binding",
  );
}
if (patching) fs.writeFileSync(routeTarget, routeSource);
routeSource = fs.readFileSync(routeTarget, "utf8");
verify(routeSource, [
  contextMarker,
  "__clawmanagerBrowserProxyMode: policy.browserProxyMode",
  "const ssrfPolicy = browserNavigationPolicyForProfile(ctx, profileCtx).ssrfPolicy;",
  "run: async ({ profileCtx, cdpUrl, tab, pw, resolveTabUrl }) => {",
  "ssrfPolicy: browserNavigationPolicyForProfile(ctx, profileCtx).ssrfPolicy",
], "OpenClaw managed Preview Browser route context");
if (routeSource.includes("ssrfPolicy: ctx.state().resolved.ssrfPolicy")) {
  throw new Error("OpenClaw managed Preview Browser route context still contains an unscoped SSRF policy");
}

const playwrightMarker = "CLAWMANAGER_MANAGED_PREVIEW_PROXY_POLICY";
const playwrightTarget = singleModule(/^pw-ai-.*\.js$/, "async function assertPageNavigationCompletedSafely(opts)", "Playwright bridge");
let playwrightSource = fs.readFileSync(playwrightTarget, "utf8");
const pageGuardStart = [
  "/** Validate a completed page navigation and quarantine policy-denied targets. */",
  "async function assertPageNavigationCompletedSafely(opts) {",
  "\tconst navigationPolicy = withBrowserNavigationPolicy(opts.ssrfPolicy, { browserProxyMode: opts.browserProxyMode });",
].join("\n");
const patchedPageGuardStart = [
  `// ${playwrightMarker}: recover only the proxy mode resolved by the route`,
  "// for this call. The marker never grants access by itself; the central",
  "// navigation guard still checks the exact managed Preview host and policy.",
  "function clawmanagerBrowserProxyModeForPolicy(policy) {",
  "\treturn policy?.__clawmanagerBrowserProxyMode === \"explicit-browser-proxy\"",
  "\t\t? \"explicit-browser-proxy\"",
  "\t\t: void 0;",
  "}",
  "/** Validate a completed page navigation and quarantine policy-denied targets. */",
  "async function assertPageNavigationCompletedSafely(opts) {",
  "\tconst navigationPolicy = withBrowserNavigationPolicy(opts.ssrfPolicy, {",
  "\t\tbrowserProxyMode: opts.browserProxyMode ?? clawmanagerBrowserProxyModeForPolicy(opts.ssrfPolicy)",
  "\t});",
].join("\n");
if (patching && !playwrightSource.includes(playwrightMarker)) {
  playwrightSource = replaceOnce(playwrightSource, pageGuardStart, patchedPageGuardStart, "Playwright page navigation guard");
  playwrightSource = replaceOnce(
    playwrightSource,
    "\t\t...withBrowserNavigationPolicy(ssrfPolicy)\n",
    "\t\t...withBrowserNavigationPolicy(ssrfPolicy, { browserProxyMode: clawmanagerBrowserProxyModeForPolicy(ssrfPolicy) })\n",
    "Playwright subframe navigation policy",
  );
  playwrightSource = replaceOnce(
    playwrightSource,
    "\t\t\t...withBrowserNavigationPolicy(opts.ssrfPolicy)\n",
    "\t\t\t...withBrowserNavigationPolicy(opts.ssrfPolicy, { browserProxyMode: clawmanagerBrowserProxyModeForPolicy(opts.ssrfPolicy) })\n",
    "Playwright download navigation policy",
  );
  fs.writeFileSync(playwrightTarget, playwrightSource);
}
playwrightSource = fs.readFileSync(playwrightTarget, "utf8");
verify(playwrightSource, [
  playwrightMarker,
  "clawmanagerBrowserProxyModeForPolicy(opts.ssrfPolicy)",
  "clawmanagerBrowserProxyModeForPolicy(ssrfPolicy)",
], "OpenClaw managed Preview Playwright policy");

process.stdout.write(
  `OpenClaw managed Preview Browser patch verified in ${path.basename(chromeTarget)}, ${path.basename(routeTarget)}, and ${path.basename(playwrightTarget)}\n`,
);
