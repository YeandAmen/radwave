const { readFileSync } = require("node:fs");
const { execFileSync } = require("node:child_process");
const line = readFileSync(process.argv[2], "utf8")
  .split(/\r?\n/)
  .find((line) => line.startsWith("DATABASE_URL="));
if (!line) throw new Error("No database configuration");
const raw = line.slice("DATABASE_URL=".length),
  url = raw.startsWith('"') ? JSON.parse(raw) : raw;
try {
  execFileSync(
    "go",
    [
      "test",
      "./internal/store",
      "-run",
      "TestHostedCatalogueTransportSizes",
      "-count=1",
      "-v",
      "-timeout",
      "90s",
    ],
    {
      env: { ...process.env, RADAR_TEST_DATABASE_URL: url },
      stdio: ["ignore", "pipe", "pipe"],
      timeout: 110000,
    },
  )
    .toString()
    .split(url)
    .join("[redacted]")
    .split("\n")
    .forEach((line) => console.log(line));
} catch (error) {
  console.error(
    (String(error.stdout || "") + String(error.stderr || error.message))
      .split(url)
      .join("[redacted]"),
  );
  process.exitCode = 1;
}
