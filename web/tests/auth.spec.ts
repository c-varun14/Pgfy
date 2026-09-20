import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";

test("initial setup, PostgreSQL degradation, settings, logout, and login", async ({
  page,
}) => {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Create your admin account" }),
  ).toBeVisible();
  await page.getByLabel("Setup token").fill("invalid");
  await page.getByLabel("Email address").fill("admin@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("a sufficiently long passphrase");
  await page.getByRole("button", { name: "Create administrator" }).click();
  await expect(page.getByRole("alert")).toContainText("valid email");
  await page
    .getByLabel("Setup token")
    .fill(readFileSync("../.cache/e2e-token", "utf8").trim());
  await page.getByRole("button", { name: "Create administrator" }).click();
  await expect(
    page.getByRole("heading", { name: "Databases", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Your server needs attention" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "No databases yet" }),
  ).toBeVisible();
  // Without PostgreSQL the fixture must refuse creation honestly instead of queueing it.
  await page.locator("header").getByRole("button", { name: "New database" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Shop");
  await page.getByRole("button", { name: "Create database" }).click();
  await expect(page.getByRole("alert")).toContainText("unavailable");
  await page.getByRole("button", { name: "Close" }).click();
  await page.getByRole("button", { name: "Backups", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Backups", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Backup storage", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("unavailable");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Settings", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Read-only", { exact: true })).toHaveCount(0);
  await expect(page.getByText("SSH tunnel only", { exact: true })).toBeVisible();
  await page.goto("/settings");
  await expect(
    page.getByRole("heading", { name: "Settings", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Databases", exact: true }).click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(
    page.getByRole("heading", { name: "Sign in", exact: true }),
  ).toBeVisible();
  await page.getByLabel("Email address").fill("admin@example.com");
  await page.getByLabel("Password", { exact: true }).fill("incorrect password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("incorrect");
  await page
    .getByLabel("Password", { exact: true })
    .fill("a sufficiently long passphrase");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Databases", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Databases", exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("heading", { name: "Databases", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "test-results/overview-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({
    path: "test-results/overview-desktop.png",
    fullPage: true,
  });
});
