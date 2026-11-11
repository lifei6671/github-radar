# github-radar

GitHub 开源项目热点雷达，提供 Star 增长、GitHub Trending、中文摘要和选题管理。

## 本地启动

在仓库根目录使用 Go 1.26 及以上和 Node.js 22.12 及以上版本。先构建前端，再复制部署配置。

```powershell
npm --prefix web ci
npm --prefix web run build
Copy-Item configs/example.yaml configs/radar.local.yaml
```

参照 [带注释的配置示例](configs/example.yaml)，在 `configs/radar.local.yaml` 中填入网站对外地址、管理员密码和会话签名密钥。密码和签名密钥分别生成，例如运行 `openssl rand -hex 24` 和 `openssl rand -hex 32`。本地配置包含真实凭据，请限制文件读取权限。

```powershell
go run ./cmd/radar server -config configs/radar.local.yaml
```

示例监听 `127.0.0.1:8080`，在 `./data/github-radar.db` 建库并自动迁移。相对路径以命令的工作目录为准。升级已有部署时沿用原数据库路径，在原 YAML 中补齐 `server.site_url`、`auth.admin_password`、`auth.session_secret`，移除旧的 `auth.admin_token_env`，重启后用新密码登录。

在浏览器打开 `http://127.0.0.1:8080` 即可浏览前台。从「后台管理」或 `/admin` 进入后台，输入 YAML 中设置的管理员密码。空库首次登录后进入 `/admin/setup`。Gin 从 `web/dist` 提供页面与静态资源，运行时只需 Go 服务。开发前端时可另开终端执行 `npm --prefix web run dev`，Vite 默认将 API 转发到本地 8080 端口。

| 接口 | 行为 |
| --- | --- |
| `GET /healthz` | 公开存活检查 |
| `GET /readyz` | 公开数据库就绪检查，连接失败时返回 503 |
| `GET /api/v1/public/repos`、`/repos/by-name/:owner/:name`、`/repos/:id/trend` | 公开项目列表、详情与 Star 趋势，路径共用 `/api/v1/public` 前缀 |
| `GET /api/v1/public/categories`、`/trending`、`/trending/sources` | 公开分类、官方榜单和榜单来源标识，路径共用 `/api/v1/public` 前缀 |
| `POST /api/v1/auth/login` | 提交 `{"password":"…"}`，成功后签发签名会话 Cookie |
| `GET /api/v1/auth/session`、`POST /api/v1/auth/logout` | 查询登录状态、清除当前浏览器的会话 Cookie |
| `GET /api/v1/setup/status` | 需要管理员会话，新库返回 `{"initialized":false}` |
| `POST /api/v1/setup/initialize` | 使用同一会话初始化空库，重复初始化返回 409 |

网站对外地址、管理员密码和会话签名密钥为 HTTP 服务的必填项。会话有效期为 24 小时，修改密码或签名密钥并重启后需要重新登录。缺少 `GITHUB_TOKEN` 时提示采集待配置。AI 地址、模型和开关通过管理接口配置，API 密钥由环境变量提供。

## 启用采集

使用可读取公共仓库的 GitHub 令牌，在启动服务的终端设置 `GITHUB_TOKEN`。后台「运行状态 → 运行配置 → GitHub 采集」可查看或修改环境变量名。设置后重启服务。新部署可在后台初始化空库，或用同一 HTTP 会话调用初始化接口。以下 PowerShell 7 示例适用于本地 HTTP 部署。

```powershell
$password = Read-Host "管理员密码" -MaskInput
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/api/v1/auth/login -ContentType 'application/json' -Body (@{password = $password} | ConvertTo-Json -Compress) -SessionVariable radarSession
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/api/v1/setup/initialize -WebSession $radarSession
Remove-Variable password
```

初始化前业务 API 返回 `503 setup_required`，业务 Worker 等待安装完成。初始化会保存关闭备份的默认设置。已有数据库重启后会自动恢复采集，普通任务租约到期可接管。缺少 GitHub 令牌时可以读取已有数据，配置令牌并重启后开始采集。

后台提供仓库管理、重点监控、选题池和运行状态。旧 `/system`、`/watchlist`、`/setup` 链接继续跳转到对应后台页。前台专题目录位于 `/topics`。重点监控、任务、配置与备份需管理员会话。

- 默认采集全站和 Go、Python、TypeScript、Rust 的 Trending 日榜、周榜、月榜，保存每个来源的完整成功批次。解析失败保留上次榜单及其真实时间。
- 每 30 分钟轮换一个 AI 主题 Search。默认限制为近 90 天更新且至少 20 Star 的公共仓库，查询中显式提供的 `pushed:`、`stars:` 条件优先。超过一千条的查询按 Star 和创建时间拆分，分页请求单独排队。
- 仓库按数字 ID 合并。新候选按容量进入 A/B/C 档，已有 S/A/B/C 按配置周期采集。基于增长指标的升降档在阶段 2 接入。
- 只有仓库详情的成功 200 或匹配已有 ETag 基线的认证 304 会产生 Star 快照。同一个五分钟计划槽保留第一条成功样本，重试沿用原槽。
- 本地预算、上游 core/search 窗口和次级限流等待都保存在 SQLite。手动采集也经过同一限流器。

采集功能复用 go-github 的 REST 数据模型、请求和分页处理，以及 goquery 的 HTML 解析。[go-github](https://github.com/google/go-github)、[goquery](https://github.com/PuerkitoBio/goquery)

## 管理配置

初始化后使用同一管理员会话调用以下接口。数据库保存一份当前配置。`revision` 用于防止并发编辑互相覆盖。每次写入先 GET，再提交返回的 `revision` 和修改后的完整配置。计数已变化时返回 `409 settings_conflict`，重新读取后再提交。

| GET / PUT 路径 | PUT 请求体 | 生效方式 |
| --- | --- | --- |
| `/api/v1/system/settings` | `{revision,settings}` | HTTP 超时、SQLite 参数、GitHub API 和预算、采样策略、增长与热度参数、保留天数及维护计划保存在数据库。响应的 `restartRequired` 指示是否需要重启 |
| `/api/v1/system/categories` | `{revision,items:[{slug,name,anyTopics}]}` | 保存后，分类列表、计数和仓库筛选在下一次请求使用新映射。空列表仅保留自动归类的 `other` |
| `/api/v1/system/discovery-rules` | `{revision,items:[{sourceType,sourceKey,query,enabled,intervalMinutes,priority}]}` | 修改、关闭或删除规则会撤销该规则的旧任务；新的启用规则由下次调度扫描安排 |

Search 的 `sourceType` 为 `search`，`sourceKey` 为规则标识，`query` 为 GitHub 搜索式。Trending 的 `sourceType` 为 `trending`，`sourceKey` 如 `all:daily` 或 `language:go:weekly`，`query` 留空。设置 `enabled: false` 暂停单条规则，提交空列表可清空所有发现规则。

首次用新版服务启动时导入默认设置、[默认分类](configs/categories.yaml)和初始规则。旧 YAML 的运行参数、`categories_file` 及旧 AI 地址和模型环境变量可参与这次导入。Compose 保留旧 AI 环境变量的转发用于升级。已有的发现规则、ID 和下次运行时间优先保留。导入完成后，重启直接使用数据库，旧文件中的业务值和首次导入字段不再生效。

## 采集接口

以下接口均使用管理员会话，空库需先初始化。

| 接口 | 用途 |
| --- | --- |
| `GET /api/v1/repos`、`GET /api/v1/repos/:id` | 候选与仓库元数据，支持状态、分类、语言、关注、字面搜索及游标分页 |
| `PATCH /api/v1/repos/:id/block` | 管理员提交 `{"blocked":true}` 屏蔽仓库，提交 false 解除 |
| `GET /api/v1/repos/:id/trend?range=7d` | 24h 和 7d 返回原始观测，30d 返回小时点，180d 返回日点 |
| `GET /api/v1/categories` | 当前数据库分类及 active 仓库数量 |
| `GET /api/v1/trending?period=daily&sourceKey=all` | 单来源最新完整榜单 |
| `POST /api/v1/repos/:id/refresh` | 请求体 `{"requestId":"一次操作的唯一值"}`，返回可轮询的任务 |
| `POST /api/v1/repos/:id/follow` | 请求体 `{"follow":true}` 加入重点监控，使用 false 移出 |
| `GET /api/v1/jobs/:id`、`GET /api/v1/system/jobs` | 任务结果和最近任务，错误只返回结构化码 |
| `GET /api/v1/system/limits` | 配额、暂停时间、本地计数和等待任务数 |
| `GET /api/v1/system/storage` | SQLite 和 WAL 大小、快照与聚合行数、维护水位 |
| `POST /api/v1/system/maintenance/run` | 返回 202 并持久化维护请求，下次扫描执行 |

公开项目列表默认返回 active，后台默认同时返回已屏蔽仓库，便于解除。可用 `?blocked=true` 在后台筛选屏蔽仓库，查询候选使用 `?state=candidate`。屏蔽后暂停发现更新、Star 采样、README 读取和 AI 处理，已有记录仍可在后台查看。解除后按监控容量恢复采样。排序支持 discovered、stars、followed、hotness、growth24h、growth7d，followed 排序要求 `followed=true`。增长榜使用精确窗口，热度排名基于全部 active 仓库。增长窗口会返回质量及实际采样时间，缺失增量为 `null`。卡片展示最近 24 个已完成 UTC 小时的累计 Star，缺测小时保留空点。

需要独立处理一批任务时运行 `go run ./cmd/radar collect-once`，支持 `-config` 选项，需要已初始化数据库和 GitHub 令牌。该命令最多处理一百个当前可领取任务，未到期或受限流影响的任务保留在数据库。按设计使用单实例，同一数据库的服务运行期间应由 HTTP 刷新接口安排任务。

## 指标与数据维护

服务默认每 15 分钟计算增长、热度及采样等级，整点聚合小时数据，01:00 UTC 聚合日数据，03:00 UTC 清理过期记录。原始快照、小时聚合和日聚合默认分别保留 14、60、180 天，清理前检查下一级聚合水位和实际观测锚点。趋势中的缺测点保持为 `null`。

后台可通过 `/api/v1/system/settings` 的 `scheduler.metricsIntervalMinutes`、`metrics` 和 `maintenance` 修改计算、评分和保留参数，保存后重启生效。旧数据库升级会补齐新增参数的默认值，并保留已有设置。维护可在缺少 GitHub 令牌的情况下运行。离线执行一轮可使用 `go run ./cmd/radar maintenance-once`，支持 `-config`，需要数据库已完成安装。

## 中文摘要和选题

在后台「运行状态 → 运行配置 → AI 中文摘要」启用摘要、填写 Base URL 和模型后保存。仅支持 OpenAI 兼容供应商，Base URL 需指向支持 Chat Completions JSON 模式与 `max_completion_tokens` 的 API 根路径，例如供应商提供的 `/v1` 路径。密钥通过服务进程的 `AI_API_KEY` 环境变量提供，README 检查使用 `GITHUB_TOKEN`，配置完成后重启服务。未生成摘要的仓库展示 GitHub About 中的 Description。每个 UTC 自然日默认允许 50 次自动调用和 20 次手动调用，失败后的新调用也占预算。

GitHub Topics 为空的活跃仓库会在同一模型预算内按调度补充主题词。系统从 GitHub About 和公开 README 提取至多五个主题词，详情页标为「AI Topics」，后台分类映射自动使用这些词。GitHub 后续补上 Topics 时，分类和详情随采集数据切换到原生主题。模型未配置时继续展示 GitHub 简介。

仓库详情在 AI 导读下展示 GitHub README。中文 README 直接显示正文，英文 README 可在译文生成后切换「中文」和「原文」。系统使用剩余自动额度为已有摘要的活跃仓库准备译文，管理员也可在详情手动生成。译文按 README 版本分批保存，访客读取不会调用模型。

| 接口 | 用途 |
| --- | --- |
| `POST /api/v1/repos/:id/summary` | 提交 `{requestId,force}` 生成摘要或复用已验证的缓存，异步任务通过 `statusUrl` 查询 |
| `POST /api/v1/jobs/:id/retry` | 对调用结果未知的摘要任务提交 `{requestId,acceptPossibleCharge:true}`，确认可能再次计费并重新排队 |
| `GET /api/v1/topics` | 查询每日选题，支持状态筛选与游标分页 |
| `POST /api/v1/repos/:id/topic` | 从仓库详情加入选题池，重复加入保留已有审核状态 |
| `PATCH /api/v1/topics/:id` | 提交 `{status,feedback}` 更新人工状态或反馈，`candidate` 可手动重新打开选题 |

仓库详情返回中文摘要、来源质量、模型和生成时间。来源 SHA 及供应商、模型、提示词版本匹配时复用缓存，README 正文只在内存中处理。Markdown 提取和语言识别分别复用 [Goldmark](https://github.com/yuin/goldmark) 和 [Whatlanggo](https://github.com/abadojack/whatlanggo)。

选题默认在每天北京时间 08:30 后生成，覆盖精确增长、近期上榜、日增长和关注信号。`settings.recommendations` 管理时间、数量、重新入池门槛和普通选题保留天数。已处理项目在 README 与功能证据同时变化、精确 24h 净增明显抬升或人工重新打开时回到候选，保留人工反馈。

## R2 备份与恢复

在「运行状态 → 备份与恢复」填写 R2 端点、存储桶、备份前缀和对象读写凭据。备份计划和 R2 设置由 SQLite 保存，默认关闭。启用后默认每天北京时间 03:00 备份，上传成功后及 03:30 清理，保留当天及之前 4 个自然日。后台可调整时间、时区和 1～30 天的保留期。

「立即备份」会更新同一天的云端对象。也可在服务运行时执行 `go run ./cmd/radar backup -config configs/radar.local.yaml`，该命令使用同一配置文件中的管理员密码登录本机 API 并等待结果。离线维护、迁移和采集命令应在停止服务后运行。

恢复时选定日期并确认覆盖。服务在校验下载文件后短暂停止业务访问，继续提供进度查询。恢复完成后自动 AI 摘要保持暂停，在管理页确认用量记录可能不完整后再启用。迁移到新实例时，登录后在 `/admin/setup` 输入该实例的 R2 凭据并选择备份，目标实例继续使用自己的部署配置和环境变量。

使用专用私有存储桶和仅限该桶的 Object Read & Write 令牌。备份包含数据库内的 R2 凭据，请限制数据库目录、暂存文件和 R2 对象的访问权限。R2 请求由 AWS SDK for Go v2 签名，接口与恢复边界见专项方案。[Cloudflare R2 令牌权限](https://developers.cloudflare.com/r2/api/tokens/)、[AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2)、[备份专项方案](docs/backup-and-restore.md)

## 迁移与验证

`go run ./cmd/radar migrate` 可以离线升级表结构，支持相同的 `-config` 选项。首次业务配置在服务启动时导入，离线迁移只应用新的 SQL 版本。Goose 管理随二进制分发的 SQL 迁移，GORM 通过 libtnb/sqlite 复用 modernc.org/sqlite 的连接池。[Goose](https://github.com/pressly/goose)、[GORM](https://github.com/go-gorm/gorm)、[libtnb/sqlite](https://github.com/libtnb/sqlite)、[modernc.org/sqlite](https://github.com/modernc-org/sqlite)

```sh
go test ./...
go vet ./...
go build ./...
npm --prefix web run check
npm --prefix web run build
npx --prefix web playwright install chromium
npm --prefix web test
```

浏览器测试覆盖 390、768、1440、1920 像素视口下的亮暗主题和关键流程。测试截图写到 `web/test-results`。前端参考与分发许可见 [第三方声明](web/THIRD_PARTY_NOTICES.md)。

### 整体验收

`go test ./cmd/radar -run '^TestAcceptanceBusiness$' -v -count=1` 使用独立 SQLite 和实际 Go 服务进程验证阶段 0～3。GitHub 与模型请求由本地 HTTP 服务提供固定响应，采集、摘要 Worker、Gin API 和数据库均走项目实际代码。

构建前端并安装 Chromium 后，可以启用 Compose 和真实 R2 验收。下面的 PowerShell 示例使用专用测试桶的本地凭据文件。

```powershell
npm --prefix web run build
npx --prefix web playwright install chromium
$env:RADAR_ACCEPTANCE_BROWSER = '1'
$env:RADAR_ACCEPTANCE_DOCKER = '1'
$env:RADAR_ACCEPTANCE_R2_FILE = (Get-Item -LiteralPath 's3-r2-test.local.json').FullName
go test ./cmd/radar -run '^TestAcceptance(Business|Compose|LiveR2)$' -v -count=1 -timeout 10m
```

R2 凭据文件读取 `endpoint`、`bucket`、`prefix` 及 `primary.access_key_id` 和 `primary.secret_access_key`。文件可放在仓库外，仓库根目录的 `s3-r2-test.local.json` 已加入 Git 和 Docker 忽略规则。也可通过 `RADAR_ACCEPTANCE_R2_ENDPOINT`、`RADAR_ACCEPTANCE_R2_BUCKET`、`RADAR_ACCEPTANCE_R2_ACCESS_KEY_ID`、`RADAR_ACCEPTANCE_R2_SECRET_ACCESS_KEY` 和可选的 `RADAR_ACCEPTANCE_R2_PREFIX` 提供连接。

Compose 使用本机随机端口和专用数据卷，重建容器后核对持久化。真实 R2 用例在唯一测试子前缀下写入六个日期对象来验证五日保留，结束时清理该前缀。浏览器经实际页面完成安装和后台恢复。技术设计要求的 7～14 天生产观察需在目标环境连续记录。

## Docker

将 [容器配置示例](deploy/container.yaml) 复制为 `deploy/radar.local.yaml`，填入网站对外地址和两项登录凭据，再运行 `docker compose -f deploy/compose.yaml up --build`。也可用 `RADAR_CONFIG_FILE` 指定实际配置的绝对路径。Compose 以只读方式挂载该文件，容器进程使用 UID `10001`，在 Linux 上需授予该 UID 读取权限。镜像运行时由 Gin 提供 UI 与 API，默认将端口发布到宿主机回环地址，并通过 `radar-data` 卷保存数据库。

## 设计文档

- [主技术设计与实施计划](docs/github-radar-technical-design.md#12-分期开发任务与交付顺序)
- [AIHOT 视觉与交互适配](docs/aihot-ui-adaptation.md)
- [SQLite 备份与恢复](docs/backup-and-restore.md)
