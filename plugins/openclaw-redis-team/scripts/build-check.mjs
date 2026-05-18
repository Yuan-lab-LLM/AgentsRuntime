import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const required = ["package.json", "openclaw.plugin.json", "dist/index.js", "README.md"];
for (const rel of required) {
  const full = path.join(root, rel);
  if (!fs.existsSync(full)) throw new Error(`missing required file: ${rel}`);
}
const manifest = JSON.parse(fs.readFileSync(path.join(root, "openclaw.plugin.json"), "utf8"));
const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
if (manifest.id !== "redis-team") throw new Error(`unexpected plugin id: ${manifest.id}`);
if (!pkg.openclaw?.extensions?.includes("./dist/index.js")) {
  throw new Error("package.json openclaw.extensions must include ./dist/index.js");
}
console.log("openclaw-redis-team build check passed");
