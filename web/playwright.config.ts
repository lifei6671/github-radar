import { defineConfig } from '@playwright/test'

const server = process.env.RADAR_TEST_ORIGIN

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  workers: 3,
  use: {
    baseURL: server ?? 'http://127.0.0.1:4173',
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: server ? undefined : {
    command: 'npm run dev -- --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
  },
})
