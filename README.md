# github-radar

GitHub 开源项目热点雷达。目前提供阶段 1 的候选发现、仓库采集与持久任务队列。

## 本地启动

在仓库根目录使用 Go 1.26 或以上版本。先把 `ADMIN_TOKEN` 设为随机令牌，再运行 `go run ./cmd/radar`。PowerShell 示例会为当前终端生成令牌。

```powershell
$bytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$rng.Dispose()
$env:ADMIN_TOKEN = [Convert]::ToBase64String($bytes)
go run ./cmd/radar
```

服务默认监听 `127.0.0.1:8080`，在 `./data/github-radar.db` 建库并自动迁移。需要改变监听地址或数据库路径时，复制 [configs/example.yaml](configs/example.yaml) 并运行 `go run ./cmd/radar server -config <文件路径>`。相对路径以命令的工作目录为准。

| 接口 | 行为 |
| --- | --- |
| `GET /healthz` | 公开存活检查 |
| `GET /readyz` | 公开数据库就绪检查，连接失败时返回 503 |
| `GET /api/v1/setup/status` | 需要 `Authorization: Bearer <ADMIN_TOKEN>`，新库返回 `{"initialized":false}` |
| `POST /api/v1/setup/initialize` | 使用同一令牌初始化空库，重复初始化返回 409 |

服务启动时缺少管理员令牌会报错退出。缺少 `GITHUB_TOKEN` 时提示采集待配置。AI 地址、模型和开关通过管理接口配置，API 密钥由环境变量提供。AI 摘要在阶段 3 接入。

## 启用采集

启动前设置 `GITHUB_TOKEN`，使用可读取公共仓库的 GitHub 令牌。启动后在另一个终端使用同一个管理员令牌初始化空库。

```powershell
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/api/v1/setup/initialize -Headers @{Authorization = "Bearer $env:ADMIN_TOKEN"}
```

初始化前业务 API 返回 `503 setup_required`，业务 Worker 等待安装完成。初始化会保存关闭备份的默认设置。已有数据库重启后会自动恢复采集，普通任务租约到期可接管。缺少 GitHub 令牌时可以读取已有数据，配置令牌并重启后开始采集。

- 默认采集全站和 Go、Python、TypeScript、Rust 的 Trending 日榜、周榜、月榜，保存每个来源的完整成功批次。解析失败保留上次榜单及其真实时间。
- 每 30 分钟轮换一个 AI 主题 Search。默认限制为近 90 天更新且至少 20 Star 的公共仓库，查询中显式提供的 `pushed:`、`stars:` 条件优先。超过一千条的查询按 Star 和创建时间拆分，分页请求单独排队。
- 仓库按数字 ID 合并。新候选按容量进入 A/B/C 档，已有 S/A/B/C 按配置周期采集。基于增长指标的升降档在阶段 2 接入。
- 只有仓库详情的成功 200 或匹配已有 ETag 基线的认证 304 会产生 Star 快照。同一个五分钟计划槽保留第一条成功样本，重试沿用原槽。
- 本地预算、上游 core/search 窗口和次级限流等待都保存在 SQLite。手动采集也经过同一限流器。

采集功能复用 go-github 的 REST 数据模型、请求和分页处理，以及 goquery 的 HTML 解析。[go-github](https://github.com/google/go-github)、[goquery](https://github.com/PuerkitoBio/goquery)

## 管理配置

初始化后使用同一管理员 Bearer 令牌调用以下接口。每次写入先 GET，再提交返回的 `revision` 和修改后的完整配置。修订号已变化时返回 `409 settings_conflict`，重新读取后再提交。

| GET / PUT 路径 | PUT 请求体 | 生效方式 |
| --- | --- | --- |
| `/api/v1/system/settings` | `{revision,settings}` | HTTP 超时、SQLite 参数、GitHub API 和预算、采样策略、增长与热度参数、保留天数及维护计划保存在数据库。响应的 `restartRequired` 指示是否需要重启 |
| `/api/v1/system/categories` | `{revision,items:[{slug,name,anyTopics}]}` | 保存后，分类列表、计数和仓库筛选在下一次请求使用新映射。空列表仅保留自动归类的 `other` |
| `/api/v1/system/discovery-rules` | `{revision,items:[{sourceType,sourceKey,query,enabled,intervalMinutes,priority}]}` | 修改、关闭或删除规则会撤销该规则的旧任务；新的启用规则由下次调度扫描安排 |

Search 的 `sourceType` 为 `search`，`sourceKey` 为规则标识，`query` 为 GitHub 搜索式。Trending 的 `sourceType` 为 `trending`，`sourceKey` 如 `all:daily` 或 `language:go:weekly`，`query` 留空。设置 `enabled: false` 暂停单条规则，提交空列表可清空所有发现规则。

首次用新版服务启动时导入默认设置、[默认分类](configs/categories.yaml)和初始规则。旧 YAML 的运行参数、`categories_file` 及旧 AI 地址和模型环境变量可参与这次导入。Compose 保留旧 AI 环境变量的转发用于升级。已有的发现规则、ID 和下次运行时间优先保留。导入完成后，重启直接使用数据库，旧文件中的业务值和首次导入字段不再生效。

## 采集接口

以下接口均使用管理员 Bearer 令牌，空库需先初始化。

| 接口 | 用途 |
| --- | --- |
| `GET /api/v1/repos`、`GET /api/v1/repos/:id` | 候选与仓库元数据，支持状态、分类、语言、关注、字面搜索及游标分页 |
| `GET /api/v1/repos/:id/trend?range=7d` | 24h 和 7d 返回原始观测，30d 返回小时点，180d 返回日点 |
| `GET /api/v1/categories` | 当前数据库分类及 active 仓库数量 |
| `GET /api/v1/trending?period=daily&sourceKey=all` | 单来源最新完整榜单 |
| `POST /api/v1/repos/:id/refresh` | 请求体 `{"requestId":"一次操作的唯一值"}`，返回可轮询的任务 |
| `POST /api/v1/repos/:id/follow` | 请求体 `{"follow":true}`，取消关注使用 false |
| `GET /api/v1/jobs/:id`、`GET /api/v1/system/jobs` | 任务结果和最近任务，错误只返回结构化码 |
| `GET /api/v1/system/limits` | 配额、暂停时间、本地计数和等待任务数 |
| `GET /api/v1/system/storage` | SQLite 和 WAL 大小、快照与聚合行数、维护水位 |
| `POST /api/v1/system/maintenance/run` | 返回 202 并持久化维护请求，下次扫描执行 |

`/repos` 默认只返回 active，查询候选使用 `?state=candidate`。排序支持 discovered、stars、followed、hotness、growth24h，followed 排序要求 `followed=true`。增长榜使用精确的 24h 净增，热度排名基于全部 active 仓库。增长窗口会返回质量及实际采样时间，缺失增量为 `null`。卡片展示最近 24 个已完成 UTC 小时的累计 Star，缺测小时保留空点。

需要独立处理一批任务时运行 `go run ./cmd/radar collect-once`，支持 `-config` 选项，需要已初始化数据库和 GitHub 令牌。该命令最多处理一百个当前可领取任务，未到期或受限流影响的任务保留在数据库。按设计使用单实例，同一数据库的服务运行期间应由 HTTP 刷新接口安排任务。

## 指标与数据维护

服务默认每 15 分钟计算增长、热度及采样等级，整点聚合小时数据，01:00 UTC 聚合日数据，03:00 UTC 清理过期记录。原始快照、小时聚合和日聚合默认分别保留 14、60、180 天，清理前检查下一级聚合水位和实际观测锚点。趋势中的缺测点保持为 `null`。

后台可通过 `/api/v1/system/settings` 的 `scheduler.metricsIntervalMinutes`、`metrics` 和 `maintenance` 修改计算、评分和保留参数，保存后重启生效。旧数据库升级会补齐新增参数的默认值，并保留已有设置。维护可在缺少 GitHub 令牌的情况下运行。离线执行一轮可使用 `go run ./cmd/radar maintenance-once`，支持 `-config`，需要数据库已完成安装。

## 迁移与验证

`go run ./cmd/radar migrate` 可以离线升级表结构，支持相同的 `-config` 选项。首次业务配置在服务启动时导入，离线迁移只应用新的 SQL 版本。Goose 管理随二进制分发的 SQL 迁移，GORM 通过 libtnb/sqlite 复用 modernc.org/sqlite 的连接池。[Goose](https://github.com/pressly/goose)、[GORM](https://github.com/go-gorm/gorm)、[libtnb/sqlite](https://github.com/libtnb/sqlite)、[modernc.org/sqlite](https://github.com/modernc-org/sqlite)

```sh
go test ./...
go vet ./...
go build ./...
```

## Docker

设置 `ADMIN_TOKEN` 后运行 `docker compose -f deploy/compose.yaml up --build`。Compose 将端口发布到宿主机回环地址，并通过 `radar-data` 卷保存数据库。

## 设计文档

- [主技术设计与实施计划](docs/github-radar-technical-design.md#12-分期开发任务与交付顺序)
- [AIHOT 视觉与交互适配](docs/aihot-ui-adaptation.md)
- [SQLite 备份与恢复](docs/backup-and-restore.md)
