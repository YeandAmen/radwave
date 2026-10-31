// Development utility: capture an actual running radar, without changing data.
const { chromium, devices } = require("@playwright/test");
const { mkdir } = require("node:fs/promises");
const path = require("node:path");

(async () => {
  const base = process.argv[2] || "http://127.0.0.1:8080";
  const output = path.resolve(__dirname, "../docs");
  await mkdir(output, { recursive: true });
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1240 },
      deviceScaleFactor: 1,
    });
    await page.goto(base);
    await page.locator(".opportunity-card:not(.skeleton)").first().waitFor();
    await page.getByLabel("Sort opportunities").selectOption("popular");
    await page.waitForFunction(
      () =>
        document
          .querySelector("#opportunity-grid")
          .getAttribute("aria-busy") === "false",
    );
    await page.screenshot({ path: path.join(output, "dashboard-desktop.png") });
    const mobile = await browser.newPage({
      ...devices["iPhone 13"],
      defaultBrowserType: undefined,
    });
    await mobile.goto(base);
    await mobile.locator(".opportunity-card:not(.skeleton)").first().waitFor();
    await mobile.getByLabel("Sort opportunities").selectOption("popular");
    await mobile.waitForFunction(
      () =>
        document
          .querySelector("#opportunity-grid")
          .getAttribute("aria-busy") === "false",
    );
    await mobile.screenshot({
      path: path.join(output, "dashboard-mobile.png"),
    });
    console.log("Captured live desktop and mobile dashboards in docs/.");
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
