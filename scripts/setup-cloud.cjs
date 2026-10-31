// Creates app-scoped secrets via stdin; no account token or secret is printed.
const { randomBytes } = require("node:crypto");
const { execFileSync } = require("node:child_process");
const { writeFileSync } = require("node:fs");

const scope = process.argv[2] || "fyield1";
const secrets = {
  RADAR_ADMIN_TOKEN: randomBytes(32).toString("base64url"),
  CRON_SECRET: randomBytes(32).toString("base64url"),
};
const ownerFile = process.argv[3];
if (!ownerFile)
  throw new Error("Supply a private owner-secret file outside the repository.");
writeFileSync(
  ownerFile,
  Object.entries(secrets)
    .map(([name, value]) => `${name}=${JSON.stringify(value)}`)
    .join("\n") + "\n",
  { mode: 0o600 },
);

function run(binary, args, input) {
  try {
    return execFileSync(binary, args, {
      input,
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"],
      timeout: 60000,
    });
  } catch (error) {
    let message = String(error.stderr || error.message);
    for (const value of Object.values(secrets))
      message = message.split(value).join("[redacted]");
    throw new Error(message);
  }
}

for (const [name, value] of Object.entries(secrets)) {
  run(
    "npx",
    [
      "--yes",
      "vercel@latest",
      "env",
      "add",
      name,
      "--environment",
      "production",
      "--force",
      "--scope",
      scope,
      "--yes",
    ],
    value + "\n",
  );
  console.log(`Configured ${name} in Vercel production.`);
}
run(
  "gh",
  [
    "secret",
    "set",
    "RADAR_CRON_SECRET",
    "--repo",
    "YeandAmen/opportunity-radar",
  ],
  secrets.CRON_SECRET,
);
console.log("Configured the app-scoped GitHub refresh secret.");
console.log(`Private owner credentials saved to ${ownerFile}.`);
