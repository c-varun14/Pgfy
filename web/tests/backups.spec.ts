import { test, expect } from "@playwright/test";
import type { BucketBackup, Discovery } from "../src/api";

// These run against the mock API (`pnpm dev:mock`), which mirrors the real
// contract, so the states that changed in the backup work are exercised in a
// browser without needing a server, a bucket or a database.
test.describe("backups", () => {
  test("older recovered backups can be paged and selected for restore", async ({ page }) => {
    const db = "app_recovered";
    const backups: BucketBackup[] = Array.from({ length: 45 }, (_, index) => {
      const takenAt = 1_800_000_000 - index * 86400;
      const stamp = new Date(takenAt * 1000).toISOString().replace(/[-:]/g, "").replace(/\.\d{3}Z$/, "Z");
      const directory = `pgfy/backups/${db}/${stamp}`;
      return { db_name: db, manifest_key: `${directory}/manifest.json`, archive_key: `${directory}/archive.dump`, taken_at: takenAt, state: "complete", installation_id: "recovered-installation", project_id: "prj_recovered", project_name: "recovered", postgres_version: "18.6", table_count: 2, size_bytes: 1000 };
    });
    const cursors: string[] = [];
    let pageAttempts = 0;
    let busy = false;
    let discoveryRequests = 0;
    await page.route("**/api/v1/recovery/backups*", async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.has("db")) {
        expect(url.searchParams.get("db")).toBe(db);
        const before = url.searchParams.get("before")!;
        cursors.push(before);
        // A failed request must leave a usable retry on the same card.
        if (++pageAttempts === 1) {
          await route.fulfill({ status: 503, json: { error: { message: "Older backups could not be read." } } });
          return;
        }
        const remaining = backups.filter((backup) => backup.manifest_key.split("/").at(-2)! < before);
        await route.fulfill({ json: { db_name: db, backups: remaining.slice(0, 20), has_more: remaining.length > 20 } });
        return;
      }
      discoveryRequests++;
      const discovery: Discovery = { state: "ok", installation_id: "demo-installation", reconciled_at: 1_800_000_000, busy, databases: [{ db_name: db, project_id: "prj_recovered", project_name: "recovered", installation_id: "recovered-installation", mixed: false, foreign: true, newest_at: backups[0].taken_at, count: backups.length, total_bytes: 20000, has_more: true, manifest_only: 0, damaged: 0, reconciled_at: 1_800_000_000, backups: backups.slice(0, 20) }] };
      await route.fulfill({ json: discovery });
    });
    await page.goto("/backups");
    const card = page.getByLabel("Backups from other servers").locator("details").filter({ hasText: "recovered" });
    await card.locator("summary").click();
    await expect(card.locator(".backup-row")).toHaveCount(20);
    await card.getByRole("button", { name: "Load older backups" }).click();
    await expect(card.getByRole("alert")).toHaveText("Older backups could not be read.");
    await expect(card.locator(".backup-row")).toHaveCount(20);
    await card.getByRole("button", { name: "Load older backups" }).click();
    await expect(card.locator(".backup-row")).toHaveCount(40);
    await expect(card.getByRole("alert")).toHaveCount(0);
    // A concurrent backup starts polling; already loaded pages must survive it.
    busy = true;
    // shop, not the first card: the mock is shared, and a new blog backup would change the deletion tests.
    await page.getByLabel("Backups by database").locator("details").filter({ hasText: "app_shop" }).getByRole("button", { name: "Back up now" }).click();
    const previousRequests = discoveryRequests;
    await expect.poll(() => discoveryRequests).toBeGreaterThan(previousRequests);
    await expect(card.locator(".backup-row")).toHaveCount(40);
    busy = false;
    await card.getByRole("button", { name: "Load older backups" }).click();
    await expect(card.locator(".backup-row")).toHaveCount(45);
    await expect(card.getByRole("button", { name: "Load older backups" })).toHaveCount(0);
    expect(cursors).toEqual([backups[19], backups[19], backups[39]].map((backup) => backup.manifest_key.split("/").at(-2)));
    await card.locator(".backup-row").last().getByRole("button", { name: "Restore", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByRole("dialog").getByRole("button", { name: "Restore", exact: true })).toBeEnabled();
  });

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

  test("daily backups can start at a preferred hour", async ({ page }) => {
    await page.goto("/settings");
    await page.getByLabel("Back up each database").selectOption("24");
    const hour = page.getByLabel("Start daily backups at");
    await expect(hour).toBeEnabled();
    await hour.selectOption("2");
    await expect(page.getByText(/start at or after this hour/)).toBeVisible();
    await page.reload();
    await expect(page.getByLabel("Start daily backups at")).toHaveValue("2");
    await page.getByLabel("Back up each database").selectOption("6");
    await expect(page.getByLabel("Start daily backups at")).toBeDisabled();
    await page.getByLabel("Back up each database").selectOption("24");
    await page.getByLabel("Start daily backups at").selectOption("-1");
    await expect(page.getByText(/start at or after this hour/)).toBeHidden();
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
  test("a folder holding only incomplete or unreadable backups keeps its warnings", async ({ page }) => {
    const empty = { project_id: "", project_name: "", installation_id: "", mixed: false, foreign: false, newest_at: 0, count: 0, total_bytes: 0, has_more: false, reconciled_at: 1_800_000_000, backups: [] };
    await page.route("**/api/v1/recovery/backups*", async (route) => {
      const discovery: Discovery = { state: "ok", installation_id: "demo-installation", reconciled_at: 1_800_000_000, busy: false, databases: [
        { ...empty, db_name: "app_blog", manifest_only: 2, damaged: 0 },
        { ...empty, db_name: "app_elsewhere", manifest_only: 0, damaged: 1 },
      ] };
      await route.fulfill({ json: discovery });
    });
    await page.goto("/backups");
    // The local project's card carries its folder's warning.
    const blog = page.getByLabel("Backups by database").locator("details").filter({ hasText: "app_blog" });
    await expect(blog.getByText("2 incomplete")).toBeVisible();
    // A folder no project here claims is still shown rather than dropped.
    const elsewhere = page.getByLabel("Backups from other servers").locator("details").filter({ hasText: "app_elsewhere" });
    await expect(elsewhere.getByText("1 unreadable")).toBeVisible();
  });

  test("a first-time storage setup submits the protection choice it shows", async ({ page }) => {
    let submitted: unknown;
    await page.route("**/api/v1/settings/storage", async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({ json: { configured: false, settings: { endpoint: "https://s3.us-east-1.amazonaws.com", region: "us-east-1", bucket: "", prefix: "pgfy", access_key: "", secret_key: "", session_token: "", path_style: false, private_endpoint: false, bucket_protection: "" } } });
        return;
      }
      submitted = route.request().postDataJSON().bucket_protection;
      await route.fulfill({ status: 400, json: { error: { code: "invalid_storage", message: "stop here" } } });
    });
    await page.goto("/backups");
    await expect(page.getByLabel(/Bucket versioning is on/)).toBeChecked();
    await page.getByLabel("Bucket", { exact: true }).fill("my-backups");
    await page.getByLabel("Access key ID").fill("key");
    await page.getByLabel("Secret access key").fill("secret");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => submitted).toBe("versioning");
  });
});
