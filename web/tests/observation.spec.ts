import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('观察池演算先展示容量，试运行参数保存后才提交', async ({ page }) => {
  await mockRadar(page)
  await session(page)
  const scheduler = {
    observationPool: true, maxHot: 30, maxActive: 150, maxNormal: 400, maxCooling: 1000,
    maxMonitoredRepos: 1580, intervalsMinutes: { S: 60, A: 240, B: 1440, C: 4320 },
  }
  await page.route('**/api/v1/system/settings', (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    return route.fulfill({ json: { revision: 1, settings: { scheduler: { ...scheduler, observationPool: false } } } })
  })
  await page.route('**/api/v1/system/observation-replay', (route) => route.fulfill({
    json: { report: { legacyActiveGrades: { A: 5000 }, trial: { S: 30, A: 150, B: 400, C: 1000 } }, scheduler },
  }))
  await page.goto('/admin/runs?tab=runtime&group=scheduler')
  const enabled = page.getByRole('checkbox', { name: '启用观察池' })
  await expect(enabled).not.toBeChecked()
  await page.getByRole('button', { name: '演算观察池' }).click()
  await expect(page.getByRole('status')).toContainText('试运行 1,580 个活跃')
  await expect(enabled).not.toBeChecked()
  await page.getByRole('button', { name: '填入试运行参数' }).click()
  await expect(enabled).toBeChecked()
  const save = page.waitForRequest((request) => request.method() === 'PUT' && request.url().endsWith('/system/settings'))
  await page.getByRole('button', { name: '保存配置' }).click()
  expect((await save).postDataJSON().settings.scheduler).toEqual(scheduler)
})

test('仓库后台详情展示档位变更理由和实测净增', async ({ page }) => {
  const fixture = await mockRadar(page)
  await session(page)
  fixture.setObservationHistory(1, [
    { id: 1, at: '2026-11-13T02:00:00Z', fromState: 'candidate', fromGrade: 'A', toState: 'active', toGrade: 'B',
      reason: 'exact_growth_15', evidence: { stars: 48, exact24h: 20 } },
  ])
  await page.goto('/admin/repos/vercel/ai')
  await expect(page.getByRole('list', { name: '观察池变更' })).toContainText('24h 实测净增 20')
})
