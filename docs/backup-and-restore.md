# github-radar｜SQLite 备份、五天保留与同进程一键恢复

- 版本：1.4.1
- 日期：2026-11-11
- 方案状态：阶段 5 已接入
- 主方案：[GitHub 热点雷达技术设计](./github-radar-technical-design.md)
- 前端方案：[AIHOT 视觉交互适配](./aihot-ui-adaptation.md)
- 部署范围：单 VPS、单 Go 进程、单 SQLite 业务库

## 1. 本期范围与简化决策

每天生成一致性 SQLite 快照，Gzip 后上传 Cloudflare R2，默认保留最近 5 个自然日（可配 1～30 天）。后台支持修改配置、立即备份及一键恢复；新 VPS 支持从 /setup 恢复或初始化空库。README 原文不入库，备份只含业务数据和已保存摘要。

**一键恢复正常完成时 Go PID 不变。** 使用 modernc.org/sqlite 的 NewRestore/Backup API，将准备好的临时库写入现有数据库；SQLite 自己处理事务与 WAL。已有 sql.DB 和 DAO 引用保持有效，HTTP 服务继续提供进度查询。

本期只需一个备份服务协调互斥、进度和临时文件，复用应用停止接单/等待任务结束的维护入口。不实现数据库文件交换、动态连接池发布、每个 DAO 的数据库租约、RecoveryJournal、recovery_pending 或自动重放恢复流程。保留必要的完整性校验、业务访问暂停和 SQLite 事务失败处理。

## 2. 设置、初始化与认证

### 2.1 持久化设置

阶段 0 迁移即创建本表；备份功能到阶段 5 启用：

~~~sql
CREATE TABLE IF NOT EXISTS system_settings (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL CHECK (json_valid(value_json)),
    updated_at INTEGER NOT NULL
);
~~~

备份设置用一个 `backup.settings.v1` JSON 对象原子更新：

~~~json
{
  "version": 1,
  "enabled": true,
  "schedule": {"hour": 3, "minute": 0, "timezone": "Asia/Shanghai"},
  "retention_days": 5,
  "r2": {
    "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
    "bucket": "github-radar-backup",
    "prefix": "github-radar/backups/",
    "access_key_id": "<key>",
    "secret_access_key": "<secret>"
  }
}
~~~

另保存 `app.initialized` 和 `ai.auto_resume_required` 布尔值。未配置 R2 时默认关闭备份。保存设置时校验时间、IANA 时区、保留范围及连接；只改计划时保留原凭据。事务成功后刷新内存配置和调度，下次运行时间随响应返回；刷新失败明确报错并允许重新加载，不报告已生效。

数据库同时保存 `runtime.settings.v1`、`categories` 和 `discovery_rules`，配置随业务库备份及恢复。恢复旧版库时，在临时库迁移中补齐缺失的配置；已有配置和规则保持备份值。恢复后在重新开放业务前应用恢复的业务配置。HTTP 超时、SQLite 日志模式和连接参数有变更时，提示下次重启应用。监听地址、数据库路径和部署环境中的密钥值由目标实例提供。

R2 连接整体更新，Secret Access Key 不回传；响应只含脱敏值与已配置标志。备份/清理使用操作接受时的配置快照；恢复从接受请求起冻结配置修改，返回 `409 restore_in_progress`，直到恢复失败或完成。

### 2.2 安装与认证

setup、备份列表和进度均使用后台密码登录后签发的管理员会话，具体浏览器行为见主方案第 9.1 节。密码和签名密钥由当前实例的部署 YAML 提供，数据库恢复后继续使用当前会话。

启动时打开数据库并执行基础迁移。数据库缺失则建立未初始化空库，开放登录、setup API、静态页面和健康检查，业务 Worker 等待安装完成。`app.initialized=true` 时加载设置、启动业务，setup 写接口返回 409，状态和进度仍可用同一管理员会话读取。

/setup 的 R2 配置仅保存在进程内存会话中，ID 为随机值，有效期 30 分钟；任务接受时复制配置，即使会话随后过期也不影响该任务。进程退出后未提交的配置需重新输入。选择初始化新库时，在一个事务中写默认设置和初始化标记，再加载业务；不能对已初始化库再次初始化。

### 2.3 凭据与空间边界

数据库、快照和 Gzip 都含 R2 凭据，Gzip 不是加密。部署必须使用专用私有 bucket 和仅限该 bucket 的 Object Read & Write 令牌，不使用账户级 Admin 权限。普通 R2 令牌的 bucket 范围不能当作 prefix 权限隔离；应用仅删除配置前缀下符合日期命名的对象。

数据库及 staging 目录仅服务账号可访问，临时文件沿用该权限，不经静态路由暴露。日志不记密钥、配置正文、会话 Cookie 或 README。恢复时由目标实例的部署者提供管理员登录配置及 GitHub、AI 密钥。上游密钥从目标实例的部署 YAML 或环境变量读取。

5,000 仓库下数据库 200～500 MB、压缩包 50～250 MB 仅为未实测的规划估算。磁盘需容纳现有库、临时库、压缩包及 WAL 增量；“两倍数据库大小”不是硬保证。VACUUM INTO 的长读可能推迟 WAL 回收，恢复也会产生目标 WAL；实现时记录实际峰值并据此配置 VPS 容量。

## 3. 备份与保留

对象键为 `<prefix>/YYYY-MM-DD.db.gz`，日期采用备份设置时区。每天自动一份，当天手动备份覆盖同一对象；不依赖 R2 对象版本化。metadata 保存压缩 SHA-256、原始/压缩大小、schema 版本及生成 UTC 时间。

1. 获取备份/恢复互斥，捕获本次设置；独立清理也使用该互斥。
2. 用 `VACUUM INTO` 生成临时一致性快照，不直接复制生产 db 或 WAL。
3. 临时库通过 `integrity_check`、`foreign_key_check`；Gzip 关闭成功且能完整解码后计算 SHA-256。
4. 用带 Content-MD5 的 PutObject 上传。HeadObject 核对大小和 metadata；metadata 是自填摘要，不能声称 R2 重新计算了 SHA-256。响应丢失先核对远端对象。
5. 成功后清理临时文件、记录结果并执行保留清理；失败不预先删除远端今日对象。上传提交结果未知时如实显示待核实，不保证仍是旧对象。

每天上传成功后及配置时区 03:30 各清理一次。保留当天及之前 N-1 天的合法日期对象，默认 N=5；只删除本前缀下匹配命名且日期早于窗口的对象。上传后的清理沿用该对象的计划日期，跨午夜时仍保留本次上传的对象。独立清理按运行日计算窗口。清理与上传串行，不遍历删除其他路径。连续 N 天没有成功备份可能没有可用对象，后台显示最后成功时间；策略是最近 N 个自然日。

备份列表从 R2 分页获取，包含日期、大小、生成时间及 schema 版本，安装模式也可读取；不固定裁成 5 条。列表、下载、上传和删除沿用外部请求超时与有限重试机制，失败留待下次操作，不无限重试。

## 4. 同进程恢复

### 4.1 准备临时库，业务继续运行

恢复接受后创建 operationId，并冻结本次设置：有新 R2 连接则采用已验证的新连接，否则使用当前实例连接；安装模式必须提供可用连接。已有部署的备份计划沿用当前设置；新安装默认沿用备份中合法计划，没有则用 03:00、Asia/Shanghai、5 天。

下载指定对象、核对 SHA-256、解压到 staging，打开临时 SQLite 检查完整性、外键及 schema。仅接受本版本支持迁移的旧 schema 或相同 schema，较新版本拒绝；迁移只在临时库上执行。检查解压大小与磁盘余量。目标使用 WAL 时，源/目标 page_size 必须相同，不匹配则拒绝此次恢复。

随后在**临时库的一个事务**内完成：

- 将本次完整 R2 连接和选定计划写入 system_settings，覆盖备份内旧配置。
- 设置 `app.initialized=true`、`ai.auto_resume_required=true`。
- 按第 4.4 节整理任务及配额状态。
- 在已有 maintenance_state 表中写 `restore.last_operation`，值含 operationId 和 preparedAt；它是最近一次恢复的提交凭证，不是过程日志，preparedAt 不冒充完成时间。

提交后重新检查完整性、外键并读回这些设置，关闭临时库的准备连接；复制期间不再修改临时库。若临时库产生 WAL，先成功 checkpoint 并关闭，保证 NewRestore 读取完整已提交状态。任一准备步骤失败只清理本次临时文件，业务库不变。

### 4.2 暂停访问并调用 SQLite 原生恢复

1. 进入应用维护模式：新的业务 API 返回 `503 restoring`，readyz 返回 503；healthz 和不访问业务库的操作进度继续可用。
2. 暂停采集、AI、Cron、清理与备份，等待已有请求、任务、Rows、Tx 和借出的 Conn 结束。复用应用优雅停机的取消与等待机制，最长 30 秒；无法排空则在调用恢复前失败，恢复原调度和正常访问。排空阶段允许已有执行者完成必要的结果/取消状态落库；只有其释放连接且退出后才计为已排空。进入复制阶段后禁止其他数据库访问，包括健康检查 SQL。
3. 从原有 sql.DB 借一个独占 sql.Conn，在 `Raw` 回调内断言驱动的 `NewRestore(string) (*sqlite.Backup, error)` 接口，源地址使用固定的本次 staging 路径。保留现有 sql.DB，不更换文件或重建 DAO。
4. `NewRestore` 后按 128 页调用 `Step`，每步间检查取消/超时。modernc 的返回值 **true 表示还有页，false 表示已完成**；任何结束路径均恰好调用一次 `Finish` 释放资源。复制使用独立于发起 HTTP 请求的操作 context，最长 5 分钟；客户端断开不取消已接受的恢复。
5. 必须同时满足 Step 报告完成且 Finish 成功才认定提交。未完成时 Finish 让 SQLite 回滚目标写事务；不能仅以 Finish 无错误判断成功。复制期间不得通过目标连接执行其他 SQL/API。
6. 释放借出的 Conn，使用原有连接池读回本次 operationId 和配置，重新加载配置及调度；开放业务并标记 completed。恢复日自动 AI 仍暂停，页面提供明确启用操作。

**排空取消的任务状态：** 在仍持有效租约时，普通可重入任务及尚未发送模型请求的 AI 任务写 `failed`、`last_error_code=maintenance_interrupted`，按现有 available_at 退避和 max_attempts 执行（已达上限则 dead），清除租约；仅在任务为 failed、还可重试时保留尚未发送的 reserved 调用，继续时复用原预留名额；任务已达上限转 dead 时，在同一事务将该未发送调用置 failed、error_code=maintenance_interrupted，保留 reserved_at 计入原预算，不退回名额。AI 已处于 sending 且未确认结果时，调用和任务在同一事务写 unknown，停止自动重发；已经确认且可持有效租约提交的结果按正常成功/失败路径处理。不将这些任务统一写 canceled；canceled/restore_reset 仅用于第 4.4 节临时恢复库内的旧任务整理。

取消后的状态落库使用独立、受排空截止时间限制的收尾 context，不用已取消的任务 context。若租约已失效或无法在 30 秒内确认执行者结束与状态落库，则放弃本次恢复，保持原库，未结束任务不伪装终态；后续由现有租约回收及 AI sending→unknown 规则处理。恢复被放弃后，failed 可按退避重试，unknown 仍须人工确认。

SQLite Backup API 将目标写入放在一个事务里；文件和 WAL 的提交/崩溃恢复由 SQLite 负责。已有空闲连接在恢复前无活跃事务，完成后可继续通过同一池访问新内容；每连接 PRAGMA 沿用启动初始化规则。阶段 5 必须用选定 modernc 版本验证这些行为，不能用伪造文件替换测试代替。

复制报错时先读取 restore.last_operation 判断是否已经提交；不能把 Finish 错误一概当作未提交。确认未提交且原生事务正常回滚时，重新读取旧库配置并恢复服务，操作为 failed。存储故障导致数据库不可读时沿用普通数据库故障处理并保持服务不可用，不假装回滚成功。**提交后**配置重载失败则状态为 `failed`、`databaseApplied=true`，保持维护，允许通过 reload 接口重试配置/调度加载；不自动恢复旧数据、不再次复制、不创建第二套恢复状态机。reload 成功后该操作转 completed。

### 4.3 安装与进程异常退出

首次安装复用上述流程，目标是未初始化的空库。恢复提交后，初始化标记、当前 R2 连接和整理后的任务一起生效，直接在同一进程启用普通路由和 Worker；setup 写入口关闭。

若进程被强制终止，下一次启动先由 SQLite 正常打开和恢复事务，再按库内初始化标记加载设置。复制未提交则仍是旧库（安装时仍未初始化）；复制已提交则新业务数据、当前 R2 配置和任务整理一起生效。不存在“换了新库、还没写新凭据”的窗口，不自动重新下载或复制。

进度仅保存在内存，最多保留最近 20 次操作，运行中的操作不淘汰。重启后只可根据 `restore.last_operation` 识别最近已提交恢复：ID 匹配且服务正常加载则返回 completed；ID 不匹配返回 404，前端重新查询安装/系统状态并提示结果无法确定，不自动再次恢复。启动时清理本应用遗留且无运行任务引用的 staging 文件，不扫描删除其他文件。

### 4.4 恢复后的任务与 AI

以下修改在临时库完成，不会改动恢复前正在工作的业务库：

- pending/running/failed/unknown 的旧 jobs 全部置 canceled，清除租约，last_error_code 记 restore_reset；保留历史成功/终止记录。
- 非终态 ai_calls 置 failed，error_code=restore_reset_unknown，明确表示调用结果不能追溯，不表示没有计费。
- 清空旧 GitHub 配额观测，让限流器启动时重新获取；将 repositories.next_fetch_at、discovery_rules.next_run_at、summary_checks.next_check_at 置当前时间，按当前扫描槽建立新任务；保留旧任务去重键，碰到同槽终态任务时跳过该槽，下个五分钟槽再建，不能重放备份时未完成的调用。
- 保留仓库、采样、指标、摘要、选题、相似项目向量及聚合水位。设置 `ai.auto_resume_required=true`，持久化暂停自动摘要和向量生成。普通重启也遵守该标志。

恢复确认框说明备份后的数据会丢失、历史 AI 调用可能已计费。用户调用 `POST /api/v1/system/ai/resume` 并提交 `{acceptIncompleteUsage:true}` 后清除暂停标志，按恢复库中可用台账继续当日预算；明确无法还原备份之后的历史费用。手动生成仍需用户主动操作，不从旧任务自动发起。普通进程重启使用主方案任务续跑规则，不执行上述清理。

## 5. 接口与界面

所有备份和恢复接口先验证管理员会话。备份、恢复、清理互斥，冲突返回 409；恢复接受后设置写入也先返回 409，其他业务请求才应用维护期 503。reload 仅在复制结束且 databaseApplied=true 的加载失败状态下执行，复用恢复互斥，禁止与复制并发访问数据库。

| 接口 | 行为 |
| --- | --- |
| GET/PUT /api/v1/system/backup/settings | 脱敏设置、保存并刷新计划 |
| POST /api/v1/system/backup/test | 测试指定 R2 连接，不持久化 |
| GET /api/v1/system/backup/list | 分页列出本前缀日期对象 |
| GET /api/v1/system/backup/status | 最近备份/恢复状态和下次备份时间 |
| POST /api/v1/system/backup/run | 立即备份，202 + operationId/statusUrl |
| POST /api/v1/system/backup/restore | `{objectKey,r2?:完整新连接}`，202 + operationId/statusUrl |
| GET /api/v1/system/backup/operations/:id | 内存进度；重启后按最近恢复凭证查询 |
| POST /api/v1/system/backup/reload | 仅已提交恢复的重载失败时重试配置和调度加载，不重新复制 |
| GET /api/v1/setup/status | `{initialized:boolean}`，维护期从运行模式读取 |
| POST /api/v1/setup/r2/test、r2/session | 测试连接/创建临时 R2 会话，后者返回 sessionId |
| GET /api/v1/setup/backups | 使用 X-Setup-Session 请求头中的会话 ID 列出对象 |
| POST /api/v1/setup/restore | `{sessionId,objectKey}`，复用同一恢复服务 |
| POST /api/v1/setup/initialize | 初始化全新库，仅未初始化时允许 |
| GET /api/v1/setup/operations/:id | 同一操作查询，初始化完成仍可查询 |

objectKey 必须来自配置前缀内的合法日期命名；会话 ID 不承载凭据、不替代管理员认证。设置响应绝不回传原始 Secret Access Key。

Operation 返回 `{id,kind,status,phase,databaseApplied,errorCode,updatedAt}`，并记录操作期间采样到的暂存文件与 WAL 大小、维护用时。status 为 running/completed/failed，phase 为 prepare/drain/copy/reload/done；databaseApplied 仅恢复提交后为 true。最近恢复的 `preparedAt` 表示备份准备完成的时间。轮询遇到断网继续查原 operationId，404 时先刷新系统状态。受理响应丢失时，通过状态接口核对新任务 ID。页面用“恢复期间暂不可访问项目数据”。

## 6. 实施与验收

阶段 5 连同 UI-08 一起交付，阶段 4 的 UI-06 不依赖备份后端。使用 modernc 已有 Backup API，初期无需额外备份库。先用真实驱动完成小型恢复集成验证，再接 R2 和 UI。

1. **每日备份与保留**：一致性快照可读，坏 gzip/摘要被拒绝；跨月/年/时区的 N 日窗口正确；上传失败不预删旧对象，响应丢失核对远端。
2. **设置与权限**：修改时间/保留期持久化；R2 使用专用 bucket 的对象读写令牌；其他 bucket 无权限，清理不删除其他前缀，接口/日志不泄露密钥。
3. **安装恢复**：空库从 R2 恢复后数据可用、Go PID 不变、当前 R2 连接整体覆盖旧连接、setup 写接口关闭；正常进度查询仍可完成。
4. **运行中恢复**：已有 WAL 未 checkpoint 数据、多个池连接及准备语句场景下，同一 sql.DB 可读取恢复数据；源/目标 page_size 不匹配预先拒绝。
5. **排空与失败**：已有长请求/事务、AI 调用和清理任务均受暂停约束；30 秒不能排空则不复制；复制期间业务 503、进度可读；取消/复制错误后旧库可用且不假报完成；分别覆盖普通任务、reserved 未发送、sending 未确认的 AI 取消，核对 failed/dead/unknown、租约清除及原预留名额复用；无法落状态或释放连接时不得开始复制。
6. **提交边界**：用实际 modernc 在复制中途强制终止子进程及完成后退出，重开库分别得到旧库或完整新库；新库必带本次 R2 配置和整理后的任务，不需自定义恢复日志。
7. **恢复后运行**：配置重载失败显示 databaseApplied=true，reload 可恢复；自动 AI 跨重启仍暂停，经明确确认后启用；旧付费任务不自动重放。
8. **边界输入与磁盘**：损坏文件、较新 schema、磁盘不足在相关阶段明确失败；临时文件权限和清理正确，记录真实快照/恢复磁盘峰值与停服时间。
9. **端到端 UI**：安装、立即备份、一键恢复、断线后查询、401/409/503 和恢复结果不明均可理解；恢复不会因浏览器断开而重复执行，亮暗主题与移动端正常。

文档中的语法/SQLite 实验不能代替上述 modernc、真实 R2 和浏览器验收；这些是实现阶段的交付要求。

## 7. 技术依据

- [SQLite Online Backup API：目标事务、Step/Finish、WAL page_size 限制](https://www.sqlite.org/c3ref/backup_finish.html)
- [modernc.org/sqlite：NewRestore、Backup.Step 与 Finish](https://pkg.go.dev/modernc.org/sqlite)
- [SQLite VACUUM INTO](https://www.sqlite.org/lang_vacuum.html)
- [Cloudflare R2 API 令牌权限](https://developers.cloudflare.com/r2/api/tokens/)
- [R2 S3 API 兼容性](https://developers.cloudflare.com/r2/api/s3/api/)
- [AWS SDK for Go v2：S3 客户端与签名](https://github.com/aws/aws-sdk-go-v2)
