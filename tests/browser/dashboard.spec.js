const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { readFile } = require("node:fs/promises");

const token = "browser-check-token";
const deadline = (days) => {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
};
const fixtures = [
  {
    url: "https://gh.linkedin.com/jobs/view/research-engineer-at-radwave-4484260323",
    organizer: "Radwave Research",
    text: `Research Engineer\nBuild reliable research systems with a global team.\nLocation: Accra, Ghana\nSalary: GHS 8000–12000 per month\nEmployment type: Full-time\nQualifications\nExperience with Go and production systems is required.\nApply now: https://apply.example.org/research-engineer\nApplication deadline: ${deadline(30)}`,
  },
  {
    url: "https://twitter.com/radwave_test/status/1234567890123456789",
    organizer: "Radwave Security",
    text: `CALL FOR APPLICATIONS: Open Source Security Bounty\nBug bounty applications are open for security researchers.\nApply now: https://apply.example.org/security-bounty\nApplication deadline: ${deadline(18)}`,
  },
  {
    url: "https://example.org/radar/green-ventures",
    organizer: "Green Ventures",
    text: `CALL FOR APPLICATIONS: Green Ventures Incubation Program\nApplications are open for young entrepreneurs who are building a more sustainable future through their businesses.\nLocation: Accra, Ghana\nEligibility\n1. Must be between 18-35 years of age\n2. Applicants from all countries may apply.\nBenefits\nMentorship and incubation support.\nApplication\nApply now: https://apply.example.org/green-ventures\nApplication deadline: ${deadline(4)}`,
  },
  {
    url: "https://example.org/radar/open-futures",
    organizer: "Open Futures",
    text: `CALL FOR APPLICATIONS: Open Futures Scholarship\nApplications are open for students who want to continue their studies and build a positive future for their communities.\nEligibility\nStudents with an offer for undergraduate study may apply.\nBenefits\nTuition support of up to $10,000.\nApplication\nApply now: https://apply.example.org/open-futures\nApplication deadline: ${deadline(21)}`,
  },
  {
    url: "https://example.org/radar/community-impact",
    organizer: "Community Impact",
    text: `CALL FOR APPLICATIONS: Community Impact Grant\nApplications are open for community leaders developing projects with meaningful local impact and sustainable results.\nEligibility\nApplicants from all countries may apply.\nBenefits\nProject funding of up to $5,000.\nApplication\nApply now: https://apply.example.org/community-impact\nApplication deadline: ${deadline(12)}`,
  },
];

test.beforeAll(async ({ request }) => {
  for (const fixture of fixtures) {
    const response = await request.post("/api/import", {
      headers: { Authorization: `Bearer ${token}` },
      data: fixture,
    });
    expect(response.ok()).toBeTruthy();
  }
});

async function goTo(page, name, isMobile) {
  if (isMobile)
    await page.getByRole("button", { name: "Open navigation" }).click();
  await page
    .locator(".primary-nav")
    .getByRole("button", { name, exact: false })
    .click();
  if (name === "Discover")
    await page
      .getByRole("button", { name: "Find opportunities", exact: true })
      .click();
}
async function unlock(page) {
  await expect(
    page.getByRole("dialog", { name: "Unlock your workspace." }),
  ).toBeVisible();
  await page.getByLabel("Management token", { exact: true }).fill(token);
  await page
    .locator("#token-dialog")
    .getByRole("button", { name: "Unlock management", exact: true })
    .click();
}

test("discovery, extraction details, search, filters, and persistent shortlist", async ({
  page,
  isMobile,
}) => {
  await page.goto("/");
  await expect(
    page.getByRole("heading", {
      name: /Your next chapter,.*beautifully arranged\./,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Find opportunities", exact: true })
    .click();
  await expect(page.locator(".opportunity-card").first()).toBeVisible();
  await page.locator('.category-tab[data-tab="scholarship"]').click();
  await expect(page.locator(".opportunity-card")).toHaveCount(1);
  await expect(
    page.getByRole("button", { name: "Open Futures Scholarship", exact: true }),
  ).toBeVisible();
  await page.locator('.category-tab[data-tab=""]').click();
  await page.getByLabel("Search opportunities").fill("Green Ventures");
  await expect(page.locator(".opportunity-card")).toHaveCount(1);
  await page
    .getByRole("button", {
      name: "Green Ventures Incubation Program",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("heading", { name: "Who can apply?" }),
  ).toBeVisible();
  await expect(page.locator("#detail-dialog")).toContainText(
    "Must be between 18-35 years of age",
  );
  await expect(
    page.getByRole("link", { name: "Go to application" }),
  ).toHaveAttribute("href", "https://apply.example.org/green-ventures");
  await page.keyboard.press("Escape");
  await page
    .getByRole("button", {
      name: "Save Green Ventures Incubation Program",
      exact: true,
    })
    .click();
  await goTo(page, "Saved opportunities", isMobile);
  await expect(
    page.getByRole("button", {
      name: "Green Ventures Incubation Program",
      exact: true,
    }),
  ).toBeVisible();
  await page.reload();
  await goTo(page, "Saved opportunities", isMobile);
  await expect(page.locator(".opportunity-card")).toHaveCount(1);
  await page
    .getByRole("button", {
      name: "Unsave Green Ventures Incubation Program",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("heading", {
      name: "A little shortlist. A lot of possibility.",
    }),
  ).toBeVisible();
});

test("token-gated manual import preserves provenance and safely displays post text", async ({
  page,
}, testInfo) => {
  await page.goto("/");
  if (testInfo.project.name === "mobile")
    await page.getByRole("button", { name: "Open navigation" }).click();
  await page
    .getByRole("button", { name: "Add opportunity", exact: true })
    .click();
  await page
    .getByLabel("Management token", { exact: true })
    .fill("incorrect-token");
  await page
    .locator("#token-dialog")
    .getByRole("button", { name: "Unlock management", exact: true })
    .click();
  await expect(page.locator("#token-error")).toBeVisible();
  await unlock(page);
  await expect(
    page.getByRole("dialog", { name: /One link\..*A clearer opportunity\./ }),
  ).toBeVisible();
  const title = `Human Centered Fellowship (${testInfo.project.name})`;
  await page
    .getByLabel("Original post or article URL")
    .fill(`https://example.org/radar/human-centered-${testInfo.project.name}`);
  await page.locator("#paste-details summary").click();
  await page
    .getByLabel("Post text", { exact: true })
    .fill(
      `CALL FOR APPLICATIONS: ${title}\nApplications are open for builders who want to create practical technology with meaningful public benefit.\nEligibility\nApplicants from all countries may apply.\n<script>window.__radarXSS=true</script>\nApplication\nApply now: https://apply.example.org/fellowship\nApplication deadline: ${deadline(15)}`,
    );
  await page
    .getByLabel("Organizer", { exact: false })
    .fill("Human Centered Foundation");
  await page.getByRole("button", { name: "Bring into focus" }).click();
  await expect(page.locator("#detail-title")).toHaveText(title);
  await expect(page.locator("#detail-dialog")).toContainText("Text import");
  await page
    .getByText("Read the captured announcement", { exact: true })
    .click();
  await expect(page.locator(".raw-details pre")).toContainText(
    "<script>window.__radarXSS=true</script>",
  );
  expect(await page.evaluate(() => window.__radarXSS)).toBeUndefined();
});

test("sources can be added and paused without losing captured opportunities", async ({
  page,
  isMobile,
}, testInfo) => {
  await page.goto("/");
  await goTo(page, "Sources & updates", isMobile);
  await page.getByRole("button", { name: "Add source", exact: true }).click();
  await unlock(page);
  const name = `Global test feed · ${testInfo.project.name}`;
  await page.getByLabel("Source name", { exact: true }).fill(name);
  await page
    .getByLabel("Public source URL")
    .fill(`https://feeds.example.org/radar-${testInfo.project.name}.xml`);
  await page.getByLabel("Source type", { exact: true }).selectOption("rss");
  await page.getByRole("button", { name: "Add to Radwave" }).click();
  const row = page
    .locator(".source-row")
    .filter({ has: page.getByRole("heading", { name, exact: true }) });
  await expect(row).toBeVisible();
  const toggle = row.getByRole("switch", {
    name: `Track ${name}`,
    exact: true,
  });
  await expect(toggle).toHaveAttribute("aria-checked", "true");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await expect(row).toContainText("Paused");
  await goTo(page, "Discover", isMobile);
  await expect(
    page.getByRole("button", {
      name: "Green Ventures Incubation Program",
      exact: true,
    }),
  ).toBeVisible();
});

test("filtered exports produce usable CSV with original links", async ({
  page,
  isMobile,
}) => {
  await page.goto("/");
  await page
    .getByRole("button", { name: "Find opportunities", exact: true })
    .click();
  await page.getByLabel("Search opportunities").fill("Open Futures");
  await expect(page.locator(".opportunity-card")).toHaveCount(1);
  if (isMobile)
    await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: "Export opportunities" }).click();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: /Spreadsheet CSV/ }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("opportunities.csv");
  const text = await readFile(await download.path(), "utf8");
  expect(text).toContain("Open Futures Scholarship");
  expect(text).toContain("https://example.org/radar/open-futures");
  expect(text).not.toContain("Green Ventures Incubation Program");
});

test("keyboard search, responsive layout, and accessible discovery page", async ({
  page,
  isMobile,
}) => {
  await page.goto("/");
  await expect(page.locator(".horizon-card").first()).toBeVisible();
  await page.keyboard.press("/");
  await expect(page.getByLabel("Search opportunities")).toBeFocused();
  await page.getByRole("button", { name: "List view", exact: true }).click();
  await expect(page.locator("#opportunity-grid")).toHaveClass(/list-mode/);
  await page.getByRole("button", { name: "Grid view", exact: true }).click();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
  if (isMobile) {
    await page.getByRole("button", { name: "Open navigation" }).click();
    await expect(page.locator("body")).toHaveClass(/header-open/);
  }
});

test("globe regions open a focused workspace and keep discovery filters in sync", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator("#scene")).toHaveAttribute(
    "data-scene-ready",
    "true",
    { timeout: 20000 },
  );
  await page.locator('#region-rail [data-region="Worldwide"]').click();
  await expect(page.locator("#workspace")).toBeVisible();
  await expect(page.locator("#region")).toHaveValue("Worldwide");
  await expect(page.locator("#active-filters")).toContainText("Worldwide");
  await page.locator("#filter-options summary").click();
  await page
    .getByLabel("Discovery region", { exact: true })
    .selectOption("Not stated");
  await expect(
    page.locator('#region-rail [data-region="Worldwide"]'),
  ).toHaveAttribute("aria-pressed", "false");
  await page
    .getByRole("button", { name: "Close opportunity workspace" })
    .click();
  await expect(page.locator("#workspace")).toBeHidden();
  await expect(
    page.getByRole("heading", {
      name: /Your next chapter,.*beautifully arranged\./,
    }),
  ).toBeVisible();
});

test("X announcements share Radwave's filters and source management", async ({
  page,
  isMobile,
}, testInfo) => {
  await page.goto("/");
  await page
    .getByRole("button", { name: "Find opportunities", exact: true })
    .click();
  await page.locator('.channel-tab[data-platform="x"]').click();
  await expect(page.locator(".opportunity-card")).toHaveCount(1);
  await expect(page.locator(".source-mark.x-mark")).toBeVisible();
  await page
    .getByRole("button", { name: "Open Source Security Bounty", exact: true })
    .click();
  await expect(page.locator("#detail-title")).toHaveText(
    "Open Source Security Bounty",
  );
  await expect(page.locator(".detail-organizer")).toHaveText(
    "Radwave Security",
  );
  await page.getByRole("button", { name: "Close opportunity details" }).click();
  await goTo(page, "Sources & updates", isMobile);
  await page.getByRole("button", { name: "Add an X watchlist" }).click();
  await unlock(page);
  await page
    .getByLabel("Source name", { exact: true })
    .fill(`X watchlist ${testInfo.project.name}`);
  await page
    .getByLabel("X search query", { exact: true })
    .fill(
      `fellowship apply -is:retweet lang:en #radwave_${testInfo.project.name}`,
    );
  await page.getByRole("button", { name: "Add to Radwave" }).click();
  const row = page.locator(".source-row").filter({
    has: page.getByRole("heading", {
      name: `X watchlist ${testInfo.project.name}`,
      exact: true,
    }),
  });
  await expect(row).toBeVisible();
  await expect(row.getByRole("switch")).toHaveAttribute(
    "aria-checked",
    "false",
  );
  await expect(page.locator("#x-connection")).toContainText("One Radwave");
});

test("job discovery shows salary and newest-first roles, and prepares keyed APIs", async ({
  page,
  isMobile,
}, testInfo) => {
  await page.goto("/");
  await page
    .getByRole("button", { name: "Find opportunities", exact: true })
    .click();
  await page.locator('.category-tab[data-tab="job"]').click();
  await expect(page.locator(".opportunity-card")).toHaveCount(1);
  await expect(page.locator("#sort")).toHaveValue("newest");
  await expect(page.locator(".opportunity-card")).toContainText(
    "GHS 8000–12000 per month",
  );
  await page
    .getByRole("button", { name: "Research Engineer", exact: true })
    .click();
  await expect(page.locator(".job-details")).toContainText("Full-time");
  await expect(page.locator("#detail-dialog")).toContainText(
    "Salary / compensation",
  );
  await page.getByRole("button", { name: "Close opportunity details" }).click();
  await goTo(page, "Sources & updates", isMobile);
  await page
    .getByRole("button", { name: "Add a job source", exact: true })
    .click();
  await unlock(page);
  await page.locator(".verified-source-presets summary").click();
  await page
    .locator("#public-source-presets")
    .getByRole("button", { name: "University of Bath · Careers", exact: true })
    .click();
  await expect(page.locator("#source-kind")).toHaveValue("rss_jobs");
  await expect(page.locator("#source-interval")).toHaveValue("10");
  await page
    .getByLabel("Source type", { exact: true })
    .selectOption("jobs_usajobs");
  const name = `Federal jobs ${testInfo.project.name}`;
  await page.getByLabel("Source name", { exact: true }).fill(name);
  await page
    .getByLabel("Job search keywords", { exact: false })
    .fill(`software ${testInfo.project.name}`);
  await expect(page.locator("#source-enabled")).not.toBeChecked();
  await expect(page.locator("#source-interval")).toHaveValue("10");
  await expect(page.locator("#job-provider-note")).toContainText(
    "USAJOBS_API_KEY",
  );
  await page.getByRole("button", { name: "Add to Radwave" }).click();
  const row = page
    .locator(".source-row")
    .filter({ has: page.getByRole("heading", { name, exact: true }) });
  await expect(row.getByRole("switch")).toHaveAttribute(
    "aria-checked",
    "false",
  );
  await expect(row).toContainText("Cadence: 10 min");
  await expect(row).toContainText("API setup:");
});
