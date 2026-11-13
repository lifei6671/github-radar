import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('详情只展示本地 Star 趋势，GitHub 回补入口只在后台', async ({ page }) => {
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
  await expect(page.getByRole('heading', { name: 'GitHub 历史日 Star' })).toHaveCount(0)
  await expect(page.getByRole('img', { name: 'GitHub 历史日 Star 柱状图' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '回补 GitHub 历史' })).toHaveCount(0)
  await session(page)
  await page.goto('/admin/repos/vercel/ai#star-trend')
  const submitted = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/1/star-history'))
  await page.getByRole('button', { name: '回补 GitHub 历史' }).click()
  expect((await submitted).postDataJSON().requestId).toMatch(/^[0-9a-f]{32}$/)
  const publicList = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/v1/public/repos')
  await page.goto('/repos?breakout=github')
  expect(new URL((await publicList).url()).searchParams.has('breakout')).toBe(false)
  await expect(page.getByRole('combobox', { name: '爆发信号' })).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()

  await page.goto('/admin/repos')
  await page.getByRole('button', { name: '筛选', exact: true }).click()
  const filters = page.getByRole('form', { name: '筛选项目' })
  await filters.getByRole('combobox', { name: '爆发信号' }).selectOption('github')
  await filters.getByRole('button', { name: '应用筛选' }).click()
  await expect(page).toHaveURL(/breakout=github/)
  await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
})
