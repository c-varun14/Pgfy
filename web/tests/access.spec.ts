import { test, expect } from "@playwright/test";

// Against the mock API, which mirrors the access and capacity contract.
test.describe("access and capacity", () => {
  test("a database open to the internet says so on its card and its page", async ({ page }) => {
    await page.goto("/");
    const card = page.getByRole("listitem").filter({ hasText: "shop" });
    await expect(card.getByText("Open to the internet")).toBeVisible();
    await card.click();
    await expect(page.getByText("Open to the internet").first()).toBeVisible();
  });

  test("limits refuse a zero temporary-file limit and save a valid change", async ({ page }) => {
    await page.goto("/projects/prj_shop?tab=access");
    await expect(page.getByRole("heading", { name: "Limits" })).toBeVisible();
    await page.getByLabel("Temporary files").fill("0");
    await page.getByRole("button", { name: "Save limits" }).click();
    await expect(page.getByText("temporary file limit must be unlimited")).toBeVisible();
    await page.getByLabel("Temporary files").fill("-1");
    await page.getByLabel("Connections").fill("40");
    await page.getByRole("button", { name: "Save limits" }).click();
    await expect(page.getByText("Limits saved")).toBeVisible();
  });

  test("changing the password shows the new connection URL once", async ({ page }) => {
    await page.goto("/projects/prj_shop?tab=connect");
    await page.getByRole("button", { name: "Change password…" }).click();
    await page.getByRole("button", { name: "Change password", exact: true }).click();
    await expect(page.getByText("Every session using the old password was closed")).toBeVisible();
    await expect(page.getByText(/rotated-password-\d+/).first()).toBeVisible();
    await page.getByRole("button", { name: "Done" }).click();
    await expect(page.getByText("Every session using the old password was closed")).toHaveCount(0);
  });

  test("settings show the connection budget and the reserved slots", async ({ page }) => {
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Connections" })).toBeVisible();
    await expect(page.getByText("21 of 137 in use")).toBeVisible();
    await expect(page.getByText(/10 are reserved for the dashboard/)).toBeVisible();
    await expect(page.getByRole("cell", { name: "pgfy_mgmt" })).toBeVisible();
  });
});
