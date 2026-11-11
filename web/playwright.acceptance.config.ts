import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './acceptance',
  workers: 1,
  outputDir: process.env.RADAR_ACCEPTANCE_RESULTS_DIR ?? './test-results/acceptance',
  timeout: 60_000,
  use: {
    baseURL: process.env.RADAR_ACCEPTANCE_ORIGIN,
    viewport: { width: 1440, height: 900 },
    // 真实 R2 登录表单含密钥，验收运行禁用浏览器记录。
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
})
