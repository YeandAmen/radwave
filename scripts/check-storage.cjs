const { readFileSync } = require("node:fs");
const { execFileSync } = require("node:child_process");

const file = process.argv[2];
if (!file)
  throw new Error("Supply the private Vercel production environment file.");
let url;
for (const line of readFileSync(file, "utf8").split(/\r?\n/)) {
  const match = line.match(/^DATABASE_URL=(.*)$/);
  if (match) url = match[1].startsWith('"') ? JSON.parse(match[1]) : match[1];
}
if (!url) throw new Error("The environment file has no DATABASE_URL.");
execFileSync(
  "go",
  [
    "test",
    "-race",
    "./internal/store",
    "-run",
    "TestHostedStoreAcrossConnections",
    "-count=1",
    "-v",
  ],
  {
    env: { ...process.env, RADAR_TEST_DATABASE_URL: url },
    stdio: "inherit",
    timeout: 120000,
  },
);
