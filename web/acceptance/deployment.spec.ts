import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

const origin = process.env.RADAR_ACCEPTANCE_ORIGIN
const admin = process.env.RADAR_ACCEPTANCE_ADMIN
const backupDate = process.env.RADAR_ACCEPTANCE_BACKUP_DATE

test.skip(!origin || !admin, '需启动独立的验收实例')

async function signIn(page: Page) {
  await page.getByLabel('管理员令牌').fill(admin!)
  await page.getByRole('button', { name: '登录', exact: true }).click()
}

test('云端首次安装从真实 R2 恢复', async ({ page }) => {
  test.skip(!backupDate || !process.env.RADAR_ACCEPTANCE_R2_SECRET_ACCESS_KEY, '需真实 R2 验收连接')
  await page.goto('/setup')
  await signIn(page)
  await page.getByRole('button', { name: '配置 R2 连接' }).click()
  await page.getByLabel('R2 端点').fill(process.env.RADAR_ACCEPTANCE_R2_ENDPOINT!)
  await page.getByLabel('存储桶').fill(process.env.RADAR_ACCEPTANCE_R2_BUCKET!)
  await page.getByLabel('备份前缀').fill(process.env.RADAR_ACCEPTANCE_R2_PREFIX!)
  await page.getByLabel('Access Key ID').fill(process.env.RADAR_ACCEPTANCE_R2_ACCESS_KEY_ID!)
  await page.getByLabel('Secret Access Key').fill(process.env.RADAR_ACCEPTANCE_R2_SECRET_ACCESS_KEY!)
  await page.getByRole('button', { name: '连接并查看备份' }).click()
  await expect(page.getByRole('radio', { name: '选择 ' + backupDate })).toBeChecked({ timeout: 35_000 })
  await page.getByRole('button', { name: '恢复 ' + backupDate }).click()
  await expect(page.getByText('备份后的数据会丢失，历史 AI 调用可能已计费。')).toBeVisible()
  await page.getByRole('button', { name: '确认恢复' }).click()
  await expect(page.getByRole('heading', { name: '发现', exact: true })).toBeVisible({ timeout: 35_000 })
  await page.goto('/repos/fixture/radar-workflow')
  await expect(page.getByRole('heading', { name: 'fixture/radar-workflow', exact: true })).toBeVisible()
})

test('云端后台恢复真实 R2 备份', async ({ page }) => {
  test.skip(!backupDate, '需真实 R2 备份')
  await page.goto('/system?tab=backup')
  await signIn(page)
  await page.getByRole('button', { name: '恢复 ' + backupDate }).click()
  const submitted = page.waitForResponse((response) => response.url().endsWith('/api/v1/system/backup/restore') &&
    response.request().method() === 'POST')
  await page.getByRole('button', { name: '确认恢复' }).click()
  const response = await submitted
  expect(response.status()).toBe(202)
  const { statusUrl } = await response.json()
  await expect.poll(async () => {
    const result = await page.request.get(statusUrl, { headers: { Authorization: 'Bearer ' + admin } })
    return (await result.json()).status
  }, { timeout: 35_000 }).toBe('completed')
  await expect(page.getByText('恢复完成。', { exact: true })).toBeVisible({ timeout: 35_000 })
  await expect(page.getByRole('button', { name: '启用自动摘要' })).toBeVisible()
})

test('Compose 首次登录、深链接和管理页', async ({ page }) => {
  await page.goto('/admin/repos/fixture/radar-workflow')
  await signIn(page)
  await expect(page).toHaveURL(/\/admin\/setup\?return=/)
  await page.getByRole('button', { name: '初始化空库' }).click()
  await expect(page).toHaveURL(/\/admin\/repos\/fixture\/radar-workflow$/)
  await expect(page.getByRole('alert')).toHaveText('记录不存在或已过保留期。')
  await page.goto('/system?tab=backup')
  await expect(page.getByText('R2 待配置', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '立即备份' })).toBeDisabled()
})

test('Compose 重建后移动导航与配置可读', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/categories')
  await expect(page.getByRole('heading', { name: '项目分类', exact: true })).toBeVisible()
  await expect(page.getByText('验收专用主题', { exact: true })).toBeVisible()
  await expect(page.getByRole('navigation', { name: '手机导航' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.getByRole('button', { name: '切换深色主题' }).click()
  await page.reload()
  await expect(page.locator('html')).toHaveClass('dark')
  await expect(page.getByText('验收专用主题', { exact: true })).toBeVisible()
})

test('业务链路实际采集数据、聚合、摘要和选题可见', async ({ page }) => {
  await page.goto('/repos/fixture/radar-workflow')
  await expect(page.getByRole('heading', { name: 'fixture/radar-workflow', exact: true })).toBeVisible()
  await expect(page.getByText('+250', { exact: true })).toBeVisible()
  await expect(page.getByText(/第2版。/)).toBeVisible()
  await page.getByRole('button', { name: '180 天', exact: true }).click()
  await expect(page.getByText('日聚合', { exact: true })).toBeVisible()
  await expect(page.getByRole('img', { name: '累计 Star 趋势，缺测点断开' }).locator('canvas')).toBeVisible()
  await page.goto('/admin/topics')
  await signIn(page)
  await page.getByRole('button', { name: '已采用', exact: true }).click()
  await expect(page.getByText('反馈：验收反馈')).toBeVisible()
})
