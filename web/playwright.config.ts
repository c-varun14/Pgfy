import { defineConfig } from "@playwright/test";
const port = process.env.PGFY_E2E_PORT || "8080";
const mockPort = process.env.PGFY_MOCK_PORT || "8081";
const launchOptions = {
  executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE,
  // Keep the required tunnel origin while allowing an isolated local fixture.
  args: port === "8080" ? [] : [`--host-rules=MAP 127.0.0.1:8080 127.0.0.1:${port}`],
};
export default defineConfig({
  testDir: "./tests",
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  use: { trace: "retain-on-failure" },
  projects: [
    // The real binary against a fresh installation: setup, login, degradation.
    {
      name: "server",
      testIgnore: /(backups|access|host|signin)\.spec\.ts/,
      use: { baseURL: "http://127.0.0.1:8080", launchOptions },
    },
    // The mock API, which mirrors the same contract with backups present, so
    // the backup states can be exercised without a bucket or a database.
    {
      name: "mock",
      testMatch: /(backups|access|host|signin)\.spec\.ts/,
      use: {
        baseURL: `http://127.0.0.1:${mockPort}`,
        launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE },
      },
    },
  ],
  webServer: [
    {
      command: "python3 ../scripts/e2e-server.py",
      url: `http://127.0.0.1:${port}/health/live`,
      reuseExistingServer: false,
      timeout: 30000,
    },
    {
      command: `pnpm dev:mock --port ${mockPort} --strictPort`,
      url: `http://127.0.0.1:${mockPort}/`,
      reuseExistingServer: false,
      timeout: 60000,
    },
  ],
});
