import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('发现规则显示年龄配置和主扫补扫记录', async ({ page }) => {
  await mockRadar(page)
  await session(page)
  await page.route('**/api/v1/system/discovery-runs?*', async (route) => {
    const plannedAt = '2026-11-13T04:00:00Z'
    await route.fulfill({ json: {
      alerts: [{ sourceType: 'search', sourceKey: 'coding', reason: 'quota_backlog' }],
      items: [{ id: 81, sourceType: 'search', sourceKey: 'coding', pass: 'primary', plannedAt,
        createdFrom: '2026-11-10T00:00:00Z', createdUntil: '2026-11-13T23:59:59Z',
        complete: false, status: 'failed', upstreamTotal: 1076, completedShards: 1, plannedShards: 2, uniqueRepos: 50, newRepos: 13,
        delayMs: 120000, errorCode: 'github_local_budget' },
      { id: 82, sourceType: 'search', sourceKey: 'coding', pass: 'backfill', plannedAt,
        createdFrom: '2026-11-09T00:00:00Z', createdUntil: '2026-11-12T23:59:59Z',
        complete: true, status: 'succeeded', upstreamTotal: 50, completedShards: 1, plannedShards: 1, uniqueRepos: 50, newRepos: 1,
        delayMs: 120000, errorCode: null }],
    } })
  })
  await page.goto('/admin/runs?tab=rules')
  const history = page.getByRole('region', { name: '规则运行记录', exact: true })
  await expect(history.getByText('积压超过 15 分钟，当前受配额限制', { exact: false })).toBeVisible()
  await expect(history.getByRole('cell', { name: '主扫描' })).toBeVisible()
  await expect(history.getByRole('cell', { name: '补扫' })).toBeVisible()
  await expect(history.getByRole('cell', { name: '2026-11-09 至 2026-11-12' })).toBeVisible()
  await expect(history.getByText('github_local_budget', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '编辑 coding', exact: true }).click()
  await expect(page.getByRole('combobox', { name: '仓库年龄' })).toHaveValue('')
  await page.getByRole('combobox', { name: '仓库年龄' }).selectOption('4-14')
  await expect(page.getByRole('combobox', { name: '仓库年龄' })).toHaveValue('4-14')
})
