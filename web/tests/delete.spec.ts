import { test, expect } from "@playwright/test";

// Against the mock API, which mirrors the deletion contract.
test.describe("deleting a database", () => {
  test("asks for the exact name and removes the database", async ({ page }) => {
    await page.goto("/projects/prj_analytics");
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.getByRole("dialog");
    const confirm = dialog.getByRole("button", { name: "Delete database" });
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel("Type analytics to confirm").fill("Analytics");
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel("Type analytics to confirm").fill("analytics");
    await confirm.click();
    await expect(page.getByText("Deletion started")).toBeVisible();
    await expect(page.getByRole("listitem").filter({ hasText: "analytics" }).getByText("Deleting…")).toBeVisible();
    await expect(page.getByRole("listitem").filter({ hasText: "analytics" })).toHaveCount(0, { timeout: 10000 });
  });

  test("a database without a recent backup needs an acknowledgment", async ({ page }) => {
    // blog finishes setting up a few seconds after the mock starts and has no backups.
    await page.goto("/projects/prj_blog");
    await expect(page.getByRole("button", { name: "Delete" })).toBeVisible({ timeout: 15000 });
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Type blog to confirm").fill("blog");
    const confirm = dialog.getByRole("button", { name: "Delete database" });
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel("I accept losing this data").check();
    await expect(confirm).toBeEnabled();
    await dialog.getByRole("button", { name: "Cancel" }).click();
  });
});
