// Inspect or forward only the X API credential from the legacy scraper.
// Values are never printed or stored in the Radwave repository.
const { readFileSync } = require("node:fs");
const { execFileSync } = require("node:child_process");

const file = process.argv[2];
const mode = process.argv[3] || "status";
if (!file)
  throw new Error("Supply the legacy scraper's private environment file.");
let token =
  process.env.X_BEARER_TOKEN || process.env.TWITTER_BEARER_TOKEN || "";
for (const line of readFileSync(file, "utf8").split(/\r?\n/)) {
  const match = line.match(
    /^\s*(?:export\s+)?(?:X_BEARER_TOKEN|TWITTER_BEARER_TOKEN)\s*=\s*(.*)$/,
  );
  if (!match || token) continue;
  let value = match[1].trim();
  if (value.startsWith('"')) {
    try {
      value = JSON.parse(value);
    } catch {
      value = value.slice(1, -1);
    }
  } else if (value.startsWith("'") && value.endsWith("'"))
    value = value.slice(1, -1);
  token = value;
}
const configured =
  !!token &&
  !/^(?:your[_-]|example|placeholder|change[_-]|test[_-])/i.test(token) &&
  token.length > 30;
if (mode === "status")
  console.log(JSON.stringify({ credential: "X_BEARER_TOKEN", configured }));
else if (mode === "check") {
  if (!configured) throw new Error("No usable X API bearer token was found.");
  const params = new URLSearchParams({
    query: "scholarship apply -is:retweet lang:en",
    max_results: "10",
  });
  fetch("https://api.x.com/2/tweets/search/recent?" + params, {
    headers: { Authorization: "Bearer " + token },
    signal: AbortSignal.timeout(20000),
  })
    .then(async (response) => {
      const data = await response.json().catch(() => ({}));
      console.log(
        JSON.stringify({
          status: response.status,
          accessible: response.ok,
          result_count: data.meta?.result_count || 0,
          title: data.title || null,
        }),
      );
    })
    .catch((error) => {
      console.error(String(error.message).split(token).join("[redacted]"));
      process.exitCode = 1;
    });
} else if (mode === "provision") {
  if (!configured)
    throw new Error(
      "No usable X API bearer token was found in the legacy environment file.",
    );
  try {
    execFileSync(
      "npx",
      [
        "--yes",
        "vercel@latest",
        "env",
        "add",
        "X_BEARER_TOKEN",
        "--environment",
        "production",
        "--force",
        "--scope",
        "fyield1",
        "--yes",
      ],
      { input: token + "\n", stdio: ["pipe", "pipe", "pipe"], timeout: 60000 },
    );
    console.log("Configured the X API credential for Radwave production.");
  } catch (error) {
    throw new Error(
      String(error.stderr || error.message)
        .split(token)
        .join("[redacted]"),
    );
  }
} else throw new Error("Use status, check, or provision mode.");
