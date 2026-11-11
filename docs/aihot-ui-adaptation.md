# github-radar 前端设计与 AIHOT 视觉交互适配方案

- 版本：1.5.1
- 日期：2026-11-11
- 执行状态：UI-01～UI-08 已接入 React 与 Gin
- 主文档：[GitHub 热点雷达技术设计](./github-radar-technical-design.md)
- 备份与一键恢复专项方案：[R2 五天备份与后台恢复](./backup-and-restore.md)
- 视觉与交互参考：https://github.com/KKKKhazix/AIHOT
- 参考范围：AIHOT 的整体布局、信息流组织、响应式导航、亮暗主题和交互风格
- 技术约束：前端 React + TypeScript + Vite；后端始终 Go + Gin + SQLite

## 1. 设计决策

### 1.1 产品目标

将 AIHOT 的阅读型热点网站视觉结构应用于“GitHub 开源项目热点雷达”：

- 桌面保留固定侧边导航、居中的内容区、信息流和热点榜卡片；窄桌面切换精简导航轨道。
- 手机保留顶部操作条、底部导航、抽屉式过滤、列表卡片和合理的触控面积。
- 亮色使用暖白背景、近白内容面和蓝灰墨色。暗色使用蓝灰背景和浅墨色。蓝青用于导航、焦点和趋势线，暖色用于榜单名次与状态。色值映射自 Radix UI Colors。
- 保留柔和分割线、圆角卡片、胶囊选择、有限的动效、清晰的信息等级和紧凑的数字展示。
- 用户打开首页首先看见“哪些 GitHub 项目正在增长”，点击项目后看到 Star 趋势、中文 200～500 字摘要、仓库链接与选题价值。

AIHOT 的原业务是“新闻/事件/多信源讨论”；github-radar 的核心实体是 **GitHub 仓库**。UI 外观和交互可借鉴，热度计算、实体身份、数据模型及接口必须使用 github-radar 自己的实现。

### 1.2 边界：复用 UI，不迁移底层

**保留/借鉴**
- CSS 主题变量、字距、间距、响应式断点、背景与文本层级。
- Sidebar / Rail / PhoneBar / TabBar 的视觉和交互。
- 卡片、状态徽标、胶囊标签、搜索、筛选弹层、收藏、返回顶部、骨架屏及加载状态。
- 热度升降提示、轻量曲线图（Sparkline）和信息流时间分组。
- React + TypeScript 的纯展示组件，按依赖情况抽取或重写。

**在 github-radar 内重新实现**
- Go + Gin 提供的 HTTP JSON API、GitHub 采集器、限流器、定时调度、任务恢复和 AI 摘要。
- SQLite 仓库模型、原始 Star 快照、小时/日聚合、候选选题。
- 页面路由和客户端 API 状态管理；不保留 AIHOT 的服务端 React Router loader、Node SSR 进程。
- 仓库详情、增长榜单、技术主题、重点监控、选题池、采集管理的业务逻辑。
- 自有站名、Logo、favicon、产品文案和部署脚本。

**第一期不迁移**
- 新闻聚簇、RSS/微信公众号/X 采集、新闻正文与文章阅读、日报排版编辑器、发布体系、AIHOT 管理后台、PostgreSQL、pg-boss、Fastify、AIHOT site/industry 私有配置结构及与本产品无关的插件模块。

最终只运行 Go 服务：Gin 提供 API 与前端静态构建产物；浏览器下载由 Vite 构建的静态文件，前端本身无需 Node 服务常驻。

### 1.3 推荐技术边界

| 层 | 采用方案 | 说明 |
|---|---|---|
| 组件与语言 | React + TypeScript | 沿用 AIHOT 的组件组织方式 |
| 样式 | Tailwind CSS 4 + 本项目语义变量 | 迁入许可允许的视觉基础，剥离 AIHOT 专有依赖 |
| 客户端路由 | React Router 浏览器路由 | 使用路由组件与客户端请求，不使用 AIHOT SSR/loader |
| 构建 | Vite | 构建 dist 静态资源 |
| 趋势图 | AIHOT Sparkline 适配 + ECharts 详情图 | 小图轻量，完整图表支持缩放/时间选择 |
| API 数据 | Go Gin JSON，统一 /api/v1 | 前端与 Go 通过 HTTP 契约解耦 |
| 数据 | SQLite | 全部写入由后端处理 |
| 认证 | 单管理员 Bearer 令牌 | 输入 ADMIN_TOKEN，sessionStorage 持有；401 清空并重新输入，setup 使用同一令牌 |
| 请求状态 | TanStack Query | 列表缓存、分页、任务轮询；统一 API client 添加 Authorization |
| 部署 | Go 可执行文件 + 静态资源 | 单实例，支持 Docker 与本地运行 |

## 2. AIHOT 源文件审查与组件迁移矩阵

来源仓库默认分支：main；实现前固定一次具体提交版本，记录所复制的源文件与修改，便于追溯许可和修复。AIHOT 来自 React Router 框架模式，只有无服务端依赖的展示组件可直接抽取；UI-01/02 需计入移除 loader/SSR、重写导航与构建验证的工作，不按整页直接搬入估时。

| AIHOT 路径（均相对其仓库根） | 价值 | 处置 | 适配要点 |
|---|---|---|---|
| apps/web/app/app.css | 颜色、尺寸、间距、暗色主题、卡片样式 | **高优先级抽取** | 提取语义 token 与必要组件样式，去掉对 site/modules 的 CSS @source |
| apps/web/app/components/ui/Badge.tsx | 精选/热度/状态徽标 | **可直接适配** | 将新闻 badge 语义改为“爆发、新上榜、关注、摘要就绪”等 |
| apps/web/app/components/ui/Controls.tsx | 按钮、筛选下拉、切换标签 | **可直接适配** | 保留主题样式和无障碍属性 |
| apps/web/app/components/ui/Tabs.tsx | 胶囊切换与滑动指示器 | **剥离依赖后复用** | 改为本项目 Router/状态，不携带 AIHOT 的导航结构 |
| apps/web/app/components/ui/Page.tsx | 阅读布局、双栏、空状态 | **选择性适配** | 保留容器宽度和空状态；仓库详情数据布局重新组合 |
| apps/web/app/components/shell/Sidebar.tsx | 桌面侧栏、窄屏 Rail | **保留外观，重写导航** | 移除 @aihot/site、webModules、品牌标记、变更日志逻辑 |
| apps/web/app/components/shell/PhoneBar.tsx | 手机顶部导航和返回交互 | **保留交互，适配路由** | 维护返回目标、操作按钮和安全区域 |
| apps/web/app/components/shell/TabBar.tsx | 手机底部导航 | **保留外观，重写入口** | “热榜/发现/选题/关注/我的”按实际页面定制 |
| apps/web/app/components/shell/ThemeSwitch.tsx | 深浅色切换 | **重用行为概念** | 设置存浏览器，本项目独立 theme 状态和首屏无闪烁 |
| apps/web/app/features/feed/FeedItem.tsx | 文章卡片的密度和分层 | **参考样式，重写组件** | 用 RepositoryCard 显示仓库/Star/摘要，不保留新闻类型 |
| apps/web/app/features/feed/Timeline.tsx | 日期分组、时间线、增量列表 | **参考布局，重写数据绑定** | 按分类展示项目发现时间流，数据分页加载 |
| apps/web/app/features/feed/Filters.tsx | 分类与筛选交互 | **保留交互模式，重写规则** | 主题、编程语言、增长窗口、等级、多选处理 |
| apps/web/app/features/feed/HotTopics.tsx | 热点内容展示 | **参考可视布局** | 改为增长领跑仓库和主题热点 |
| apps/web/app/features/hot/Sparkline.tsx | 空值断线、24h 小曲线 | **优先适配** | 使用 Star 净增或累计 Star 序列，保持 null 断线 |
| apps/web/app/features/hot/Delta.tsx | 上升/下降/新增标签 | **参考交互，重写语义** | 指标明确写 24h 增量、排名变化，不混淆二者 |
| apps/web/app/routes/home.tsx | 精选页排版 | **重写路由与数据逻辑** | 适配 /api/v1/repos、首页精选仓库 |
| apps/web/app/routes/hot.tsx | 热点榜布局 | **重写路由与数据逻辑** | 按仓库增长和数据质量排序 |
| apps/web/app/lib/api.server.ts | Node 服务端 fetch/SSR 缓存逻辑 | **重写** | 使用浏览器 fetch、AbortController、分页和统一错误处理 |
| apps/web/app/root.tsx | SSR HTML 根、全局模块与导航 | **重写** | 自己的 AppShell、路由、主题、错误页和权限边界 |
| apps/web/app/routes.ts | 原新闻路由 | **重写** | 使用下文明确的仓库路由 |
| apps/web/app/components/BrandMark.tsx 及 site/brand/ | AIHOT 品牌 | **使用本项目品牌实现** | 不复制 AIHOT 名称及 Logo |

### 2.1 源文件复用方式

- 第一优先：CSS token、纯展示组件和不依赖领域模型的动画/控件。
- 第二优先：导航容器和响应式外壳，保留 DOM/视觉行为，重新定义 props、routes 和 icons。
- 领域组件（新闻卡片、事件热度、原始报道、聚簇）一律按仓库业务重新建模，**不用“强行把 Repo 转成 News”** 的兼容层。
- 迁入文件的 import 不得保留对 @aihot/contracts、@aihot/site、@aihot/industry、apps/api 等包的依赖。
- 可保留 React Router 的客户端功能，但不引入 SSR 环境变量、服务端 route loader 或 Node 执行路径。
- AIHOT 原库使用 MIT 许可证；若复制或改编源码，应在本项目保留原版权与许可文本，并维护第三方声明（如 web/THIRD_PARTY_NOTICES.md）。站名与 Logo 使用 github-radar 的自有资产；第三方字体、图像或品牌资产单独核对许可。参考仓库 LICENSE、NOTICE。

### 2.2 本项目主题参数与 UI 约束

AIHOT 的语义变量命名可参考，颜色值必须映射到 Radix UI Colors。亮色使用 light 色阶，暗色使用对应 dark 色阶，组件只引用语义变量；图表、状态色、遮罩和阴影也遵守这一约束。

| 语义 | Radix 色阶 | 用途 |
|---|---|---|
| --bg / --surface / --sidebar | 亮色 sand、brown、whiteA，暗色 slate、cyan | 区分页面底色、内容卡与导航 |
| --ink / --ink-muted | slate-12 / slate-11 | 主文字和次级文字 |
| --control / --raised | 亮色 sand、sage、whiteA，暗色 slate | 搜索底色、筛选底轨和选中面 |
| --line / --control-line | 亮色 sage-5/7，暗色 slate-5/7 | 分隔线与控件边框 |
| --accent / --accent-text / --focus | cyan-11/12 | 导航、可见焦点和主要操作 |
| --hot / --ok / --warning | tomato-11 / green-11 / amber-11 | 热度、成功和警示 |
| --rank-first / --rank-second / --rank-third | tomato、brown、amber | 榜单前三名 |
| --overlay / --shadow | blackA 色阶 | 仅必要弹层的遮罩/阴影，按对比度选取级别 |

优先用留白和字体层级组织界面，不为每条信息增加卡片、阴影或左侧彩色边线。需要圆角时默认控件 8px、容器 12px，嵌套圆角结合内间距保持同心；胶囊仅用于选择控件。移动端点击区域尽量不低于 44×44px，数字使用等宽数字特性。

- 所有边框与分隔线使用 0.5px；焦点使用可见 outline，不以移除键盘焦点换取视觉简洁。
- 全站文字默认 12/14/20px 三档，最小 12px，不超过四档；优先用字重、颜色、间距表达层级。按钮文字、图标与数字进行光学对齐。
- 默认无装饰动画；必要状态过渡以 ease-out、250ms 为基准，尊重 prefers-reduced-motion。
- 文案不使用中点分隔，不添加重复眉题、装饰性英文标签或解释性长描述；采样不足、可能计费、恢复影响等会改变用户决策的信息保留在对应操作处。
- 对照 AIHOT 官网确认页面、内容面和导航的层次。实色按钮前景使用 Radix 提供的对比色，并检查亮暗模式对比度。

设计断点取 AIHOT 的布局思想：
- 小于 700px：手机顶栏 + 底部导航，详情主列优先。
- 700～940px：窄桌面/平板，使用紧凑 Rail 或单列布局。
- 941px 及以上：桌面侧边栏和全宽时间线；宽屏可使用两栏。
- 1176px 及以上：仓库详情可显示主内容 + 辅助侧栏。
- 横屏和刘海安全区域使用 env(safe-area-inset-*)。
- 深浅主题切换遵守系统偏好与用户选择，首屏避免主题闪烁；动画尊重 prefers-reduced-motion。
- CSS 用语义变量而不是复制大量分散的颜色常量；禁止复用 AIHOT 标志或原站站名。

## 3. 页面信息架构与路由

### 3.1 统一导航

**桌面侧栏**（保留 AIHOT 样式和层级）：
- 首页 /：展示当前热点和项目时间线，顶部标签可在后台配置，右侧提供搜索。
- 热点榜 /trending：24h/7d 增长排名与官方 Trending 上榜标签。
- 全部项目 /repos：已监控仓库按发现时间展示，顶部分类标签从数据库读取，右侧提供搜索。
- 主题 /categories：从后台维护的 topics 映射获取分类，默认含 AI 智能体、AI 编程、MCP、AI 多媒体、开发工具及其他。
- 收藏 /starred：展示当前浏览器收藏的仓库，按收藏时间倒序排列，支持取消收藏。
- 选题池 /admin/topics：按入池日期展示选题版面，往期索引、状态筛选和反馈使用已保存的仓库数据。
- 重点监控 /admin/watchlist：按加入时间展示重点监控仓库，顶部提供搜索和结果数量。
- 运行状态 /admin/runs：与其他后台页面共用主区宽度。侧栏保留一级入口，标题下方的横向导航用于切换概览、任务、运行配置、分类映射、发现规则、发现页标签和备份恢复，窄屏自动换行。概览展示采集与模型额度、队列、各类任务最近状态、任务时间线和 SQLite 维护。运行配置使用页面内左侧分组导航，窄屏在表单上方横向滚动。分类映射和发现规则支持搜索、拖动排序、编辑与删除。发现页标签支持点击名称编辑、拖动排序、选择现有分类或新建分类。
- 仓库管理 /admin/repos：参考后台信源列表，在同一张表格内提供仓库搜索、分类、排序和高级筛选。点击仓库名称进入 /admin/repos/:owner/:name，详情复用图表，并提供加入或移出重点监控、刷新采样、生成摘要和加入选题池。返回列表时保留搜索和筛选条件。
- 初始化 /admin/setup：登录后通过临时内存会话配置 R2，从前缀内可用备份中选择并恢复。成功后关闭安装写入口，状态与进度仍可查询。

**前台**公开浏览。手机底栏提供发现、项目、热点榜、收藏和更多，更多菜单提供主题和后台入口。**后台**从 /admin/login 登录，桌面与手机均提供仓库管理、重点监控、选题池、运行状态。

### 3.2 页面映射与展示目标

| AIHOT 原页面/概念 | 新路由 | 对应 UI | 主要数据 |
|---|---|---|---|
| 精选 / 首页 | / | AIHOT 时间线、精选卡、热点推荐 | 最近发现 + 热度 Top + 最近新上榜 |
| 热点榜 | /trending | 榜首跨两行、右侧叠放第二第三名，后续排名按同一列宽展示 | 自算热度/净增、累计 Star 和原始 Trending 名次 |
| 全部动态 | /repos | 分类标签、搜索、日期流和右侧书签 | 分类映射、repositories 分页与浏览器收藏 |
| 文章/事件详情 | /repos/:owner/:name | 独立公开页面、面包屑、粘性章节导航、摘要与趋势正文、分卡信息栏 | repo + metrics + ai_summary |
| 主题列表/主题页 | /categories、/categories/:slug | 宽屏四列卡片、窄桌面三列、手机列表、分类标签与日期流 | 分类统计、最新仓库预览与项目收藏 |
| 收藏 | /starred | 浏览器存储提示、桌面阅读卡片、手机分隔线列表 | 浏览器 localStorage |
| 重点监控 | /admin/watchlist | 顶部搜索、仓库数量，与收藏共用阅读卡片和手机分隔线布局 | starred_by_user、followed_at 和仓库摘要 |
| 日报/精选 | /admin/topics | 往期索引、日期报头、头条、状态分版、反馈与导出 | topic_candidates 的入池日期与已保存内容 |
| MyHOT 后台运行与信源 | /admin/runs | 五格运行指标、双栏监控面板、任务表格、可筛选的发现规则及分类映射列表、独立编辑表单 | jobs/limits/storage、数据库配置、R2 备份与恢复 |
| 首次部署 | /admin/setup | 初始化向导/云端恢复 | 连接 R2、列出备份、选择恢复 |

### 3.3 项目卡片 RepositoryCard

将 AIHOT FeedItem 的视觉结构迁移为：

- 发现时间流按日期分组，卡片左侧时间轨标记时刻。收藏和重点监控默认按添加时间倒序，卡片顶部展示仓库元数据、添加时间和移除入口。
- 卡片顶部展示主语言和上榜信号。全部项目的卡片右侧提供收藏按钮。仓库 owner/name 作为正文标题，接着展示后端 titleZh（已有中文摘要首句，最多 40 个 Unicode 字符，超长追加省略号）。缺少 titleZh 时展示原始 description。摘要状态随仓库数据展示。
- 第三行：中文摘要预览 80～120 字；摘要以标题开头时只展示一次。完整 200～500 字放仓库详情。
- 第四行：Star 总数、24h 净增长、7d 净增长、相对增长率（有质量保障才显示）。
- 底部展示迷你曲线、最后采样时间和数据质量。
- 卡片点击进入 /repos/:owner/:name，详情页不选中侧栏菜单。仓库原站使用独立外链，新窗口带 rel=noopener noreferrer。
- “官方 Trending 排名”、“自算热度排名”、“AI 选题评分”分别标示，不用同一个数字暗示同一种分数。
- 24h 增长未知时显示“采样不足”，不能用 0 或虚构数据填充；S/A/B/C 等级以状态标签展示。

### 3.4 仓库详情 RepoDetail

- 面包屑、仓库名和 GitHub 指标条位于章节导航上方。左栏展示中文摘要与 Star 趋势，右栏依次展示热点信号、GitHub 仓库及 Trending 名次、相关主题和监控记录。License 缺失时显示“未声明”，NOASSERTION 显示“未识别许可”，其余优先显示名称。
- 24h/7d/30d 增长、数据质量和上次采集时间。
- 24h/7d/30d/180d 趋势：按 API 返回粒度自动切换原始/小时/日聚合，并标明缺失点。
- **AI 导读和 README**：导读显示已有中文摘要与来源。README 正文置于导读下方，中文原文直接展示，生成译文后提供「中文」「原文」标签。
- “生成总结”允许命中有效缓存；“重新总结”发送 force=true 并计入手动预算。页面显示 queued/running/ready/failed/stale/unknown，unknown 明确说明可能已计费，仅在管理员选择再次调用时确认这一影响。
- 后台仓库详情提供加入或移出重点监控，以及加入选题池，人工操作反馈明确。
- 小屏按摘要、仓库信息、Star 趋势的顺序单列展示。章节链接保留已选择的趋势范围。

### 3.5 热点榜 HotRanking

首页的「全部」标签有有效热点时展示热点前五名、热度分、小趋势图和最近成功计算时间。各标签直接展示项目时间线，空列表复用收藏空态。完整榜单的榜首使用大卡和 Star 趋势，第二、第三名在右侧叠放。后续排名对齐名次、仓库信息、Star 趋势和当前榜单指标。窄屏依次展示前三卡片与排名列表。

- 可切换 24h 净增长、7d 净增长、热度综合分、最近上榜。
- 显示每条数据对应的时间窗口和采样质量；增长不完整时降级显示。
- Sparkline 使用采样/聚合时间点，缺测时折线断开，不能把缺失补 0。
- 增长曲线中的“上升”明确表示增长速度/排名变化，不等于仓库 Star 绝对值上涨。
- 以项目 repo_id 作为列表 key，不以排名作为实体 ID。

### 3.6 主题和选题池

- 分类多选、关键词搜索，支持“AI 智能体 / AI 编程 / MCP / AI 多媒体 / 开发者工具”等。
- 主题目录展示每个分类的最新监控仓库、简介、发现时间和仓库数。主题详情用分类标签切换，项目行右侧可收藏。
- 分类以仓库标签与规则为基础，中文摘要仅用于增强说明，不作为唯一分类证据。
- 选题卡片保留“用户收益、可演示程度、复现要求、技术亮点、证据链接、人工状态”。
- status 操作：candidate → shortlisted / accepted / ignored / published；状态变化在 API 成功后更新。
- Markdown/JSON 导出只包含候选与摘要、仓库元数据，不包含 README 原文。

## 4. 前后端数据契约

### 4.1 REST 原则

- 后端：Gin 路由前缀 /api/v1；前端不调用 AIHOT 的 /api/site/*。
- 时间：对外统一 ISO 8601 UTC，例如 2026-11-10T11:00:00Z；数据库内部可以使用 Unix 毫秒。
- Star 当前数是仓库累计值，增长是净值；缺失增长以 null 返回，附质量标识。
- 大列表游标分页；limit 默认 30、最大 100；筛选条件在 URL 查询参数中，刷新和返回后仍保留。
- 普通展示 GET 可以缓存少量时间；管理状态和 POST/PATCH 不缓存；突发限流时保留 last_success_at。
- API 数据模型由本项目维护，不引入 @aihot/contracts。

### 4.2 TypeScript 共享契约草案

~~~typescript
export type DataQuality = "exact" | "estimated" | "insufficient";
export type SummaryStatus = "missing" | "queued" | "running" | "ready" | "stale" | "failed" | "unknown";
export type SourceQuality = "sufficient" | "incomplete_source" | "insufficient_source";
export type Resolution = "raw" | "hourly" | "daily" | "mixed";
export type RepoGrade = "S" | "A" | "B" | "C";
export type RepoState = "candidate" | "active" | "paused" | "unavailable" | "address_pending";
export interface CategoryView {
  slug: string;
  name: string;
  count: number;
  latestRepo: { id: number; fullName: string; description: string | null; discoveredAt: string } | null;
}

export interface GrowthWindow {
  delta: number | null;       // 净变化，不是新增收藏人数
  quality: DataQuality;
  startAt: string | null;     // 实际起点观测时间，不是名义窗口起点
  endAt: string | null;
  resolution: Resolution;
}

export interface TrendingEntry {
  sourceKey: string;         // 如 all、language:python
  period: "daily" | "weekly" | "monthly";
  rank: number;
  observedAt: string;
}

export interface TrendingResponse {
  sourceKey: string;
  period: "daily" | "weekly" | "monthly";
  batch: {
    observedSlot: number;    // UTC Unix 毫秒，计划批次键
    observedAt: string;      // 实际成功时间，UTC ISO8601
  } | null;
  items: RepositoryCardView[]; // 同一批次，按该来源 rank 升序，全批返回
  updatedAt: string | null;  // 等于 batch.observedAt；尚无批次则 null
}

export interface SparklinePoint {
  at: string;               // UTC ISO8601，固定小时网格
  stars: number | null;
  observedAt: string | null; // 数值来自的真实采样时间
}

export interface RepositoryCardView {
  id: number;                 // GitHub 数字仓库 ID
  fullName: string;
  htmlUrl: string;
  description: string | null;
  titleZh: string | null;     // summary_zh 首句派生，规则见主方案 7.4
  language: string | null;    // 编程语言
  categories: string[];      // 当前数据库映射派生的 slug，未命中为 ["other"]
  state: RepoState;
  stars: number;
  forks: number;
  grade: RepoGrade;
  growth24h: GrowthWindow;
  growth7d: GrowthWindow;
  hotnessScore: number | null;
  hotnessRank: number | null; // 系统内排名
  trendingEntries: TrendingEntry[]; // 各来源最新一次成功完整榜单中的名次
  sparkline: SparklinePoint[]; // 最近 24 个已完成 UTC 小时桶，每桶最后观察，无观察则 null
  summaryPreview: string | null;
  summaryStatus: SummaryStatus;
  summarySourceQuality: SourceQuality | null;
  followed: boolean;
  followedAt: string | null;
  lastSuccessAt: string | null;
}

export interface RepositoryDetailView extends RepositoryCardView {
  license: { spdxId: string | null; name: string | null } | null;
  summaryZh: string | null;   // 目标 200～500 字；来源不足允许更短
  sourceReadmeUrl: string | null;
  summaryGeneratedAt: string | null;
  summaryModel: string | null;
  growth30d: GrowthWindow;
}

export interface TrendPoint {
  at: string;
  stars: number | null;
  delta: number | null;
  quality: DataQuality;
  startAt: string | null;
  endAt: string | null;      // 计算 delta 的真实两端，不能用桶时间代替
}

export interface TrendResponse {
  points: TrendPoint[];
  resolution: Resolution;
  updatedAt: string;
}

export type JobStatus = "pending" | "running" | "succeeded" | "failed" | "dead" | "unknown" | "canceled";
export interface JobView {
  id: number;
  type: string;
  status: JobStatus;
  attempts: number;
  maxAttempts: number;
  errorCode: string | null;
  repoId: number | null;
  createdAt: string;
  updatedAt: string;
}

export type SummaryResponse =
  | { cached: true; summaryStatus: "ready" }
  | { jobId: number; statusUrl: string };

export interface ApiList<T> {
  items: T[];
  nextCursor: string | null;
  updatedAt: string | null; // 指标榜首轮计算成功前为 null
}
~~~

Go HTTP DTO 需使用 JSON tags 与上方字段名称一致（lowerCamelCase）。实现时自动维护 OpenAPI 或 Go contract tests，以固定 JSON 快照测试防止字段漂移。

增长质量来自每个窗口自己的端点，不能把热度综合 data_quality 复制到全部窗口。Trending 来源失败时可保留上次完整榜单，但必须展示 observedAt；最新成功榜单已无该仓库时移除该来源名次，不用历史最优排名冒充当前排名。Sparkline 统一展示累计 Star，不在不同卡片混用增量；24 个小时网格一致，缺测断线，不把 C 级一天一次采样画成连续小时走势。来源不足/截断与任务执行状态分别展示，合法短摘要可为 ready + insufficient_source。

TrendingResponse 的 batch=null 表示尚无成功抓取；成功空榜仍有 batch。失败时继续展示旧批次及其 observedAt，不用请求时间更新榜单时间；updatedAt 同 observedAt，无批次时为 null。period/sourceKey 参数与排序见主方案第 9 章。热度/评级默认每 15 分钟重算，UI 以成功计算结果为准，不把每次采样等同于评级已更新。

### 4.3 主要页面到 API 映射

| 前端页面/交互 | API 请求 | 返回/动作约束 |
|---|---|---|
| 首页最近发现列表 | GET /api/v1/repos?sort=discovered&limit=30 | 返回仓库卡片数据与游标 |
| 首页热点推荐 | GET /api/v1/repos?sort=hotness&limit=10 | 归一化热度与更新时刻 |
| GitHub 热榜 | GET /api/v1/trending?period=daily&sourceKey=all | TrendingResponse，完整批次不分页，不过滤非 active 成员 |
| 自算增长榜 | GET /api/v1/repos?sort=growth24h 或 growth7d | ApiList<RepositoryCardView>，与 GitHub 榜单独立 |
| 全部项目检索 | GET /api/v1/repos?q=...&category=...&language=...&cursor=... | URL 驱动过滤/分页 |
| 项目详情 | GET /api/v1/public/repos/by-name/:owner/:name | 返回公开 RepositoryDetailView |
| Star 曲线 | GET /api/v1/repos/:id/trend?range=7d | TrendResponse，含点位、真实边界与粒度 |
| 加入或移出重点监控 | POST /api/v1/repos/:id/follow | 请求体 {follow:true/false}；更新后返回状态，容量占满返回 409 monitoring_capacity_full |
| 点击刷新 | POST /api/v1/repos/:id/refresh | {requestId}；202 + jobId/statusUrl，异步查询结果 |
| 点击生成/重新总结 | POST /api/v1/repos/:id/summary | {force,requestId}；200 缓存命中或 202 + jobId/statusUrl；其他请求已有未决任务时 409 + existingJobId/statusUrl |
| 查询采集/摘要任务 | GET /api/v1/jobs/:id | 管理鉴权，返回 JobView；维护期可 503 |
| 确认再次调用模型 | POST /api/v1/jobs/:id/retry | 仅 unknown 摘要，{requestId,acceptPossibleCharge:true}，原子取消旧任务并创建手动任务 |
| 恢复后启用自动 AI | POST /api/v1/system/ai/resume | {acceptIncompleteUsage:true}，提示恢复日计量不完整 |
| 主题页 | GET /api/v1/repos?category=agent | 使用当前数据库的 topics 映射，重复 category 参数为 OR |
| 分类列表 | GET /api/v1/categories | {items:CategoryView[]}，每项包含 active 计数和可为空的最新仓库预览 |
| 重点监控 | GET /api/v1/repos?followed=true&sort=followed | 包含重点监控名单中的全部仓库状态，followedAt 降序 |
| 选题池 | GET /api/v1/topics | 返回候选、得分和状态 |
| 往期选题 | GET /api/v1/topics/days | 按选题时区返回入池日期、首条标题和计数，可按状态筛选 |
| 加入选题池 | POST /api/v1/repos/:id/topic | 幂等加入并保留已有审核状态 |
| 改变选题状态 | PATCH /api/v1/topics/:id | 成功后更新，本地失败恢复 |
| 管理页任务 | GET /api/v1/system/jobs | 鉴权，绝不返回 README 内容 |
| API 配额页 | GET /api/v1/system/limits | 分 core/search 配额 |
| 存储状态页 | GET /api/v1/system/storage | SQLite 使用量与安全清理水位 |
| 运行配置 | GET/PUT /api/v1/system/settings | 保存后按 restartRequired 提示重启 |
| 分类管理 | GET/PUT /api/v1/system/categories | 编辑名称及 topics 映射，保存后刷新分类和仓库查询 |
| 发现规则 | GET/PUT /api/v1/system/discovery-rules | 编辑启用状态、查询、周期和优先级，规则变更由下次扫描安排新任务 |
| 备份配置页 | GET/PUT /api/v1/system/backup/settings | 加载/保存每日时间、保留天数、R2 连接（密钥脱敏） |
| 连接测试 | POST /api/v1/system/backup/test | 检查 R2 连接并给出明确结果 |
| 备份列表 | GET /api/v1/system/backup/list | 按实际保留策略显示云端备份，默认 5 天；可分页查看未清理旧对象 |
| 立即备份 | POST /api/v1/system/backup/run | 异步生成并覆盖当天备份对象 |
| 一键恢复 | POST /api/v1/system/backup/restore | 临时库准备后进入维护模式，同一 Go 进程内原生事务恢复，复用连接池 |
| 恢复状态 | GET /api/v1/system/backup/operations/:id | 通过独立内存操作管理器跟踪恢复状态；短暂维护期间仍可轮询 |
| 首次安装 | /api/v1/setup/r2/test、r2/session、backups、restore、initialize | R2 设置仅存临时进程内存，预先写入待恢复库；使用同一 ADMIN_TOKEN |
| 安装/恢复状态 | GET /api/v1/setup/status、GET /api/v1/setup/operations/:id | ADMIN_TOKEN 鉴权，初始化完成后仍可读取进度 |
| 重试配置加载 | POST /api/v1/system/backup/reload | databaseApplied=true 且重载失败时只重试加载，不再次复制 |

查询参数、分类和游标使用主方案第 3.4、9.2 节契约。cursor 由后端生成，前端原样透传，修改筛选或排序后清空游标与分页结果。前台使用 /api/v1/public 的只读接口，后台和安装接口统一经 Bearer 鉴权。两类仓库响应分别缓存。License 无对象时为 null，NOASSERTION 原样传递并显示“未识别许可”，其他有效对象优先显示 name。

### 4.4 加载、失败和异步状态

- 列表：首屏骨架、空结果、上游暂时不可用、加载更多、防重复请求。
- 详情：获取失败、仓库失效、增长质量不足、摘要待生成、摘要生成失败。
- 手动刷新：后端接受任务后显示“已排队”并可查看任务状态；前端只轮询任务查询接口，不因按钮点击直接修改星数。
- failed 是仍可能退避重试的任务状态；succeeded/dead/canceled 为终态。unknown 停止自动轮询并提示人工处理；404 表示任务不存在或已过保留期，重新查询仓库结果，不能直接显示成功。
- 已有摘要的更新任务失败或 unknown 时继续展示上次成功摘要及生成时间；任务状态与摘要内容有效性分开，不清空旧摘要。
- /admin/setup 使用管理员令牌。恢复 completed 后清空查询缓存、重新加载数据。若 databaseApplied=true 但重载失败，显示“数据已恢复，加载失败”及重试加载操作。进程重启后操作 404 表示无法确认，刷新安装状态和系统状态。
- 429/503：显示上次成功采集时间与可重试提示；不用旧值冒充实时数据。
- 退出页面取消无关 GET 请求；对 POST/PATCH 避免重复提交；网络超时对用户可解释。
- 手动采集/摘要的 requestId 按用户操作生成，网络重试沿用，明确再次操作才更新；409 summary_in_progress 时转而查询 existingJobId，不自动生成新 ID 连续重试。
- 使用 TanStack Query 管理分页缓存和轮询，401 时清空令牌与缓存；不复制 AIHOT Node SSR 页面缓存。POST/PATCH 默认不自动重试，GET 依错误类别有限重试；503 维护期恢复进度仍可轮询。
- 前台收藏通过浏览器 localStorage 保存，刷新和同源标签页共享收藏列表。后台重点监控使用 SQLite 存储，用于后续采集与跟踪。

## 5. 交互验收细节

### 5.1 响应式与可访问性

- 390×844：顶部栏、底部标签，内容卡单列，24h/7d 增长清楚可读，无横向滚动。
- 768×1024：紧凑导航轨道或折叠布局，不出现侧栏挤压主内容。
- 1440×900：桌面固定侧栏，主区从侧栏后按固定边距铺开，主题目录为四列。
- 1920×1080 及更宽：主区沿用相同边距，阅读页由页内布局安排主栏和侧栏。
- 亮/暗主题组件色彩一致；主题切换后刷新保持；系统减少动态效果时关闭复杂动画。
- 允许键盘 Tab 导航，筛选弹层有焦点管理与 Esc 关闭，按钮有文本或 aria-label。
- 主题对比度、聚焦状态、空态和错误态需在亮暗主题分别检查。
- 路由回退恢复筛选项、滚动位置与详情来源；移动端打开筛选弹层不能造成背景滚动失控。

### 5.2 数据真实性

- 无历史基线：24h 显示“待观察”，不显示 0。
- 采样中断：图表缺口明确断线；若只有估计值，图例/悬浮提示显示 quality。
- Trending 和系统热度独立标注；用户不能从文案误解二者为同一排名。
- 当前累计 Star 和最近 24h 净增长不混用。
- 无 AI 摘要时显示原始简介与“可生成总结”按钮；成功后显示最新合格摘要，目标 200～500 字，来源不足的更短结果也允许展示并标注原因。
- 详情的 README 读取接口返回安全渲染后的公开正文与匹配译文。原始 Markdown 不作为静态资源或持久存储。

### 5.3 许可与归属

- 抽取 AIHOT MIT 代码后保留许可和版权；维护可追溯的源路径与源版本记录。
- 使用 github-radar 自有站名/Logo/favicon。
- 不直接嵌入 AIHOT 专有字体文件、名称、Logo 或演示图；第三方字体分别按许可证处理。
- 页面设计参考 AIHOT，但用户不应误认为这是 AIHOT 官方网站或关联产品。

## 6. 实现任务和建议提交边界

每阶段形成可运行、可审查的增量，避免先复制整个 monorepo 再大规模删除。

### UI-01 设计变量及合法复用

- 固定 AIHOT 上游提交引用，整理允许复用的源文件。
- 将语义颜色映射到 Radix Colors，并按本项目三档字号、0.5px 边框及圆角规则适配按钮/标签，复用布局断点。
- 建立 github-radar 自有品牌资源和第三方代码声明。
- 建立主题切换、CSS 冒烟页与深浅色截图基线。

**验收：** UI 组件独立在 web/ 构建通过，不依赖 @aihot/* 包，亮暗主题可切换。

### UI-02 响应式应用外壳

- 实现 AppShell、DesktopSidebar、CompactRail、MobileTopBar、MobileTabBar、ThemeSwitch。
- 实现上述路由和导航激活规则；小屏筛选弹层/搜索入口。
- 支持直接访问深链接时 Gin 回退 index.html，/api/* 永不被 SPA 回退吞掉。

**验收：** 390/768/1440/1920 宽度导航正常，刷新深链接仍打开目标页面；键盘操作可用。

### UI-03 仓库卡片与信息流

- 实现 RepositoryCard、GrowthBadge、TopicChip、Sparkline、RepoFilter、RepositoryTimeline。
- 用模拟 fixture 展示完整/缺失/增长为负/摘要缺失四种状态。
- 首页、全部项目、热点榜采用统一卡片数据契约与不同排序。

**验收：** 每个卡片和图表的数值语义准确；null 不被显示成 0；移动端布局稳定。

### UI-04 Go API DTO 与页面联调

- Go 端定义 RepositoryCardView、RepositoryDetailView、TrendPoint 等接口 DTO。
- 实现 /repos、/trending、/repos/:id、/repos/:id/trend 等接口，处理 filter、排序、游标。
- 浏览器端增加请求取消、错误映射、分页加载、缓存策略。
- 编写 Go JSON 快照测试及前端 mock contract 测试。

**验收：** 真实 SQLite 数据能展示；切换排序、筛选、分页与浏览器前进后退结果正确。

### UI-05 仓库详情与 AI 中文摘要

- 详情页使用 AIHOT 阅读型布局，增加 Star 主图、中文摘要、来源链接、最后生成日期。
- 对接 POST /repos/:id/summary、任务状态与刷新；摘要状态明确。
- 详情页的 README 入口只读取当前公开内容，模型输入不出现在接口响应中。

**验收：** 摘要任务成功/失败/unknown/缓存命中均有正确 UI；目标 200～500 字，短摘要显示 summarySourceQuality；force 与普通生成语义明确，不触发自动重复付费调用。

### UI-06 选题池、关注与业务管理（阶段 4）

- 完成 /admin/topics、/admin/watchlist、/admin/runs 的业务状态页面和操作。
- 接入关注及 followedAt、选题状态、系统配额、采集积压、清理水位。
- 接入阶段 1 的配置管理接口。保存时携带 revision，收到 settings_conflict 时重新加载，保留用户的未保存输入供核对。
- 统一令牌输入、请求鉴权、退出及 401 失效处理。

**验收：** 关注时间和审核数据刷新后仍存在；配额/任务数与后台一致；鉴权失败不能读取或修改业务数据。本任务不依赖备份后端。

### UI-07 视觉/体验回归与构建交付

- 参考 AIHOT 布局，依据本项目 Radix 色阶、字号、边框、对齐规则建立桌面/手机亮暗主题截图基线。
- 使用 Playwright 或等效浏览器测试覆盖关键跳转和异常状态。
- 测试 5,000 仓库分页列表（每页 30～100），避免一次加载全部。
- Vite 产物由 Go Gin 正确托管，缓存静态资源并支持 SPA fallback。
- Docker 一次构建最终 Go 运行镜像；不依赖常驻 Node 进程。

**验收：** 实现仓库实际提供的构建、TypeScript 类型检查、Go 单测与 API 合约检查通过；颜色均来自 Radix，边框 0.5px、文字不少于 12px 且默认三档，无中点分隔/重复眉题，键盘焦点和亮暗对比度合格；构建后可在单 Go 实例访问所有页面。

### UI-08 备份与恢复（阶段 5）

- 实现后台 R2 设置、备份时间/时区/保留期、云端列表、立即备份和一键恢复。
- 实现 /admin/setup 的内存 R2 会话、全新初始化和恢复入口，共用 ADMIN_TOKEN。
- 实现独立操作轮询、维护状态、配置重载失败后的重试及自动 AI 重新启用。
- 对新增页面补做 UI-07 的响应式、亮暗主题与交互回归。

**验收：** 设置重启后可读；保留期 5→30 的列表正确；正常恢复 Go PID 不变，维护期可查询进度；当前 R2 配置随恢复生效；断线不会自动重复恢复，提交后失败不冒充未恢复；自动 AI 待明确启用。

## 7. 推荐目录

~~~text
github-radar/
├── cmd/radar/
├── internal/
│   ├── api/
│   ├── github/
│   ├── scheduler/
│   ├── metrics/
│   ├── summary/
│   └── storage/
├── migrations/
├── web/
│   ├── src/
│   │   ├── app/                 AppShell、路由、主题
│   │   ├── styles/              tokens.css、global.css
│   │   ├── components/
│   │   │   ├── ui/              Badge、Button、PillTabs、Dialog
│   │   │   ├── shell/           Sidebar、Rail、MobileBar、ThemeSwitch
│   │   │   ├── repo/            RepositoryCard、GrowthBadge、StarSparkline
│   │   │   └── charts/          StarTrendChart
│   │   ├── pages/               Discover、Trending、Repos、RepoDetail
│   │   │                       Categories、Topics、Watchlist、System、Setup
│   │   │                       SystemBackupSettings、RestoreProgress
│   │   ├── api/                 Client、DTO、error mapping、query hooks
│   │   └── state/               主题、过滤条件、用户 UI 偏好
│   ├── public/                  本项目图标
│   ├── tests/
│   ├── THIRD_PARTY_NOTICES.md  从 AIHOT 复制代码时记录
│   ├── package.json
│   └── vite.config.ts
├── deploy/
└── docs/
    ├── github-radar-technical-design.md
    ├── aihot-ui-adaptation.md
    └── backup-and-restore.md
~~~

## 8. 与主技术设计的实施关系

- 主文档第 1～8 章定义数据采集、Star 快照、AI 摘要、SQLite、定时维护，保持其设计。
- 主文档第 9 章的 API 路径作为对外接口基线；本方案补充请求参数、字段契约、前端状态。
- 主文档第 10 章的前端页面按本方案扩展为首页、热榜、全部项目、详情、分类、选题、关注和系统管理。
- 主文档第 12 章阶段 0 即可并行推进 UI-01/02；阶段 1/2 落地后执行 UI-03/04；阶段 3 接入 UI-05；阶段 4 完成 UI-03～UI-07 联调验收，阶段 5 单独交付 UI-08。
- UI 引入不改变原始 Star 快照 14 天、小时聚合 60 天、日聚合 180 天，也不改变 README 临时处理与 200～500 字中文摘要的边界。
- 如果 API 数据契约与页面模型冲突，优先修正真实实体及接口定义，而不是迁就 AIHOT 的新闻业务类型。

## 9. 后台备份与恢复页面

- 备份配置区：开关、每日执行时间、时区、保留天数（默认 5）、R2 端点、存储桶、对象前缀和可更新的访问凭据；“测试连接”“保存设置”两种操作状态独立。
- 列表区：按日期倒序展示实际保留期内的备份（默认五天）、大小、时间、数据库版本与恢复按钮，并允许分页查看未清理旧对象；当前对象由后端 R2 列表返回，不依赖本地 SQLite 索引。首次安装不固定限制五天。
- 任务区：“立即备份”“从 R2 恢复”均显示异步操作状态；恢复前确认后端将短暂停止数据库访问。Go HTTP 进程保持运行，原生事务复制期间展示维护状态并继续轮询。
- 新 VPS 的 /admin/setup：填写**本次新的 R2 连接**（仅当前 Go 内存会话）、加载云端最近备份、默认选择最新、点击恢复、展示进度；恢复库的旧 R2 连接必须被当前新连接**整体覆盖并校验**，完成后才显示成功。空库时可用，使用同一管理员令牌。
- 后台保存配置后更新 SQLite system_settings 并即时重设定时调度；运行中恢复时优先使用本次操作提交的新 R2 连接，没有新连接则使用当前实例生效配置；临时库复制前完成旧 R2 连接覆盖和校验，原生复制提交后加载配置再重新调度。
- 恢复进行中设置区禁止保存，后端仍以 409 拒绝并发修改；恢复完成提示自动 AI 暂停，用户在该处明确选择重新启用。Go/SQLite 等实现细节只用于技术文档，产品页面用“恢复期间暂不可访问项目数据”等与决策有关的文案。
- 验收界限、HTTP 接口与恢复安全顺序见 [备份专项方案](./backup-and-restore.md)。

## 10. 来源与设计参考

- 源仓库：https://github.com/KKKKhazix/AIHOT
- 视觉变量：https://github.com/KKKKhazix/AIHOT/blob/main/apps/web/app/app.css
- 导航外壳：https://github.com/KKKKhazix/AIHOT/tree/main/apps/web/app/components/shell
- 基础控件：https://github.com/KKKKhazix/AIHOT/tree/main/apps/web/app/components/ui
- 信息流：https://github.com/KKKKhazix/AIHOT/tree/main/apps/web/app/features/feed
- 热点列表：https://github.com/KKKKhazix/AIHOT/blob/main/apps/web/app/routes/hot.tsx
- 趋势线：https://github.com/KKKKhazix/AIHOT/blob/main/apps/web/app/features/hot/Sparkline.tsx
- 前端依赖与运行模型：https://github.com/KKKKhazix/AIHOT/blob/main/apps/web/package.json
- 许可：https://github.com/KKKKhazix/AIHOT/blob/main/LICENSE
- 声明：https://github.com/KKKKhazix/AIHOT/blob/main/NOTICE
- Radix Colors 色阶用途：https://www.radix-ui.com/colors/docs/palette-composition/understanding-the-scale
