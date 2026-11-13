import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('手机和桌面可以读日报、切换往期并返回原期号', async ({ page }) => {
  await mockRadar(page)
  const first = '2026-11-12'
  const second = '2026-11-13'
  const body = '本地仓库导读保留完整正文。\n项目支持开发者按需配置工具链。'
  const item = {
    repoId: 1, fullName: 'vercel/ai', htmlUrl: 'https://github.com/vercel/ai',
  }
  const headline = '让 AI 应用的模型调用更统一'
  const storyTitle = '用统一接口减少模型接入成本'
  await page.route('**/api/v1/public/daily-reports**', (route) => {
    const date = new URL(route.request().url()).searchParams.get('date') ?? second
    return route.fulfill({ json: {
      archive: [{ date: second, count: 1, headline }, { date: first, count: 1, headline: '前一期的头条' },
        { date: '2026-10-31', count: 2, headline: '上月头条' }], nextBefore: null, total: 3, calendar: [second, first],
      report: { date, number: date === first ? 1 : 2, observedFrom: date + 'T10:30:00Z', observedTo: date + 'T12:00:00Z',
        generatedAt: date + 'T12:30:00Z', headline, lead: body, stories: [{ title: storyTitle, body, repoIds: [1] }], items: [item] },
    } })
  })
  await page.setViewportSize({ width: 390, height: 850 })
  await page.goto('/')
  await page.getByRole('navigation', { name: '手机导航' }).getByRole('link', { name: '日报' }).click()
  await expect(page.getByRole('heading', { name: 'GitHub 雷达日报' })).toBeVisible()
  await expect(page.getByText(/实际采集.*18:30.*20:00.*北京时间/)).toBeVisible()
  await expect(page.getByRole('region', { name: '头条' }).getByText(body)).toBeVisible()
  await expect(page.getByRole('heading', { name: headline, exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: storyTitle, exact: true })).toBeVisible()
  await expect(page.getByText('第 2 期')).toBeVisible()
  await page.getByRole('button', { name: '本期目录' }).click()
  await page.getByRole('dialog', { name: '本期目录' }).getByRole('link', { name: storyTitle }).click()
  await expect(page).toHaveURL(/#story-1$/)
  await expect(page.getByRole('dialog', { name: '本期目录' })).toHaveCount(0)
  await expect.poll(async () => page.locator('#story-1').evaluate((heading) =>
    heading.getBoundingClientRect().top >= (document.querySelector('.mobile-bar')?.getBoundingClientRect().bottom ?? 0),
  )).toBe(true)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  const archive = page.getByRole('navigation', { name: '往期日报' })
  await archive.getByRole('link', { name: /11月12日/ }).click()
  await expect(page).toHaveURL(/\/daily\/2026-11-12$/)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.reload()
  await expect(page.locator('.daily-front')).toHaveCSS('display', 'grid')
  await expect(archive.getByText('2026 年 11 月')).toBeVisible()
  await expect(archive.getByRole('link', { name: /11月12日/ })).toHaveAttribute('aria-current', 'page')
  const calendar = page.getByRole('navigation', { name: '日报出刊日历' })
  await expect(calendar.getByRole('link', { name: /11月12日/ })).toHaveAttribute('aria-current', 'page')
  await calendar.getByRole('link', { name: /11月13日/ }).click()
  await expect(page).toHaveURL(/\/daily\/2026-11-13$/)
  await calendar.getByRole('link', { name: /11月12日/ }).click()
  await expect(page).toHaveURL(/\/daily\/2026-11-12$/)
  await page.getByRole('region', { name: storyTitle }).getByRole('link', { name: 'vercel/ai' }).click()
  await expect(page).toHaveURL(/\/repos\/vercel\/ai$/)
  await page.getByRole('link', { name: '返回列表' }).click()
  await expect(page).toHaveURL(/\/daily\/2026-11-12$/)
  await page.locator('.daily-archive').getByRole('link', { name: '日报合订本' }).click()
  await expect(page).toHaveURL(/\/daily\/archive$/)
  const bookTitle = page.getByRole('heading', { name: '日报合订本' })
  await expect(bookTitle).toBeVisible()
  expect(await bookTitle.evaluate((title) => parseFloat(getComputedStyle(title).fontSize))).toBeGreaterThanOrEqual(68)
  await expect(page.getByText('共 3 期')).toBeVisible()
  await expect(archive).toBeVisible()
  const november = page.getByRole('region', { name: '2026 年 11 月' })
  await expect(november.getByRole('list')).toHaveCSS('grid-template-columns', /^\S+ \S+$/)
  await expect(november.getByRole('link', { name: /前一期的头条.*1 个选题/ })).toBeVisible()
  await expect(page.getByRole('region', { name: '2026 年 10 月' }).getByText('上月头条')).toBeVisible()
  await page.setViewportSize({ width: 390, height: 850 })
  await expect(bookTitle).toHaveCSS('font-size', '44px')
  await expect(november.getByRole('list')).toHaveCSS('grid-template-columns', /^\S+$/)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  await november.getByRole('link', { name: /前一期的头条/ }).click()
  await expect(page).toHaveURL(/\/daily\/2026-11-12$/)
})

test('周报、月报可从报刊切换，显示实际采集和日报来源', async ({ page }) => {
  await mockRadar(page)
  await page.route('**/api/v1/public/daily-reports**', (route) => {
    const params = new URL(route.request().url()).searchParams
    const kind = params.get('kind')
    const current = kind === 'weekly' ? '2026-W46' : '2026-11'
    const date = params.get('date') ?? current
    const official = kind === 'weekly' ? '2026-W45' : '2026-10'
    const preview = date === current
    const periodFrom = kind === 'weekly' ? '2026-11-09' : '2026-11-01'
    const periodTo = kind === 'weekly' ? '2026-11-15' : '2026-11-30'
    const observedAt = '2026-11-12T12:00:00Z'
    return route.fulfill({ json: {
      archive: kind ? [{ date: official, periodFrom: kind === 'weekly' ? '2026-11-02' : '2026-10-01',
        count: 1, headline: '为智能体复用项目技能' }] : [], nextBefore: null, total: kind ? 1 : 0, calendar: kind ? [official] : [],
      report: kind ? {
        date, number: preview ? null : 1, periodFrom, periodTo,
        publicationStatus: preview ? 'preview' : 'published', coverageDays: preview ? 1 : kind === 'weekly' ? 5 : 24,
        requiredDays: kind === 'weekly' ? 5 : 23,
        observedFrom: '2026-11-12T10:30:00Z', observedTo: observedAt, generatedAt: observedAt,
        headline: '为智能体复用项目技能', lead: '复用已经保存的导读。',
        stories: [{ title: '把项目操作变成可复用的技能', body: '两个项目分别提供技能编排和可复用的工具接口。', repoIds: [1, 2] }],
        items: [{ repoId: 1, fullName: 'vercel/ai', htmlUrl: 'https://github.com/vercel/ai' },
          { repoId: 2, fullName: 'openai/codex', htmlUrl: 'https://github.com/openai/codex' }],
      } : null,
    } })
  })
  await page.goto('/daily')
  await expect(page.getByText('首期日报尚未发布')).toBeVisible()
  const switcher = page.getByRole('navigation', { name: '切换日报、周报、月报' })
  await switcher.getByRole('link', { name: '周报', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'GitHub 雷达周报' })).toBeVisible()
  await expect(page.getByText('采集预览')).toBeVisible()
  await expect(page.getByText('未出刊')).toBeVisible()
  await expect(page.getByText('个观测日', { exact: true })).toBeVisible()
  await expect(page.getByText(/出刊至少需 5 个观测日/)).toBeVisible()
  const story = page.getByRole('region', { name: '把项目操作变成可复用的技能' })
  await expect(story.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
  await expect(story.getByRole('link', { name: 'openai/codex', exact: true })).toBeVisible()
  const weeklyCalendar = page.getByRole('navigation', { name: '周报出刊日历' })
  await expect(weeklyCalendar.getByRole('link', { name: /第 46 周/ })).toHaveCount(0)
  await expect(page.getByText(/实际采集.*18:30.*20:00.*北京时间/)).toBeVisible()
  await expect(switcher.getByRole('link', { name: '周报', exact: true })).toHaveAttribute('aria-current', 'true')
  await page.getByRole('navigation', { name: '往期周报' }).getByRole('link', { name: /第 45 周/ }).click()
  await expect(page).toHaveURL(/\/weekly\/2026-W45$/)
  await expect(page.getByText('第 1 期')).toBeVisible()
  await expect(weeklyCalendar.getByRole('link', { name: /第 45 周/ })).toHaveAttribute('aria-current', 'page')
  await page.setViewportSize({ width: 390, height: 850 })
  await switcher.getByRole('link', { name: '月报', exact: true }).click()
  await expect(page).toHaveURL(/\/monthly$/)
  await expect(page.getByRole('heading', { name: 'GitHub 雷达月报' })).toBeVisible()
  await expect(page.getByRole('region', { name: '头条' }).getByText('复用已经保存的导读。')).toBeVisible()
  await expect(page.getByRole('navigation', { name: '月报出刊日历' }).getByRole('link', { name: '2026 年 11 月' })).toHaveCount(0)
  await expect(page.getByRole('navigation', { name: '手机导航' }).getByRole('link', { name: '日报' })).toHaveAttribute('aria-current', 'page')
  await page.getByRole('navigation', { name: '往期月报' }).getByRole('link', { name: '2026 年 10 月' }).click()
  await expect(page).toHaveURL(/\/monthly\/2026-10$/)
  await page.reload()
  await expect(page.getByRole('heading', { name: 'GitHub 雷达月报' })).toBeVisible()
})

test('报刊的未决调用只能在确认可能再次计费后重试', async ({ page }) => {
  const fixture = await mockRadar(page)
  fixture.setRecentJobs([{
    id: 88, type: 'compose_report', status: 'unknown', attempts: 1, maxAttempts: 4, errorCode: 'ai_delivery_unknown',
    repoId: null, createdAt: '2026-11-12T12:00:00Z', updatedAt: '2026-11-12T12:00:00Z',
  }])
  const requests: { acceptPossibleCharge: boolean; requestId: string }[] = []
  await page.route('**/api/v1/jobs/88/retry', (route) => {
    requests.push(route.request().postDataJSON())
    return route.fulfill({ status: 202, json: { jobId: 89, statusUrl: '/api/v1/jobs/89' } })
  })
  await session(page)
  await page.goto('/admin/runs?tab=jobs')
  const job = page.getByRole('row', { name: /报刊组稿/ })
  await job.getByRole('button', { name: '确认并重试' }).click()
  const dialog = page.getByRole('dialog', { name: '确认重新组稿' })
  await expect(dialog).toBeVisible()
  expect(requests).toHaveLength(0)
  await dialog.getByRole('button', { name: '接受可能再次计费' }).click()
  await expect(dialog).toHaveCount(0)
  expect(requests).toEqual([{ acceptPossibleCharge: true, requestId: expect.any(String) }])
})
