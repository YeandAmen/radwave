const { readFileSync } = require("node:fs");
const { chromium, devices, expect } = require("@playwright/test");

const base = process.argv[2];
const file = process.argv[3];
if (!base || !file)
  throw new Error(
    "Supply the production URL and private owner credential file.",
  );
const secrets = {};
for (const line of readFileSync(file, "utf8").split(/\r?\n/)) {
  const match = line.match(/^([A-Z_]+)=(.*)$/);
  if (match)
    secrets[match[1]] = match[2].startsWith('"')
      ? JSON.parse(match[2])
      : match[2];
}

async function call(path, options = {}) {
  const response = await fetch(base + path, {
    ...options,
    signal: AbortSignal.timeout(options.long ? 270000 : 30000),
  });
  const data = await response.json();
  if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`);
  return data;
}

(async () => {
  const health = await call("/healthz");
  expect(health.status).toBe("ok");
  const before = await call("/api/meta");
  expect(before.storage).toBe("PostgreSQL");
  expect(before.hosted).toBe(true);
  expect(before.name).toBe("Radwave");
  expect(before.stats.total).toBeGreaterThanOrEqual(23);
  const anonymous = await fetch(base + "/api/unlock", { method: "POST" });
  expect(anonymous.status).toBe(401);
  const headers = {
    Authorization: `Bearer ${secrets.RADAR_ADMIN_TOKEN}`,
    "Content-Type": "application/json",
  };
  expect(
    (await call("/api/unlock", { method: "POST", headers })).unlocked,
  ).toBe(true);
  const sources = await call("/api/sources");
  const source = sources.items.find((item) => item.kind === "linkedin_post");
  expect(source).toBeTruthy();
  try {
    await call(`/api/sources/${source.id}`, {
      method: "PATCH",
      headers,
      body: JSON.stringify({ enabled: false }),
    });
    expect(
      (await call("/api/sources")).items.find((item) => item.id === source.id)
        .enabled,
    ).toBe(false);
  } finally {
    await call(`/api/sources/${source.id}`, {
      method: "PATCH",
      headers,
      body: JSON.stringify({ enabled: source.enabled }),
    });
  }
  const cron = await call("/api/cron", {
    headers: { Authorization: `Bearer ${secrets.CRON_SECRET}` },
    long: true,
  });
  const after = await call("/api/meta");
  expect(after.stats.total).toBeGreaterThanOrEqual(before.stats.total);
  const csv = await fetch(base + "/api/export?format=csv&kind=program");
  expect(csv.status).toBe(200);
  expect(await csv.text()).toContain("Business Incubation");
  const browser = await chromium.launch();
  try {
    const desktop = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
      reducedMotion: "reduce",
    });
    await desktop.goto(base);
    await expect(desktop.locator(".horizon-card").first()).toBeVisible();
    await desktop
      .getByRole("button", { name: "Find opportunities", exact: true })
      .click();
    await desktop.getByLabel("Search opportunities").fill("incubation");
    await expect(desktop.locator(".opportunity-card")).toHaveCount(1);
    await desktop.locator(".card-title button").first().click();
    await expect(
      desktop.getByRole("heading", { name: "Who can apply?" }),
    ).toBeVisible();
    await expect(desktop.locator("#detail-dialog")).toContainText("18-35");
    const mobile = await browser.newPage({
      ...devices["iPhone 13"],
      defaultBrowserType: undefined,
      deviceScaleFactor: 1,
      reducedMotion: "reduce",
    });
    await mobile.goto(base);
    await expect(mobile.locator(".horizon-card").first()).toBeVisible();
    expect(
      await mobile.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await mobile.getByRole("button", { name: "Open navigation" }).click();
    await expect(mobile.locator("body")).toHaveClass(/header-open/);
  } finally {
    await browser.close();
  }
  console.log(
    JSON.stringify({
      url: base,
      storage: after.storage,
      opportunities: after.stats.total,
      sources: after.stats.sources,
      cron: cron.run?.status || "not due",
      desktop: "passed",
      mobile: "passed",
      management: "passed",
      persistence: "passed",
    }),
  );
})().catch((error) => {
  let message = String(error.message);
  for (const value of Object.values(secrets))
    message = message.split(value).join("[redacted]");
  console.error(message);
  process.exitCode = 1;
});
