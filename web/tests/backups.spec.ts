import { test, expect } from "@playwright/test";

// These run against the mock API (`pnpm dev:mock`), which mirrors the real
// contract, so the states that changed in the backup work are exercised in a
// browser without needing a server, a bucket or a database.
test.describe("backups", () => {
  test("discovery groups by database and shows the recoverable age", async ({ page }) => {
    await page.goto("/backups");
    await expect(page.getByRole("heading", { name: "Backups", exact: true })).toBeVisible();
    const cards = page.getByLabel("Backups by database");
    await expect(cards.getByText("shop", { exact: true }).first()).toBeVisible();
    await expect(cards.getByText("Newest recoverable backup").first()).toBeVisible();
    // A database with no backups still gets a card: those need attention most.
    await expect(cards.getByText("app_blog", { exact: true })).toBeVisible();
    // Another server's backups stay in their own section and are never merged.
    await expect(page.getByRole("heading", { name: "From other servers" })).toBeVisible();
    await expect(page.getByLabel("Backups from other servers").getByText("From another server").first()).toBeVisible();
  });

  test("a bucket that keeps no versions is refused, and the choice is offered", async ({ page }) => {
    await page.goto("/backups");
    await page.getByRole("button", { name: "Edit storage" }).click();
    await expect(page.getByText("Deleted backups")).toBeVisible();
    await expect(page.getByLabel(/Bucket versioning is on/)).toBeChecked();
    await page.getByRole("button", { name: "Test storage" }).click();
    await expect(page.getByText("protection")).toBeVisible();
  });

  test("the backup target is a setting, and says it is a target", async ({ page }) => {
    await page.goto("/settings");
    const interval = page.getByLabel("Back up each database");
    await expect(interval).toHaveValue("24");
    await interval.selectOption("6");
    await expect(page.getByText(/A target, not a guarantee/)).toBeVisible();
    await expect(page.getByText(/14 daily and 8 weekly/)).toBeVisible();
    await page.reload();
    await expect(page.getByLabel("Back up each database")).toHaveValue("6");
  });

  test("a restore reports what it verified", async ({ page }) => {
    await page.goto("/backups");
    await page.getByRole("button", { name: "Restore latest" }).first().click();
    const dialog = page.getByRole("dialog");
    const start = dialog.getByRole("button", { name: "Restore", exact: true });
    await expect(start).toBeEnabled();
    await start.click();
    await expect(dialog.getByText("You can close this — it continues on the server.")).toBeVisible();
    // "Verified" is claimed only when every check the backup supports was made.
    await expect(dialog.getByText("Restored and verified against the baselines recorded at backup time.")).toBeVisible({ timeout: 30000 });
    await expect(dialog.getByRole("button", { name: "Open database" })).toBeVisible();
  });
});
