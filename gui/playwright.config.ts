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
    // Overridable per-run (worktree dev server on another port) — same
    // getEnv posture as the CI retries knob below.
    baseURL: getEnv("PLAYWRIGHT_BASE_URL") ?? "http://localhost:1420",
    headless: true,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  retries: getEnv("CI") ? 2 : 0,
  webServer: {
    command: "npm run dev",
    // Probe the URL the tests will actually hit: with PLAYWRIGHT_BASE_URL
    // set, an already-running override server is reused, otherwise the
    // stock 1420 check stands.
    url: getEnv("PLAYWRIGHT_BASE_URL") ?? "http://localhost:1420",
    reuseExistingServer: true,
    timeout: 30_000,
  },
});
