import { defineConfig, devices } from "@playwright/test";

// Access process.env without @types/node — use globalThis
function getEnv(key: string): string | undefined {
  try {
    // @ts-ignore — process is available in Node runtime
    return globalThis.process?.env?.[key];
  } catch {
    return undefined;
  }
}

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  use: {
    baseURL: "http://localhost:1420",
    headless: true,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  retries: getEnv("CI") ? 2 : 0,
  webServer: {
    command: "npm run dev",
    url: "http://localhost:1420",
    reuseExistingServer: true,
    timeout: 30_000,
  },
});
