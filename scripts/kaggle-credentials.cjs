const { readFileSync } = require("node:fs");
const { execFileSync } = require("node:child_process");
const file = process.argv[2],
  mode = process.argv[3] || "status";
if (!file) throw new Error("Supply the private Kaggle environment file.");
const line = readFileSync(file, "utf8")
  .split(/\r?\n/)
  .find((line) => line.startsWith("KAGGLE_API_TOKEN="));
const token =
  process.env.KAGGLE_API_TOKEN ||
  line?.slice("KAGGLE_API_TOKEN=".length).trim();
if (!token || !token.startsWith("KGAT_"))
  throw new Error("No Kaggle API token was configured.");
if (mode === "status") console.log(JSON.stringify({ configured: true }));
else if (mode === "check") {
  fetch("https://www.kaggle.com/api/v1/datasets/list?group=my", {
    headers: { Authorization: `Bearer ${token}` },
    signal: AbortSignal.timeout(30000),
  })
    .then(async (response) => {
      const data = await response.json().catch(() => null);
      console.log(
        JSON.stringify({
          status: response.status,
          authenticated: response.ok,
          readable: !!data,
        }),
      );
      if (!response.ok) process.exitCode = 1;
    })
    .catch((error) => {
      console.error(String(error.message).split(token).join("[redacted]"));
      process.exitCode = 1;
    });
} else if (mode === "provision") {
  try {
    execFileSync(
      "npx",
      [
        "--yes",
        "vercel@latest",
        "env",
        "add",
        "KAGGLE_API_TOKEN",
        "--environment",
        "production",
        "--force",
        "--scope",
        "fyield1",
        "--yes",
      ],
      { input: token + "\n", stdio: ["pipe", "pipe", "pipe"], timeout: 60000 },
    );
    console.log("Configured the Kaggle credential for Radwave production.");
  } catch (error) {
    throw new Error(
      String(error.stderr || error.message)
        .split(token)
        .join("[redacted]"),
    );
  }
} else if (mode === "import") {
  const archive = process.argv[4],
    environment = process.argv[5];
  if (!archive || !environment)
    throw new Error(
      "Supply the dataset ZIP and private database environment file.",
    );
  try {
    const output = execFileSync(
      "go",
      [
        "run",
        "./cmd/radar",
        "import-kaggle",
        "-from",
        archive,
        "-env-file",
        environment,
      ],
      {
        env: { ...process.env, KAGGLE_API_TOKEN: token },
        stdio: ["ignore", "pipe", "pipe"],
        timeout: 600000,
        maxBuffer: 1 << 20,
      },
    );
    const info = JSON.parse(output.toString());
    console.log(
      JSON.stringify({
        rows: info.rows,
        open: info.open,
        closed: info.closed,
        as_of: info.as_of,
        version: info.version,
        compressed_bytes: info.compressed_bytes,
        duplicates: info.duplicates,
        fields: info.fields,
      }),
    );
  } catch (error) {
    throw new Error(
      (String(error.stderr || "").trim() || error.message)
        .split(token)
        .join("[redacted]"),
    );
  }
} else throw new Error("Use status, check, provision, or import mode.");
