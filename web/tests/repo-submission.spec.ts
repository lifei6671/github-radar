import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('管理员提交仓库，查看逐行结果和来源', async ({ page }) => {
  const fixture = await mockRadar(page)
  await session(page)
  const source = 'https://example.test/community/post'
  let requestId = ''
  let polls = 0
  await page.route('**/api/v1/repo-submissions', async (route) => {
    const body = route.request().postDataJSON()
    requestId = body.requestId
    expect(body).toMatchObject({ repositories: 'vercel/ai\n错误地址', sourceUrl: source, note: '社区讨论' })
    await route.fulfill({ status: 202, json: { items: [
      { line: 1, input: 'vercel/ai', jobId: 101 }, { line: 2, input: '错误地址', errorCode: 'invalid_submission_repo' },
    ] } })
  })
  await page.route('**/api/v1/repo-submissions/*', async (route) => {
    expect(route.request().url()).toContain('/' + requestId)
    const done = ++polls > 1
    await route.fulfill({ json: { items: [{ id: 101, status: done ? 'succeeded' : 'running',
      repoId: done ? 1 : null, repoFullName: done ? 'vercel/ai' : null }] } })
  })
  fixture.setDiscoveryEvents(1, [
    { sourceType: 'manual', sourceKey: source, sourceUrl: source, firstObservedAt: '2026-11-11T12:00:00Z', note: '社区讨论' },
  ])

  await page.goto('/admin/repos')
  const heading = page.getByRole('heading', { name: '仓库管理', exact: true })
  const header = page.locator('header').filter({ has: heading })
  const submitButton = header.getByRole('button', { name: '提交仓库' })
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 })
    await expect(submitButton).toBeInViewport()
    const [titleBox, buttonBox, headerBox] = await Promise.all([heading.boundingBox(), submitButton.boundingBox(), header.boundingBox()])
    expect(buttonBox!.x).toBeGreaterThan(titleBox!.x + titleBox!.width)
    expect(buttonBox!.x + buttonBox!.width).toBeCloseTo(headerBox!.x + headerBox!.width, 0)
    expect(buttonBox!.y).toBeCloseTo(titleBox!.y, 0)
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  await submitButton.click()
  const dialog = page.getByRole('dialog', { name: '提交仓库' })
  await dialog.getByLabel('仓库地址，每行一个，最多 30 个').fill('vercel/ai\n错误地址')
  await dialog.getByLabel('来源链接', { exact: false }).fill(source)
  await dialog.getByLabel('备注', { exact: false }).fill('社区讨论')
  await dialog.getByRole('button', { name: '提交', exact: true }).click()
  await expect(dialog.getByText('请输入 GitHub 仓库链接或 owner/repo。')).toBeVisible()
  await expect(dialog.getByText('执行中')).toBeVisible()
  await expect(dialog.getByText('已收录')).toBeVisible()
  await dialog.getByRole('link', { name: 'vercel/ai' }).click()
  await expect(page).toHaveURL(/\/admin\/repos\/vercel\/ai\?repoId=1$/)
  await expect(page.getByRole('heading', { name: 'vercel/ai' })).toBeVisible()
  const events = page.getByRole('region', { name: '发现来源' })
  await expect(events.getByText('人工提交')).toBeVisible()
  await expect(events.getByRole('link', { name: source })).toHaveAttribute('href', source)
  await expect(events.getByText('社区讨论')).toBeVisible()

  await page.goto('/repos/vercel/ai')
  await expect(page.getByRole('heading', { name: 'vercel/ai' })).toBeVisible()
  await expect(page.getByRole('button', { name: '提交仓库' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: '发现来源' })).toHaveCount(0)
})
