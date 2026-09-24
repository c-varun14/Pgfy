import { test, expect } from "@playwright/test";

// Against the mock API: the host report states the Settings page must show.
test.describe("host status", () => {
  test.afterEach(async ({ request }) => { await request.put("/api/v1/__mock/host", { data: { scenario: "ok" } }); });

  test("a healthy host shows disks, clock and the certificate expiry", async ({ page }) => {
    await page.goto("/settings");
    const card = page.locator("section.card").filter({ has: page.getByRole("heading", { name: "Host", exact: true }) });
    await expect(card.getByText("Healthy", { exact: true })).toBeVisible();
    await expect(page.getByText("PostgreSQL data")).toBeVisible();
    await expect(page.getByText("Synchronised")).toBeVisible();
    await expect(page.getByText(/Certificate expires/)).toBeVisible();
  });

  test("low disk, an expiring certificate and a failed delivery are called out", async ({ page, request }) => {
    await request.put("/api/v1/__mock/host", { data: { scenario: "low-disk" } });
    await page.goto("/settings");
    await expect(page.getByText("Needs attention")).toBeVisible();
    await expect(page.getByText("Low", { exact: true }).first()).toBeVisible();
    await request.put("/api/v1/__mock/host", { data: { scenario: "expiring" } });
    await page.reload();
    await expect(page.getByText("The certificate expires within 14 days")).toBeVisible();
    await expect(page.getByText(/The last certificate delivery to PostgreSQL failed/)).toBeVisible();
  });

  test("a silent host is not shown as healthy", async ({ page, request }) => {
    await request.put("/api/v1/__mock/host", { data: { scenario: "stale" } });
    await page.goto("/settings");
    await expect(page.getByText("Report stale")).toBeVisible();
    await request.put("/api/v1/__mock/host", { data: { scenario: "missing" } });
    await page.reload();
    await expect(page.getByText("The host has not reported disk and clock status")).toBeVisible();
  });
});
