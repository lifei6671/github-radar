import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('详情分开展示两种 Star 口径，回补入口只在后台', async ({ page }) => {
  const fixture = await mockRadar(page)
  fixture.setStarHistory(1, {
    days: [{ date: '2026-11-11', count: null, state: 'missing' },
      { date: '2026-11-12', count: 120, state: 'complete' }, { date: '2026-11-13', count: 0, state: 'partial' }],
    breakout: { source: 'github_daily_stars', value: 120, at: Date.parse('2026-11-13'), startAt: Date.parse('2026-11-12'), coveredDays: 6,
      baseline: 0, ratio: 13, absolute: false, minimum: 100, direct: 500, minimumRatio: 2.5, minimumDays: 5, smoothing: 10 },
  })
  await page.goto('/repos/vercel/ai?range=180d#star-trend')
  await expect(page.getByRole('heading', { name: '本地累计 Star 与窗口净增' })).toBeVisible()
  await expect(page.getByRole('img', { name: '累计 Star 趋势，缺测点断开' })).toBeVisible()
  await expect(page.getByRole('img', { name: 'GitHub 历史日 Star 柱状图' })).toBeVisible()
  await expect(page.getByText('GitHub 日 Star 爆发待确认')).toBeVisible()
  await expect(page.getByRole('button', { name: '回补 GitHub 历史' })).toHaveCount(0)
  const extended = page.waitForRequest((r) => r.url().includes('/star-history?days=90'))
  await page.getByRole('combobox', { name: 'GitHub 历史范围' }).selectOption('90')
  await extended
  await session(page)
  await page.goto('/admin/repos/vercel/ai#star-trend')
  const submitted = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/1/star-history'))
  await page.getByRole('button', { name: '回补 GitHub 历史' }).click()
  expect((await submitted).postDataJSON().requestId).toMatch(/^[0-9a-f]{32}$/)
  await page.goto('/repos')
  await page.getByRole('combobox', { name: '爆发信号' }).selectOption('github')
  await expect(page).toHaveURL(/breakout=github/)
  await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
})
