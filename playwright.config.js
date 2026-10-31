const { defineConfig, devices } = require("@playwright/test");

module.exports = defineConfig({
  testDir: "./tests/browser",
  timeout: 60000,
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: "http://127.0.0.1:8091",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    reducedMotion: "reduce",
  },
  projects: [
    {
      name: "desktop",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 1000 },
      },
    },
    {
      name: "mobile",
      use: { ...devices["iPhone 13"], deviceScaleFactor: 1, defaultBrowserType: "chromium" },
    },
  ],
  webServer: {
    command:
      "go run ./cmd/radar serve -addr 127.0.0.1:8091 -db :memory: -seed=false -auto-crawl=false -interval=0",
    url: "http://127.0.0.1:8091/healthz",
    timeout: 120000,
    reuseExistingServer: false,
    env: { RADAR_ADMIN_TOKEN: "browser-check-token" },
  },
});
