import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './browser-test',
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    ...(process.env.OSANWE_BROWSER_CHANNEL ? { channel: process.env.OSANWE_BROWSER_CHANNEL } : {}),
    baseURL: 'http://127.0.0.1:3100',
    trace: 'retain-on-failure',
  },
  projects: [
    // Use the installed engine's real user agent, not a preset for another version.
    { name: 'chromium', use: { viewport: { width: 1280, height: 720 } } },
  ],
  webServer: {
    command: 'npm run dev -- --hostname 127.0.0.1 --port 3100',
    url: 'http://127.0.0.1:3100/client',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
