import fs from "node:fs";

const configPath = process.argv[2] || "/defaults/.openclaw/openclaw.json";
const raw = fs.readFileSync(configPath, "utf8");
const config = JSON.parse(raw);

config.channels ||= {};
config.channels["redis-team"] ||= {};
config.channels["redis-team"].accounts ||= {};
config.channels["redis-team"].accounts.default = {
  ...(config.channels["redis-team"].accounts.default || {}),
  fromEnv: true,
};

config.plugins ||= {};
config.plugins.entries ||= {};
config.plugins.entries["redis-team"] = {
  ...(config.plugins.entries["redis-team"] || {}),
  enabled: true,
  config: {
    ...(config.plugins.entries["redis-team"]?.config || {}),
    fromEnv: true,
  },
};

fs.writeFileSync(configPath, JSON.stringify(config, null, 2) + "\n", "utf8");
