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

	"github.com/lifei6671/github-radar/internal/storage"
)

const sufficient = "sufficient"
const insufficient = "insufficient_source"
const incomplete = "incomplete_source"

type source struct {
	fullName                                                          string
	sha, etag, url, fingerprint, language, quality, body, description string
}

var bareURL = regexp.MustCompile(`https?://[^\s<>]+`)

// prose 使用 Markdown AST 提取正文，跳过代码、图片、徽标和链接地址。
func prose(data []byte) string {
	var b strings.Builder
	root := goldmark.DefaultParser().Parse(text.NewReader(data))
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
		return "und"
	}
	lang := whatlanggo.DetectLang(value)
	if code := lang.Iso6391(); code != "" {
		return code
	}
	if code := lang.Iso6393(); code != "" {
		return code
	}
	return "und"
}

func descriptionKey(s string) string {
	return fmt.Sprintf("description:%x", sha256.Sum256([]byte(strings.Join(strings.Fields(s), " "))))
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

func (s source) prompt(budget int) (string, source) {
	// 字节作为保守上界，给系统消息、角色和 JSON 留出固定开销。
	prefix := "公开仓库：" + limitText(s.fullName, 160) + "\n检测语言：" + s.language + "\n"
	const descriptionLabel, readmeLabel = "仓库 Description：\n", "仓库 README：\n"
	available := max(0, budget-len(systemPrompt)-len(prefix)-128)
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
	return prefix, s
}

type answer struct {
	Language    string   `json:"source_language"`
	Summary     string   `json:"summary_zh"`
	Features    []string `json:"features"`
	Audience    []string `json:"audience"`
	Tags        []string `json:"value_tags"`
	Evidence    []string `json:"evidence"`
	Uncertainty []string `json:"uncertainty"`
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
	if size < 200 {
		if quality == sufficient {
			return storage.AISummary{}, false
		}
		quality = insufficient
	}
	if size < 20 || size > 500 || han < 12 || 3*han < letters || copiesSource(a.Summary, s.body) || copiesSource(a.Summary, s.description) {
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
	return storage.AISummary{
		RepoID: repoID, ReadmeSHA: optional(s.sha), SourceFingerprint: s.fingerprint, SourceQuality: quality,
		SourceLanguage: s.language, SummaryZh: a.Summary, FeaturesJSON: tags(a.Features), AudienceJSON: tags(a.Audience),
		ValueTagsJSON: tags(a.Tags), SourceReadmeURL: optional(s.url), ModelProvider: provider, ModelName: model,
		PromptVersion: version, SummaryStatus: "ready", GeneratedAt: now,
	}, true
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

// Title 从摘要首句生成卡片标题，不增加模型调用。
func Title(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "。！？!?\n"); i >= 0 {
		s = s[:i]
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return string(runes)
}
