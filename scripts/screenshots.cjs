// Capture Radwave's live globe and workspace without changing source data.
const { chromium, devices } = require("@playwright/test");
const { mkdir } = require("node:fs/promises");
const path = require("node:path");

(async () => {
  const base = process.argv[2] || "https://radwave.vercel.app";
  const output = path.resolve(__dirname, "../docs");
  await mkdir(output, { recursive: true });
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
      deviceScaleFactor: 1,
      reducedMotion: "reduce",
    });
    await page.goto(base);
    await page.locator(".horizon-card").first().waitFor();
    await page.waitForFunction(
      () => document.getElementById("scene").dataset.sceneReady,
    );
    await page.screenshot({ path: path.join(output, "dashboard-desktop.png") });
    await page
      .getByRole("button", { name: "Find opportunities", exact: true })
      .click();
    await page.screenshot({ path: path.join(output, "workspace-desktop.png") });
    const mobile = await browser.newPage({
      ...devices["iPhone 13"],
      defaultBrowserType: undefined,
      reducedMotion: "reduce",
      deviceScaleFactor: 1,
    });
    await mobile.goto(base);
    await mobile.locator(".horizon-card").first().waitFor();
    await mobile.waitForFunction(
      () => document.getElementById("scene").dataset.sceneReady,
    );
    await mobile.screenshot({
      path: path.join(output, "dashboard-mobile.png"),
      fullPage: true,
    });
    await mobile
      .getByRole("button", { name: "Find opportunities", exact: true })
      .click();
    await mobile.screenshot({
      path: path.join(output, "workspace-mobile.png"),
    });
    console.log("Captured live desktop and mobile dashboards in docs/.");
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
