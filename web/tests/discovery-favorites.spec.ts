import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('后台保存发现页标签的名称、顺序和显示范围', async ({ page }) => {
  await mockRadar(page)
  await session(page)
  await page.goto('/admin/runs?tab=discovery-tabs')
  await page.getByRole('button', { name: '上移 AI 编程' }).click()
  await page.getByRole('textbox', { name: 'AI 编程的标签名称' }).fill('代码智能体')
  await page.getByRole('button', { name: '移除 AI 多媒体' }).click()
  await page.getByRole('button', { name: '移除 其他' }).click()
  await page.getByRole('combobox', { name: '添加标签' }).selectOption('media')
  await page.getByRole('button', { name: '保存配置' }).click()
  await expect(page.getByRole('status')).toContainText('已保存')

  await page.getByRole('link', { name: '返回前台', exact: true }).click()
  const tabs = page.getByRole('group', { name: '项目分类' })
  await expect(tabs.getByRole('button')).toHaveText(['全部', '代码智能体', 'AI 智能体', 'MCP', '开发者工具', 'AI 多媒体'])
  await tabs.getByRole('button', { name: '代码智能体' }).click()
  await expect(page).toHaveURL(/category=coding/)
  await expect(tabs.getByRole('button', { name: '代码智能体' })).toHaveAttribute('aria-pressed', 'true')

  await page.goto('/?category=other')
  await expect(tabs.getByRole('button', { name: '代码智能体' })).toBeVisible()
  await expect(tabs.getByRole('button', { name: '全部', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.goto('/repos')
  await expect(tabs.getByRole('button', { name: 'AI 编程' })).toBeVisible()
  await expect(tabs.getByRole('button', { name: '其他', exact: true })).toBeVisible()
})

test('匿名收藏在同一浏览器中持久化并跨标签页同步', async ({ page, browser }) => {
  const radar = await mockRadar(page)
  await page.goto('/repos')
  const add = page.getByRole('button', { name: '收藏 vercel/ai', exact: true })
  await expect(add).toHaveAttribute('aria-pressed', 'false')
  await add.click()
  const remove = page.getByRole('button', { name: '取消收藏 vercel/ai', exact: true })
  await expect(remove).toHaveAttribute('aria-pressed', 'true')
  await page.reload()
  await expect(remove).toHaveAttribute('aria-pressed', 'true')

  const url = new URL('/starred', page.url()).href
  const isolated = await browser.newContext()
  try {
    const other = await isolated.newPage()
    await mockRadar(other)
    await other.goto(url)
    await expect(other.getByText('还没有收藏的仓库')).toBeVisible()
  } finally {
    await isolated.close()
  }

  const tab = await page.context().newPage()
  const list = await mockRadar(tab)
  await tab.goto(url)
  await expect(tab.getByRole('heading', { name: '我的收藏' })).toBeVisible()
  await expect(tab.getByText('收藏保存在此浏览器', { exact: false })).toBeVisible()
  const row = tab.getByRole('list', { name: '收藏仓库' }).getByRole('listitem')
  await expect(row.getByRole('link', { name: 'vercel/ai' })).toHaveAttribute('href', '/repos/1')
  await expect(row).toHaveCSS('border-radius', '12px')
  await expect(row.locator('time')).toBeVisible()
  await tab.setViewportSize({ width: 390, height: 844 })
  await expect(row).toHaveCSS('border-top-width', '0px')
  await expect(row.locator('time')).toBeHidden()
  expect(await tab.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await row.getByRole('button', { name: '取消收藏 vercel/ai', exact: true }).click()
  await expect(tab.getByText('还没有收藏的仓库')).toBeVisible()
  await expect(add).toHaveAttribute('aria-pressed', 'false')
  expect([...radar.requests, ...list.requests].every((request) => request.startsWith('GET '))).toBe(true)
  await page.getByRole('link', { name: '收藏', exact: true }).click()
  await expect(page).toHaveURL(/\/starred$/)
})
