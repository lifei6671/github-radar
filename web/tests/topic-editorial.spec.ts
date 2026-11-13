import { expect, test } from '@playwright/test'
import { mockRadar, session } from './fixture'

test('选题分项和资料版本随人工复核提交', async ({ page }) => {
  const fixture = await mockRadar(page)
  fixture.setTopicAssessment({
    revision: 'shown-revision',
    dimensions: [
      { key: 'reader', points: 24, maximum: 30, source: '已有导读', confidence: 'inferred', evidence: '能批量整理字幕' },
      { key: 'reproduction', points: 0, maximum: 20, source: '人工实测', confidence: 'pending', evidence: '等待复现' },
    ],
    brief: { demo: '比较两次导出结果', risks: ['code_execution'], steps: ['在隔离环境验证'] },
  })
  await session(page)
  await page.goto('/admin/topics')
  await page.getByText('分项评分与复核', { exact: true }).click()
  await expect(page.getByText('实测与复现', { exact: true })).toBeVisible()
  await expect(page.getByText('需人工核验　执行代码')).toBeVisible()
  await page.getByRole('button', { name: '编辑状态与反馈' }).click()
  await page.getByRole('combobox', { name: '人工核验' }).selectOption('verified')
  await page.getByLabel('实测结果或证据').fill('隔离环境中成功导出')
  await page.getByLabel('覆盖建议理由').fill('选题适合普通读者')
  const save = page.waitForRequest((request) => request.method() === 'PATCH' && request.url().endsWith('/topics/1'))
  await page.getByRole('button', { name: '保存反馈' }).click()
  expect((await save).postDataJSON()).toMatchObject({
    verification: 'verified', verificationEvidence: '隔离环境中成功导出', overrideReason: '选题适合普通读者', assessmentRevision: 'shown-revision',
  })
})
