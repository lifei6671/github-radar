import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { mockRadar, session } from './fixture'

const backup = {
  objectKey: 'github-radar/backups/2026-11-10.db.gz',
  date: '2026-11-10', size: 4096, generatedAt: '2026-11-10T01:00:00Z', schemaVersion: 7,
}
const settings = {
  enabled: true, schedule: { hour: 3, minute: 0, timezone: 'Asia/Shanghai' }, retentionDays: 5,
  r2: { endpoint: 'https://' + 'a'.repeat(32) + '.r2.cloudflarestorage.com', bucket: 'radar-test',
    prefix: 'github-radar/backups/', configured: true, accessKeyIdMasked: '••••1234' },
}
const operation = { id: 'restore-123', kind: 'restore', phase: 'copy', status: 'running', databaseApplied: false }

async function fillConnection(page: Page) {
  await page.getByLabel('R2 端点').fill(settings.r2.endpoint)
  await page.getByLabel('存储桶').fill(settings.r2.bucket)
  await page.getByLabel('Access Key ID').fill('fixture-access-1234')
  await page.getByLabel('Secret Access Key').fill('fixture-secret')
}

test('首次安装从 R2 恢复，刷新和断线后继续查询同一次操作', async ({ page }) => {
  const fixture = await mockRadar(page, false)
  let restorePosts = 0
  let dropped = false
  let completed = false
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice('/api/v1'.length)
    if (path === '/system/backup/status') {
      return route.fulfill({ json: { initialized: completed, restoring: restorePosts > 0 && !completed,
        maintenance: restorePosts > 0 && !completed, lastRestore: restorePosts ? operation : null } })
    }
    if (path === '/setup/r2/test') return route.fulfill({ json: { ok: true } })
    if (path === '/setup/r2/session') {
      expect(request.postDataJSON()).toMatchObject({ secretAccessKey: 'fixture-secret' })
      return route.fulfill({ json: { sessionId: 'temporary-r2-session', expiresAt: '2026-11-11T01:00:00Z' } })
    }
    if (path === '/setup/backups') {
      expect(request.headers()['x-setup-session']).toBe('temporary-r2-session')
      return route.fulfill({ json: { items: [backup], nextCursor: null } })
    }
    if (path === '/setup/restore') {
      restorePosts++
      expect(request.postDataJSON()).toEqual({ objectKey: backup.objectKey, sessionId: 'temporary-r2-session' })
      return route.fulfill({ status: 202, json: { operationId: operation.id, statusUrl: '/api/v1/setup/operations/' + operation.id } })
    }
    if (path === '/setup/operations/' + operation.id) {
      if (dropped) return route.abort('internetdisconnected')
      return route.fulfill({ json: completed ? { ...operation, phase: 'done', status: 'completed', databaseApplied: true } : operation })
    }
    return route.fallback()
  })
  await session(page)
  await page.goto('/')
  await page.getByRole('button', { name: '配置 R2 连接' }).click()
  await fillConnection(page)
  await page.getByRole('button', { name: '测试连接' }).click()
  await expect(page.getByText('R2 连接测试通过。')).toBeVisible()
  await page.getByRole('button', { name: '连接并查看备份' }).click()
  await expect(page.getByRole('radio', { name: '选择 2026-11-10' })).toBeChecked()
  await page.getByRole('button', { name: '恢复 2026-11-10' }).click()
  await expect(page.getByText('备份后的数据会丢失，历史 AI 调用可能已计费。')).toBeVisible()
  await page.getByRole('button', { name: '确认恢复' }).click()
  await expect(page.getByText('正在恢复数据，恢复期间暂不可访问项目数据。')).toBeVisible()
  await expect(page.getByRole('button', { name: '初始化空库' })).toBeDisabled()
  dropped = true
  await page.reload()
  await expect(page.getByText('暂时无法查询进度，连接恢复后会继续查询。')).toBeVisible()
  expect(await page.evaluate(() => JSON.stringify(sessionStorage))).not.toMatch(/fixture-secret|temporary-r2-session/)
  dropped = false
  completed = true
  fixture.completeSetup()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('heading', { name: '发现' })).toBeVisible()
  expect(restorePosts).toBe(1)
})

test('管理页设置、手动备份、重载和恢复后 AI 确认', async ({ page }) => {
  await mockRadar(page)
  let failed = false
  let reloaded = false
  let resumeAI = false
  let run = false
  let save = false
  const restored = () => ({ ...operation, databaseApplied: true, status: reloaded ? 'completed' : 'failed',
    phase: reloaded ? 'done' : 'reload', ...(!reloaded && { errorCode: 'restore_reload_failed' }) })
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice('/api/v1'.length)
    if (path === '/system/backup/status') {
      return route.fulfill({ json: { initialized: true, restoring: false, maintenance: failed && !reloaded, lastBackup: backup,
        lastBackupOperation: run ? { ...operation, id: 'backup-1', kind: 'backup', status: 'completed' } : null,
        lastRestore: failed ? restored() : null, autoAIResumeRequired: reloaded && !resumeAI, restartRequired: reloaded } })
    }
    if (path === '/system/backup/settings') {
      if (request.method() === 'PUT') {
        save = true
        expect(request.postDataJSON()).toEqual({ enabled: true, schedule: settings.schedule, retentionDays: 8 })
      }
      return route.fulfill({ json: { ...settings, retentionDays: save ? 8 : 5 } })
    }
    if (path === '/system/backup/list') return route.fulfill({ json: { items: [backup], nextCursor: null } })
    if (path === '/system/backup/run') {
      run = true
      return route.fulfill({ status: 202, json: { operationId: 'backup-1' } })
    }
    if (path.endsWith('/operations/backup-1')) return route.fulfill({ json: { ...operation, id: 'backup-1', kind: 'backup', status: 'completed' } })
    if (path === '/system/backup/restore') {
      failed = true
      return route.fulfill({ status: 202, json: { operationId: operation.id } })
    }
    if (path.endsWith('/operations/' + operation.id)) return route.fulfill({ json: restored() })
    if (path === '/system/backup/reload') {
      reloaded = true
      return route.fulfill({ status: 202, json: { operationId: operation.id } })
    }
    if (path === '/system/ai/resume') {
      expect(request.postDataJSON()).toEqual({ acceptIncompleteUsage: true })
      resumeAI = true
      return route.fulfill({ json: { autoAIResumeRequired: false } })
    }
    return route.fallback()
  })
  await session(page)
  await page.goto('/system?tab=backup')
  await expect(page.getByText('••••1234')).toBeVisible()
  await page.getByLabel('保留天数').fill('8')
  await page.getByRole('button', { name: '保存配置' }).click()
  await expect.poll(() => save).toBeTruthy()
  await page.getByRole('button', { name: '立即备份' }).click()
  await expect(page.getByText('备份完成。', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '恢复 2026-11-10' }).click()
  await page.getByRole('button', { name: '确认恢复' }).click()
  await expect(page.getByText('数据已恢复，加载失败。')).toBeVisible()
  await page.getByRole('button', { name: '重试加载' }).click()
  await expect(page.getByText('恢复完成。', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '启用自动摘要' }).click()
  await page.getByRole('button', { name: '确认启用' }).click()
  await expect(page.getByRole('button', { name: '启用自动摘要' })).toHaveCount(0)
  expect(resumeAI).toBeTruthy()
})

test('操作记录丢失提示结果不明，移动深色界面无横向滚动', async ({ page }) => {
  await mockRadar(page)
  await page.route('**/api/v1/system/backup/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/status')) return route.fulfill({ json: { restoring: false, maintenance: false } })
    if (path.endsWith('/settings')) return route.fulfill({ json: settings })
    if (path.endsWith('/list')) return route.fulfill({ json: { items: [backup], nextCursor: null } })
    return route.fulfill({ status: 404, json: { code: 'backup_operation_not_found' } })
  })
  await session(page)
  await page.addInitScript(() => {
    sessionStorage.setItem('radar.backup-operation.system', 'lost-on-restart')
    localStorage.setItem('radar.theme', 'dark')
  })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/system?tab=backup')
  await expect(page.getByText('无法确认该任务结果，已刷新系统状态。')).toBeVisible()
  await expect(page.getByRole('button', { name: '恢复 2026-11-10' })).toBeVisible()
  await expect(page.locator('html')).toHaveClass('dark')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
})

test('受理响应丢失后从服务端补回恢复，离开备份页也刷新旧项目缓存', async ({ page }) => {
  const fixture = await mockRadar(page)
  let posts = 0
  let completed = false
  await page.route('**/api/v1/system/backup/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/status')) return route.fulfill({ json: { initialized: true, restoring: posts > 0 && !completed,
      maintenance: posts > 0 && !completed, lastRestore: posts ? completed ? { ...operation, phase: 'done', status: 'completed', databaseApplied: true } : operation : null } })
    if (path.endsWith('/settings')) return route.fulfill({ json: settings })
    if (path.endsWith('/list')) return route.fulfill({ json: { items: [backup], nextCursor: null } })
    if (path.endsWith('/restore')) {
      posts++
      return route.abort('connectionreset')
    }
    return route.fulfill({ json: completed ? { ...operation, status: 'completed', phase: 'done', databaseApplied: true } : operation })
  })
  await session(page)
  await page.goto('/repos/1')
  await expect(page.getByRole('heading', { name: 'vercel/ai', exact: true })).toBeVisible()
  await page.getByRole('link', { name: '运行状态', exact: true }).click()
  await page.getByRole('button', { name: '备份与恢复' }).click()
  await page.getByRole('button', { name: '恢复 2026-11-10' }).click()
  await page.getByRole('button', { name: '确认恢复' }).click()
  await expect(page.getByText('正在恢复数据，恢复期间暂不可访问项目数据。')).toBeVisible()
  await page.getByRole('link', { name: '全部项目', exact: true }).first().click()
  await expect(page.getByRole('link', { name: '查看恢复进度' })).toBeVisible()
  fixture.renameFirstRepo('restored/ai')
  completed = true
  await expect(page.getByRole('link', { name: '查看恢复进度' })).toHaveCount(0)
  await expect(page.getByText('restored/ai', { exact: true })).toBeVisible()
  expect(posts).toBe(1)
})

test('恢复提交前发出的安装状态响应无法覆盖已完成状态', async ({ page }) => {
  await mockRadar(page, true)
  let started!: () => void
  let finish!: () => void
  const oldStarted = new Promise<void>((resolve) => { started = resolve })
  const finishOld = new Promise<void>((resolve) => { finish = resolve })
  await page.route('**/api/v1/setup/status', async (route) => {
    started()
    await finishOld
    await route.fulfill({ json: { initialized: false } })
  })
  await page.route('**/api/v1/system/backup/status', async (route) => {
    await oldStarted
    await route.fulfill({ json: { initialized: true, restoring: false, maintenance: false,
      lastRestore: { ...operation, status: 'completed', phase: 'done', databaseApplied: true } } })
  })
  await session(page)
  const canceled = page.waitForEvent('requestfailed', (request) => request.url().endsWith('/api/v1/setup/status'))
  await page.goto('/setup')
  await expect(page.getByRole('heading', { name: '系统已初始化' })).toBeVisible()
  await canceled
  finish()
  await page.getByRole('link', { name: '进入发现' }).click()
  await expect(page.getByRole('heading', { name: '发现' })).toBeVisible()
  await expect(page).toHaveURL(/\/$/)
})
