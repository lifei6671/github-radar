import { QueryClient } from '@tanstack/react-query'
import { useSyncExternalStore } from 'react'

interface ErrorBody { code?: string; message?: string; existingJobId?: number }

export class ApiError extends Error {
  readonly code: string
  readonly existingJobId?: number

  constructor(readonly status: number, body: ErrorBody = {}) {
    super(body.message ?? body.code ?? '请求未完成')
    this.code = body.code ?? ''
    this.existingJobId = body.existingJobId
  }
}

function newQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 20_000,
        refetchOnWindowFocus: false,
        retry: (attempt, error) => attempt < 1 && error instanceof ApiError && (error.status === 0 || error.status >= 500 && error.status !== 503),
      },
      mutations: { retry: false },
    },
  })
}

const listeners = new Set<() => void>()
// 清理旧版浏览器留下的明文令牌，新的凭据由 HttpOnly Cookie 持有。
try { sessionStorage.removeItem('radar.adminToken') } catch { /* 隐私模式下可能不可用。 */ }
let session = { authenticated: null as boolean | null, client: newQueryClient(), generation: 0 }

export function setAuthenticated(value: boolean) {
  if (session.authenticated === value) return
  const previous = session
  // 会话间不共享缓存。旧请求的回调只能持有已退出会话的 QueryClient。
  session = previous.authenticated === null
    ? { ...previous, authenticated: value }
    : { authenticated: value, client: newQueryClient(), generation: previous.generation + 1 }
  if (previous.client !== session.client) previous.client.clear()
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function useSession() {
  return useSyncExternalStore(subscribe, () => session)
}

export function sessionQueryClient() { return session.client }

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT'
  body?: unknown
  signal?: AbortSignal
  setupSession?: string
  timeoutMs?: number
}

// API 仅使用同源请求。迟到的旧会话 401 不应清除新会话。
async function request<T>(path: string, options: RequestOptions, anonymous: boolean): Promise<T> {
  const generation = session.generation
  const headers = new Headers({ Accept: 'application/json' })
  if (options.setupSession) headers.set('X-Setup-Session', options.setupSession)
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  const timeout = AbortSignal.timeout(options.timeoutMs ?? 15_000)
  const signal = options.signal ? AbortSignal.any([timeout, options.signal]) : timeout
  let response: Response
  try {
    response = await fetch('/api/v1' + (anonymous ? '/public' : '') + path, {
      method: options.method,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal,
      credentials: anonymous ? 'omit' : 'same-origin',
      cache: 'no-store',
      redirect: 'error',
    })
  } catch (error) {
    if (options.signal?.aborted) throw error
    throw new ApiError(0, { code: timeout.aborted ? 'timeout' : 'network_error' })
  }
  if (generation !== session.generation) throw new ApiError(409, { code: 'session_changed' })
  const payload = await response.json().catch(() => null)
  if (generation !== session.generation) throw new ApiError(409, { code: 'session_changed' })
  if (!anonymous && response.status === 401 && payload?.code === 'unauthorized') setAuthenticated(false)
  if (!response.ok) throw new ApiError(response.status, payload ?? {})
  if (payload === null) throw new ApiError(500, { code: 'invalid_response' })
  return payload as T
}

export function api<T>(path: string, options: RequestOptions = {}) {
  return request<T>(path, options, false)
}

// 即使同一浏览器已登录，公开页面也不发送管理凭据。
export function publicApi<T>(path: string, options: Pick<RequestOptions, 'signal'> = {}) {
  return request<T>(path, options, true)
}

const messages: Record<string, string> = {
  unauthorized: '管理员会话已失效，请重新登录。',
  invalid_admin_password: '管理员密码错误，请重新输入。',
  invalid_origin: '请通过网站配置的对外地址访问后台。',
  login_rate_limited: '登录请求过于频繁，请稍后重试。',
  timeout: '请求超时。可重试同一次操作。',
  network_error: '无法连接服务，请检查网络后重试。',
  setup_required: '请先完成系统初始化。',
  invalid_filter: '筛选条件无效，请检查或清除筛选。',
  invalid_cursor: '分页位置已失效，请重新加载列表。',
  invalid_trending_source: '此 GitHub Trending 来源尚未配置。',
  invalid_request_id: '提交请求无效，请检查输入后重试。',
  invalid_submission_count: '每次提交 1 至 30 行仓库。',
  invalid_submission_repo: '请输入 GitHub 仓库链接或 owner/repo。',
  invalid_submission_source: '请输入有效的 HTTP 或 HTTPS 来源链接。',
  invalid_submission_note: '备注最多 500 字。',
  submission_conflict: '本次提交已使用不同的输入，请重新提交。',
  github_not_found: 'GitHub 上找不到该公开仓库。',
  github_gone: '该仓库已不可访问或已转为私有。',
  github_not_configured: '请先配置 GitHub 访问令牌。',
  summary_unavailable: '摘要服务未配置或手动摘要额度已关闭。',
  summary_repo_unavailable: '此仓库当前无法生成摘要。',
  monitoring_capacity_full: '重点监控容量已满，请移出一个仓库或调整容量。',
  repo_blocked: '此仓库已屏蔽，请先解除屏蔽。',
  settings_conflict: '配置已被其他操作更新，当前编辑已保留。',
  possible_charge_not_accepted: '请确认再次调用可能产生费用。',
  not_found: '记录不存在或已过保留期。',
  invalid_response: '服务返回了无法读取的数据。',
  already_initialized: '系统已经初始化。',
  backup_operation_in_progress: '已有备份或清理任务正在运行。',
  restore_in_progress: '恢复正在进行，暂时无法修改设置或发起新任务。',
  restoring: '恢复期间暂不可访问项目数据。',
  r2_not_configured: '请先保存 R2 连接。',
  invalid_backup_settings: '请检查 R2 连接、时区和保留天数。',
  invalid_backup_object: '备份已不存在或格式无效。',
  setup_session_expired: 'R2 会话已过期，请重新连接。',
  r2_request_failed: 'R2 请求失败，请检查连接和存储桶读写权限。',
  backup_upload_unconfirmed: '无法确认上传结果，请刷新云端备份列表。',
  backup_insufficient_disk: '暂存空间不足或不可写。',
  backup_checksum_mismatch: '备份的校验值或压缩大小不匹配。',
  backup_gzip_invalid: '备份解压失败。',
  backup_compression_failed: '生成压缩备份失败。',
  backup_integrity_failed: 'SQLite 完整性检查失败。',
  backup_foreign_key_failed: 'SQLite 外键检查失败。',
  backup_schema_unsupported: '备份的数据库版本不受当前服务支持。',
  backup_migration_failed: '备份数据库升级失败。',
  restore_page_size_mismatch: '备份与当前 SQLite 数据库的页大小不匹配。',
  restore_drain_failed: '未能在限时内暂停业务，已结束本次恢复。',
  restore_copy_failed: '复制失败，已恢复原有业务访问。',
  restore_database_unavailable: '无法读取数据库确认恢复结果，业务暂不可用。',
  restore_receipt_missing: '无法确认恢复提交结果，业务暂不可用。',
  restore_reload_failed: '数据已恢复，加载失败。',
  reload_not_available: '当前没有需要重试加载的恢复任务。',
  incomplete_usage_not_accepted: '请确认恢复前的 AI 用量记录可能不完整。',
}

export function errorText(error: unknown): string {
  if (error instanceof ApiError) {
    if (messages[error.code]) return messages[error.code]
    if (error.status === 400 && error.message !== error.code) return error.message
    if (error.status === 429) return '请求达到频率限制，请稍后重试。'
    if (error.status === 503) return '服务暂不可用，请稍后重试。'
    if (error.status === 404) return messages.not_found
  }
  return '操作未完成，请稍后重试。'
}
