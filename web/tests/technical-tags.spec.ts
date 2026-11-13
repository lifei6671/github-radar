import { expect, test } from '@playwright/test'
import type { ListSettings, TechnicalTagSetting, TechnicalTagSettings } from '../src/api/types'
import { mockRadar, session } from './fixture'

// 通过后台入口验证标签编辑、保存请求、搜索和删除。
test('管理员可以维护技术词表', async ({ page }) => {
  await mockRadar(page)
  await session(page)
  let catalog: TechnicalTagSettings = { revision: 1, taxonomyVersion: '1.0.0',
    domains: [{ id: 'media-creation', name: '媒体工具', definition: '' }],
    items: [{ slug: 'vector-graphics', name: '矢量图形编辑', aliases: [], domain: 'media-creation', definition: '设计和编辑矢量图' }] }
  await page.route('**/api/v1/system/technical-tags', async (route) => {
    if (route.request().method() === 'PUT') {
      const edited: ListSettings<TechnicalTagSetting> = route.request().postDataJSON()
      expect(edited.revision).toBe(catalog.revision)
      catalog = { ...catalog, revision: edited.revision + 1, items: edited.items }
    }
    await route.fulfill({ json: catalog })
  })
  await page.goto('/admin/runs?tab=technical-tags')
  await expect(page.getByRole('heading', { name: '技术词表', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: '矢量图形编辑', exact: true })).toBeVisible()
  await page.getByRole('link', { name: '添加标签' }).click()
  await page.getByLabel('标签名称').fill('结构化提取')
  await page.getByLabel('标签标识').fill('structured-extraction')
  await page.getByLabel('技术领域').selectOption('media-creation')
  await page.getByLabel('定义').fill('从输入内容提取结构化数据')
  await page.getByLabel('别名').fill('结构化抽取, 提取字段')
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('link', { name: '结构化提取', exact: true })).toBeVisible()
  expect(catalog.items[1]).toEqual({ slug: 'structured-extraction', name: '结构化提取', aliases: ['结构化抽取', '提取字段'], domain: 'media-creation', definition: '从输入内容提取结构化数据' })
  await page.getByRole('link', { name: '编辑 结构化提取', exact: true }).click()
  await expect(page.getByLabel('标签标识')).not.toBeEditable()
  await page.getByLabel('标签名称').fill('结构化抽取')
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('link', { name: '结构化抽取', exact: true })).toBeVisible()
  await page.getByRole('searchbox', { name: '搜索标签' }).fill('提取字段')
  await expect(page.getByRole('link', { name: '矢量图形编辑', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '删除 结构化抽取' }).click()
  await page.getByRole('button', { name: '确认删除' }).click()
  await expect(page.getByText('没有符合条件的标签')).toBeVisible()
  expect(catalog.items.map((item) => item.slug)).toEqual(['vector-graphics'])
})
