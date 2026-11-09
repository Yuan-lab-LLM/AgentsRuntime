import fs from "node:fs";

const configPath = process.argv[2];
if (!configPath) throw new Error("OpenClaw config path is required");
const config = JSON.parse(fs.readFileSync(configPath, "utf8"));
config.channels ||= {};
config.channels["redis-team"] = {
  enabled: true,
  accounts: {
    default: {
      autoRun: true,
      consumerGroup: "team-members",
      dlqKey: "clawmanager:team:38:dlq",
      enabled: true,
      eventsKey: "clawmanager:team:38:events",
      fromEnv: true,
      inboxKey: "clawmanager:team:38:member:210:inbox",
      managerUrl: "https://clawmanager.invalid",
      memberId: "210",
      presenceKey: "clawmanager:team:38:presence",
      redisUrl: "redis://redis.invalid:6379/0",
      role: "leader",
      sharedDir: "/team",
      teamId: "38",
    },
  },
};
fs.writeFileSync(configPath, `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 });
