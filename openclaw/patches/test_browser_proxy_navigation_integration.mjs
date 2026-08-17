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
const profileDir = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-preview-profile-"));
const proxy = http.createServer((req, res) => {
  res.writeHead(200, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store" });
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

try {
  const cdpUrl = `http://127.0.0.1:${cdpPort}`;
  let ready = false;
  for (let attempt = 0; attempt < 80; attempt += 1) {
    try {
      const response = await fetch(`${cdpUrl}/json/version`);
      if (response.ok) {
        ready = true;
        break;
      }
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!ready) throw new Error("Chromium CDP did not become ready");

  const distDir = path.join(packageRoot, "dist");
  const pwFile = fs.readdirSync(distDir).find((name) => /^pw-ai-.*\.js$/.test(name));
  if (!pwFile) throw new Error("OpenClaw Playwright bridge was not found");
  const pw = await import(pathToFileURL(path.join(distDir, pwFile)).href);
  const ssrfPolicy = {
    dangerouslyAllowPrivateNetwork: true,
    __clawmanagerBrowserProxyMode: "explicit-browser-proxy",
  };
  const page = await pw.createPageViaPlaywright({
    cdpUrl,
    url: previewUrl,
    ssrfPolicy,
    browserProxyMode: "explicit-browser-proxy",
  });
  assert.equal(page.url, previewUrl);

  const snapshot = await pw.snapshotAiViaPlaywright({ cdpUrl, targetId: page.targetId, ssrfPolicy });
  assert.match(JSON.stringify(snapshot), /increment/i);

  const before = await pw.executeActViaPlaywright({
    cdpUrl,
    targetId: page.targetId,
    ssrfPolicy,
    evaluateEnabled: true,
    action: { kind: "evaluate", fn: "() => Number(document.querySelector('#value').textContent)" },
  });
  assert.equal(before.result, 0);
  await pw.executeActViaPlaywright({
    cdpUrl,
    targetId: page.targetId,
    ssrfPolicy,
    evaluateEnabled: true,
    action: { kind: "click", selector: "#inc" },
  });
  const after = await pw.executeActViaPlaywright({
    cdpUrl,
    targetId: page.targetId,
    ssrfPolicy,
    evaluateEnabled: true,
    action: { kind: "evaluate", fn: "() => Number(document.querySelector('#value').textContent)" },
  });
  assert.equal(after.result, 1);
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
