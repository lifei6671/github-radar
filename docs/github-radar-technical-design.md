# GitHub 热点雷达：技术设计与实施计划

- 版本：1.7.0
- 日期：2026-11-11
- 目标仓库：github-radar
- 开发状态：待实现
- 技术基线：Go + Gin + SQLite + React + TypeScript + Vite
- 前端专项设计：[AIHOT 视觉与交互适配方案](./aihot-ui-adaptation.md)
- 备份与迁移：[SQLite 备份和 VPS 一键恢复方案](./backup-and-restore.md)
- 前端参考：https://github.com/KKKKhazix/AIHOT（整体布局、导航、时间线和视觉交互）
- 运行方式：单机、单实例、单 GitHub 认证身份
- 主要目标：及时发现 GitHub 高增长仓库，自动生成适合中文读者的 AI 项目摘要和选题候选

## 1. 产品目标与边界

### 1.1 面向的问题

雷达需要回答四个具体问题：

1. 今天有哪些新出现、增长速度异常的仓库？
2. 哪些仓库在最近数小时或数天持续增长，哪些仅仅短期爆发？
3. 非中文仓库到底做什么？能否直接阅读 200～500 字的准确中文总结？
4. 哪些项目值得进一步复现和用于公众号选题？

**核心链路：发现候选 → 仓库建档 → 分级跟踪 Star → 计算趋势 → 筛选非中文候选 → 临时获取 README → AI 总结 → 展示/收藏选题。**

本系统是单用户的热点发现与选题辅助工具，优先保证数据时效、解释性、低运行成本和简单部署。

### 1.2 固定范围

- 使用 GitHub Trending 网页榜单、GitHub 仓库搜索和已发现仓库定期更新作为三个发现/采样入口。
- 每次仓库元数据采集读取当前 **stargazers_count**，按实际时间写入短期 Star 快照。
- 自动计算 24 小时、7 天等窗口的增长数据，并显示数据覆盖质量。
- 使用 Star 动量、加速度、上榜情况、持续性等信号发现热点；项目适用性与热度分别评分。
- 非中文仓库按条件触发大模型摘要，产出**中文 200～500 字的自然语言项目介绍**及少量结构化标签。
- **README 原文仅在分析任务和详情读取时保存在进程内存；SQLite 只缓存当前版本的译文片段。**
- 数据库只保留仓库元信息、短期 Star 快照、聚合趋势、榜单观测、中文摘要、选题元数据和任务状态。
- 原始 Star 快照到期删除；趋势聚合有限期保留；每天自动备份 SQLite 到 R2，并保留最近 5 个自然日的每日备份，超期自动清理；后台可编辑备份配置及一键恢复。
- 支持人工收藏、忽略、触发重新总结以及手动采集。

### 1.3 非目标

- 不做多版本 README 翻译档案、代码克隆与源码全文分析。
- 不保存完整 README 原文和大模型完整请求或响应。
- 不追踪 Star 用户列表，也不将 GitHub 搜索的累计 Star 排名冒充“最近新增 Star”榜单。
- 不采集全量 GitHub 活动事件，不使用 Kafka、Redis、分布式调度与微服务。
- 不自动发布公众号文章，也不声称 AI 总结等同于代码实测。
- 第一版不追求分钟级、全网无遗漏的发现能力；发现延迟取决于上游榜单/搜索覆盖范围。

### 1.4 验收目标

- 以 5,000 个活跃监控仓库为设计规模；可从 1,000 个逐步扩容。
- 5 分钟一次任务调度；S/A/B/C 仓库分别约 15 分钟、1 小时、6 小时、24 小时采集。
- Trending 日榜约 1 小时刷新一次；AI 主题搜索约 30 分钟轮换一次。
- 默认常规 REST 配额软预算 2,500 次/小时；根据真实 GitHub 响应头动态节流。
- 定时任务普通重启后可续跑；重复投递不会产生重复采样结果或重复选题。AI 结果幂等与供应商计费分开保证：结果未知的调用禁止盲目重试，不承诺所有供应商都能避免重复计费。备份恢复按独立规则重建任务。
- 中文摘要目标 200～500 字；证据不足时允许更短，不补造功能。
- 原始 Star 快照保留 14 天，小时聚合保留 60 天，日聚合保留 180 天。
- 任何处理路径（包括失败日志、数据库、崩溃诊断）都不主动持久化完整 README。

## 2. 系统结构与技术选型

本版实施决策（2026-11-11）：

| 问题 | 一期决定 | 实现前置阶段 |
| --- | --- | --- |
| 分类 | SQLite 保存可编辑的 GitHub topics 映射，YAML 仅提供初始数据 | 阶段 1，UI-03/04 前 |
| 配置 | 后台维护分类、发现规则和运行参数，部署 YAML 保存监听地址、网站对外地址、数据库路径和管理员登录配置 | 阶段 1 |
| 采样槽 | UTC 五分钟固定网格；等级只影响调度频率，不改变网格 | 阶段 1 |
| 仓库状态 | candidate/active/paused/unavailable/address_pending，转换见 3.4 | 阶段 1 |
| 鉴权 | 单管理员密码登录，签名 HttpOnly Cookie；首次安装共用同一会话 | 阶段 0 |
| 元数据 | 保存 license_spdx_id、license_name、followed_at | 阶段 1 |
| 备份前端 | UI-06 只做业务管理，UI-08 在阶段 5 实现备份恢复 | 阶段 4/5 |
| 恢复 | 保留同进程一键恢复，使用 SQLite 原生 Backup API；删除文件交换及自定义恢复日志 | 阶段 5 |
| 依赖 | SQLite 固定 modernc.org/sqlite；前端使用 TanStack Query，统一请求缓存与轮询 | 阶段 0 |

上述是本期选型，不新增并行兼容实现。具体依赖版本在建立 go.mod/package.json 时锁定，并用本项目测试验证。

~~~text
GitHub Trending HTML ─┐
GitHub Search API ────┼─> Discovery ─> repositories ─> Scheduler ─> Github HTTP Client
Manual Follow ───────┘                     │                       │
                                          │                  Rate Limiter
                                          │                       │
                                          └─> SQLite Jobs <─> Workers
                                                               │
                                  ┌────────────────────────────┼──────────────────────┐
                                  │                            │                      │
                              Star Samples                Hotness Metrics         AI Summary
                                  │                            │                      │
                                  └──────── SQLite ────────────┴──────────────────────┘
                                                     │
                                         Gin API + React 看板
                                                     │
                                          榜单 / 趋势 / 中文摘要
~~~

建议代码结构：

~~~text
github-radar/
├── cmd/radar/main.go
├── internal/
│   ├── config/                配置加载、校验
│   ├── github/                REST Client、Search、Repository、README、RateLimit
│   ├── trending/              HTML 抓取、字段规范化、解析器
│   ├── discovery/             发现源与候选去重
│   ├── scheduler/             Cron、任务领取、租约与重试
│   ├── collector/             仓库元数据和短期 Star 采样
│   ├── metrics/               增量、加速度、持续性、评分、覆盖质量
│   ├── summary/               语言识别、README 临时解析、大模型 Provider、结构化输出
│   ├── topics/                选题生成、状态与人工反馈
│   ├── storage/               SQLite、GORM 显式映射、查询和事务
│   ├── settings/              数据库配置、首次导入及修订校验
│   ├── maintenance/           聚合、清理与数据库优化
│   ├── backup/                SQLite 在线快照、R2 上传清理、同进程恢复协调
│   ├── api/                   Gin 路由、鉴权、分页及校验
│   └── observability/         slog、指标与健康检查
├── migrations/
├── web/                       React + TypeScript + Vite + Tailwind CSS 4 + ECharts
├── configs/example.yaml
├── deploy/Dockerfile
├── deploy/compose.yaml
├── docs/
│   ├── github-radar-technical-design.md
│   ├── aihot-ui-adaptation.md
│   └── backup-and-restore.md
└── tests/
~~~

依赖建议：
- Go 近期稳定版，Gin，database/sql。
- SQLite 驱动使用纯 Go 的 modernc.org/sqlite，通过 database/sql 访问，免 CGO；恢复复用其 NewRestore/Backup API，不同时支持第二种驱动。
- robfig/cron/v3、goquery、Go 标准 HTTP 客户端或 resty、log/slog。
- React + TypeScript + Vite + Tailwind CSS 4 + React Router 客户端路由 + ECharts。页面布局与交互参考 AIHOT，按本项目仓库数据重写业务组件；后端嵌入构建后的静态资源。前端不保留 AIHOT 的 Node SSR、Fastify、PostgreSQL 或 pg-boss。
- GORM 显式映射表与列，Goose 管理版本化 SQL 迁移。每个持久化模型提供对应的 `ModelColumns`，Go 中构造查询条件、排序、更新和冲突键时引用表名常量及字段映射。
- 查询优先使用简单的条件、索引和参数绑定。原子任务领取、跨层保留检查等场景保留必要的复合 SQL。SQLite 专有语法用于通用 SQL 无法满足现有需求的场景。状态、任务类型、操作阶段和错误码等业务标识统一定义在 `internal/consts`。
- 浏览器端使用 TanStack Query 管理请求缓存、失效和任务轮询。fetch 封装处理管理会话、JSON 和错误解析。

部署形态：单 Go 进程运行 API、调度器、GitHub 工作池、AI 工作池和维护任务；SQLite 存于持久化卷。第一版禁止多个进程同时对同一套任务进行调度，数据库仍需具备租约和幂等防护以处理重启场景。

## 3. 数据发现与采集

### 3.1 三种数据来源

**A. Trending 页面**

- 每 60 分钟：全站每日榜单及有限的重点语言榜单（如 Go、Python、TypeScript、Rust）。
- 每 6 小时：全站周榜、月榜及少量重点语言榜单。
- 解析仓库全名、榜单窗口、上榜排名、窗口内显示的 Star 增量、抓取时间。
- 抓取结果只用于“GitHub Trending 上榜观测”，不是官方的完整 Star 时间序列。
- HTML 结构变化、页面受限或解析数量异常时，保留旧榜单、记录失败并报警，不以空结果覆盖有效数据。
- Trending 页面没有专门的官方 REST API；抓取器与其他数据源解耦，带可测试的 HTML 固定样例。

**B. Repository Search API**

- 每 30 分钟轮换一批 AI 主题搜索，如 ai-agent、mcp、agent-skill、coding-agent、browser-automation、llm、rag、tts 等。
- 用 GitHub 支持的 stars、created、pushed、topic、language 等查询限定范围；按窗口分页与分片。
- 不能用按累计 stars 排序的查询直接计算最近增长量；新增 Star 必须来自快照差值，或单独标记的官方历史统计。
- 单次搜索最多可返回的结果存在上限；对拥挤查询按时间/Star 范围拆分，去重后入库。
- 单次批次限制请求数量，并避免同一个主题在短时间内反复翻页抓取。
- Search 使用独立的限流资源；通过响应头确定当前窗口，而非假设与 core 共同拥有 5,000/小时。

**C. 已发现仓库持续采集**

- 对所有已激活项目调用 GET /repos/{owner}/{repo}。
- 以 API 返回的**数字仓库 ID**为主键；支持 owner/repo 改名迁移。
- 每次按名称采集都核对响应 repo_id；名称被其他仓库复用时，不把新仓库的 Star、摘要或采样写入旧 ID，按第 5 章解除旧名称映射。
- 保存 stargazers_count、forks_count、description、topics、language、created_at、pushed_at、archived，以及 license.spdx_id/name；GitHub license 缺失时两个许可字段为 null，NOASSERTION 原样保存并展示“未识别许可”，不推断许可。
- 每次成功采集（200 或可证明资源未变化的认证 304）记录本次观察的 Star 快照；失败不写采样成功时间。
- 新仓库第一次采集只有基线，历史趋势记为“待建立”，不能把历史累计 Star 当作最近增长。

### 3.2 可选的 Star 历史回填

GitHub 2026-03-10 API 版本提供 GET /repos/{owner}/{repo}/stargazers/history，按周返回每日新增 Star。可用于**新发现且热度较高的仓库**补充历史背景。

- 第一阶段不作为依赖；第三阶段可为每日前 50 个新候选限额回填一次。
- 明确区分该接口“每日新增 Star”与快照累计总数差值的“净变化”。
- 文档说明该历史接口的周/日边界不保证按 UTC 对齐；显示时单独标注来源和时间口径。
- 历史回填失败不妨碍实时热度评分；不可凭空补造缺失窗口。

### 3.3 项目身份、重复来源与种子数据

- Trending、搜索、人工关注都进入同一仓库主表，使用 repo_id 合并。
- 每种发现源产生独立的发现/上榜观测数据。
- Trending 每次成功解析按 source_key + window + observed_slot 原子保存完整批次，包括成功空榜的批次标记；先按数量异常规则判定成功或失败。最新批次标记保存在 maintenance_state，查询仅取该批次成员，不为未上榜项目沿用历史名次；失败保持上次成功批次及其时间。
- 初始可从全站热门、AI 关键词搜索填充 1,000 个仓库。
- 在后台编辑 `discovery_rules` 的开关、搜索式、周期和优先级。未变规则保留其 ID 和下次运行时间；修改、关闭或删除规则会在同一事务撤销旧任务租约。新的启用规则由下次扫描安排。
- 用户关注项目应有强制保留标志，避免因低热度被自动退监控。

### 3.4 仓库状态与分类

`state` 仅表示采集生命周期，archived/disabled 是 GitHub 元数据，关注是独立用户状态：

| 状态 | 含义 | 允许的转换与触发方 |
| --- | --- | --- |
| candidate | 已发现、尚未准入，不周期采样 | 准入器/人工关注有容量时 → active；确认不存在 → unavailable；身份冲突 → address_pending |
| active | 占监控容量并周期采样 | 准入器退监控 → paused；确认不可访问 → unavailable；名称复用 → address_pending |
| paused | 因容量暂停，保留已有历史 | 准入器/人工关注有容量时 → active；确认不可访问 → unavailable；名称复用 → address_pending |
| unavailable | 确认 404/410 或仓库 disabled | 重新发现/人工刷新取得同 ID 且 disabled=false 的 200 后，经容量检查 → active，容量不足 → paused |
| address_pending | 原地址返回其他 ID，停止按旧名称采集 | 发现源或人工确认同 ID 的新地址后，经容量检查 → active，容量不足 → paused |

网络失败、403、429 不迁移状态；首次 404 写 first_not_found_at 并让下次确认不早于一小时后；后续 404 距该时刻至少一小时才置 unavailable，同 ID 的 200/有基线 304 清除此字段，410/disabled 可直接确认。取消关注仅清空关注标记，不立即停监控。`candidate` 30 天未再次发现且无关注/人工选题可清理；其余状态不套用“从未激活候选”清理。状态变化、容量检查和相应任务创建/取消在短事务内完成。

分类规则保存在 SQLite 的 `categories` 表，管理员可更新名称、顺序及 topics 映射。`configs/categories.yaml` 仅提供首次导入数据，每项为 `{slug,name,any_topics}`。slug 唯一且稳定，topics 转小写后精确匹配，命中任一项即归入该分类，可多分类，未命中归入保留分类 `other`。初始映射：

| slug | 名称 | any_topics |
| --- | --- | --- |
| agent | AI 智能体 | ai-agent, ai-agents, autonomous-agents |
| coding | AI 编程 | coding-agent, ai-coding, code-generation |
| mcp | MCP | mcp, mcp-server, model-context-protocol |
| media | AI 多媒体 | text-to-speech, tts, text-to-image, text-to-video |
| tools | 开发工具 | developer-tools, devtools, cli |

原始 topics 保存在 repositories.topics_json。后台保存后，后续请求按数据库里的当前映射派生分类。GET /api/v1/categories 返回 `{items:[{slug,name,count,latestRepo}]}`。count 只统计 active 仓库，包含 other。latestRepo 返回同一分类下最新发现的 active 仓库，字段为 id、fullName、description 和 discoveredAt。没有仓库时返回 null。列表 category 支持重复参数，多选取并集，未知 slug 返回 400。同一请求用同一份映射生成筛选条件和展示字段。

## 4. 采样等级、请求预算与调度

### 4.1 默认采样频率

| 等级 | 初始容量 | 采样周期 | 每小时约需仓库请求 |
| --- | ---: | --- | ---: |
| S：爆发期 | 200 | 15 分钟 | 800 |
| A：活跃期 | 800 | 1 小时 | 800 |
| B：普通观察 | 2,000 | 6 小时 | 333 |
| C：低活跃 | 2,000 | 24 小时 | 83 |
| 合计 | 5,000 |  | 2,016 |

这些数字只估算仓库详情请求，不包含 Search、Trending、README、重试及首次历史回填。普通已认证用户 REST 主配额通常为 5,000 次/小时；该额度也可能与用户的其他应用调用共享，不应按“独立令牌”重复计算。

建议：
- 每 5 分钟扫描 SQLite 到期任务，不代表每 5 分钟抓取所有仓库。
- 默认“仓库详情”软预算约 2,100 次/小时；所有 core 接口总软预算 2,500 次/小时；其余实际配额预留缓冲。
- 每个请求统一经过限流器；单服务初期 GitHub 请求并发 1～3，启动默认 1，结合实时反馈逐步上调。
- 对仓库 next_fetch_at 添加稳定抖动，避免整点拥挤；手动刷新需经过同一配额闸门。
- S 级上限 200、A 级上限 800；容量不足时按热度优先、降低低优先级仓库频率。
- 先运行 1,000 个仓库并观测一周，再逐步扩到 5,000 个。

### 4.2 分级算法

默认升档信号（作为初始可配置参数而非固定真理）：

- S：24h 可确认净增 ≥200，或在所有 active 且 growth24h.quality=exact 的仓库中按 delta_24h 降序进入前 200（同值 repo_id 升序），且净增长为正。
- A：24h 可确认净增 ≥50，或首次上榜/被用户关注的新项目。
- B：近期存在有效增长但未达 A 的活跃仓库。
- C：长期低增长项目。

迁移机制：
- 指标驱动的升降档在下一轮 compute_metrics 成功计算后生效（默认每 15 分钟，另加排队/执行耗时），不等待日汇总；首次上榜/人工关注的初始 A 级准入仍按容量规则即时处理。
- S→A 至少连续 12h 低于阈值；A→B 至少连续 48h；B→C 至少连续 7d。
- 新仓库先按 A 级观察，积累足够时间覆盖后再调整等级。
- 低频仓库突然发现较大跨度增长，先标“候选爆发”并升档复采；不能将 24h 采样差异误判成分钟级增长。

容量与准入：
- `max_monitored_repos` 默认 5,000，只统计 `state=active`；S/A/B 分别最多 200/800/2,000，剩余活跃项目进入 C，容量数字与预算一起配置校验。等级不足时依次降频，关注不意味着永远占用 A 级。
- 新候选先按 repo_id 建档为 candidate；有容量时转 active，按 A 级观察，A 已满则按可用等级采样。总容量满时保持 candidate，不创建周期刷新或 README 检查任务。
- 管理员屏蔽按 GitHub 数字 ID 保存在 `repositories.blocked`，与 GitHub 原生 `disabled` 分开。屏蔽仓库从公开读路径撤下，不再写入搜索、Trending 或 Star 观测，暂停仓库详情采样、README 读取及 AI 任务。屏蔽事务撤销在途写入的租约，可能已经计费的模型调用留待确认。历史记录按原保留期处理，候选清理保留屏蔽记录。解除后按监控容量恢复。
- Trending 能提供数字 ID 时按 ID 匹配。条目只有名称且本地该名称已屏蔽时，先跳过单仓库补查，直到发现源给出可区分的新 ID。
- 每日准入评估按人工关注、有效热度、新上榜、发现时间排序（并以 repo_id 稳定打破平局），用更高优先级候选替换未关注的低优先级活跃项目。退监控只改为 `paused`，取消未运行的周期任务，保留历史和人工状态。
- 人工关注优先保留跟踪；容量全被关注项目占满时，新关注请求返回 `409 monitoring_capacity_full`，保持原状态，用户需先取消一项关注或调整容量及预算。普通发现不突破上限。
- candidate 且未关注、没有人工处理选题的仓库，last_discovered_at 超过 30 天可按外键依赖顺序清理；重新发现可重新建档。暂停项目重新激活时不补跑错过的采样槽。

### 4.3 GitHub API 限流器

所有 API 请求统一由 GithubClient 执行并解析：

- X-RateLimit-Resource
- X-RateLimit-Limit
- X-RateLimit-Remaining
- X-RateLimit-Reset
- Retry-After
- ETag
- HTTP 状态码

实现要求：
1. 按 core/search 等资源分别维护当前窗口状态及本地节流预算。
2. 以响应头作为权威数据；允许外部同账号应用同时消耗额度。
3. core 剩余 <1000 时，优先暂停 C/B 类刷新；<300 时进一步暂停普通后台刷新。
4. 主配额归零时等待 reset 时间及缓冲；次级限流按 Retry-After 或至少 60 秒退避，并对连续失败指数增长。
5. 搜索初始主动节流目标为每分钟不超过 6 次，实际服从响应头、接口限制和全局预算。
6. 同一资源认证条件 GET 使用 ETag/If-None-Match；只有经过认证且返回 304 才享受官方文档说明的主配额豁免；304 仍是一次网络请求。
7. 对 200/304、404/410、403/429、5xx、超时、网络断开分别建模：404 可进入失效状态，403 不都意味着限流，必须识别响应体。
8. 按计划窗口和上游反馈计算 next_fetch_at，不在持有 SQLite 写事务时执行网络请求。

速率限制状态需要暴露在 API 和管理页：各资源剩余、重置时间、实际请求频率、最近 403/429 次数、等待任务数。

### 4.4 持久化任务与幂等

任务类型：

- discover_trending、discover_search
- refresh_repo
- refresh_readme_summary
- compute_metrics
- generate_topic
- rollup_hourly、rollup_daily、cleanup、backup

任务字段以第 5 章 DDL 为准。status 为 pending/running/succeeded/failed/dead/unknown/canceled；failed 表示可重试且受 available_at 控制，dead/unknown/canceled 不被普通领取器重领。

执行：
- 自动调度任务的唯一 dedup_key 为 job_type + 仓库或搜索范围 + 计划窗口；手动任务使用 job_type + repo_id + requestId，不带计划窗口。requestId 在首次点击时生成，网络重试沿用；新一次用户操作才生成新值。
- 领取时在 SQLite 短事务中原子转 running、递增 attempts、生成本次唯一 lease_token 并设置 lease_until（可采用 UPDATE ... RETURNING）。
- Worker 在事务外请求 GitHub/模型；每隔不超过租期的三分之一续租。续租失败即取消本次执行；所有外部调用设置有限超时，默认租期 180 秒不等于调用可无限等待。
- 成功提交必须在同一短事务中核对 status=running、lease_token 匹配且租约未过期，再写业务结果及 succeeded；条件不符则回滚，旧执行者不得更新结果、缓存或调度时间。
- 维护排空取消的落状态沿用普通任务语义，具体见备份专项第 4.2 节：未发送 AI/普通任务按重试上限记 failed 或 dead，已发送且结果未确认的 AI 调用及任务记 unknown；不因恢复尚未执行就批量记 canceled。
- 普通可重入任务租约到期可重领；失败指数退避并加抖动。AI 已发出调用但结果未确认时转 unknown，按第 7.5 节处理，不能直接再调用模型。备份/恢复另受 BackupManager 互斥和操作状态约束。
- 每次采样按 (repo_id, sample_slot) 幂等，实际 observed_at 独立保存。
- 摘要缓存按 (repo_id, source_fingerprint, provider, model, prompt_version) 匹配；每仓库同一时刻最多一个未决摘要任务，摘要任务 repo_id 必填。强制重新生成使用新的 requestId，但不能绕过已有 pending/running/failed/unknown 任务；先按手动 dedup_key 查重，相同请求返回原任务，其他请求与未决任务冲突时返回 409 summary_in_progress 及 existingJobId，不创建请求别名。
- 设置重试上限和失败隔离（failed/dead），避免坏仓库长期占用预算。
- 任务 payload 与日志禁止承载 README 原文或大模型完整 prompt。

采样时间约定：`sample_slot = floor(planned_at_ms / 300000) * 300000`，固定 UTC 五分钟网格，值为该槽起点的 Unix 毫秒。自动任务的 planned_at 是调度扫描决定本次采集的时刻；手动采集是请求首次被接受的时刻，均在创建任务时固化到 payload，重试不重算。等级周期与 next_fetch_at 的抖动只控制何时到期，不决定槽宽；升降级不改变已创建任务的槽。同槽的手动和自动采集最多保留第一条成功快照（200/有基线的 304 同规则），仓库最新元数据只接受比 last_success_at 更新的观察。

Trending 的 observed_slot 同样取计划抓取时刻所在的五分钟槽，用作该来源/窗口的批次键，不表示真实抓取时间；批次实际成功时间单独存 maintenance_state，API observedAt 读取实际时间。Search 任务也使用该计划槽去重，不向 Star 快照写入搜索结果。积压的旧采集任务不批量补跑：跨过该仓库当前采样周期时取消旧任务，仅为当前扫描建一条新任务。

## 5. SQLite 数据模型与索引

时间统一以 UTC Unix **毫秒** INTEGER 保存（source 日期仍保留精确字段）。下面为第一版核心 DDL；生成迁移时可增加 CHECK/外键约束与业务字段，但必须保留身份、幂等键和冷热分层边界。

~~~sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE repositories (
  repo_id INTEGER PRIMARY KEY,
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  full_name TEXT NOT NULL,
  name_is_current INTEGER NOT NULL DEFAULT 1 CHECK(name_is_current IN (0,1)),
  html_url TEXT NOT NULL,
  description TEXT,
  topics_json TEXT NOT NULL DEFAULT '[]',
  primary_language TEXT,
  license_spdx_id TEXT,
  license_name TEXT,
  stars INTEGER NOT NULL DEFAULT 0,
  forks INTEGER NOT NULL DEFAULT 0,
  archived INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  grade TEXT NOT NULL DEFAULT 'A' CHECK(grade IN ('S','A','B','C')),
  starred_by_user INTEGER NOT NULL DEFAULT 0,
  followed_at INTEGER,
  state TEXT NOT NULL DEFAULT 'candidate'
    CHECK(state IN ('candidate','active','paused','unavailable','address_pending')),
  github_created_at INTEGER,
  github_pushed_at INTEGER,
  discovered_at INTEGER NOT NULL,
  last_discovered_at INTEGER NOT NULL,
  last_checked_at INTEGER,
  last_success_at INTEGER,
  first_not_found_at INTEGER,
  next_fetch_at INTEGER NOT NULL,
  metadata_etag TEXT,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_repos_current_fullname
  ON repositories(full_name COLLATE NOCASE) WHERE name_is_current=1;
CREATE INDEX idx_repos_schedule ON repositories(state,next_fetch_at,grade);
CREATE INDEX idx_repos_stars ON repositories(stars DESC);

CREATE TABLE repo_snapshots (
  repo_id INTEGER NOT NULL,
  sample_slot INTEGER NOT NULL,
  observed_at INTEGER NOT NULL,
  stars INTEGER NOT NULL,
  forks INTEGER NOT NULL,
  PRIMARY KEY(repo_id,sample_slot),
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
) WITHOUT ROWID;
CREATE INDEX idx_samples_observed ON repo_snapshots(observed_at);

CREATE TABLE trending_observations (
  repo_id INTEGER NOT NULL,
  source_key TEXT NOT NULL,
  window TEXT NOT NULL CHECK(window IN ('daily','weekly','monthly')),
  observed_slot INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  reported_stars INTEGER,
  PRIMARY KEY(repo_id,source_key,window,observed_slot),
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
) WITHOUT ROWID;
CREATE INDEX idx_trending_recent ON trending_observations(window,observed_slot DESC,rank);

CREATE TABLE metrics_hourly (
  repo_id INTEGER NOT NULL,
  hour_start INTEGER NOT NULL,
  stars_open INTEGER,
  stars_close INTEGER,
  open_observed_at INTEGER,
  close_observed_at INTEGER,
  delta INTEGER,
  sample_count INTEGER NOT NULL,
  quality TEXT NOT NULL,
  PRIMARY KEY(repo_id,hour_start),
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
) WITHOUT ROWID;

CREATE TABLE metrics_daily (
  repo_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  stars_open INTEGER,
  stars_close INTEGER,
  open_observed_at INTEGER,
  close_observed_at INTEGER,
  delta INTEGER,
  sample_count INTEGER NOT NULL,
  quality TEXT NOT NULL,
  hotness_score REAL,
  rank_in_day INTEGER,
  PRIMARY KEY(repo_id,day),
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
) WITHOUT ROWID;
CREATE INDEX idx_daily_rank ON metrics_daily(day,hotness_score DESC);

CREATE TABLE latest_metrics (
  repo_id INTEGER PRIMARY KEY,
  delta_1h REAL,
  delta_24h REAL,
  delta_7d REAL,
  delta_30d REAL,
  acceleration REAL,
  persistence_score REAL,
  trending_score REAL,
  hotness_score REAL,
  data_quality TEXT NOT NULL, -- 热度综合质量，不代替各窗口质量
  window_details_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(window_details_json)),
  calculated_at INTEGER NOT NULL,
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
);

CREATE TABLE ai_summaries (
  repo_id INTEGER PRIMARY KEY,
  readme_sha TEXT,
  source_fingerprint TEXT NOT NULL,
  source_quality TEXT NOT NULL DEFAULT 'sufficient'
    CHECK(source_quality IN ('sufficient','incomplete_source','insufficient_source')),
  source_language TEXT,
  summary_zh TEXT NOT NULL,
  features_json TEXT NOT NULL DEFAULT '[]',
  audience_json TEXT NOT NULL DEFAULT '[]',
  value_tags_json TEXT NOT NULL DEFAULT '[]',
  source_readme_url TEXT,
  model_provider TEXT NOT NULL,
  model_name TEXT NOT NULL,
  prompt_version INTEGER NOT NULL,
  summary_status TEXT NOT NULL DEFAULT 'ready' CHECK(summary_status IN ('ready','stale')),
  generated_at INTEGER NOT NULL,
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
);

CREATE TABLE summary_checks (
  repo_id INTEGER PRIMARY KEY,
  readme_etag TEXT,
  last_seen_readme_sha TEXT,
  last_checked_at INTEGER,
  next_check_at INTEGER NOT NULL,
  last_error_code TEXT,
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
);

CREATE TABLE topic_candidates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id INTEGER NOT NULL,
  angle_key TEXT NOT NULL,
  title_zh TEXT NOT NULL,
  reader_value TEXT,
  topic_score REAL,
  status TEXT NOT NULL DEFAULT 'candidate'
    CHECK(status IN ('candidate','shortlisted','accepted','ignored','published')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(repo_id,angle_key),
  FOREIGN KEY(repo_id) REFERENCES repositories(repo_id)
);

CREATE TABLE jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  repo_id INTEGER REFERENCES repositories(repo_id),
  dedup_key TEXT NOT NULL UNIQUE,
  payload_json TEXT NOT NULL DEFAULT '{}',
  priority INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK(status IN ('pending','running','succeeded','failed','dead','unknown','canceled')),
  available_at INTEGER NOT NULL,
  lease_until INTEGER,
  lease_token TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  last_error_code TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_jobs_ready ON jobs(status,available_at,priority DESC);
CREATE INDEX idx_jobs_lease ON jobs(status,lease_until);
CREATE UNIQUE INDEX idx_jobs_summary_unresolved ON jobs(repo_id)
  WHERE type='refresh_readme_summary'
    AND status IN ('pending','running','failed','unknown');

CREATE TABLE ai_calls (
  call_key TEXT PRIMARY KEY,
  job_id INTEGER NOT NULL REFERENCES jobs(id),
  provider TEXT NOT NULL,
  provider_request_id TEXT,
  origin TEXT NOT NULL CHECK(origin IN ('auto','manual')),
  status TEXT NOT NULL CHECK(status IN ('reserved','sending','succeeded','failed','unknown')),
  reserved_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  input_tokens INTEGER,
  output_tokens INTEGER,
  estimated_cost REAL,
  error_code TEXT
);
CREATE INDEX idx_ai_calls_budget ON ai_calls(origin,reserved_at);

CREATE TABLE rate_limits (
  resource TEXT PRIMARY KEY,
  limit_total INTEGER NOT NULL,
  remaining INTEGER NOT NULL,
  reset_at INTEGER NOT NULL,
  paused_until INTEGER,
  observed_at INTEGER NOT NULL
);

CREATE TABLE discovery_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_type TEXT NOT NULL,
  source_key TEXT NOT NULL,
  query TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  interval_minutes INTEGER NOT NULL,
  next_run_at INTEGER NOT NULL,
  priority INTEGER NOT NULL,
  UNIQUE(source_type,source_key)
);

CREATE TABLE categories (
  slug TEXT PRIMARY KEY NOT NULL CHECK (slug <> 'other'),
  name TEXT NOT NULL,
  topics_json TEXT NOT NULL CHECK (json_valid(topics_json) AND json_type(topics_json) = 'array'),
  position INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE maintenance_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
~~~

注：上面 repo_snapshots 使用采样槽 (sample_slot) 去重，而 observed_at 是真实成功观测时刻。SQLite 外键启用必须对**每条连接**生效，连接池初始化应测试外键、busy_timeout；PRAGMA journal_mode=WAL 需要在初始化时检查实际返回结果。长期运行的单机程序应限制写事务耗时，并为高频查询建立覆盖索引。

`ai_summaries` 只保存成功内容及 ready/stale，queued/running/failed/unknown 来自最新未决或失败的摘要 job，missing 表示无内容且无任务，由 DTO 组合呈现。关注时同事务设置 starred_by_user=1、followed_at=当前时间；重复关注保留原时间，取消关注清空 followed_at。再次发现更新 last_discovered_at，不改首次 discovered_at。

对于“已有 repo_id 但 owner/repo 改名”的情况，依赖数字 ID 更新 canonical 名称；读取时对 GitHub 301/重定向、404 结合后续确认策略处理，不能立即丢弃历史。`full_name` 是可变化的名称，不是实体身份；`name_is_current=0` 的历史名称允许重复，不参与名称寻址。确认同名响应返回不同 ID 时，在同一事务中解除旧 ID 的当前名称映射、暂停其按旧名称采集，再建立新 ID 映射；保留旧 ID 的历史及关注标记，状态显示“地址待确认”，查明新地址后再恢复跟踪。已知新 canonical 名称时直接更新旧 ID 后再登记新 ID。

`latest_metrics.window_details_json` 按 `1h/24h/7d/30d` 保存每个窗口的 `{quality,startAt,endAt,resolution}`；startAt/endAt 为实际观测的 UTC Unix 毫秒，缺失时为 null，resolution 为 raw/hourly/daily/mixed。API 转为 ISO8601 UTC。数值列与该 JSON 同事务更新，热度用 data_quality，不得用一个质量值填充所有增长窗口。

## 6. Star 趋势算法

### 6.1 净增量与采样覆盖

- Delta24 = S(now) - S(nearest(now-24h))，使用最近合格的实际采样点。
- Delta7d/Delta30d 同理；必须对目标边界设置可配置最大容差。
- 目标窗口不存在可用采样时返回 NULL，标注 insufficient_history，不返回 0。
- 原始仓库接口返回的是**当前累计 Star**，快照差值是净增长，可出现负数（取消 Star）；不能写成“新增收藏人数”。
- 只做观察，不因 Star 变动本身重做 README 总结。
- 单次 304 可将上次已确认的仓库数据作为新的有效观察值，仍要记录 observed_at 与采样来源；若不存在此前可靠基线则不能创建数值快照。

质量定义：
- exact：采样点接近窗口两端（例如小时窗口 ±10 分钟、日窗口 ±90 分钟；阈值可配置）。
- estimated：有采样但边界偏差超出 exact、仍在允许边界偏差范围内。
- insufficient：采样缺失或跨度过长，不计算该窗口增量。
- 估算值不得混入精确排名而不提示；第一期可只用 exact 进行热度评分，estimated 供趋势预览。

**跨保留层的取点规则：**
- 1h/24h/7d 优先从原始快照取点；30d 起点从小时聚合的 open/close 观测点取值，终点优先取最新原始观察。趋势图 30d 使用小时点，180d 使用日点，响应明确 resolution。
- 小时桶保存桶内首末真实采样及各自 observed_at；日桶从小时桶首末观测合并，保留真实时间，不用整点或午夜替代。单样本桶可保存锚点，但桶内 delta 为 null；边界增长另取相邻桶锚点计算，不能简单累加桶内 delta。
- 目标两端分别选择容差内最近的实际观测，距离相同取较早者；要求 startAt < endAt。exact 两端容差默认分别为 1h ±10 分钟、24h ±90 分钟、7d ±3 小时、30d ±6 小时；estimated 最大偏差分别为 30 分钟、6 小时、12 小时、24 小时。超过最大偏差或缺少任意端点则 delta=null、quality=insufficient。
- 第一期 estimated 使用实际端点的净差，展示实际覆盖时间，不按跨度缩放、不插值补零；终点过旧同样降低质量。新发现仓库不能用一个观测同时充当两个端点。
- 原始数据清理前检查小时锚点和时间均已落库，小时数据清理前检查日锚点；无样本的桶保留缺失语义。清理后不重新从缺失原始数据覆盖已完成的聚合。

### 6.2 增长、加速度和持续性

基线参考：
- 24h 绝对净增：delta_24h。
- 7d 绝对净增：delta_7d。
- 相对增长：delta_7d / max(stars_at_7d_start, 100)；下限 100 是防止小基数失真的调参值。
- 加速比：(max(delta_last_24h,0)+1)/(max(delta_previous_24h,0)+1)；两个窗口均有效才计算。
- 持续性：过去 7 个已完成 UTC 自然日中有效日窗口净增为正的比例；单日集中度和多次上榜仅作展示。
- Trending 指标独立保存：上榜频次、当前排名、连续上榜等；不得把 Trending 展示的“stars today”与净增长盲目相加。
- 不以一次单日增长直接认定项目有长期维护质量；可以将 pushed_at/Release 信息作为可解释的辅助信号。

一期热度采用固定全局缩放，不先做仓库规模分组。令 `L(x,c)=100*min(1,ln(1+max(x,0))/ln(1+c))`，使用自然对数；初始规则如下，后续修改须同步 fixture：

- Velocity24=L(delta_24h,1000)，Velocity7=L(delta_7d,5000)，只使用 exact 窗口；缺失为 null，负增长原值仍展示，仅该热度项取零。
- Acceleration=L(max(acceleration-1,0),3)，两个相邻 24h 窗口均 exact 才有值。
- Persistence=100×过去 7 个已完成 UTC 自然日中 exact 且净增为正的天数/有效天数；有效天数不足 3 则 null，单日集中度仅展示，不参与一期评分。
- TrendingStrength：取全站 daily 最新一次成功完整榜单，成功时间在最近 24h 内时用该仓库名次 r，按 `100/max(r,1)`；有新鲜完整榜单但未上榜为 0，没有新鲜榜单为 null。语言榜及周/月榜先只展示。

~~~text
H = 0.35 * Velocity24
  + 0.20 * Velocity7
  + 0.15 * Acceleration
  + 0.15 * Persistence
  + 0.15 * TrendingStrength
~~~

缺失特征按已知特征重新归一化权重，同时输出热度 data_quality；至少具备 Velocity24 且总共三项有效信号才计算 H，否则 H=null、待观察。热度排名仅在 active 且 H 非 null 的仓库中按 H 降序、repo_id 升序排列；筛选条件不改变仓库的全局 hotnessRank。

内容选题的价值另算，不直接复用 H：

- 用户收益 35%、可演示性 25%、可复现性 20%、新颖程度 10%、证据充分性 10%。
- 上述分数初期是启发式，人工审核后逐步校准。
- 综合推荐可以从 40% 热度 + 60% 选题价值开始，但必须同时显示两项原分数和推荐原因。

### 6.3 每日推荐

按 recommendations 的时刻和时区生成候选池，默认 Asia/Shanghai 每天 08:30：
- 过滤无效仓库、重复选题和明显缺乏使用价值的项目。
- 选择高增长、新上榜、持续增长和人工关注四类候选，保持多样性。
- 对非中文且尚无有效摘要的高优先级项目先入 AI 任务队列。
- 将评分、证据链接、中文摘要和推荐理由保存为候选记录。
- 手工状态：candidate / shortlisted / accepted / ignored / published。
- 已处理项目只在重大更新、明显新增长拐点或人工触发时重新进入推荐。
- 不自动将模型生成的功能描述当作实测结果。

## 7. 非中文项目的 AI 中文摘要

### 7.1 唯一持久化内容

- 摘要保存 200～500 字的中文总结（资料不足时可更短）、可选的一句中文推荐理由、精简的功能/受众/用途标签、README SHA、源链接、模型/提示词版本、生成时间和处理状态。AI Topics 另存于 `repo_ai_topics`。推荐理由在同一次摘要调用中生成，只写一项有来源依据的阅读价值。旧摘要在下一次符合生成条件时补齐。
- repo 的 description 可作为主表元数据保留。译文片段按 README SHA 与模型提示词标识保存，当前仓库只保留一版。
- README 内容只作为临时内存数据：读取 → 清洗 → 模型调用 → 验证 → 保存总结 → 释放引用。
- 任何临时文本不得写入 SQLite jobs.payload_json、数据库日志、结构化日志、磁盘缓存、失败追踪、任务审计详情或崩溃转储附件。
- Go 运行时无法保证敏感内存被瞬时物理擦除；这里定义的是“应用不主动持久化”，而不是强行承诺物理级擦除。
- 禁止为 AI 分析原样记录请求与响应正文；只记录长度、Token、耗时、模型、状态和错误类别。

### 7.2 自动触发条件

默认采用以下条件：
1. 新仓库进入 S 或 A 级，或热点排名前 100，且来源文本为非中文。
2. 用户手动点击“生成中文总结”。
3. 已有总结但 README SHA 发生变化，且仓库仍处于高优先级或被关注。
4. 模型提示词版本升级后，仅对符合预算与热度门槛的项目刷新。

对 README 内容没有变化、仅 Star 增长的仓库，**绝不因为 Star 数变化调用大模型**。

README 更新检查频率：
- S/A 或收藏项目：每 24 小时。
- B 项目：每 72 小时。
- C 项目：每 7 天。
- 新进入 AI 总结队列的仓库立即获取 README。
- 有 ETag 时可条件检查 README。304 表示 README 未变，AI Topics 仍需核对 About。需要生成的结果须有匹配的源指纹、供应商、模型和提示词版本才能复用。缺少所需结果或强制重新生成时，重新获取 README 正文。
- 无 README 的仓库可依据仓库 description 生成简短介绍，但要标明来源不足，且不能虚构功能。

启用模型后，自动队列也处理缺少 GitHub Topics 的活跃仓库。需要同时生成摘要和主题词时合并一次调用，中文仓库或已有有效摘要的仓库可单独提取主题词。主题词和摘要共用检查间隔和 AI 调用额度，已有仓库按检查计划补齐。低优先级的主题词补齐使用剩余自动额度。

AI Topics 从公开 About 和 README 提取，使用至多五个小写主题词，并保存源指纹、模型、提示词版本和生成时间。检查确认来源已变更时，撤下旧主题词。分类匹配、筛选与计数统一优先使用 `repositories.topics_json`，该数组为空时使用 `repo_ai_topics.topics_json`。详情接口通过 `aiTopics` 返回当前生效的 AI 主题词，页面单独标明来源。模型未配置时，未生成摘要的仓库展示 GitHub About。

### 7.2.1 README 阅读与翻译

- 详情页把 AI 摘要作为导读。中文 README 直接展示正文。英文 README 展示原文，有译文时默认显示中文并提供原文标签。站内读取遇到长度限制或译文还在生成时显示状态，并保留 GitHub 入口。
- GET README 时重新核对仓库 ID、公开性及地址。原文与译文均经 Goldmark 安全渲染。GitHub README 读取与 AI 任务分别计量，公开访问不触发模型调用。
- 已有有效摘要的活跃仓库用剩余自动额度准备译文。待补齐主题词的低优先级仓库较多时，调度会为译文留出部分额度。管理员也可在仓库详情手动生成。参考 AIHOT 的译文提示词，以 JSON 分批翻译 Markdown 文本，保留代码、链接地址和媒体结构。每批结算用量并保存进度，后续按 SHA 和读取范围续译。模型未配置时仍可读取原文。备份恢复取消原未决任务，管理员可从已保存进度手动续译。

### 7.3 处理步骤

~~~text
候选仓库命中规则
    ↓
检查 summary_checks 中 ETag / SHA / 下次检查时间
    ↓
GET /repos/{owner}/{repo}/readme
    ↓
在内存中 Base64 解码、截断体积、抽取标题/功能/安装/案例等
    ↓
检测 Markdown 正文语言（忽略代码块、URL、badge）
    ↓
中文主导：可做轻量中文摘要；非中文：中文理解、翻译式总结
    ↓
模型输出严格 JSON
    ↓
解析校验 + 事实限制 + 字数检查
    ↓
事务内保存本次生成的摘要、AI Topics 和检查状态
    ↓
释放 README 临时对象；只留下摘要和元数据
~~~

具体约束：

- GET README 的 GitHub 返回对象可包含 SHA、size、content；默认 JSON 媒体类型下可能是 Base64 内容。
- 限制下载体积（建议原始 README ≤256 KiB），大文件只取安全截断片段并标记 incomplete_source，不应尝试无限制读取。
- 遇到非常长的 README，只在内存中提取结构化片段（项目简介、功能、使用方式、限制），总体输入不超过可配置 Token 预算。
- 语言识别依据自然语言正文，不使用 repository.language 充当语言检测。
- 非中文项目输出中文，不进行逐行完整翻译；保留命令、技术名词和链接含义。
- README 是不可信输入，任何“忽略规则/发起网络请求/输出密钥”等指令均应当作正文，不赋予工具调用权限。
- 摘要可包含简介、功能、适用人群和限制；用户侧默认展示中文摘要，不复刻整篇 README。
- 模型服务商可能接收提交的文档片段，属于必要的第三方处理；仅分析公开仓库内容。

### 7.4 模型输出契约

~~~json
{
  "source_language": "en",
  "summary_zh": "以中文自然叙述，目标 200～500 字；介绍项目解决的问题、主要能力、适合谁使用、使用前提和文档可证实的限制。",
  "features": ["功能一", "功能二"],
  "audience": ["开发者", "产品经理"],
  "value_tags": ["浏览器自动化", "开源工具"],
  "evidence": ["仓库 README", "仓库 Description"],
  "uncertainty": ["需要实际运行验证的部分"]
}
~~~

以上 evidence 与 uncertainty 是模型输出的瞬时校验字段。摘要记录保存短摘要、精简标签、来源链接及模型版本，译文使用独立记录。原始 README 片段留在内存。

卡片 titleZh 由后端从已保存的 summary_zh 派生，不新增模型字段或数据库列：trim 后取第一个句末符号（。！？!? 或换行）之前的文本，最多 40 个 Unicode 字符（Go rune），超长截断并追加“…”；没有非空摘要或所得文本为空则返回 null。新摘要任务失败时仍从上次成功摘要派生；前端 null 时回退 description，再无内容则省略该行，不额外调用模型生成标题。

校验要求：
- summary_zh 为中文，目标 200～500 个 Unicode 字符（按 Go rune 数统计）。文档信息不足时允许不足 200 字并显式标注 insufficient_source。
- features 最多 5 项，每项尽量短；发现无来源支撑的能力时删除或重新生成。
- 模型 JSON 解析失败允许有限次结构修复重试；不因字数不足而允许添加臆测内容。
- 模型输出前由固定系统提示词限定“事实来自原文；禁止编造安装结果、性能、兼容性、实验结论”。
- 复核结果的 README SHA 与任务请求 SHA 必须一致；若分析期间再次更新文档，旧结果标记 stale 并另行调度。
- 如果已有摘要而新任务失败，保留旧摘要并显示上次成功时间及“待更新”。

### 7.5 大模型预算与 Provider

- 使用统一 Provider 接口（兼容 OpenAI 风格的 Chat Completions 结构化输出；具体供应商可替换）。
- Base URL、Model、超时、最大输入/输出 Token 和额度通过后台保存到数据库，重启后生效。API Key 通过环境变量提供。
- 默认 AI 工作池并发 1～2，单日最多 50 次自动模型请求、20 次手动模型请求；包含结构修复调用，计数按下文持久化规则执行。
- 为每次成功分析记录输入/输出 Token、估算成本与耗时（不记录原文）。
- 高热度候选优先于普通项目；失败指数退避，达到上限转 dead 并可人工重试。
- 缓存键使用 repo_id + source_fingerprint + provider + model + prompt_version；有 README 时 fingerprint 为 `readme:<sha>`，无 README 时为规范化 description 的 SHA-256，避免所有无 README 情况共用 null 键。readme_sha 在无 README 时仍为 null。
- AI 分析依赖可用预算但不阻断 Star 采集和榜单展示。
- 摘要 Worker 独立调度。README 下载及公开性复核使用 GitHub core 配额中的本地子预算，默认每小时 120 次，每个 Worker 至少预留 8 次。发送模型前再次核对数字仓库 ID、公开状态及当前地址。

调用与费用边界：
- 本地结果幂等不代表供应商只计费一次。调用前在短事务中预留当日名额并创建 ai_calls，发送前核对仍持有有效 lease_token 并持久化 sending；收到有效结果后与摘要及任务完成状态一起提交 succeeded。任何重新领取必须先检查该任务的调用记录，存在 sending/unknown 时只转 unknown，不发送新请求。数据库中只存调用标识、状态和计量，不存请求正文。
- 收到完整响应但 JSON/内容校验不合格时，在仍持有有效租约的事务内将本次调用设为 failed、error_code=invalid_output，保存已知计量；明确失败的供应商响应也记录相应失败类别。有限结构修复必须在上一调用终态提交后创建新 call_key 并占新名额，不复用旧调用记录。若在终态落库前崩溃，仍按无法确认结果转 unknown，不能假装已知失败后自动重发。
- 自动每日最多 50 次模型请求，手动每日默认最多 20 次；结构修复、重新生成和确定可重试的再次调用均占请求名额。按 UTC 自然日、reserved_at 计数，预留后不因失败退回，避免重启或多 Worker 绕过限额。
- sending 状态在超时、断连或崩溃后无法确认结果时，调用和任务转 unknown，停止自动重发；页面显示“调用结果待确认，可能已计费”。只有供应商已验证支持幂等重试/结果查询时才以相同 call_key 对账，其保证范围和有效期由适配器明示。
- 不支持对账的供应商，需要管理员明确接受可能再次计费后，取消旧 unknown 任务并用新 requestId 发起手动分析；丢失或过期的租约执行者不能直接补写摘要。reserved 且尚未发送的请求可安全继续。
- `ai_calls` 随关联任务保留；unknown 调用及其关联任务（即使已 canceled）在人工解决前不自动清理，其他调用先于到期任务删除。管理员接受可能再次计费时，将该次 unknown 调用标记 failed、error_code=unknown_acknowledged，表示人工关闭未决记录，不表示未计费。恢复旧备份会回退调用台账，因此恢复后的自动 AI 默认暂停，见备份专项方案第 4.4 节。

## 8. 归档、清理与数据库维护

本产品偏向短期发现和选题，默认**不做永久原始快照存储**。

| 数据类型 | 保留策略 | 清理条件 |
| --- | --- | --- |
| repo_snapshots 原始快照 | 14 天 | 已完成对应聚合，且早于安全水位 |
| metrics_hourly 小时聚合 | 60 天 | 已具备可靠日聚合或允许缺失标记 |
| metrics_daily 日聚合 | 180 天 | 超期分批删除 |
| trending_observations | 30 天 | 超期分批删除 |
| search_observations | 30 天 | 与 Trending 共用发现记录保留天数 |
| jobs 已完成/取消/终止的普通失败记录 | 14 天 | 不删除仍待重试或 unknown 任务，关联 ai_calls 先清理 |
| jobs dead 记录 | 30 天 | 保留错误类别便于排查 |
| ai_summaries | 只保留每个仓库最新成功摘要 | 项目被彻底移除时清理 |
| README 原文 | 请求或任务内存 | 不进入持久存储 |
| README 译文 | 仓库当前版本 | 按 README SHA 匹配，随仓库删除 |
| topic_candidates | 有人工处理状态的长期保留；普通候选 90 天 | 删除过期未采用候选 |
| rate_limits / 系统配置 | 当前状态 | 覆盖更新 |
| slog 运行日志 | 14 天 | stdout，由 Docker/systemd 或部署日志工具保留/轮转；不新建日志表，jobs/ai_calls 独立保留 |
| SQLite 远端备份 | 每天一份，保留最近 5 个自然日 | Cloudflare R2 按日期覆盖与超期自动删除 |

调度建议（UTC）：
- compute_metrics 按 scheduler.metrics_interval_minutes（默认 15，须为 scheduler.tick_minutes 的正整数倍）在 UTC 对齐窗口执行。每轮以统一 asOf 重算全部 active 仓库的增长、热度及评级，再更新 latest_metrics；hotnessRank 从这些结果按第 6.2 节排序取得。直接读取已提交采样及聚合，不等待小时/日汇总。全局任务串行、按窗口去重；启动/恢复后安排一轮，积压只计算当前窗口，不逐窗补跑。时间窗口和榜单时效即使没有新采样也需重算；失败保留上次成功值及 calculated_at，UI 不将其显示为实时结果。
- 每小时完成小时聚合。
- 每天 01:00 UTC 汇总前一日指标。
- 每天 03:00 UTC 执行分批清理。
- 每天北京时间 03:00 默认执行 SQLite 一致性快照和 R2 上传；03:30 清理超过最近 5 个自然日的备份。具体时刻、时区和保留天数通过后台修改并立即生效。
- 每周执行 PRAGMA optimize；定期观察数据库页数、freelist_count、WAL 大小。
- 仅在空闲页占比大且维护窗口允许时进行 VACUUM；普通删除不一定立即缩小数据库文件。

**删除安全顺序**：
1. 把“已完成的时间窗口”及聚合最后水位写入 maintenance_state。
2. 校验聚合数量、边界和质量字段。
3. 按事务批次（例如每 5,000 行）删除安全水位之前的原始快照。
4. checkpoint/优化在低峰时执行；异常中断后下次可继续。
5. 保留每个仓库 repositories.stars 的最新状态，即使历史快照已删除，也能继续下一次采样，但不得伪造已过期的旧窗口数据。

聚合质量：
- 小时 delta 必须有有效时间边界；不完整则 NULL + insufficient。
- 日 delta 同理；长时间采样缺失不能线性填充当作精确事实。
- 早期 B/C 级仓库的历史通常比较稀疏，不能伪造小时曲线。
- 系统初始化时自动清理超期数据，不因宕机数日而永久积压。

阶段 2 使用 `metric_rollup_pending` 在采样提交时记录小时增量，合并小时桶时将本次新增样本同步传给日层。旧库迁移按实际观测时间补建待合并锚点。清理只读取已提交的水位和聚合，遇到待合并的迟到样本会保留源数据。Trending 以 `trending_batches` 记录完整批次及其实际成功时间，包含成功空榜。

### 8.1 参考清理 SQL

~~~sql
DELETE FROM repo_snapshots
WHERE (repo_id, sample_slot) IN (
  SELECT repo_id, sample_slot
  FROM repo_snapshots
  WHERE observed_at < :safe_cutoff_ms
  ORDER BY observed_at
  LIMIT 5000
);
~~~

重复执行直到无记录可删。:safe_cutoff_ms 应取“14 天阈值”和“聚合成功水位”中的更早时刻，而不是直接删除所有超过 14 天的采样。

### 8.2 数据库备份、五天保留和 VPS 一键恢复

阶段 5 实现每日 VACUUM INTO 快照、Gzip、R2 上传及自然日保留，默认 5 天可配 1～30 天。配置存 system_settings，该表在阶段 0 按[备份专项第 2.1 节](./backup-and-restore.md)迁移创建；未配置时备份关闭。

恢复保留同进程、同一 sql.DB：先在临时库完成迁移、完整性检查、当前 R2 配置覆盖及旧任务整理；暂停新请求和 Worker，排空数据库访问后通过 modernc NewRestore/Backup API 原子写回目标库，完成后重新加载配置与调度。下载准备期间业务继续，复制期间业务 503、进度仍可读，正常完成 Go PID 不变。

不交换 db/WAL/SHM 文件，不替换连接池，不建立 RecoveryJournal 或 recovery_pending。SQLite 自己处理复制事务和崩溃恢复；提交时业务数据、当前 R2 连接、初始化标记和任务整理一起生效。旧任务不重放、自动 AI 暂停；提交后配置重载失败可重试加载，不自动回退旧数据。

首次安装使用同一管理员会话和恢复引擎；临时 R2 会话只在内存。备份包含 R2 凭据，部署使用专用私有 bucket、仅该 bucket 对象读写令牌及受限本地文件权限。空间估算、错误分支、接口和验收以[备份专项方案](./backup-and-restore.md)为准。

## 9. API 设计

统一前缀 /api/v1，字段采用 lowerCamelCase，时间返回 UTC ISO8601；热度/时序接口返回质量与 updatedAt。大列表统一 limit + cursor，默认 30，最大 100；/trending 是有限规模的单来源完整榜单，按下述专用契约不分页。

| 方法/路径 | 说明 |
| --- | --- |
| GET /healthz | 存活检查 |
| GET /readyz | 数据库与运行环境就绪 |
| GET /api/v1/repos | 按增长、热度、语言、AI 标签、等级、收藏状态筛选排序 |
| GET /api/v1/categories | 数据库分类、active 仓库数量与最新仓库预览 |
| GET /api/v1/repos/:id | 仓库详情、中文摘要、采集状态 |
| GET /api/v1/public/repos/by-name/:owner/:name | 按当前完整仓库名读取公开详情 |
| GET /api/v1/repos/:id/trend?range=24h/7d/30d/180d | 自适应返回原始/小时/日级点位和质量 |
| POST /api/v1/repos/:id/refresh | 手动调度采集，遵守配额 |
| POST /api/v1/repos/:id/summary | 手动安排/复用 AI 总结 |
| POST /api/v1/repos/:id/follow | 加入或移出重点监控 |
| POST /api/v1/repos/:id/topic | 幂等加入选题池，返回原有或新建选题的状态 |
| GET /api/v1/trending?period=daily&sourceKey=all | 单来源最新完整 Trending 榜单及批次，返回 TrendingResponse；本地增长榜使用 /repos?sort=growth24h |
| GET /api/v1/topics | 候选选题及人工状态 |
| GET /api/v1/topics/days | 按入池日期返回版面索引 |
| PATCH /api/v1/topics/:id | 修改 shortlisted/accepted/ignored/published |
| GET /api/v1/system/jobs | 任务状态、排队、失败详情（不含 README） |
| PATCH /api/v1/repos/:id/block | 管理员设置 `{blocked:boolean}`，后台列表支持 `blocked` 筛选 |
| GET /api/v1/public/repos/:id/readme | 当前公开 README 的安全 HTML 与匹配的译文 |
| POST /api/v1/repos/:id/readme/translate | 管理员提交译文任务，使用现有手动额度与任务查询 |
| GET /api/v1/jobs/:id | 管理鉴权的单任务查询，供手动采集/摘要轮询 |
| POST /api/v1/jobs/:id/retry | 管理员确认 unknown 摘要可能已计费后重新创建手动任务 |
| POST /api/v1/system/ai/resume | 恢复后确认计量不完整，重新启用自动 AI |
| GET /api/v1/system/limits | GitHub core/search 配额使用情况 |
| GET/PUT /api/v1/system/settings | 读取或保存进程级配置，返回是否需要重启 |
| GET/PUT /api/v1/system/categories | 读取或替换分类映射 |
| GET/PUT /api/v1/system/discovery-rules | 读取或替换 Trending 和 Search 规则 |
| GET/PUT /api/v1/system/discovery-tabs | 读取或保存发现页分类标签及精选门槛 |
| GET /api/v1/public/discovery-tabs | 读取发现页分类标签，按展示顺序返回分类标识和显示名称 |
| GET /api/v1/public/discovery | 精选仓库，支持分类和游标，返回命中信号的时间 `discoveryAt`。`sort=hotness` 返回精选中的热度前五 |
| GET /api/v1/public/project-themes、/project-themes/:slug | 专题目录和详情，返回简介、可选图标和颜色、项目总数、30 天新增数及最近发现的项目。详情额外返回 `groupName` 和 `milestones` |
| GET/PUT /api/v1/system/project-themes | 后台读取和保存专题，使用共享修订号 |
| GET /api/v1/system/storage | SQLite 大小、快照条数、清理水位 |
| POST /api/v1/system/maintenance/run | 鉴权后手动启动维护任务 |
| GET /api/v1/system/backup/settings | 读取后台备份配置（凭据脱敏） |
| PUT /api/v1/system/backup/settings | 更新 R2 连接、备份时间、保留天数并立即重新调度 |
| POST /api/v1/system/backup/test | 测试 R2 连接 |
| GET /api/v1/system/backup/list | 列出 R2 最近的每日备份对象 |
| GET /api/v1/system/backup/status | 最近备份、恢复操作与下次运行时间 |
| POST /api/v1/system/backup/run | 手动执行当天备份 |
| POST /api/v1/system/backup/restore | 后台选择日期，创建同进程数据库恢复任务 |
| GET /api/v1/system/backup/operations/:id | 查询异步备份/恢复进度 |
| POST /api/v1/system/backup/reload | 恢复已提交但配置重载失败时重试加载 |

首次安装另提供 POST /api/v1/setup/r2/test、r2/session、restore、initialize，GET /api/v1/setup/backups、status、operations/:id，均使用管理员会话。R2 会话仅保存在内存，恢复后的数据库包含本次新配置；已初始化时关闭 setup 写接口，状态和操作查询仍可用。维护期间健康检查、setup/status 和操作进度只读内存；重载入口仅在复制已结束且 databaseApplied=true 的加载失败状态下，受恢复互斥保护读取数据库。鉴权优先；恢复期间设置写入及冲突操作先返回 409，其余业务 API 返回 503。详见[专项方案](./backup-and-restore.md)。

这些配置接口共用数据库修订号。写入提交上次读取的 `revision`，冲突返回 409 `settings_conflict`。进程级配置 PUT 传 `{revision,settings}`，响应含 `restartRequired`。分类 PUT 传 `{revision,items:[{slug,name,anyTopics}]}`。规则 PUT 传 `{revision,items:[{sourceType,sourceKey,query,enabled,intervalMinutes,priority}]}`，`sourceType` 可为 search 或 trending。Trending 键为 `all:daily` 或 `language:go:weekly` 等，`query` 留空。分类和发现规则接口接受空数组，用于清空对应规则。分类 `other` 由查询派生。

发现页标签 PUT 传 `{revision,items:[{slug,label}]}`，按数组顺序展示，空 `label` 沿用分类名称。可选 `newCategories` 使用分类接口的条目格式，将新分类与标签在同一事务保存。分类映射新增条目后出现在可选列表，管理员可单独加入发现页。已有库首次修改分类映射时保留原有标签顺序。前台固定显示「全部」，保存空数组只保留「全部」。

发现页只收录公开且处于监控中的仓库。近 7 天首次发现的 S/A 级项目需在近 48 小时采样成功。精确 24h 净增达到现有热度升档阈值的项目，以窗口终点是否在近 24 小时内为准。最近一次成功采集的全站日榜在近 24 小时内时，榜单前 10 也可入选。三项命中任意一项即可，同一仓库取最新命中时间，按北京时间日期、热度、命中时间倒序排列。后台通过 `selection` 保存 `recentDays`、`recentSampleHours`、`growthSampleHours`、`trendingHours` 和 `trendingTop`，保存立即生效。

专题独立保存在 `project_themes`，通过匹配词筛选项目。GitHub Topics 非空时以原生主题为准，原生主题为空时使用 AI Topics。公开 `/discovery` 和 `/repos` 接受 `theme` 标识，分页游标绑定该专题。

专题方向保存在 `project_theme_groups`，每个方向有名称、简介和显示顺序。后台通过 `/api/v1/system/project-theme-groups` 管理方向，在专题表单选择归属。移除仍有专题的方向前，须先迁移这些专题。公开目录返回 `groups`，每个专题以 `groupSlug` 指定所属方向。

专题大事记从当前数据库保留的观测中取最近 12 个自然月。收录全站 GitHub Trending 日榜前三，同仓库每月只保留最佳名次。Star 里程碑使用日聚合首末实测值跨过 1,000、5,000、10,000、50,000 或 100,000 Star 的记录，同一天跨多档时取最高档。每月最多五件，榜单按名次优先，Star 事件按档位优先。事件按北京时间分月，展示顺序为时间倒序。

分类映射和发现规则按提交数组持久化后台列表顺序。发现规则的 `display_position` 仅用于后台列表排序，规则的采集优先级、下次计划时间与任务租约按既有调度机制维护。

阶段 2 的计算周期、窗口容差、采样分级门槛、评分参数及清理计划由 `settings.scheduler.metricsIntervalMinutes`、`settings.metrics` 和 `settings.maintenance` 管理，保存后重启生效。评分权重按 24h 增长、7d 增长、加速度、持续性和 Trending 排列。维护接口返回 202，持久化请求由后台下次扫描领取。已初始化的库也可以用 `maintenance-once` 离线执行维护。

阶段 3 使用 `settings.ai` 和 `settings.ai.summary` 配置供应商、模型、预算、提示词版本、来源检查和重试。`settings.recommendations` 配置每日选题时刻、时区、数量、重入阈值及保留期，保存后重启生效。选题通过 `GET /topics?status=&day=&limit=&cursor=` 分页。提供 `day=YYYY-MM-DD` 时按选题时区和入池时间列出当天选题，游标绑定状态与日期。`GET /topics/days` 提供同一时区下的日期、首条标题和条目数。`PATCH /topics/:id` 支持 `{status,feedback}`，将状态设回 `candidate` 可人工重新打开。每日任务以仓库和选题角度去重，README 指纹和功能证据同时变化或精确 24h 净增相对上次推荐达到重入门槛时，可重新进入候选并保留人工反馈。

异步任务契约：refresh 请求 `{requestId}`，接受后返回 `202 {jobId,statusUrl}`；summary 请求 `{force:false,requestId:"客户端生成的唯一值"}`，先查手动 dedup_key，相同请求返回原任务；其他请求遇到未决摘要任务返回 `409 {code:"summary_in_progress",existingJobId,statusUrl}`。无冲突且缓存命中返回 `200 {cached:true,summaryStatus:"ready"}`，否则返回同样的 202 结构；`force:true` 明确要求重新生成并计入手动额度。摘要执行条件未满足时返回 503 `summary_unavailable`，已保存的摘要仍可读取。unknown 任务通过 `POST /api/v1/jobs/:id/retry`、请求 `{requestId,acceptPossibleCharge:true}` 在管理鉴权下关闭未决调用、取消旧任务并创建新的手动任务；上述变化在同一事务中完成。重试接口先按新手动 dedup_key 查重，已存在则直接返回，随后才检查旧任务必须为 unknown，保证响应丢失后的重试仍返回同一新任务。

`GET /api/v1/jobs/:id` 返回 `{id,type,status,attempts,maxAttempts,errorCode,createdAt,updatedAt,repoId}`，时间为 ISO8601，状态见 4.4。succeeded/dead/canceled 停止轮询，unknown 停止自动轮询并显示人工处理入口；failed 仍可能退避重试。任务不存在/已过保留期返回 404，前端重新查询仓库结果，不宣称成功。维护期该接口可返回 503，恢复进度使用独立的 operations 接口。

Trending 专用契约：GET /api/v1/trending 的 period 为 daily/weekly/monthly（默认 daily），sourceKey 为已配置来源（默认 all，如 language:python），非法值返回 400。一次读取同一来源/周期的最新成功完整批次及其成员，按该批次 rank 升序、repo_id 升序返回全部卡片，包含尚未准入 active 的上榜仓库，不套用 /repos 的默认 state 过滤；对应名次取卡片中匹配 sourceKey/period 的 TrendingEntry。响应为 `{sourceKey,period,batch,items,updatedAt}`，batch 为 `{observedSlot,observedAt}` 或 null；observedSlot 是 Unix 毫秒批次键，observedAt 是实际成功时间。尚无成功批次时 batch=null、items=[]；成功空榜仍有非空 batch。抓取失败保留上次成功批次，updatedAt 等于该批次 observedAt，尚无批次时为 null；不得用本次查询时间掩盖旧榜。类型见 UI 文档 TrendingResponse，增长榜仍用 ApiList<RepositoryCardView>。

### 9.1 单管理员认证

部署 YAML 必填 `server.site_url`、`auth.admin_password` 和 `auth.session_secret`，缺失时 HTTP 服务报错退出。管理接口通过签名会话 Cookie 鉴权，包括 setup 和恢复进度。前台通过 /api/v1/public 的独立 GET 路由获取仓库列表、详情、Star 趋势、分类、官方榜单及来源标识。公开读取仅展示当前可发布仓库，摘要仅展示已生成内容与来源，关注记录、摘要任务编号及模型配置留在管理接口。两组业务路由共用初始化与恢复门禁。静态资源、/healthz、/readyz 可公开，健康响应只提供就绪状态。

首次进入后台时提交密码到 `POST /api/v1/auth/login`。Cookie 设为 HttpOnly、SameSite=Strict，有效期 24 小时，网站对外地址使用 HTTPS 时追加 Secure。后台写请求检查来源。刷新时通过 `GET /api/v1/auth/session` 确认状态，管理接口返回 401 时清理管理缓存并返回登录页。`POST /api/v1/auth/logout` 清除当前浏览器的 Cookie。会话校验独立于数据库，修改密码或签名密钥并重启会使既有会话失效，SQLite 恢复继续使用当前实例的部署配置。

### 9.2 列表与游标

/repos 默认仅列 state=active；watchlist 使用 followed=true，包含所有已关注状态，按 followedAt 降序。可显式传 state 枚举查询其他状态；q 匹配 full_name/description，language 精确匹配，重复 category 参数按 OR 匹配第 3.4 节的数据库分类。/categories 返回全部分类及 active 计数；未知分类/排序/状态返回 400。

sort 可取 discovered、hotness、growth24h、growth7d、stars、followed，分别以 discovered_at、hotness_score、exact 的 delta_24h、exact 的 delta_7d、stars、followed_at 降序，repo_id 升序作唯一次键；可用性不足的排序值为 null，统一排最后。followed 排序要求 followed=true。热度榜和增长榜的 updatedAt 为最近成功计算时间，首轮成功前为 null。cursor 是无填充 Base64URL 编码的 UTF-8 JSON：`{v:1,sort,filters,last:{value,repoId}}`；filters 为标准化 q/language/state/followed/grade/tag 和排序去重后的 category 数组，不含 limit。比较当前过滤条件及版本，非法编码/字段/不匹配返回 400 invalid_cursor。后续页以该排序键执行 keyset 查询，包括 null 段的 repo_id 续页，不使用 offset。

游标只描述查询位置，不承载授权；不需要签名或服务端分页会话。热度、关注及采集数据会变动，分页为实时弱一致，前端按 repoId 去重，刷新列表重新从首页取；不承诺跨页固定快照。响应为 `{items,nextCursor,updatedAt}`，无下一页时 nextCursor=null。

## 10. 前端界面与 AIHOT 交互适配

前端采用 **React + TypeScript + Vite + Tailwind CSS 4**，参考 AIHOT 的整体阅读型布局与交互风格；后端始终采用 Go + Gin + SQLite。**文件级迁移、组件矩阵、数据契约、验收清单以 [AIHOT 视觉与交互适配方案](./aihot-ui-adaptation.md) 为准。**

### 10.1 页面与路由

| 路由 | AIHOT 页面对应关系 | github-radar 业务目标 |
| --- | --- | --- |
| / | 精选首页 / 时间线 | 最近发现的增长项目、精选候选与热度领跑 |
| /trending | 热点榜 | 24h/7d Star 净增、排名、上榜与走势 |
| /repos | 全部动态 | 仓库筛选、技术语言、主题、等级和浏览器收藏 |
| /repos/:owner/:name | 新闻详情 | 独立公开页面，展示 Star 曲线、中文摘要和来源 |
| /topics、/topics/:slug | 专题页 | 独立专题目录，详情展示所属方向、大事记和近期精选，可切换全部项目 |
| /categories、/categories/:slug | 分类页 | 复用顶部分类映射 |
| /admin/topics | 日报与精选 | 候选选题、读者收益、审核状态、导出 |
| /starred | 收藏 | 当前浏览器收藏的仓库 |
| /admin/watchlist | 重点监控 | 管理员指定优先跟踪的仓库 |
| /admin/runs | 管理后台 | GitHub 限流、调度任务、SQLite 清理与备份恢复状态 |
| /admin/repos、/admin/repos/:owner/:name | 后台仓库管理 | 加入或移出重点监控、刷新采样、摘要任务与加入选题池 |
| /admin/login、/admin/setup | 后台登录与首次安装 | 验证管理员密码、初始化空库或从 R2 恢复 |

### 10.2 视觉系统与可复用组件

- 从 AIHOT 参考布局、信息密度与响应式断点；颜色统一映射到 Radix UI Colors 的亮暗色阶，边框 0.5px、字号和圆角按前端专项规范，不直接复制上游颜色常量。
- 参考其 Sidebar、PhoneBar、TabBar、Badge、Controls、Tabs、Sparkline 等纯 UI 组件；重写与 `@aihot/site`、`@aihot/contracts`、新闻事件模型绑定的逻辑。
- 保留桌面侧栏、窄屏导航轨道、移动端顶栏和底部导航、时间线卡片、胶囊筛选和轻量动画；主页与详情的内容结构改为仓库实体。
- React Router 使用浏览器端路由，Gin 服务端提供静态资源和单页应用的深链接回退；`/api/*` 不进入静态资源回退。
- AIHOT 的名称与 Logo 不纳入本项目；复用源码时保留 MIT 许可与版权声明，独立检查第三方字体与素材授权。

### 10.3 数据与交互语义

- 热榜必须区分 **GitHub Trending 排名**、**本系统热度排名** 与 **AI 选题优先级**。
- Star 数表示当前累计 Star；24h/7d 值表示净变化，支持负数；采样不足返回 `null + data_quality`，不能显示成 0。
- RepositoryCard 展示仓库、中文摘要预览、24h/7d 净增、轻量曲线、数据质量和收藏；完整 200～500 字摘要放详情页。
- 筛选、分页、排序写入 URL 查询参数，刷新与浏览器前进后退应保留状态。
- 手动刷新、AI 总结、候选审核均是异步/持久化操作；API 接受后展示排队/运行/成功/失败状态，不提前宣布成功。
- 详情页按需请求安全渲染后的 README，公开接口校验源可见性。正文响应不写入浏览器持久缓存。
- 响应式和可用性至少覆盖 390×844、768×1024、1440×900 和 1920×1080，亮暗主题和键盘导航均须验收。
- 系统后台增加可编辑的“备份与恢复”页，支持 R2 设置、每天备份时间、实际保留期（默认五天）的备份列表与一键恢复；新 VPS 的 /admin/setup 使用同一恢复引擎。

### 10.4 接口契约与开发顺序

- 前端只调用本项目 `/api/v1`；不依赖 AIHOT 的 `/api/site/*`、Node SSR Loader 或其站点/行业模块。
- 前后端统一 `RepositoryCardView`、`RepositoryDetailView`、`TrendPoint` 等字段模型与 JSON 快照测试，详情字段允许明确的 `null` 状态。
- 在第 9 章 API 基础上，增加适配页面的 `sort`、`category`、`q`、`cursor` 查询参数，以及第 3.4 节确定的分类统计接口，使用第 9.2 节的游标契约。
- UI-01/02 建立视觉组件与响应式应用壳，可在后端基础阶段并行；UI-03/04 连接采集/增长 API；UI-05 接入中文摘要；UI-06/07 在阶段 4 补齐业务管理、视觉回归与单 Go 服务构建；UI-08 在阶段 5 实现备份和 setup。

## 11. 示例配置与部署

参见 [本地部署配置示例](../configs/example.yaml) 和 [容器部署配置示例](../deploy/container.yaml)。配置包含监听地址、网站对外地址、数据库路径、管理员密码和会话签名密钥。

首次用新版服务启动时，先用临时连接执行迁移，再把初始运行参数、分类及缺失的规则集写入数据库。运行参数与统一修订号存于 `system_settings` 的 `runtime.settings.v1` 键，分类存于 `categories`，发现规则存于 `discovery_rules`。升级保留已有发现规则的 ID、定义及下次执行时间。旧 YAML 的业务值、分类文件和 AI 地址/模型环境变量可用于这次导入；已写入运行设置后，启动直接读取数据库。可编辑的 HTTP 超时、SQLite 连接参数、GitHub 请求预算和并发、采样周期和容量、AI 开关、地址及模型由下一次启动应用。分类与发现规则的生效方式见第 3 章和第 9 章。

后续阶段新增的推荐时刻、增长计算周期、AI 预算、保留期和清理计划随各自功能存入数据库。采样槽、聚合和 AI 日预算的 UTC 口径属于数据契约。

部署要求：
- GITHUB_TOKEN、AI_API_KEY 通过环境变量提供。后台密码与会话签名由部署 YAML 提供，示例文件使用空凭据，Compose 以只读方式挂载实际配置。
- GitHub API 接口使用 api.github.com，Trending 使用 github.com；网络超时/重试分别设置。
- Docker 镜像可多阶段编译 Go + React；SQLite 数据目录 ./data 挂载持久化卷。
- 运行单实例；可选 Caddy/Nginx 反向代理并配置 HTTPS/访问控制。
- 已初始化应用从 SQLite 的 system_settings 读取/保存 R2 与备份策略，后台修改后热更新调度；首次安装从 /admin/setup 仅以进程内存临时配置 R2，恢复成功才写回数据库。
- 正常恢复在同一 Go 进程内通过 SQLite 原生 Backup API 完成，复用原连接池；临时文件在受限 staging/，不建立文件交换日志。
- 支持优雅停止和版本化迁移。`migrate` 仅升级表结构，首次运行服务时导入初始配置及种子查询。
- 运行命令应支持 server、migrate、collect-once、maintenance-once、backup；便于本地测试和故障演练。
- 日志只记录结构化元数据，不打印授权头、完整 URL 中的敏感参数、README 和模型完整输入输出。

## 12. 分期开发任务与交付顺序

### 阶段 0：项目骨架与基础约束

- 建立 Go module、Gin、SQLite migrations、配置加载、日志、测试与 Docker 基础。
- 并行执行 UI-01/02：建立 AIHOT 风格主题变量、基础展示组件和响应式应用壳，沿用本项目 React + TypeScript + Vite 构建。
- 建立 README 内的启动说明与 docs 方案入口。
- 完成数据库初始化，验证 WAL、外键、busy_timeout、健康检查和管理接口鉴权。

验收：本地一个命令启动空数据库服务；迁移可重复执行；配置缺密钥时行为明确。

### 阶段 1：候选发现与 Star 采集

- 实现 GitHub 认证客户端、响应头配额管理、ETag 缓存、Search、Trending HTML 解析。
- 实现 repo_id 合并、任务租约、重试、四级调度和短期快照。提供分类、发现规则和运行配置的后台管理接口。
- 仓库首次入库、改名、304、404、403/429、5xx 等状态处理。

验收：能持续发现候选并更新 Star；重复领取无重复记录；接口预算受控。已有配置和空规则列表在重启后保持，后台规则变更使旧任务失去提交资格。

### 阶段 2：增长计算与存储维护

- 实现 1h/24h/7d/30d 增长、加速度、持续性、Trending 频次、热度评分。
- 建立小时/日聚合表、质量标记、30 天与 180 天趋势接口。
- 实现原始快照 14 天清理、小时 60 天、日 180 天和存储指标；本阶段不实现备份，R2 快照、上传及恢复统一在阶段 5 交付。

验收：缺失窗口不会显示为零增量；聚合重跑幂等；清理后趋势仍可展示保留期内的数据。

### 阶段 3：非中文项目智能摘要与选题

- 增加 README 临时下载、语言识别、SHA/ETag 变化检查。
- 建立统一模型 Provider、结构化输出、200～500 字中文摘要、缓存、限额和任务重试。
- 实现 AI 候选筛选、主题标签、选题状态、用户反馈和重复候选抑制。
- 不保存完整 README；新增测试验证数据库、日志、任务记录均不含 README 全文。

验收：新高热度非中文项目可以生成中文总结；Star 变化不触发模型重复调用；README 变化可按需更新。

### 阶段 4：AIHOT 风格前端集成与稳定性验收

- 按 [AIHOT UI 专项方案](./aihot-ui-adaptation.md) 实施 UI-03～UI-07：仓库卡片、增长榜、时间线、响应式导航、仓库详情、选题池和运行状态页面。
- 固定 Gin 与 React 的 JSON 数据契约，补齐分页、过滤、缓存、状态操作、接口鉴权与合约测试。
- 在亮暗主题、手机/平板/桌面四种宽度进行视觉与交互回归；核对复用的 MIT 源码归属和第三方资源许可。
- 将 Vite 静态构建产物交给 Gin 托管，保持单 Go 服务部署，验证深链接回退及 /api/* 路由边界。
- 连续运行 7～14 天观察配额、数据新鲜度、AI 费用、SQLite 容量和任务积压。
- 基于实际监控数据再调节 200/800 的等级上限和热度阈值。

验收：页面参考 AIHOT 布局并符合本项目 Radix 色阶、字号、0.5px 边框与对齐规则，数据完全来自 github-radar API；实时指标质量正确、异步状态明确、移动/桌面适配可用、数据库增长受控。

### 阶段 5：R2 每日备份与后台一键恢复

- 按 [备份与恢复专项方案](./backup-and-restore.md) 增加 Go backup 模块、SQLite `VACUUM INTO` 快照、Gzip/SHA-256、R2 S3 API 连接与每日备份。
- 备份保留最近 5 个自然日，每天一个对象；超期按独立定时清理任务删除，同日手动备份替换当日对象。
- 将可编辑的 R2 设置与备份策略写入 SQLite `system_settings`；后台保存后无须重启进程即可更新调度与 R2 连接。
- 实施 UI-08，新增管理后台备份配置/列表/恢复页面；空数据库首次安装时通过 /admin/setup 在内存中暂存 R2，选择备份，成功恢复后写入 SQLite。
- 恢复采用临时库预校验及配置/任务整理、排空业务访问、modernc NewRestore 原生事务复制、读取验证及重载配置；沿用同一 sql.DB，正常路径 Go PID 保持不变。

验收：连续 6 天备份后仅存在最近 5 个自然日的每日对象；修改时间和 R2 设置重启后仍有效；新 VPS 从 /admin/setup 一键恢复完整仓库、摘要和选题；异常备份不破坏已有对象。

## 13. 自动化测试与验收用例

必须覆盖以下情形：

1. **首次采样**：仓库已有 10,000 Star，但没有历史快照；24h/7d 增长保持未知。
2. **正常净增长**：两次有效窗口观测 1,000→1,250，正确返回 +250。
3. **取消 Star**：1,000→980，净增长 -20，不能修正为 0。
4. **缺失观测**：停机 48h 后恢复，不能把 48h 变化标为 24h 精确增长。
5. **304**：有可靠上次值时记录新的成功观测，无可靠值时不造数。
6. **仓库改名**：repo_id 相同，历史采样与摘要仍关联。
7. **Trending 解析失败**：旧榜单仍在，失败可见且不会清空数据。
8. **Search 分页/重复**：同一项目跨不同主题重复出现，只创建一个主数据实体。
9. **主限流**：remaining=0 时等待 reset，队列持久；恢复后继续。
10. **次级限流**：403/429 + Retry-After 正确暂停、退避，不密集重试。
11. **任务崩溃**：任务领取后进程退出，租约到期重新执行且幂等。
12. **分级抖动**：热度围绕边界变化时不会频繁升降档。
13. **摘要缓存**：相同来源 fingerprint、Provider、模型、提示词版本且有效时不再次调用模型；外部调用结果未知时停止自动重试，不宣称能够保证供应商不重复计费。
14. **文档更新**：README SHA 改变后重算，高优先级任务才能进入模型队列。
15. **无 README**：从元数据产生来源不足的简短总结，不虚构功能。
16. **恶意 README**：文本中的提示词注入对模型工具、日志和服务配置不起作用。
17. **输出长度**：在证据充足情况下总结 200～500 字，结构化标签符合约束。
18. **原文不落盘**：单元/集成测试搜索 DB、日志、任务 payload、临时文件，均无 README 的唯一标识段落。
19. **聚合后清理**：只有完成汇总且通过安全水位的原始快照才删除。
20. **中途停机**：清理任务断点重启可继续，不重复删除错误时间段。
21. **备份恢复**：从最近一致性备份启动查询，指标/摘要数据可用。
22. **数据库连接**：启用 WAL 与多个读取连接情况下没有长期 SQLITE_BUSY/异常增长 WAL。
23. **人工控制**：收藏仓库保持跟踪，忽略选题不会立即重复推荐。
24. **无 AI 密钥**：采集、趋势、榜单功能正常，AI 功能显示待配置。
25. **导航与深链接**：移动端底部导航、桌面侧栏与浏览器路由正常，Gin 对 /repos/:owner/:name 等深链接返回 SPA 页面，API 使用自身路由。
26. **指标字段契约**：前后端对累计 Star、24h/7d 净增、Trending 原始排名、热度排名和 null 数据质量解释一致。
27. **响应式回归**：390/768/1440/1920 宽度和亮暗主题下，卡片、筛选和图表没有横向溢出、导航失效或对比度问题。
28. **AI 阅读交互**：验证摘要任务和缓存状态，中文 README 直接显示正文，英文 README 在有译文时支持切换原文和中文。
29. **第三方代码与品牌**：可追溯复用组件的 MIT 声明齐全，站名/Logo 为自有品牌，字体和素材授权经过核对。
30. **单实例构建**：Vite 构建产物与 Gin API 可由一个 Go 进程对外提供，生产运行无需 Node SSR。
31. **五天备份**：每天一份，连续六天备份后仅保留最近五个自然日；同日手动备份不增加对象数量。
32. **配置热更新**：后台更改 R2 连接、备份时间和保留天数后立即生效，配置在 SQLite system_settings 中持久化，重启仍可读取。
33. **首次部署恢复**：空数据库可通过受保护的 /admin/setup 在内存会话输入新 R2 连接、选择云端备份，恢复库中旧 R2 配置由新连接完整覆盖并成功验证后才标记完成。旧 VPS 不参与。
34. **同进程原生恢复**：同一 PID/sql.DB 完成恢复，多连接与 WAL 场景可读新数据；校验失败不改旧库，复制未完成原生回滚，具体边界见专项验收。
35. **恢复后的运行状态**：数据库恢复后仓库、Star 指标、AI 摘要、选题及调度正常；新 R2 连接覆盖旧连接，后台和后续备份都使用新配置，管理后台能查看真实恢复结果。
36. **租约过期**：两个领取批次竞争同一任务时，旧 lease_token 无法提交结果；续租失败取消执行，未知 AI 请求不自动重发。
37. **304 与重算**：提示词升级、摘要缺失、force 请求即使遇到 304 也重新获取正文；无 README 时 description 改变会改变缓存键。
38. **容量**：总量满额时新发现项目保持 candidate；关注占满时新关注返回 409，不突破预算；退监控不删除人工记录。
39. **名称复用**：旧仓库失效或改名后，新 ID 可使用同名入库；历史仍属于旧 ID，同名响应不会污染旧采样。
40. **聚合锚点**：原始快照清理前后 30d 增量及质量一致；单样本桶、跨小时边界、无样本桶不伪造增长，24h/7d/30d 可分别返回不同质量。
41. **调用预算**：并发领取、进程重启、JSON 修复和手动 force 均受持久化请求名额限制；恢复旧台账后自动 AI 暂停，启用前提示当日历史费用无法完整恢复。
42. **恢复原子性**：原生复制中途及提交后分别终止进程，重启得到旧库或带当前 R2 配置的完整新库；不重放旧付费任务，提交后重载失败可在同进程重试加载。
43. **手动请求幂等**：相同 requestId 跨调度窗口重试返回同一任务；同一窗口内两个已先后完成的不同 force 请求可各自创建任务；存在其他未决任务时返回 409 并可查询 existingJobId。
44. **输出无效与修复**：模型完整响应但 JSON 无效时，本次调用落库为 failed/invalid_output，下一次修复使用新 call_key 且占新名额；终态落库前崩溃仍按 unknown 停止自动重发。

45. **新增契约**：分类多重匹配及 other 回退一致；同槽手动/自动/304 去重、重试跨槽仍沿原槽；非法状态被 CHECK 拒绝；关注时间幂等；License 缺失可空；游标同值/null 续页与筛选不匹配处理正确；后台及 setup 共用会话鉴权与 401 清理。

46. **计算与榜单契约**：模拟 15 分钟边界验证指标、热度及评级更新，不依赖日汇总；无新采样仍处理窗口过期，积压不补算旧窗口。titleZh 的空值、短句及 40 字截断一致；Trending 无批次/成功空榜/失败保留旧榜的响应可区分，单批次成员与 rank 一致。

核心监控指标及初始目标：
- S 级在 API 可用与配额正常时，采样延迟通常 <20 分钟；A 级通常 <70 分钟。
- Trending 日榜数据刷新周期约 60 分钟；失败以 last_success_at 明确显示。
- HTTP 429/secondary limit 事件可见，并能自动退避恢复。
- GitHub 采集主预算平稳运行不超过设置的 core 软阈值；持续积压触发调低低等级频率。
- 历史快照 14 天后可被批次清理；长时间运行不发生无界文件增长。
- AI 自动总结在日预算内完成，README 仅临时处理。

## 14. 公开技术参考

GitHub API：
- 主/次级限流：https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api
- 配额接口与资源分类：https://docs.github.com/en/rest/rate-limit/rate-limit
- API 最佳实践（含 ETag/304）：https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api
- Repository Search：https://docs.github.com/en/rest/search/search
- 仓库 README：https://docs.github.com/en/rest/repos/contents
- 仓库 Star 历史：https://docs.github.com/en/rest/activity/starring

SQLite：
- WAL 与 checkpoint：https://www.sqlite.org/wal.html
- PRAGMA optimize、VACUUM 与其他维护：https://www.sqlite.org/pragma.html

本方案采用 2026-11-10 检索到的 GitHub 文档作为接口基线。实现时需将 GitHub API 版本设为可配置项，并通过真实 API 集成测试验证响应、权限和配额。

## 15. 本轮修订记录

2026-11-11：按评审补齐分类、采样槽、仓库状态、单管理员鉴权、License/关注时间和 UI 排期；固定初版热度公式、分页与技术选型。根据用户明确要求保留同进程一键恢复，改用 SQLite 原生 Backup API，删除自定义文件切换和恢复日志方案。上述选择为本期实施基线，尚不代表真实驱动、R2 或 UI 已验收。

2026-11-11 补充：固定 15 分钟指标/评级计算周期和配置，titleZh 从持久化摘要首句派生，补全 TrendingResponse 的完整榜单与批次契约，明确维护排空取消时普通任务重试及 AI unknown 的落库边界。
