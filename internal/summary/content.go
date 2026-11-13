package summary

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/abadojack/whatlanggo"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/topics"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

const sufficient = consts.SourceSufficient
const insufficient = consts.SourceInsufficient
const incomplete = consts.SourceIncomplete

const (
	minShortSummaryChars = 20
	minFullSummaryChars  = 80
	maxSummaryChars      = 200
)

const editorialInstruction = "。可选 editorial_brief 对象含 readerBenefit（读者收益）、demo（演示场景）、angle（写作角度）、steps（最多三条实测步骤）、risks（仅可用 code_execution、system_access、personal_data）。以资料为依据简短填写，勿编造命令或声称实测。"

type source struct {
	fullName                                                          string
	sha, etag, url, fingerprint, language, quality, body, description string
	githubTopics                                                      []string
	themePrompt                                                       string
	technicalTags                                                     storage.TechnicalTags
	formsSent                                                         bool
	readme                                                            gh.ReadmeResult
}

var bareURL = regexp.MustCompile(`https?://[^\s<>]+`)
var nonProjectHeading = regexp.MustCompile(`^(table of contents|contents|quick links|useful links|目录|目錄|内容索引|快速链接|快捷链接|directory (structure|tree)|folder structure|目录结构|目錄結構|目录树|文件夹结构|(our )?sponsors?|sponsorship|donat(e|ions?)|赞助(商|者|我们|信息)?|贊助(商|者)?|捐赠|捐贈|advertisements?|推广|广告|廣告)$`)

func nonProjectSection(heading *ast.Heading, data []byte) bool {
	label := strings.ToLower(strings.TrimFunc(string(heading.Text(data)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}))
	return nonProjectHeading.MatchString(label)
}

func removeNonProjectSections(root ast.Node, data []byte, sourceHeadings map[int]bool) {
	skippedLevel, headingIndex := 0, 0
	for node := root.FirstChild(); node != nil; {
		next := node.NextSibling()
		if heading, ok := node.(*ast.Heading); ok {
			if skippedLevel >= heading.Level {
				skippedLevel = 0
			}
			if skippedLevel == 0 && (sourceHeadings[headingIndex] || nonProjectSection(heading, data)) {
				skippedLevel = heading.Level
			}
			headingIndex++
		}
		if skippedLevel != 0 {
			root.RemoveChild(root, node)
		}
		node = next
	}
}

// prose 使用 Markdown AST 提取正文，跳过代码、图片、徽标和链接地址。
func prose(data []byte) string {
	var b strings.Builder
	root := goldmark.DefaultParser().Parse(text.NewReader(data))
	removeNonProjectSections(root, data, nil)
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		switch n.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindCodeSpan, ast.KindImage, ast.KindAutoLink, ast.KindHTMLBlock, ast.KindRawHTML:
			return ast.WalkSkipChildren, nil
		case ast.KindHeading:
			if enter {
				b.WriteString("\n## ")
			} else {
				b.WriteByte('\n')
			}
		case ast.KindParagraph, ast.KindListItem:
			if !enter {
				b.WriteByte('\n')
			}
		}
		if leaf, ok := n.(*ast.Text); ok && enter {
			b.Write(leaf.Value(data))
			if leaf.SoftLineBreak() || leaf.HardLineBreak() {
				b.WriteByte('\n')
			}
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(bareURL.ReplaceAllString(strings.ToValidUTF8(b.String(), ""), ""))
}

func language(value string) string {
	if strings.TrimSpace(value) == "" {
		return consts.LanguageUndetermined
	}
	lang := whatlanggo.DetectLang(value)
	if code := lang.Iso6391(); code != "" {
		return code
	}
	if code := lang.Iso6393(); code != "" {
		return code
	}
	return consts.LanguageUndetermined
}

func descriptionKey(s string) string {
	return fmt.Sprintf(consts.DescriptionFingerprintFormat, sha256.Sum256([]byte(strings.Join(strings.Fields(s), " "))))
}

// 主题词同时依赖简介和 README，单独更新简介也应触发刷新。
func topicsFingerprint(fingerprint, description string) string {
	return fingerprint + "|" + descriptionKey(description)
}

func limitText(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// excerpts 优先保留简介和功能、使用、安装、限制等标题下的正文。
func excerpts(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	sections := strings.Split(s, "\n## ")
	first := limitText(sections[0], limit/3)
	var b strings.Builder
	b.WriteString(first)
	for _, section := range sections[1:] {
		heading, _, _ := strings.Cut(section, "\n")
		h := strings.ToLower(heading)
		for _, keyword := range []string{"overview", "feature", "usage", "install", "example", "require", "limitation", "功能", "用法", "安装", "限制", "示例"} {
			if strings.Contains(h, keyword) {
				b.WriteString("\n## ")
				b.WriteString(limitText(section, min(limit/4, limit-b.Len())))
				break
			}
		}
		if b.Len() >= limit-4 {
			break
		}
	}
	if b.Len() <= len(first) {
		return limitText(s, limit)
	}
	return limitText(b.String(), limit)
}

func (s source) prompt(budget int, summary, topics, editorial bool) (string, source) {
	// 字节作为保守上界，给系统消息、角色和 JSON 留出固定开销。
	task := "摘要"
	if topics && summary {
		task = "摘要和主题词"
	} else if topics {
		task = "主题词"
	}
	task = "任务：" + task
	const fullLength = "（摘要正文目标 80 至 160 个字符，最多 200 个字符，不超过三句）"
	const shortLength = "（摘要正文目标 50 至 80 个字符，信息很少时可短至 20 个字符，最多 200 个字符，不超过三句）"
	instructionBytes := 0
	if summary {
		instructionBytes = max(len(fullLength), len(shortLength))
	}
	editorial = editorial && summary && budget >= 4096
	if editorial {
		instructionBytes += len(editorialInstruction)
	}
	prefix := "\n公开仓库：" + limitText(s.fullName, 160) + "\n检测语言：" + s.language + "\n"
	if topics {
		// 在资料之前明确本轮分类输出字段，避免长 README 使模型遗漏协议。
		prefix += "分类必填 primary_technology、primary_quote、mcp_role、theme_slugs、value_tags。\n"
		prefix += s.themePrompt
		if len(s.githubTopics) > 0 {
			prefix += "GitHub Topics（参考）：\n" + limitText(strings.Join(s.githubTopics, ", "), 384) + "\n"
		}
		if budget >= 8192 {
			_, _, forms, _ := storage.TaxonomyMetadata()
			if len(forms) > 0 {
				prefix += "产品形态（仅填 forms）："
				for _, form := range forms {
					prefix += form.ID + ":" + form.Name + "；"
				}
				prefix += "\n"
				s.formsSent = true
			}
		}
		// 输入较小时，在保留证据的前提下只发送完整词条，并用同一子集校验。
		left := max(0, budget-len(systemPrompt)-len(task)-instructionBytes-len(prefix)-128)
		glossary, included := technicalTagPrompt(s.technicalTags, min(budget/3, left/2), s.description+"\n"+limitText(s.body, 12_000))
		prefix += glossary
		s.technicalTags = included
	}
	const descriptionLabel, readmeLabel = "仓库 Description：\n", "仓库 README：\n"
	available := max(0, budget-len(systemPrompt)-len(task)-instructionBytes-len(prefix)-128)
	descriptionLimit := min(512, available-len(descriptionLabel)-1)
	if s.body != "" {
		descriptionLimit = min(descriptionLimit, available/4)
	}
	s.description = strings.TrimSpace(limitText(s.description, descriptionLimit))
	if s.description != "" {
		prefix += descriptionLabel + s.description + "\n"
		available -= len(descriptionLabel) + len(s.description) + 1
	}
	body := strings.TrimSpace(excerpts(s.body, max(0, available-len(readmeLabel))))
	if len(body) < len(s.body) && s.quality == sufficient {
		s.quality = incomplete
	}
	s.body = body
	if body != "" {
		prefix += readmeLabel + body
	}
	if summary && s.quality == sufficient {
		task += fullLength
	} else if summary {
		task += shortLength
	}
	if editorial {
		// 预算较小时优先把输入留给 README。
		task += editorialInstruction
	}
	return task + prefix, s
}

type answer struct {
	Title                json.RawMessage `json:"title_zh"`
	Language             string          `json:"source_language"`
	Summary              string          `json:"summary_zh"`
	RecommendationReason json.RawMessage `json:"recommendation_reason"`
	Features             []string        `json:"features"`
	Audience             []string        `json:"audience"`
	Tags                 []string        `json:"value_tags"`
	Evidence             []string        `json:"evidence"`
	Uncertainty          []string        `json:"uncertainty"`
	EditorialBrief       json.RawMessage `json:"editorial_brief"`
}

// resultSummary 校验中文、长度和引用类别，只返回允许持久化的短文本和标签。
func resultSummary(raw string, s source, repoID int64, provider, model string, version int, now int64) (storage.AISummary, bool) {
	var a answer
	if json.Unmarshal([]byte(raw), &a) != nil {
		return storage.AISummary{}, false
	}
	a.Summary = strings.TrimSpace(a.Summary)
	size := utf8.RuneCountInString(a.Summary)
	han, letters := 0, 0
	for _, r := range a.Summary {
		if unicode.IsLetter(r) {
			letters++
		}
		if unicode.In(r, unicode.Han) {
			han++
		}
	}
	quality := s.quality
	if size < minFullSummaryChars {
		if quality == sufficient {
			return storage.AISummary{}, false
		}
		quality = insufficient
	}
	if size < minShortSummaryChars || size > maxSummaryChars || han < 12 || 3*han < letters || copiesSource(a.Summary, s.body) || copiesSource(a.Summary, s.description) {
		return storage.AISummary{}, false
	}
	validEvidence := false
	for _, e := range a.Evidence {
		switch e {
		case "仓库 README":
			if s.body == "" {
				return storage.AISummary{}, false
			}
		case "仓库 Description":
			if s.description == "" {
				return storage.AISummary{}, false
			}
		default:
			return storage.AISummary{}, false
		}
		validEvidence = true
	}
	if !validEvidence {
		return storage.AISummary{}, false
	}
	var proposedReason string
	_ = json.Unmarshal(a.RecommendationReason, &proposedReason)
	reason := recommendationReason(proposedReason, a.Summary, s)
	var proposedTitle string
	_ = json.Unmarshal(a.Title, &proposedTitle)
	title := summaryTitle(proposedTitle, a.Summary)
	return storage.AISummary{
		RepoID: repoID, ReadmeSHA: optional(s.sha), SourceFingerprint: s.fingerprint, SourceQuality: quality,
		SourceLanguage: s.language, TitleZh: &title, SummaryZh: a.Summary, RecommendationReason: &reason,
		FeaturesJSON: tags(a.Features), AudienceJSON: tags(a.Audience),
		ValueTagsJSON: "[]", SourceReadmeURL: optional(s.url), ModelProvider: provider, ModelName: model,
		EditorialBriefJSON: editorialBrief(a.EditorialBrief),
		PromptVersion:      version, SummaryStatus: consts.StatusReady, GeneratedAt: now,
	}, true
}

// editorialBrief 校验可选材料，错误的选题字段不触发重新调用，也不阻断摘要发布。
func editorialBrief(raw json.RawMessage) string {
	var brief topics.Brief
	if json.Unmarshal(raw, &brief) != nil {
		return ""
	}
	short := func(value string) string {
		value = strings.Join(strings.Fields(value), " ")
		if utf8.RuneCountInString(value) > 120 || !containsChinese(value) {
			return ""
		}
		return value
	}
	brief.ReaderBenefit, brief.Demo, brief.Angle = short(brief.ReaderBenefit), short(brief.Demo), short(brief.Angle)
	steps := make([]string, 0, 3)
	for _, step := range brief.Steps {
		if step = short(step); step != "" {
			steps = append(steps, step)
			if len(steps) == 3 {
				break
			}
		}
	}
	var risks []string
	for _, key := range []string{"code_execution", "system_access", "personal_data"} {
		for _, value := range brief.Risks {
			if key == value {
				risks = append(risks, key)
				break
			}
		}
	}
	brief.Steps, brief.Risks = steps, risks
	if brief.ReaderBenefit == "" && brief.Demo == "" && brief.Angle == "" && len(steps) == 0 && len(risks) == 0 {
		return ""
	}
	data, _ := json.Marshal(brief)
	return string(data)
}

func summaryTitle(raw, brief string) string {
	title := strings.Join(strings.Fields(raw), " ")
	size := utf8.RuneCountInString(title)
	if size < 4 || size > 40 || !containsChinese(title) || strings.ContainsAny(title, "。！？!?<>…") ||
		strings.HasSuffix(title, ".") || strings.HasPrefix(strings.Join(strings.Fields(brief), " "), title) {
		return ""
	}
	return title
}

// NULL 区分尚未刷新过的旧摘要，空字符串表示已尝试但没有有效值。
func hasStructuredSummary(row storage.AISummary) bool {
	return row.TitleZh != nil && row.RecommendationReason != nil
}

// 推荐理由是可选字段，无效理由不影响已通过校验的摘要。
func recommendationReason(raw, brief string, s source) string {
	reason := strings.Join(strings.Fields(raw), " ")
	sentence := strings.TrimRight(reason, "。！？!?.")
	n := utf8.RuneCountInString(reason)
	if n < 12 || n > 100 || !strings.ContainsFunc(reason, func(r rune) bool { return unicode.In(r, unicode.Han) }) ||
		strings.ContainsAny(sentence, "。！？!?<>") || strings.Contains(brief, sentence) ||
		copiesSource(sentence, s.body) || copiesSource(sentence, s.description) {
		return ""
	}
	return reason
}

// copiesSource 拒绝把整段来源或长原文片段包装进摘要，忽略 Markdown 标题和空白差异。
func copiesSource(summary, original string) bool {
	compact := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || r == '#' {
				return -1
			}
			return r
		}, s)
	}
	brief, body := compact(summary), compact(original)
	if body == "" {
		return false
	}
	if strings.Contains(body, brief) || (utf8.RuneCountInString(body) >= 20 && strings.Contains(brief, body)) {
		return true
	}
	runes := []rune(brief)
	const excerptLength = 64
	for i := 0; i+excerptLength <= len(runes); i++ {
		if strings.Contains(body, string(runes[i:i+excerptLength])) {
			return true
		}
	}
	return false
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func tags(values []string) string {
	out := make([]string, 0, 5)
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" || utf8.RuneCountInString(value) > 40 || seen[value] {
			continue
		}
		out = append(out, value)
		seen[value] = true
		if len(out) == 5 {
			break
		}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}
