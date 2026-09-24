import { test, expect } from "@playwright/test";

// Against the mock API, which follows the HTTPS-mode steps: a password, then a
// code; or first a key to enrol. The mock accepts 123456 as the code.
test.describe("sign-in with a second factor", () => {
  test.beforeEach(async ({ request }) => { await request.post("/api/v1/auth/logout", { data: {} }); });

  test("a password step asks for a code, and a wrong code says so", async ({ page }) => {
    await page.goto("/");
    await page.getByLabel("Email address").fill("admin@example.com");
    await page.getByLabel("Password").fill("a sufficiently long passphrase");
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.getByText("Enter the 6-digit code")).toBeVisible();
    await page.getByLabel("Code").fill("000000");
    await page.getByRole("button", { name: "Verify" }).click();
    await expect(page.getByText("That code is not right")).toBeVisible();
    await page.getByLabel("Code").fill("123456");
    await page.getByRole("button", { name: "Verify" }).click();
    await expect(page.getByRole("heading", { name: "Databases" })).toBeVisible();
  });

  test("a reset shows a key to enrol, which survives a reload", async ({ page }) => {
    await page.goto("/");
    await page.getByRole("button", { name: "Reset access" }).click();
    await expect(page.getByText("sudo pgfyctl reset-admin")).toBeVisible();
    await page.getByLabel("Reset token").fill("x".repeat(43));
    await page.getByLabel("New password").fill("another long enough passphrase");
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page.getByLabel("Authenticator key")).toHaveText("JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP");
    await page.reload();
    await expect(page.getByLabel("Authenticator key")).toBeVisible();
    await page.getByLabel("Code").fill("123456");
    await page.getByRole("button", { name: "Confirm and continue" }).click();
    await expect(page.getByRole("heading", { name: "Databases" })).toBeVisible();
  });
});
