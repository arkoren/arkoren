import { defineConfig } from "@playwright/test";

/** Browser checks use an isolated fixture build; no developer server is reused. */
export default defineConfig({
  testDir: "./test/browser",
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  timeout: 60000,
  reporter: "list",
  use: {
    browserName: "chromium",
    channel: process.env.PLAYWRIGHT_CHANNEL || undefined,
    trace: "retain-on-failure",
  },
});
