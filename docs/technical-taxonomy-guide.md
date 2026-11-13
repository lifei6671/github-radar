# GitHub Radar 技术标签体系与 README 分类规范（V1.0）

配套词表：`docs/technical-taxonomy-v1.yaml`。第一批已接入内置能力词表、后台维护、主能力证据校验和产品形态记录。既有分类和专题保留，行业和运行特点暂不参与发现页。以下候选收缩、置信度阈值和大样本回测是后续评估方案。

## 1. 设计动机与参考依据

AIHOT 把分类标签、主题标签、实体标签分开，模型只能从受控词表中选择。GitHub 仓库不同于新闻：项目最重要的是“能做什么、怎么用、用于什么场景”。因此保留 AIHOT 的分轴与闭集分类思想，但不使用其“模型发布、产品更新、观点、公司”作为开源仓库的技术用途标签。

GitHub Topics 提倡用主要用途、主题、语言等描述仓库，稳定标识使用小写字母、数字和连字符，最多 50 字符。CNCF Landscape 的层次化技术分类可作为基础设施领域的参考；本文 16 个领域是 **GitHub Radar 的产品设计**，不是某个行业组织发布的统一标准。

### 五个互不混淆的轴

| 轴 | 标签数量 | 释义 |
| --- | ---: | --- |
| 技术领域 domains | 16 个大类 | 技术系统属于什么能力领域 |
| 技术能力 capabilities | 219 个细标签 | 仓库核心实现的可复现用途 |
| 产品形态 forms | 16 个 | 桌面应用、命令行、库、服务等 |
| 应用行业 industries | 19 个 | 金融、教育、医疗、工业等实际场景 |
| 运行特点 traits | 12 个 | 离线、本地优先、自建、GPU 加速等 |

总计 282 个受控词项，其中领域是分组节点，技术能力是主要分类对象。

## 2. 选择原则

- **主技术能力必选 1 个，但证据不足时允许 null。** 不能为了填满而猜测。
- 技术能力最多 5 项（含主能力），推荐 2～4；附带集成不应挤占核心能力标签。
- 技术领域最多 2 个，原则上从已经选择的主能力及主要补充能力反推，不靠描述里的一个泛技术词确定。
- 产品形态最多 2 个，应用行业最多 2 个，运行特点最多 3 个，证据不足返回空数组。
- 模型只能输出词表已有的稳定 id，所有可显示的中文名称由服务端根据 id 查词库。
- 别名是**召回提示**，不是无条件判定规则；比如 README 提及 Rust、Tauri、Docker、MCP，不等于该仓库是编译器、桌面框架、自建平台或 MCP 服务端。
- 不能从仓库的 GitHub Star、创建日期、作者名推出技术能力；热度与文章选题价值属于独立系统。
- 语言、框架、协议依赖和模型品牌另作为技术栈元数据处理，不要与核心用途混在同一受控轴。

### 核心能力和产品形态的区别

- `video-downloading` 是主要能力；`desktop-app` 是产品形态。
- `browser-automation` 是核心能力；`cli` 是使用形态。
- `mcp-server` 是协议角色和能力；使用者还可将产品视为 `api-service` 或 `plugin`，但必须有相应具体证据。
- `agent-skill` 描述能力；`agent-skill-package` 描述交付形态。
- `desktop-framework` 是专门开发桌面框架的项目能力；一个普通 Tauri 应用不能仅因依赖 Tauri 就标为框架。

## 3. README 分类流程与成本控制

考虑当前仓库已有 `repo_ai_topics`、`project_themes`、`ai_summaries.value_tags_json` 与 `summary/content.go` 中的 `primary_technology`、`mcp_role`、`theme_slugs` 输出流程，建议未来按如下方式接入：

1. **读取数据**：优先使用真实 README 的介绍、功能、用法、部署章节，加上仓库 Description 与 GitHub Topics；省略徽标、赞助、单纯引用的竞品、无关列表和依赖安装说明。
2. **两阶段候选收缩**：先基于英文别名、中文名称、README 关键词及仓库描述筛出可能的 1～3 个领域，形成 25～40 个能力候选，允许跨领域回退，避免一次请求发送完整词表。
3. **一次模型调用完成结构化分类**：与目前成功的摘要流程复用已有输入/指纹缓存，尽量避免“生成摘要一次、生成技术标签又一次”的额外模型消耗。
4. **模型闭集输出**：每个标签用稳定 id，主能力说明一句简短证据，典型不确定情况要输出 `needs_review=true`；无法判断时保持空。
5. **服务端后处理**：检查 id 是否在对应轴内、数量上限、领域与能力的归属一致性、同一项目的过度标签、README 证据；不允许模型自行造新标签。
6. **结果持久化**：未来建议新建 `repo_technical_tags` 或等效表，保存 `repo_id, axis, tag_id, taxonomy_version, source_fingerprint, confidence, evidence`。不得覆盖现有 `repo_ai_topics.topics_json` 的单个主技术用途语义，亦不直接修改已有 `project_themes.slug`。
7. **升级词表**：别名或中文名修改不强制重做已有的有效全文摘要；标签定义发生实质变化时，记录版本并只重分类受影响的仓库。

### 推荐结构化输出（示意，不代表已读取真实项目）

```json
{
  "taxonomy_version": "1.0.0",
  "primary_capability": "video-downloading",
  "capabilities": [
    "video-downloading",
    "batch-downloading",
    "media-extraction"
  ],
  "domains": ["media-creation"],
  "forms": ["desktop-app"],
  "industries": [],
  "traits": [],
  "confidence": 0.91,
  "evidence": [
    {"tag_id": "video-downloading", "quote": "Downloads videos from supported sites"},
    {"tag_id": "batch-downloading", "quote": "Supports downloading multiple videos"}
  ],
  "needs_review": false
}
```

原文短证据仅为示例，实际分类必须从当前仓库有效 README 或 Description 提取，不能让模型虚构引用。证据最长 140 字；建议自动确认阈值 ≥0.80，0.55～0.80 人工复核，低于 0.55 暂缓，这是首轮实验参数而非行业规范。

## 4. 典型误分类与判断边界

| README 主要能力 | 应考虑的标签 | 应避免的误判 |
| --- | --- | --- |
| 使用浏览器自动化执行固定流程 | `browser-automation` | 仅因兼容 AI 就标为 `browser-agent` |
| 模型实时观察网页、决策下一步操作 | `browser-agent`、`browser-automation` | 仅标成普通网页测试 |
| 实现 MCP Server 并提供工具/资源 | `mcp-server` | 与只支持 MCP Client 混淆 |
| 普通桌面工具提供可选 MCP 客户端 | 该项目的真实能力、必要时 `mcp-client` | `mcp-server` |
| 批量获取抖音、TikTok 等内容 | `video-downloading`、`batch-downloading` | `video-generation` |
| EPUB 转换为 Markdown | `ebook-processing`、`document-conversion` | `knowledge-base`（无管理能力时） |
| 安卓 APK 反编译和行为分析 | `android-reverse-engineering`、`decompiler`（有实际实现时） | `android-development` |
| 安全扫描代码与依赖 | `vulnerability-scanning` 或 `dependency-security` | 模糊的 `ai-agent` |
| 代理、VPN 隧道网络工具 | `proxy-server`、`vpn-tunneling` | 仅因使用加密就称为隐私或安全产品 |
| 基于文本生成配音与克隆说话人 | `speech-synthesis`、`voice-cloning` | `speech-recognition` |
| 项目用 Tauri 编写桌面应用 | 主业务能力、形态 `desktop-app` | 不是 `desktop-framework` |
| 模型评测平台 | `model-evaluation` | 不是 `agent-evaluation`，除非评测智能体轨迹 |

用户自己关注的应用类别（视频下载、AI 逆向、Skill、浏览器自动化、软件架构、语音和图像工具）已经在技术能力轴中重点覆盖，同时包括基础设施、安全、科学计算、金融科技和机器人领域。

## 5. 升级和治理

### 身份与版本

- id 属于公开的稳定业务身份；不应在上线后因换中文名称、替换别名而改变。
- 如果两个标签合并，应添加旧 id → 新 id 映射、保留历史解释并迁移；废弃 id 不能给无关技术复用。
- 新行业场景通常应添加 `industries` 标签，而不是为每个技术 × 行业创建混合标签。例如金融领域使用 RAG 应分别标 `rag` + `finance`。
- 对技术生态中经常出现但不一定代表功能的关键词，优先更新 synonyms，不要增设相似标签导致歧义扩张。
- 不要把所有细项直接映射为首页菜单；先用 16 个领域作聚合，细标签供搜索、专题和推荐查询。

### 回测要求

从真实仓库建立 120～200 个人工标注样本，推荐分布：AI/智能体 35%，开发工具及逆向 15%，音视频和图形 15%，网络/云/数据/安全 20%，科研/机器人/行业应用 15%。每个样本记录主能力、其他主要能力、至少两个容易误判的负标签和 README 证据。

初步评估：
- 主技术能力准确率和前 3 个候选命中率；
- 多标签精确率、召回率、F1；
- 额外打错标签率（过度标注）；
- 无法分类与需要人工复核的比率；
- 高频易混淆对（MCP server/client，browser agent/automation，video generation/downloading）的误判率；
- 模型输入 Token 和每仓库平均调用数。

分类体系本身是后续发现页筛选和选题的基础设施，但不应直接修改现有准入和热度排序，避免把标签质量问题放大为采样漏判。

## 6. 参考资料

- AIHOT 行业分类结构：https://github.com/KKKKhazix/AIHOT/blob/main/industry/taxonomy.ts
- AIHOT 行业改造说明：https://github.com/KKKKhazix/AIHOT/blob/main/docs/customize.md
- GitHub Topics：https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/classifying-your-repository-with-topics
- CNCF Landscape：https://landscapeapp.cncf.io/cncf/guide
