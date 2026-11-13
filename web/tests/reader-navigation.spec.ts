import { readFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import { barrier, mockRadar } from './fixture'

test('分类和详情的慢请求显示顶部进度，标签指示器移动到当前分类', async ({ page }) => {
  await mockRadar(page)
  const category = barrier()
  const detail = barrier()
  await page.route('**/api/v1/public/repos?*', async (route) => {
    if (new URL(route.request().url()).searchParams.get('category') === 'mcp') await category.promise
    await route.fallback()
  })
  await page.route('**/api/v1/public/repos/by-name/modelcontextprotocol/servers', async (route) => {
    await detail.promise
    await route.fallback()
  })
  try {
    await page.goto('/repos')
    const tabs = page.getByRole('group', { name: '项目分类' })
    const indicator = tabs.locator('.pill-thumb')
    const progress = page.getByRole('progressbar', { name: '正在加载页面' })
    await expect(progress).toHaveCount(0)
    const before = await indicator.evaluate((element) => element.style.transform)
    await tabs.getByRole('link', { name: 'MCP', exact: true }).click()
    await expect(progress).toBeVisible()
    await expect.poll(() => indicator.evaluate((element) => element.style.transform)).not.toBe(before)
    await expect(page.locator('.repo-results')).toHaveAttribute('aria-busy', 'true')
    await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
    category.resolve()
    const repo = page.getByRole('link', { name: 'modelcontextprotocol/servers', exact: true })
    await expect(repo).toBeVisible()
    await expect(progress).toHaveCount(0)
    await repo.click()
    await expect(progress).toBeVisible()
    await expect(page).toHaveURL(/\/categories\/mcp$/)
    await expect(repo).toBeVisible()
    detail.resolve()
    await expect(page.getByRole('heading', { name: 'modelcontextprotocol/servers', exact: true })).toBeVisible()
    await expect(progress).toHaveCount(0)
  } finally {
    category.resolve()
    detail.resolve()
  }
})

test('详情返回恢复列表筛选、已加载的分页和滚动位置', async ({ page }) => {
  await mockRadar(page)
  await page.setViewportSize({ width: 1100, height: 620 })
  const filter = '/repos?' + new URLSearchParams({ tag: '开发工具', state: 'active' })
  await page.goto(filter)
  await page.getByRole('button', { name: '加载更多' }).click()
  const repo = page.getByRole('link', { name: 'charmbracelet/gum', exact: true })
  await expect(repo).toBeVisible()
  await repo.scrollIntoViewIfNeeded()
  const scroll = await page.evaluate(() => scrollY)
  expect(scroll).toBeGreaterThan(100)
  const index = await page.evaluate(() => history.state.idx)

  await repo.click()
  await expect(page.getByRole('heading', { name: 'charmbracelet/gum', exact: true })).toBeVisible()
  await page.getByRole('navigation', { name: '仓库详情章节' }).getByRole('link', { name: 'Star 趋势' }).click()
  await page.getByRole('button', { name: '30 天', exact: true }).click()
  await expect(page).toHaveURL(/\?range=30d#star-trend$/)
  await page.getByRole('link', { name: '返回列表', exact: true }).click()

  await expect(page).toHaveURL((url) => url.pathname + url.search === filter)
  await expect(page.getByRole('heading', { name: '#开发工具', exact: true })).toBeVisible()
  await expect(repo).toBeVisible()
  await expect.poll(() => page.evaluate(() => scrollY)).toBeCloseTo(scroll, 0)
  expect(await page.evaluate(() => history.state.idx)).toBe(index)
})

test('返回收藏时等可见性校验完成再恢复滚动', async ({ page }) => {
  await mockRadar(page)
  await page.setViewportSize({ width: 1100, height: 420 })
  await page.addInitScript(() => {
    const names = ['vercel/ai', 'openai/codex', 'pydantic/pydantic-ai', 'modelcontextprotocol/servers', 'astral-sh/ruff', 'charmbracelet/gum']
    localStorage.setItem('radar.favorites', JSON.stringify(names.map((fullName, index) => ({
      id: index + 1, fullName, language: null, excerpt: '测试收藏列表的仓库简介', savedAt: '2026-11-12T00:00:00Z',
    }))))
  })
  const check = barrier()
  let holdReturn = false
  await page.route('**/api/v1/public/repos/published?*', async (route) => {
    if (holdReturn) await check.promise
    await route.fallback()
  })
  try {
    await page.goto('/starred')
    const repo = page.getByRole('link', { name: 'charmbracelet/gum', exact: true })
    await expect(repo).toBeVisible()
    await repo.scrollIntoViewIfNeeded()
    const scroll = await page.evaluate(() => scrollY)
    expect(scroll).toBeGreaterThan(100)
    await repo.click()
    const heading = page.getByRole('heading', { name: 'charmbracelet/gum', exact: true })
    await expect(page).toHaveURL(/\/repos\/charmbracelet\/gum$/)
    await expect(heading).toBeVisible()
    holdReturn = true
    await page.getByRole('link', { name: '返回列表', exact: true }).click()
    await expect(page.getByRole('progressbar', { name: '正在加载页面' })).toBeVisible()
    await expect(heading).toBeVisible()
    check.resolve()
    await expect(page).toHaveURL(/\/starred$/)
    await expect(repo).toBeVisible()
    await expect.poll(() => page.evaluate(() => scrollY)).toBeCloseTo(scroll, 0)
  } finally {
    check.resolve()
  }
})

test('列表小趋势图用虚线补中间缺测，保留首尾空白和真实样本', async ({ page }) => {
  const fixture = await mockRadar(page)
  // 覆盖单点不出图、隔开的孤立观测能插值、连续观测仍用实线。
  fixture.setSparkline(1, [2])
  fixture.setSparkline(2, [1, 2, 3, 5, 6, 7, 21, 22])
  fixture.setSparkline(3, [2, 16])
  await page.goto('/repos')
  const sparse = page.locator('.repo-card').filter({ has: page.getByRole('link', { name: 'vercel/ai', exact: true }) })
  const sufficient = page.locator('.repo-card').filter({ has: page.getByRole('link', { name: 'openai/codex', exact: true }) })
  await expect(sparse.locator('.repo-card-meta time')).toBeVisible()
  await expect(sparse.locator('.sparkline')).toHaveCount(0)
  const graph = sufficient.getByRole('img', { name: /累计 Star 曲线，虚线为插值估算，部分时段采样不足/ })
  await expect(graph).toBeVisible()
  const path = await graph.locator('.sparkline-stroke').getAttribute('d')
  expect(path?.match(/M/g)).toHaveLength(3)
  expect(path?.match(/C/g)).toHaveLength(5)
  const estimates = graph.locator('.sparkline-estimated')
  await expect(estimates).toHaveAttribute('d', /^M[^M]+ L[^M]+ M[^M]+ L[^M]+$/)
  const sparsePair = page.locator('.repo-card').filter({ has: page.getByRole('link', { name: 'pydantic/pydantic-ai', exact: true }) })
  await expect(sparsePair.locator('.sparkline-stroke')).toHaveCount(0)
  await expect(sparsePair.locator('.sparkline-estimated')).toBeVisible()
  const line = await sparsePair.locator('.sparkline-estimated').evaluate((path: SVGPathElement) =>
    [path.getPointAtLength(0).x, path.getPointAtLength(path.getTotalLength()).x])
  // 第 2 和第 16 小时是两个真实端点，首尾不会填满 24 小时。
  expect(line[0]).toBeCloseTo(5 + 2 / 23 * 94)
  expect(line[1]).toBeCloseTo(5 + 16 / 23 * 94)
})

test('标签异步切换时保留旧列表，加载完显示标签标题和分类', async ({ page }) => {
  const fixture = await mockRadar(page)
  const tagRequest = barrier()
  await page.route('**/api/v1/public/repos?*', async (route) => {
    if (new URL(route.request().url()).searchParams.get('tag') === '流式响应') await tagRequest.promise
    await route.fallback()
  })
  const title = page.getByRole('heading', { level: 1 })
  const progress = page.getByRole('progressbar', { name: '正在加载页面' })
  const other = page.getByRole('link', { name: 'astral-sh/ruff', exact: true })
  const results = page.locator('.repo-results')
  try {
    await page.goto('/repos')
    await expect(other).toBeVisible()
    await page.getByRole('link', { name: '#流式响应', exact: true }).click()
    await expect(progress).toBeVisible()
    await expect(results).toHaveAttribute('aria-busy', 'true')
    await expect(results).toHaveCSS('opacity', '0.5')
    await expect(page).toHaveURL(/\/repos$/)
    await expect(title).toHaveText('全部项目')
    await expect(other).toBeVisible()
    await expect(page.getByRole('status', { name: '正在加载' })).toHaveCount(0)
    tagRequest.resolve()

    await expect(title).toHaveText('#流式响应')
    await expect(page.locator('.page-head .page-subtitle')).toHaveCount(0)
    await expect(page.getByRole('button', { name: '清除标签筛选 流式响应' })).toBeHidden()
    await expect(other).toHaveCount(0)
    await expect(progress).toHaveCount(0)
    await expect(page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name: '全部项目' })).toHaveAttribute('aria-current', 'page')

    const tabs = page.getByRole('group', { name: '项目分类' })
    await tabs.getByRole('link', { name: 'AI 编程', exact: true }).click()
    await expect(page).toHaveURL(/\/categories\/coding\?tag=/)
    await expect(title).toHaveText('#流式响应')
    await expect.poll(() => fixture.requests.some((request) =>
      request.includes('tag=' + encodeURIComponent('流式响应')) && request.includes('category=coding'))).toBeTruthy()
    await page.goBack()
    await expect(page).toHaveURL(/\/repos\?tag=/)
    await page.reload()
    await expect(title).toHaveText('#流式响应')
    await expect(tabs.getByRole('link', { name: '全部', exact: true })).toHaveAttribute('aria-current', 'page')
  } finally {
    tagRequest.resolve()
  }
})

test('标签列表请求失败后在当前页面重试', async ({ page }) => {
  await mockRadar(page)
  let failures = 0
  await page.route('**/api/v1/public/repos?*', async (route) => {
    if (new URL(route.request().url()).searchParams.has('tag') && failures === 0) {
      failures++
      await route.fulfill({ status: 503, json: {} })
    } else {
      await route.fallback()
    }
  })
  await page.goto('/repos')
  await page.getByRole('link', { name: '#流式响应', exact: true }).click()
  await expect(page.getByRole('heading', { name: '#流式响应', exact: true })).toBeVisible()
  await expect(page.getByRole('alert')).toHaveText('服务暂不可用，请稍后重试。')
  await page.getByRole('button', { name: '重试', exact: true }).click()
  await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
  await expect(page.getByRole('alert')).toHaveCount(0)
})

test('离线时切换标签仍进入可重试的页面', async ({ page, context }) => {
  await mockRadar(page)
  await page.route('**/api/v1/public/repos?*', async (route) => {
    if (!await page.evaluate(() => navigator.onLine)) await route.abort('internetdisconnected')
    else await route.fallback()
  })
  await page.goto('/repos')
  const link = page.getByRole('link', { name: '#流式响应', exact: true })
  await expect(link).toBeVisible()
  await context.setOffline(true)
  try {
    await expect.poll(() => page.evaluate(() => navigator.onLine)).toBe(false)
    await link.click()
    await expect(page.getByRole('heading', { name: '#流式响应', exact: true })).toBeVisible()
    await expect(page.getByRole('alert')).toHaveText('无法连接服务，请检查网络后重试。')
    await expect(page.getByRole('progressbar')).toHaveCount(0)
  } finally {
    await context.setOffline(false)
  }
  await page.getByRole('button', { name: '重试', exact: true }).click()
  await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
})

test('仓库详情可收藏、复制当前页面链接并下载 Markdown 和海报', async ({ page }) => {
  const fixture = await mockRadar(page)
  fixture.setReadme(1, {
    state: 'ready', complete: true, truncated: false,
    chineseHtml: '<p>安装依赖</p><pre><code>npm install ai</code></pre>',
  })
  await page.addInitScript(() => Object.defineProperty(navigator, 'share', {
    value: () => { document.documentElement.dataset.nativeShareUsed = 'true' }, configurable: true,
  }))
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/repos/vercel/ai?range=30d#readme')
  const tools = page.getByRole('group', { name: '项目操作', exact: true })
  await expect(tools.getByRole('link', { name: '访问仓库' })).toHaveAttribute('href', 'https://github.com/vercel/ai')
  await expect(tools.getByRole('link', { name: '访问仓库' })).toHaveCSS('height', '32px')
  await tools.getByRole('button', { name: '收藏 vercel/ai', exact: true }).click()
  await expect(tools.getByRole('button', { name: '取消收藏 vercel/ai', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.goto('/starred')
  await expect(page.getByRole('link', { name: 'vercel/ai', exact: true })).toBeVisible()
  await page.goBack()

  await expect(page).toHaveURL('/repos/vercel/ai?range=30d#readme')
  const url = page.url()
  await page.clock.install()
  const more = tools.getByRole('button', { name: '更多操作' })
  await more.click()
  await tools.getByRole('button', { name: '分享链接', exact: true }).click()
  const copied = page.getByRole('status').getByText('已复制链接', { exact: true })
  await expect(copied).toBeVisible()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(url)
  expect(await page.evaluate(() => document.documentElement.dataset.nativeShareUsed)).toBeUndefined()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.clock.fastForward(2500)
  await expect(copied).toHaveCount(0)

  await more.click()
  const markdownDownload = page.waitForEvent('download')
  await tools.getByRole('button', { name: '导出 Markdown' }).click()
  const markdown = await markdownDownload
  expect(markdown.suggestedFilename()).toBe('vercel-ai.md')
  const contents = await readFile(await markdown.path(), 'utf8')
  expect(contents).toContain('# vercel/ai')
  expect(contents).toContain('## AI 导读')
  expect(contents).toContain('https://github.com/vercel/ai/blob/main/README.md')
  expect(contents).toContain('## README')
  expect(contents).toContain('```\nnpm install ai\n```')

  await more.click()
  await tools.getByRole('button', { name: '生成分享海报' }).click()
  const poster = page.getByRole('dialog', { name: '分享海报' })
  await expect(poster.getByRole('img', { name: 'vercel/ai 分享海报' })).toBeVisible()
  const pngDownload = page.waitForEvent('download')
  await poster.getByRole('link', { name: '下载 PNG' }).click()
  const png = await pngDownload
  expect(png.suggestedFilename()).toBe('vercel-ai-poster.png')
  const bytes = await readFile(await png.path())
  expect(bytes.subarray(1, 4).toString()).toBe('PNG')
  expect([bytes.readUInt32BE(16), bytes.readUInt32BE(20)]).toEqual([900, 1120])
})

test('导出等待 README 重查，仓库撤回时不会下载缓存正文', async ({ page }) => {
  const fixture = await mockRadar(page)
  fixture.setReadme(1, {
    state: 'ready', complete: true, truncated: false, chineseHtml: '<p>已缓存的 README</p>',
  })
  await page.goto('/repos/vercel/ai')
  await expect(page.getByText('已缓存的 README', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '返回列表' }).click()
  const withdrawn = barrier()
  await page.route('**/api/v1/public/repos/1/readme', async (route) => {
    await withdrawn.promise
    await route.fulfill({ status: 404, json: { code: 'not_found' } })
  })
  const downloads: string[] = []
  page.on('download', (download) => downloads.push(download.suggestedFilename()))
  try {
    await page.getByRole('link', { name: 'vercel/ai', exact: true }).click()
    await page.getByRole('button', { name: '更多操作' }).click()
    await page.getByRole('button', { name: '导出 Markdown' }).click()
    await expect(page.getByRole('status').filter({ hasText: '正在导出 Markdown' })).toBeVisible()
    withdrawn.resolve()
    await expect(page.getByRole('alert')).toHaveText('记录不存在或已过保留期。')
    expect(downloads).toEqual([])
  } finally {
    withdrawn.resolve()
  }
})
