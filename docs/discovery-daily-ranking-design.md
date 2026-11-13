# GitHub Radar 发现页每日入选与年龄加权排序技术方案

> 适用项目：`lifei6671/github-radar`  
> 设计基线：本地 `master`，`bb872845bc02f95bba83618b90670f4b9840fc07`  
> 开发范围：发现页的每日入选、排序与历史归档；现有 Go + Gin + GORM + SQLite 单体架构  
> 状态：待 Codex 实施

## 1. 产品目标与不可变约束

发现页是一条按 **北京时间（UTC+08:00）自然日** 分组的「项目入选时间线」，不是按 GitHub 仓库创建日期分组的排行榜。

1. **发现 ≠ 入选。** 一个仓库可以早在几天前就被爬虫或人工收录，但只有当天首次满足发现页资格时，才成为该自然日的一条入选记录。
2. **不是所有仓库都进入发现页。** 继续复用现有 `PublishableRepositoryCondition` 和 `discoverystore.Matches()` 的公开条件、近期发现/精确增长/Trending 触发条件以及连续曝光冷却。满足今日资格才允许入选。
3. **当天增量入选。** 全天不断产生新仓库、指标和中文摘要；后台将首次满足资格的仓库增量加入今日列表。同一仓库同一天最多一条记录，可在不同自然日再次入选（受冷却规则约束）。
4. **当天动态排序。** 已入选仓库随新指标和新成员一起重算发现优先级；入选集合原则上单调增加。增长降档、Trending 失效不使它从今日列表消失。
5. **历史永久冻结。** 跨日后，昨天及更早的成员、入选时间、展示分、排序名次和历史数字指标均不再重新计算。系统重启也不得用最新指标重放昨天的榜单。
6. **公开安全性优先。** 屏蔽、删除、停用、撤回发布资格的仓库在前台即时隐藏；底层归档记录和原始名次保留，过滤后允许名次出现空号。
7. **影响范围仅为发现页。** `/repos?sort=hotness`、24h/7d 增长榜、Trending 原始榜、选题分、仓库采样等级、收藏以及日报/周报逻辑保持原语义。不可将发现分写入全局 `latest_metrics.hotness_score`。
8. **现有项目规范。** 按 `AGENTS.md` 使用中文注释；新增持久化优先 GORM 简单查询与事务，排序用 Go 实现，尽量避免数据库专属表达式；迁移沿用当前 goose 机制。

**名词与时间：**
- `github_created_at`：GitHub 创建时间，只用于项目年龄。
- `discovered_at`：系统第一次收录时间，继续用于原有仓库管理、详情等功能。
- `selected_at`：第一次满足发现页入选资格并成功提交入选事务的时间，是发现时间线展示日期的依据。
- `day`：`BeijingDay(selected_at)` 的自然日编号（使用仓库已有函数）。
- `calculated_at`：该日榜最后一次成功排名计算时间。
- `sealed_at`：每日榜封存操作时间，通常晚于该日结束时刻。

## 2. 本地现状与代码入口

| 文件 / 函数 | 当前事实 | 改造要求 |
| --- | --- | --- |
| `internal/storage/discoverystore/store.go` `Matches()` | 判定可发布且触发近期发现、精确增长或 Trending，并计算 `discovery_score` | **拆分资格与展示评分**。资格复用；年龄分另算 |
| 同上 `Record()` | 每个有效自然日记录连续入选 `discovery_rotation` | 改为与「当日首次入选」处于同一事务；一天只增一次 |
| `internal/maintenance/service.go` `compute()` | 计算指标后调用 `Record()` | 指标发布成功后触发今日榜刷新；保留周期性兜底 |
| `internal/api/discovery.go` | 发现页现查 `Matches()`，按 `discovered_at` 日期 + 展示分排序；顶部五项按 `raw_hotness` | 改从每日入选快照读取，顶部五项采用今日发现分 |
| `internal/api/repos.go` `repoListCards()` | 以动态 `Matches()` 标记 `selected` | 发现页是否入选改读持久化当日成员；不扰动通用榜单原始热度 |
| `internal/settings/discovery_tabs.go` | 默认热度 70、新鲜度 30、连续 3 天、冷却 2 天 | 改为年龄保底、半衰周期、连续天数、冷却天数；读取旧 JSON 时兼容 |
| `internal/storage/collection.go` | 已有 `github_created_at`、`discovered_at`、`stars`、公开资格条件 | 复用字段，无需增加仓库年龄字段 |
| `internal/domain/metrics/score.go` | 全局五维热度；缺精确采样可能无 `hotness_score` | 维持原算法；发现页独立提供新仓库冷启动估计分 |
| `web/src/pages/repos.tsx` | `DiscoverHot` 展示全局热度名次；`RepoFeed` 使用 `repo.discoveredAt` 分组 | 改用发现名次、发现分、入选时间分组；其他页面仍用 `discoveredAt` |
| `web/src/api/types.ts`、`queries.ts` | `Repo` 有 `discoveryAt`，但没有发现分/名次 | 扩展专用响应字段和缓存刷新 |
| `migrations/00017_discovery_rotation.sql` | 已有连续曝光持久化 | 保留表及其已有历史，调整未来更新时机 |
| `docs/early-discovery-and-editorial-roadmap.md` | 涉及早期采集与后续选题 | 本改造为「展示层增量入选与冻结」，与采集扩展独立，不重写上游方案 |

特别注意：现有 `TimeSQL = MAX(recent_at, growth_at, trending_at)` 得到的是**最新命中信号时间**，不等同于入选时间。必须将入选时间单独持久化；不能再用 `discovered_at` 或最新信号时间替代。

## 3. 今日入选规则

### 3.1 双层检查

**基础发布资格**（沿用项目现有逻辑）：
- 仓库身份有效、处于允许公开的状态、未被屏蔽或禁用；
- 至少一次成功采集，中文摘要状态 ready 且有实际内容；
- 满足已有来源规则的 Star 发布门槛，或具有项目已认可的 Trending 例外；
- 首版发现页仍要求 `state=active`；单纯入候选池、人工粘贴链接不等于自动发布。

**每日触发条件**（复用当前 `Matches()`）：
- S/A 级近期首次发现并成功采样；
- 在配置窗口内有精确 24h 增长，达到当前 `HotGrowth` 门槛；
- 在有效全站日榜的指定名次范围内；
- 符合当前轮换/冷却机制。

三种信号为「或」关系。保存首次入选时的一个或多个命中理由（如 `recent`、`growth24h`、`trending`），可保存结构化 JSON 便于追踪。未达到条件的仓库继续留在原有仓库池，后续摘要、指标或榜单更新后还可重新评估。

### 3.2 成员生命周期

- `未入选` → 首次满足资格 → **原子插入** `(day, repo_id)`。
- `今日已入选` → 热度变化/新增信号 → **只更新分数和名次**，`selected_at` 和首次命中理由不变。
- `今日已入选` → 增长信号回落 → **保留成员**，继续用当前指标算分。
- `今日已入选` → 不再公开/管理员屏蔽 → 前台隐藏，持久化入选记录与原始排名保留。必要时今日榜可以重新排显示中的成员，但被隐藏记录也保留名次快照。
- `昨日或更早` → 只读封存记录，不再接受新增/覆盖。
- 旧仓库今天首次达到信号也归入**今天**，允许旧仓库未来再次上榜，但仍遵循冷却。

**边界语义**：以「入选事务成功提交时的北京时间日期」确定 `day`，而不是搜索任务创建时间或 GitHub 事件时间。23:59 发起、次日才处理完成的任务只能属于次日；不得回写昨天。

## 4. 发现优先级算法

### 4.1 正式综合热度

现有 `hotness_score` 继续由 24h/7d 增量、加速度、持续性和 Trending 五维计算，范围一般为 0～100。

### 4.2 仓库年龄加权

`ageDays = max(0, BeijingDay(asOf) - BeijingDay(github_created_at))`（以当日业务日期计，使同一天年龄权重固定）。

```text
ageFactor = base + (1 - base) / (1 + ageDays / halfLifeDays)
discoveryScore = max(0, heatForDiscovery * ageFactor - exposurePenalty)
```

- `base=0.35`，老仓库最低保留热度的 35%；
- `halfLifeDays=30`，额外年龄优势在 30 天时减半；
- `exposurePenalty` 沿用已有连续入选扣分概念，默认前 2 天每连续一天扣 10 分；只根据**今天开始前**已确定的连续曝光记录计算，今天同日刷新不得改动该分项；
- `github_created_at` 为空或明显无效时，`ageFactor=base`，不推断成新建仓库；
- `ageFactor` 不应影响 `Matches()` 的资格，只参与已入选仓库排序；
- 分数保留内部完整精度，响应展示可以保留一位小数；排序键使用 `score DESC, selected_at DESC, repo_id ASC`。若希望新入选项目在同分时靠前，采用 `selected_at DESC`；确定性 ID 兜底。

### 4.3 缺少精确综合热度的冷启动仓库

现有 `score.go` 可能因为采样窗口不足而返回 `hotness_score=NULL`。如果直接乘以 0，新创建的热门仓库会沉底。因此增加**仅用于发现页的临时热度代理**：

- 条件：已经通过第 3 节入选资格、`hotness_score` 为空、`github_created_at` 有效、年龄 `<=14` 天、`stars>0` 且存在成功采集；
- 计算：`provisionalHeat = 50 * log1p(min(stars, 500)) / log1p(500)`，上限 50；
- 标记 `score_kind=provisional`，在前台可用「早期观察」标记，不冒充精确 24h 增长；
- 一旦有有效的全局热度，则使用全局热度。若不满足条件，`heatForDiscovery=0`；
- 此代理**不代替资格判断**、不进入全局热度榜，后续按模拟数据和实测调整是否启用/阈值。

举例：创建 2 天、约 300 Star 的仓库可以得到一定临时排序分，已经增长稳定的老仓库仍按其正式热度竞争位置。

### 4.4 参数管理

新增 `ageFloorPercent=35`、`ageHalfLifeDays=30`，保留 `consecutiveDays=3` 和 `cooldownDays=2`。废弃旧 `heatWeight+freshnessWeight` 的直接加和算法；不要两套新鲜度重复加权。

- 兼容旧数据库 JSON：读取旧配置时补新字段；首次保存时迁移为新结构；支持旧客户端的明确兼容策略和校验。
- 修改排序参数只触发**今日**重排；历史条目的 `score`、`rank` 及算法版本不改变。
- 配置百分比范围建议 20～60，半衰期 7～180 天，校验 `base>0`、`halfLifeDays>0`。不新增跨服务配置文件。

## 5. 数据结构与索引

新增 goose 迁移文件（必须在开始实现时检查并接续本地最新迁移号；不与并行开发中的迁移冲突），建议两张表：

```sql
CREATE TABLE discovery_days (
    day INTEGER PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('open','sealed')),
    revision INTEGER NOT NULL DEFAULT 0,
    algorithm_version INTEGER NOT NULL,
    parameters_json TEXT NOT NULL DEFAULT '{}',
    calculated_at INTEGER,
    sealed_at INTEGER
);

CREATE TABLE discovery_day_entries (
    day INTEGER NOT NULL,
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    selected_at INTEGER NOT NULL,
    reasons_json TEXT NOT NULL DEFAULT '[]',
    score REAL NOT NULL DEFAULT 0,
    rank INTEGER NOT NULL DEFAULT 0,
    score_kind TEXT NOT NULL DEFAULT 'provisional',
    age_days INTEGER,
    age_factor REAL,
    hotness_at_calc REAL,
    stars_at_calc INTEGER,
    growth24h_at_calc INTEGER,
    growth24h_quality TEXT,
    calculated_at INTEGER,
    source TEXT NOT NULL DEFAULT 'live',
    PRIMARY KEY (day, repo_id)
);

CREATE INDEX idx_discovery_entries_order
ON discovery_day_entries(day, rank, repo_id);
```

说明：
- `rank` 为当日全体成员的全局名次，从 1 开始；不能按分类子集重新写入名次。
- `source` 可为 `live` / `legacy_imported`，区分正式实时入选与既有历史迁移基线。
- `parameters_json` 和 `algorithm_version` 保存当日使用的算法口径；封存后固定。
- `growth24h_quality` 记录快照窗口的质量，历史卡片不能使用今天的新增长替换其数值。
- 已有每日指标表 `metrics_daily` 不能替代发现入选表；它针对所有被采样仓库，不记录发现页入选资格和顺序。
- 由于历史需要长期保留，**不要把新表纳入 `DiscoveryDays=30` 的采集观测清理**；仓库候选清理应跳过已存在发现归档的仓库（或提供独立稳定归档身份），不能级联删除历史成员。
- 新旧归档都应遵守实时的公开访问控制。历史数据存储不等于永久允许公开展示。

如最终选择 GORM 自动生成 SQL，迁移结构与字段语义必须等价。代码内数据写入与排序尽量使用 GORM 与 Go `sort.SliceStable`，不引入依赖 SQLite 版本的窗口函数计算名次。

## 6. 后台刷新、封存与崩溃恢复

新增 `discoverystore.RefreshToday(ctx, db, now)` 与 `discoverystore.SealPastDays(ctx, db, today)`。

### 6.1 调度

- 指标计算完成后通知/触发一次今日刷新，替代当前 `compute() -> Record()` 的单独调用。
- 搜索、Trending、人工提交、摘要生成状态变为 ready 后，标记发现榜待刷新；避免所有事件直接串行执行完整排序。
- 采用约 2 分钟的合并刷新间隔；每次定时维护/指标计算兜底全量重新评估今日入选。即使漏掉某次事件，下一个周期也能补入。
- 页面 GET 始终只读，不运行评分写事务、不请求 GitHub；额外刷新请求也仅调用已有后台任务调度，不在 HTTP 请求内做长任务。
- 系统只有一个 SQLite 数据库；用单个受管刷新任务、数据库事务及 `(day,repo_id)` 主键实现幂等，不需要 Redis/消息中间件。

### 6.2 每次刷新操作

1. 从服务器取得 `now` 与 `today=BeijingDay(now)`，在**写事务内部再验证**今天的日期。
2. 首先将 `day<today AND state=open` 的历史日标记 `sealed`，**只更新状态**，不得调用历史日期的 `Matches()` 重新计算，也不得补入成员。停机跨日后沿用前一天最后一次成功提交的排名。
3. 确保存在今日 `discovery_days`（`state=open`）；即使暂无入选也允许创建空日。
4. 用现有的 `Matches()` 得到当前资格集合。对 `discovery_day_entries` 不存在的合格仓库执行幂等插入：`selected_at=now`、`reasons_json=首次匹配原因`；已经入选的行不覆盖这两个字段。
5. 对今日全部已入选记录重新计算优先级（包括当下不再满足「增长信号」的成员），用第 4 节评分规则确定全局名次。
6. 在同一数据库事务写入所有今日行的 `score/rank/score_kind/...at_calc`，更新 `discovery_days.calculated_at/revision/parameters_json`；仅结果确实变化时递增 revision。根据**新增成功入选行**更新 `discovery_rotation`，每仓库每天一次，不能同日重入重复增加。
7. 所有写入原子提交；失败整个回滚，下个任务周期重试。前端在事务完成前继续读到上一版完整榜。

**特别边界：**如果事务跨过北京时间午夜，不能以已过期的 `today` 提交到昨天；回滚并按照新日期重跑。读取 `Matches()` 时若与排名写入不是同一事务，要检查当前日期和数据版本，防止跨日脏写。优先在短事务内做确定性排序与写入，避免持锁执行 GitHub 请求或模型调用。

### 6.3 连续曝光冷却

现有 `discovery_rotation` 的定义为 `last_day, streak`；该表继续保留。更新依据改为「新增今日入选记录成功提交」，而不是每次 `Matches()` 返回的瞬时集合。

- 同一天重复刷新不更新 streak。
- 对今天已经入选的成员，改变配置不能使其从今日成员集合中撤出；新规则只影响后续新增资格和重新计算的分数。
- 同一天排名的曝光惩罚只使用前一天及以前的完成记录，避免入选之后再次刷新导致当天自我扣分。
- 过往归档与 rotation 不一致时，以已归档成员为入选事实来源；提供修复/审计测试，但不自动改写封存日的历史排名。

### 6.4 封存时机与跨日空档

- 23:59 前最后一次成功计算的当日快照，就是该日归档版本；00:00 之后仅标记封存。
- 进程 22:00 停止、次日 10:00 恢复：昨天保留 22:00 的排名，标记 `sealed_at=恢复时刻`，不补昨日丢失的采集结果。
- 如果前一天任务未曾成功，保留一个可识别的空日/缺快照状态，**不得用今天最新热度推算昨天**。
- 幂等封存：反复重启或并发执行都不改变历史分数与成员。
- 可用 SQLite 的唯一键、事务和短周期刷新保证重启安全；备份/恢复沿用现有 R2/SQLite 流程。

## 7. 历史迁移与首次上线

旧版本没有「某天实际入选」记录，不能准确恢复过去每一天真实的最终排序。因此：

1. 在迁移/启用时记录 `activation_day`，写入新的每日状态元数据。
2. 对此前已展示的历史时间线，如产品需要保留，执行**一次性冻结式历史导入**：以启用时旧版发现页可见集合及排序作为基线，按原有展示日期生成 `source=legacy_imported`，保存静态 score、rank、相关数字字段，并立即封存。只导入当前能可靠取到的历史展示范围，不推断缺失日期。
3. 对已导入的历史，说明这是「启用时的历史展示基线」，不伪称为当日真正最后排名；导入操作必须幂等。
4. 今日启用后的成员从现有 `Matches()` 合格集合开始写入；今天已经存在的仓库可以正常入选。不重写今天以前的 `discovered_at`。
5. 一旦新方案启用，后续历史页只读快照，不以实时 `Matches()` 重算。兼容灰度期间明确采用单一读路径，避免同一日同时出现两套排序。

迁移完成前后，既有 `repositories`、`latest_metrics`、`discovery_rotation`、中文 README、人工来源、主题、选题、备份设置都应保持原内容和身份。

## 8. API、分页、缓存及前端

### 8.1 API

继续使用 `GET /api/v1/public/discovery` 与分类/专题过滤能力。新增（或补充）参数：`day` 可选，默认混合时间线；`sort=priority` 表示「今日发现优先级前五」，兼容旧的 `sort=hotness` 查询名作为别名，但**独立 `/repos?sort=hotness` 原始热度保持不变**。

默认时间线服务端排序：

```text
day DESC, rank ASC, repo_id ASC
```

接口项新增：
- `discoveryAt`：`selected_at` 的 ISO 时间，用于发现页分组与时间线标签；
- `discoveryScore`：该日计算/封存的发现分；
- `discoveryRank`：该日全局名次；
- `discoveryScoreKind`：`measured` 或 `provisional`；
- `selectionReasons`：入选时命中信号；
- `discoveryDay` 和 `discoverySealed` 供前端显示今日动态/历史封存状态。

`Repo.discoveredAt` 保持系统首次收录时间，不覆盖。今日/历史卡片的发现专用数值从当日快照读取；`Repo.hotnessScore` 仍指全局热度（若展示历史数值则明确标记快照时点）。历史卡片中出现增长数、Star、热度的地方应展示当日保存的值；历史 24h 动态折线图要么冻结成当时的数据，要么不显示，不能冒用今日增长作当日数据。

顶栏当前五项改为**今日入选集合中发现优先级最高的五项**，不混入历史条目；前端标题建议「今日值得关注」，显示发现分和发现排名，不能继续展示全局 `hotnessRank` 充当发现排名。

### 8.2 游标和稳定性

- 游标版本升级，包含：排序版本、过滤快照、`day`、`rank`、`repo_id`、**当日 revision**，保留必要的时间边界。
- 默认混合日期分页只要包含今日数据，就校验当日 revision；当新成员插入、排名变化造成 revision 变化，旧游标报 `invalid_cursor`，前端清除旧分页并重新获取首屏，不把新旧分页盲目合并。
- 历史日期分页没有今日修订影响；一个封存日可稳定翻页。
- 分类和专题过滤是在**已经确定的每日成员集合上做子集**，不得把筛选后未入选仓库加入榜，也不得重新写回原始全局 rank。过滤后名次不连续是预期。
- 展示侧保持时间线日期优先，不将历史日期跨日混排。
- 若参数在日内被修改，重新计算今日全部成员，但不改变历史版本；同一事务更新 revision。

### 8.3 前端

- `DiscoverHot()` 查询今日前五的优先级，而非原始全局热度；排名编号使用 `discoveryRank`。
- `RepoFeed` 仅在 `discovery=true` 时改用 `repo.discoveryAt` 作为分组/时间标签；其余页面仍用 `repo.discoveredAt`。
- 提供「今日动态 / 历史封存」轻量标识；今日列表获取新 revision 时触发重新加载；避免频繁闪烁，可在窗口聚焦与合理间隔刷新。
- 更新 `web/src/api/types.ts`、查询缓存失效规则与发现页测试，保持分类导航、专题详情、收藏、分页的原有体验。
- 历史数值冻结不妨碍用户点进仓库详情查看最新指标；详情仍是实时数据。

## 9. 分阶段任务清单（Codex 执行顺序）

**阶段 A｜领域逻辑与数据迁移**
- 在 `internal/domain/discovery/` 建立纯 Go 评分函数、年龄天数和并列名次排序；添加单元测试。
- 增加两张日榜表、GORM model、columns 和 goose 迁移；测试旧 SQLite 数据库升级以及空库初始化。
- 创建 `internal/storage/discoverystore` 中的每日状态与成员读写函数；明确事务边界与锁竞争重试。

**阶段 B｜资格接入与动态刷新**
- 将 `Matches()` 提炼为不依赖旧 `discovered_at` 组别的入选资格查询；保留来源信号、发布条件及冷却。
- 实现今日入选追加、重排名、revision、曝光记录同事务更新。
- 连接 `internal/maintenance/service.go` 指标完成事件；采集/摘要成功后标记刷新；增加周期兜底与跨日封存、启动恢复。
- 实现历史一次性冻结导入及其幂等性；不接入新的外部接口或模型功能。

**阶段 C｜只读 API 与页面**
- `internal/api/discovery.go` 改为快照读取，增设今日前五；发现页响应中的 `selected` 以每日入选记录为准。通用 `/repos` 中 `repoListCards()` 的既有语义和独立榜单排序保持不变。
- 扩展字段、更新游标和过滤行为。
- 前端改分组时间、展示分、今日前五与历史状态；处理游标变化、空榜和部分缺测。

**阶段 D｜验收、回滚与文档**
- Go 单测与实际 HTTP 验收覆盖以下矩阵；前端类型检查/测试/构建和浏览器交互。
- 更新相关设计文档和配置说明，说明历史导入基线、清理策略和封存规则。
- 以 feature flag 或迁移启用点控制切换；新旧版本不能并行写同一份每日排名，回滚时保留新增两表，不破坏已有仓库数据。
- 检查 `git diff`，确保仅包含本次发现页改造相关文件；给出变更摘要、测试结果及剩余风险。

## 10. 必须通过的验收矩阵

| 场景 | 验收结果 |
| --- | --- |
| 09:00 首次入选 A，13:00 新增 B，17:00 旧仓库 C 因爆发入选 | 三者均属于今天，`selected_at` 独立，按最新发现分重排 |
| A 当天下午增长跌破入选阈值 | 今日仍保留 A，只调整分数和名次 |
| 同一仓库连续多轮 Search、Trending、人工重复提交 | 今日最多一条 `(day,repo_id)`；首次时间、理由不被覆盖 |
| 早已收录的老仓库今天达到增长阈值 | `discovered_at` 保持旧值，`selected_at` 属于今天 |
| 缺少 24h 精确窗口、创建 2 天且符合发布资格的新仓库 | 使用临时 `provisional` 热度，不伪称真实 24h 增量 |
| 新仓库低热度与老仓库超高热度对比 | 低热度不能仅凭年龄必然第一；数值符合年龄衰减公式 |
| 23:58 榜单提交，00:01 重启 | 昨日分数、成员、名次不变；标为 sealed；新成员归入今日 |
| 停机一整晚，次日恢复 | 历史使用停机前最后成功的快照，不调用今天的指标回算昨天 |
| 修改保底比例、半衰期或连续冷却配置 | 今天实时排名/新增资格可变；昨天数据逐字节保持一致 |
| 历史日归档后新采集到同一仓库 | 不回填历史；若今天符合条件则新建今日记录 |
| 每日入选项目被屏蔽、禁用、撤回发布资格 | 公开页面立即隐藏，历史底层 `rank` 不修改、不压缩 |
| 相同自然日多次刷新 | 不重复添加、不重复增加连续曝光次数 |
| 今日页游标取第一页后，新成员插入导致重排 | 旧游标明确失效，前端重载，无重复或遗漏 |
| 只读取历史日的分页 | 跨天重启、今日热度变化均不影响历史游标与结果 |
| 按分类或专题筛选 | 从已有日榜选子集，不改变原始名次和当天成员 |
| `/repos?sort=hotness` 及 24h/7d 增长榜 | 计算数值和排序与改造前相同 |
| 新库、旧库升级、备份恢复 | 可正常启动、归档存在、无重复记录、事务幂等 |
| 参数非法、缺少 GitHub 创建时间、时钟边界、数据库 BUSY | 有明确回退/错误，不能产生伪造年龄和半写入排名 |

**最低验证命令**（以仓库实际脚本为准）：
- `go test ./...`
- `cd web && npm run typecheck`（若 package scripts 不存在，则改用仓库当前可用的 `npx tsc --noEmit`）
- `cd web && npm test` 或现有前端测试命令
- `cd web && npm run build`
- 针对发现页的 HTTP 接口、游标、跨日、并发与归档测试，使用固定测试时钟，不依赖真实日期。

## 11. 交付标准

Codex 应交付：迁移文件、领域评分函数、持久化增量刷新与封存逻辑、兼容接口、前端发现页适配、回归测试与设计说明。方案只改变**发现页的每日入选与排序**，尽量复用采集、指标、摘要、类别和队列，不扩大到其它榜单、模型摘要、选题评分或新的外部数据源。
