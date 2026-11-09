import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { pathToFileURL } from "node:url";

const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const chromium = ["/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome"]
  .find((candidate) => fs.existsSync(candidate));
if (!chromium) throw new Error("Chromium is required for the managed Preview integration test");

const proxyPort = 18080;
const cdpPort = 19222;
const previewUrl = "http://p-abcdefghijklmnop.clawmanager-team-preview.invalid/v2/interactive/test/index.html";
const dnsContainmentUrl = "http://p-abcdefghijklmnop.clawmanager-team-preview.invalid/v2/interactive/test/dns-contained.html";
const profileDir = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-preview-profile-"));
const proxy = http.createServer((req, res) => {
  res.writeHead(200, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store" });
  if (String(req.url).includes("dns-contained.html")) {
    res.end("<!doctype html><title>DNS containment</title><h1>main document survived</h1><iframe src='http://rt.openclaw-dns-failure.invalid/tracker'></iframe>");
    return;
  }
  res.end("<!doctype html><title>Managed Preview</title><button id=inc>increment</button><output id=value>0</output><script>inc.onclick=()=>value.textContent=String(Number(value.textContent)+1)</script>");
});

await new Promise((resolve, reject) => {
  proxy.once("error", reject);
  proxy.listen(proxyPort, "127.0.0.1", resolve);
});
const browser = spawn(chromium, [
  "--headless=new",
  "--no-sandbox",
  "--disable-gpu",
  "--disable-dev-shm-usage",
  `--remote-debugging-port=${cdpPort}`,
  "--remote-debugging-address=127.0.0.1",
  `--user-data-dir=${profileDir}`,
  `--proxy-server=http://127.0.0.1:${proxyPort}`,
  "--proxy-bypass-list=<-loopback>",
  "about:blank",
], { stdio: ["ignore", "ignore", "pipe"] });
let browserDiagnostic='';
browser.stderr.on('data',chunk=>{browserDiagnostic=(browserDiagnostic+chunk).slice(-4000);});

try {
	const unhandled = [];
	const onUnhandled = (reason) => unhandled.push(reason);
	process.on("unhandledRejection", onUnhandled);
  const cdpUrl = `http://127.0.0.1:${cdpPort}`;
  let ready = false;
  // Cold Chromium startup competes with the canonical Go build on small VMs.
  // Bound that environment wait, and retain the actual failure if it exits.
  for (let attempt = 0; attempt < 300; attempt += 1) {
    try {
      const response = await fetch(`${cdpUrl}/json/version`);
      if (response.ok) {
        ready = true;
        break;
      }
    } catch {}
    if(browser.exitCode!==null)break;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!ready) throw new Error(`Chromium CDP did not become ready (exit=${browser.exitCode}): ${browserDiagnostic}`);

  const distDir = path.join(packageRoot, "dist");
  const pwFile = fs.readdirSync(distDir).find((name) => /^pw-ai-.*\.m?js$/.test(name));
  if (!pwFile) throw new Error("OpenClaw Playwright bridge was not found");
  const pwModule = await import(pathToFileURL(path.join(distDir, pwFile)).href);
  const pw = pwModule.pwAi;
  if (!pw || typeof pw.createPageViaPlaywright !== "function") {
    throw new Error("OpenClaw 8.1 Playwright runtime object was not exported");
  }
  const version = JSON.parse(fs.readFileSync(path.join(packageRoot, 'package.json'), 'utf8')).version;
  const capture = version === '2026.10.2' ? pw.snapshotRoleViaPlaywright : pw.snapshotAiViaPlaywright;
  assert.equal(typeof capture, 'function', 'native snapshot API unavailable');
  const ssrfPolicy = { dangerouslyAllowPrivateNetwork: true };
  const page = await pw.createPageViaPlaywright({
    cdpUrl,
    url: previewUrl,
    ssrfPolicy,
    browserProxyMode: "explicit-browser-proxy",
  });
  assert.equal(page.url, previewUrl);

  const snapshot = await capture({ cdpUrl, targetId: page.targetId, ssrfPolicy, browserProxyMode: "explicit-browser-proxy" });
  assert.match(JSON.stringify(snapshot), /increment/i);

  const before = await pw.executeActViaPlaywright({
    cdpUrl,
    targetId: page.targetId,
    ssrfPolicy,
    browserProxyMode: "explicit-browser-proxy",
    evaluateEnabled: true,
    action: { kind: "evaluate", fn: "() => Number(document.querySelector('#value').textContent)" },
  });
  assert.equal(before.result, 0);
  await pw.executeActViaPlaywright({
    cdpUrl,
    targetId: page.targetId,
    ssrfPolicy,
    browserProxyMode: "explicit-browser-proxy",
    evaluateEnabled: true,
    action: { kind: "click", selector: "#inc" },
  });
  const after = await pw.executeActViaPlaywright({
    cdpUrl,
    targetId: page.targetId,
    ssrfPolicy,
    browserProxyMode: "explicit-browser-proxy",
    evaluateEnabled: true,
    action: { kind: "evaluate", fn: "() => Number(document.querySelector('#value').textContent)" },
  });
  assert.equal(after.result, 1);

  const dnsPage = await pw.createPageViaPlaywright({
    cdpUrl,
    url: dnsContainmentUrl,
    ssrfPolicy,
    browserProxyMode: "explicit-browser-proxy",
  });
  assert.equal(dnsPage.url, dnsContainmentUrl);
  await new Promise((resolve) => setTimeout(resolve, 250));
  assert.equal(unhandled.length, 0, `subframe DNS failure escaped the request boundary: ${unhandled}`);
  const dnsSnapshot = await capture({ cdpUrl, targetId: dnsPage.targetId, ssrfPolicy, browserProxyMode: "explicit-browser-proxy" });
  assert.match(JSON.stringify(dnsSnapshot), /main document survived/i);
  process.off("unhandledRejection", onUnhandled);
} finally {
  browser.kill("SIGTERM");
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, 2000);
    browser.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
  await new Promise((resolve) => proxy.close(resolve));
  fs.rmSync(profileDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}

process.stdout.write("OpenClaw managed Preview Chromium integration test passed\n");
