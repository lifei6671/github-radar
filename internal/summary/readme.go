package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

var readmeMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))

// EmbeddingReadme 复用公开身份检查和 Markdown 摘录，正文不写入数据库。
func (e *Engine) EmbeddingReadme(ctx context.Context, repo storage.Repository) (string, error) {
	if e.GitHub == nil {
		return "", nil
	}
	result, err := e.readReadme(ctx, repo, false)
	if err != nil {
		if withdraw := withdrawRepository(err, repo.RepoID); withdraw != nil {
			if saveErr := withdraw(e.Queue.DB.WithContext(ctx)); saveErr != nil {
				return "", saveErr
			}
		}
		return "", err
	}
	return excerpts(prose(result.Body), 4096), nil
}

// readReadme 按仓库 ID 验证公开性和当前地址后临时获取 README，不写入原文。
func (e *Engine) readReadme(ctx context.Context, repo storage.Repository, priority bool) (gh.ReadmeResult, error) {
	policy := gh.Policy{RepositoryID: repo.RepoID, Grade: repo.Grade, ReadmeBudget: e.Queue.AI.Summary.GitHubBudget, Priority: priority}
	remote, err := e.publicRepo(ctx, repo.RepoID, policy)
	if err != nil {
		return gh.ReadmeResult{}, err
	}
	fullName := remote.GetFullName()
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok {
		return gh.ReadmeResult{}, &gh.Failure{Code: consts.CodeGitHubInvalidRepository}
	}
	result, err := e.preferredReadme(ctx, owner, name, "", policy)
	if err != nil {
		return result, err
	}
	// README 404 也可能源于仓库刚转私，缺失时仍须复核仓库身份。
	remote, err = e.publicRepo(ctx, repo.RepoID, policy)
	if err != nil {
		return gh.ReadmeResult{}, err
	}
	if result.NotModified || !strings.EqualFold(remote.GetFullName(), fullName) {
		return gh.ReadmeResult{}, &gh.Failure{Code: consts.CodeGitHubReadmeSourceChanged}
	}
	return result, nil
}

type localizedReadmeSource interface {
	LocalizedReadme(context.Context, string, string, string, int, gh.Policy) (gh.ReadmeResult, error)
}

type readmeSiblingsSource interface {
	ReadmeSiblings(context.Context, string, string, string, gh.Policy) ([]string, error)
}

var chineseReadmeName = regexp.MustCompile(`^readme[._-](zh([._-](cn|hans|tw|hant))?|cn|chinese)\.md$`)

// Follow one explicit link to a Markdown README in the same repository.
func linkedChineseReadme(source gh.ReadmeResult) string {
	var filename string
	root := readmeMarkdown.Parser().Parse(text.NewReader(source.Body))
	_ = ast.Walk(root, func(node ast.Node, enter bool) (ast.WalkStatus, error) {
		link, ok := node.(*ast.Link)
		if !enter || !ok {
			return ast.WalkContinue, nil
		}
		u, err := url.Parse(string(link.Destination))
		if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" ||
			u.Path == "" || strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "\\") {
			return ast.WalkContinue, nil
		}
		target := path.Join(path.Dir(source.Path), u.Path)
		if strings.HasPrefix(target, "../") || target == source.Path {
			return ast.WalkContinue, nil
		}
		base := strings.ToLower(path.Base(target))
		label := string(link.Text(source.Body))
		if chineseReadmeName.MatchString(base) || (base == "readme.md" && (strings.Contains(label, "中文") || strings.Contains(label, "漢語"))) {
			filename = target
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return filename
}

func (e *Engine) preferredReadme(ctx context.Context, owner, name, etag string, policy gh.Policy) (gh.ReadmeResult, error) {
	limit := e.Queue.AI.Summary.ReadmeMaxBytes
	source, err := e.GitHub.Readme(ctx, owner, name, etag, limit, policy)
	if err != nil || source.NotModified {
		return source, err
	}
	client, ok := e.GitHub.(localizedReadmeSource)
	if !ok || language(prose(source.Body)) == consts.LanguageChinese {
		return source, nil
	}
	readLocalized := func(filename string) (gh.ReadmeResult, error) {
		localized, err := client.LocalizedReadme(ctx, owner, name, filename, limit, policy)
		var failure *gh.Failure
		if err != nil && !(errors.As(err, &failure) && failure.Code == consts.CodeGitHubInvalidReadme) {
			return gh.ReadmeResult{}, err
		}
		if err == nil && !localized.Missing && language(prose(localized.Body)) == consts.LanguageChinese {
			localized.ETag = ""
			return localized, nil
		}
		return gh.ReadmeResult{}, nil
	}
	filename := linkedChineseReadme(source)
	if filename != "" {
		source.ETag = ""
		localized, err := readLocalized(filename)
		if err != nil || localized.SHA != "" {
			return localized, err
		}
	}
	if siblings, ok := e.GitHub.(readmeSiblingsSource); ok {
		// 默认文件的 ETag 不覆盖独立中文版的变化。
		source.ETag = ""
		dir := path.Dir(source.Path)
		paths, err := siblings.ReadmeSiblings(ctx, owner, name, dir, policy)
		if err != nil {
			return source, err
		}
		for _, candidate := range paths {
			if candidate == filename || path.Dir(candidate) != dir || !chineseReadmeName.MatchString(strings.ToLower(path.Base(candidate))) {
				continue
			}
			localized, err := readLocalized(candidate)
			if err != nil || localized.SHA != "" {
				return localized, err
			}
		}
	}
	return source, nil
}

type readmePart struct {
	start, end int
	text       string
}

func readmeBody(data []byte) []byte {
	return bytes.ToValidUTF8(data, nil)
}

// readmeParts 按 Markdown 文本节点提取可翻译片段，代码、媒体及链接地址留在原文结构中。
func readmeParts(data []byte, accept func(rune) bool) []readmePart {
	var parts []readmePart
	root := readmeMarkdown.Parser().Parse(text.NewReader(data))
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindCodeSpan, ast.KindImage, ast.KindAutoLink, ast.KindHTMLBlock, ast.KindRawHTML:
			return ast.WalkSkipChildren, nil
		}
		leaf, ok := n.(*ast.Text)
		if !ok {
			return ast.WalkContinue, nil
		}
		value := string(data[leaf.Segment.Start:leaf.Segment.Stop])
		offset := leaf.Segment.Start
		for value != "" {
			chunk := limitText(value, 512)
			if len(chunk) < len(value) {
				if space := strings.LastIndexByte(chunk, ' '); space >= 256 {
					chunk = chunk[:space+1]
				}
			}
			left := strings.TrimLeftFunc(chunk, unicode.IsSpace)
			content := strings.TrimRightFunc(left, unicode.IsSpace)
			if strings.ContainsFunc(content, accept) {
				start := offset + len(chunk) - len(left)
				parts = append(parts, readmePart{start, start + len(content), content})
			}
			offset += len(chunk)
			value = value[len(chunk):]
		}
		return ast.WalkContinue, nil
	})
	return parts
}

var escapeMarkdown = strings.NewReplacer(
	"\\", "\\\\", "&", "&amp;", "<", "\\<", ">", "\\>", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]",
	"!", "\\!", "|", "\\|", "#", "\\#", "+", "\\+", "-", "\\-", ".", "\\.", "~", "\\~",
)

func translatedMarkdown(data []byte, parts []readmePart, translations []string) []byte {
	var out bytes.Buffer
	start := 0
	for i, value := range translations {
		part := parts[i]
		out.Write(data[start:part.start])
		if value == "" {
			out.WriteString(part.text)
		} else {
			out.WriteString(escapeMarkdown.Replace(value))
		}
		start = part.end
	}
	out.Write(data[start:])
	return out.Bytes()
}

func renderReadme(data []byte, sourceURL string, original []byte) (string, error) {
	base, _ := url.Parse(sourceURL)
	resolve := func(value []byte, image bool) []byte {
		target, err := url.Parse(string(value))
		if err != nil || target.User != nil {
			return nil
		}
		if target.Scheme == "" && target.Host == "" && base != nil && base.Host != "" {
			target = base.ResolveReference(target)
			if image && target.Host == "github.com" {
				target.Path = strings.Replace(target.Path, "/blob/", "/raw/", 1)
				target.RawPath = ""
			}
		}
		if target.Scheme != "https" && target.Scheme != "http" {
			return nil
		}
		return []byte(target.String())
	}
	root := readmeMarkdown.Parser().Parse(text.NewReader(data))
	var sourceHeadings map[int]bool
	if original != nil {
		sourceHeadings = make(map[int]bool)
		source := readmeMarkdown.Parser().Parse(text.NewReader(original))
		index := 0
		for node := source.FirstChild(); node != nil; node = node.NextSibling() {
			if heading, ok := node.(*ast.Heading); ok {
				sourceHeadings[index] = nonProjectSection(heading, original)
				index++
			}
		}
	}
	removeNonProjectSections(root, data, sourceHeadings)
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if enter {
			switch node := n.(type) {
			case *ast.Link:
				node.Destination = resolve(node.Destination, false)
			case *ast.Image:
				node.Destination = resolve(node.Destination, true)
			}
		}
		return ast.WalkContinue, nil
	})
	var out bytes.Buffer
	// Goldmark 的安全渲染会过滤原始 HTML 和危险链接。
	err := readmeMarkdown.Renderer().Render(&out, data, root)
	return out.String(), err
}

type ReadmeView struct {
	State            string `json:"state"`
	Language         string `json:"language,omitempty"`
	SourceURL        string `json:"sourceUrl,omitempty"`
	ChineseHTML      string `json:"chineseHtml,omitempty"`
	Complete         bool   `json:"complete"`
	Truncated        bool   `json:"truncated"`
	TranslationJobID *int64 `json:"translationJobId,omitempty"`
}

// CachedReadme 只读后台已经准备的当前版本。
func CachedReadme(db *gorm.DB, repoID int64) (ReadmeView, error) {
	var row storage.RepoReadmeDocument
	if err := db.Find(&row, "repo_id = ?", repoID).Error; err != nil {
		return ReadmeView{}, err
	}
	state := consts.StatusPending
	switch {
	case row.Complete:
		state = consts.StatusReady
	case row.RepoID != 0 && row.ReadmeBytes == 0:
		state = consts.StatusMissing
	}
	return ReadmeView{State: state, Language: row.Language, SourceURL: row.SourceURL,
		ChineseHTML: row.ChineseHTML, Complete: row.Complete, Truncated: row.Truncated}, nil
}

func saveReadme(tx *gorm.DB, repoID int64, source gh.ReadmeResult) error {
	var visible int64
	if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
		Where(storage.RepositoryColumns.RepoID+" = ?", repoID).Count(&visible).Error; err != nil || visible == 0 {
		return err
	}
	row := storage.RepoReadmeDocument{RepoID: repoID, ReadmeSHA: source.SHA, ReadmeBytes: len(source.Body),
		SourceURL: source.URL, Truncated: source.Truncated}
	if !source.Missing && len(source.Body) > 0 {
		data := readmeBody(source.Body)
		var original []byte
		row.Language = language(prose(data))
		if row.Language != consts.LanguageChinese && row.Language != consts.LanguageUndetermined {
			var saved storage.RepoReadmeTranslation
			cols := storage.RepoReadmeTranslationColumns
			if err := tx.Find(&saved, cols.RepoID+"=? AND "+cols.ReadmeSHA+"=? AND "+cols.ReadmeBytes+"=? AND "+cols.Complete+"=1",
				repoID, row.ReadmeSHA, row.ReadmeBytes).Error; err != nil {
				return err
			}
			if saved.RepoID == 0 {
				data = nil
			} else {
				parts := readmeParts(data, unicode.IsLetter)
				var translations []string
				if json.Unmarshal([]byte(saved.PartsJSON), &translations) == nil &&
					len(translations) == len(parts) && slices.ContainsFunc(translations, containsChinese) {
					original = data
					data = translatedMarkdown(data, parts, translations)
				} else {
					data = nil
				}
			}
		}
		if len(data) > 0 {
			html, err := renderReadme(data, source.URL, original)
			if err != nil {
				return err
			}
			row.ChineseHTML, row.Complete = html, strings.TrimSpace(html) != ""
		}
	}
	if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return err
	}
	// 同一事务中撤下基于旧 README 的导读。
	cols := storage.AISummaryColumns
	return tx.Model(&storage.AISummary{}).
		Where(cols.RepoID+"=? AND "+cols.SummaryStatus+"=? AND COALESCE("+cols.ReadmeSHA+",'')<>?",
			repoID, consts.StatusReady, row.ReadmeSHA).
		Update(cols.SummaryStatus, consts.StatusStale).Error
}

func (e *Engine) rememberReadme(ctx context.Context, job storage.Job, source gh.ReadmeResult) error {
	return e.Queue.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owns(tx, job); err != nil {
			return err
		}
		return saveReadme(tx, *job.RepoID, source)
	})
}
