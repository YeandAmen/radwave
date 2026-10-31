const { readFileSync } = require("node:fs");
const { createHash } = require("node:crypto");
const base = process.argv[2],
  archive = process.argv[3],
  ownerFile = process.argv[4];
if (!base || !archive || !ownerFile)
  throw new Error(
    "Supply the hosted URL, dataset ZIP and private owner credential file.",
  );
const secrets = {};
for (const line of readFileSync(ownerFile, "utf8").split(/\r?\n/)) {
  const m = line.match(/^([A-Z_]+)=(.*)$/);
  if (m) secrets[m[1]] = m[2].startsWith('"') ? JSON.parse(m[2]) : m[2];
}
const data = readFileSync(archive),
  digest = createHash("sha256").update(data).digest("hex");
async function call(path, body, type = "application/octet-stream") {
  let failure;
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      const r = await fetch(base + path, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${secrets.RADAR_ADMIN_TOKEN}`,
          "Content-Type": type,
        },
        body,
        signal: AbortSignal.timeout(path.includes("/commit") ? 270000 : 60000),
      });
      if (!r.ok) throw new Error(`${path.split("?")[0]}: HTTP ${r.status}`);
      return r.json();
    } catch (error) {
      failure = error;
      if (attempt < 3)
        await new Promise((resolve) =>
          setTimeout(resolve, 1000 * (attempt + 1)),
        );
    }
  }
  throw failure;
}
(async () => {
  const status = await fetch(
    `${base}/api/catalog/upload-status?digest=${digest}`,
    {
      headers: { Authorization: `Bearer ${secrets.RADAR_ADMIN_TOKEN}` },
      signal: AbortSignal.timeout(60000),
    },
  );
  if (!status.ok) throw new Error(`Upload status: HTTP ${status.status}`);
  const stored = (await status.json()).parts;
  const pending = [];
  for (let part = 0, start = 0; start < data.length; part++, start += 1 << 20) {
    const end = Math.min(start + (1 << 20), data.length);
    if (stored[part] !== end - start) pending.push({ part, start, end });
  }
  console.log(
    `Dataset upload: ${Object.keys(stored).length} chunks stored; ${pending.length} remaining.`,
  );
  let next = 0;
  await Promise.all(
    Array.from({ length: 4 }, async () => {
      while (next < pending.length) {
        const chunk = pending[next++];
        await call(
          `/api/catalog/chunks?digest=${digest}&part=${chunk.part}`,
          data.subarray(chunk.start, chunk.end),
        );
        console.log(`Uploaded dataset chunk ${chunk.part + 1}.`);
      }
    }),
  );
  const m = await (
    await fetch(
      "https://www.kaggle.com/api/v1/datasets/view/chandranshbinjola/job-listings",
      { signal: AbortSignal.timeout(30000) },
    )
  ).json();
  const result = await call(
    "/api/catalog/commit",
    JSON.stringify({
      digest,
      bytes: data.length,
      version: m.currentVersionNumber || 0,
    }),
    "application/json",
  );
  console.log(
    JSON.stringify({
      imported: result.imported,
      rows: result.info.rows,
      open: result.info.open,
      closed: result.info.closed,
      as_of: result.info.as_of,
    }),
  );
})().catch((error) => {
  let message = error.message;
  for (const value of Object.values(secrets))
    if (value) message = message.split(value).join("[redacted]");
  console.error(message);
  process.exitCode = 1;
});
