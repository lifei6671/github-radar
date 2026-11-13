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
	"strconv"
	"strings"
	"unicode"

	sdk "github.com/google/go-github/v88/github"
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

// readmeHeadingIDs 以标题文字生成保留中英文的片段标识，并处理重复标题。
func readmeHeadingIDs(root ast.Node, data []byte) (ids []string, aliases []string) {
	used := make(map[string]bool)
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		old, _ := heading.AttributeString("id")
		legacy, _ := old.([]byte)
		label := strings.ToLower(strings.TrimSpace(string(heading.Text(data))))
		base := strings.Map(func(r rune) rune {
			switch {
			case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_':
				return r
			case unicode.IsSpace(r):
				return '-'
			default:
				return -1
			}
		}, label)
		if base == "" {
			base = "heading"
		}
		id := base
		for suffix := 1; used[id]; suffix++ {
			id = base + "-" + strconv.Itoa(suffix)
		}
		used[id] = true
		heading.SetAttributeString("id", []byte(id))
		ids, aliases = append(ids, id), append(aliases, string(legacy))
		return ast.WalkContinue, nil
	})
	return ids, aliases
}

func renderReadme(data []byte, sourceURL string, original []byte) (string, error) {
	base, _ := url.Parse(sourceURL)
	repoURL := ""
	if base != nil && strings.EqualFold(base.Hostname(), "github.com") {
		repoPath, _, ok := strings.Cut(base.Path, "/blob/")
		if ok {
			repoURL = (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: repoPath}).String()
		}
	}
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
	// 译文标题保留原文锚点映射，避免相同标题或中文改名后指向错误位置。
	headingIDs := make(map[string]string)
	translated, aliases := readmeHeadingIDs(root, data)
	for i, alias := range aliases {
		headingIDs[alias] = translated[i]
	}
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
		sourceIDs, sourceAliases := readmeHeadingIDs(source, original)
		for i := range min(len(sourceIDs), len(translated)) {
			headingIDs[sourceIDs[i]] = translated[i]
			headingIDs[sourceAliases[i]] = translated[i]
		}
	}
	removeNonProjectSections(root, data, sourceHeadings)
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if enter {
			switch node := n.(type) {
			case *ast.Link:
				destination, err := url.Parse(string(node.Destination))
				destinationBase := ""
				if err == nil && base != nil {
					destinationBase = strings.SplitN(base.ResolveReference(destination).String(), "#", 2)[0]
				}
				if err == nil && destination.Fragment != "" && (strings.HasPrefix(string(node.Destination), "#") ||
					destinationBase == sourceURL || repoURL != "" && strings.EqualFold(strings.TrimSuffix(destinationBase, "/"), repoURL)) {
					fragment := destination.Fragment
					if translated := headingIDs[fragment]; translated != "" {
						fragment = translated
					}
					node.Destination = []byte((&url.URL{Fragment: fragment}).String())
				} else {
					node.Destination = resolve(node.Destination, false)
				}
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
	if err := db.Select("repo_id", "readme_bytes", "language", "source_url", "cleaned_html", "cleanup_verified", "complete", "truncated").Find(&row, "repo_id = ?", repoID).Error; err != nil {
		return ReadmeView{}, err
	}
	view := ReadmeView{State: consts.StatusPending, Language: row.Language, SourceURL: row.SourceURL, Truncated: row.Truncated}
	switch {
	case row.RepoID != 0 && row.ReadmeBytes == 0:
		view.State = consts.StatusMissing
	case row.Complete && row.CleanupVerified && row.CleanedHTML != nil:
		if *row.CleanedHTML == "" {
			view.State = consts.StatusUnavailable
		} else {
			view.State, view.Complete, view.ChineseHTML = consts.StatusReady, true, *row.CleanedHTML
		}
	}
	return view, nil
}

func saveReadme(tx *gorm.DB, repoID int64, source gh.ReadmeResult, fullName string, jobID int64) error {
	var visible int64
	if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
		Where(storage.RepositoryColumns.RepoID+" = ?", repoID).Count(&visible).Error; err != nil || visible == 0 {
		return err
	}
	row := storage.RepoReadmeDocument{RepoID: repoID, ReadmeSHA: source.SHA, ReadmeBytes: len(source.Body),
		SourceURL: source.URL, Truncated: source.Truncated, SourceMarkdown: source.Body, SourceRepoName: fullName,
		SourceETag: source.ETag, SourcePath: source.Path, SourceJobID: jobID}
	if err := buildReadmeDraft(tx, &row); err != nil {
		return err
	}
	// 普通来源复查只在正文版本、发布地址或译文变化时使清理结果过期。
	if row.Complete {
		var current storage.RepoReadmeDocument
		if err := tx.Select("cleaned_html", "cleanup_verified").Find(&current,
			"repo_id = ? AND readme_sha = ? AND source_url = ? AND chinese_html = ?", repoID, row.ReadmeSHA, row.SourceURL, row.ChineseHTML).Error; err != nil {
			return err
		}
		row.CleanedHTML, row.CleanupVerified = current.CleanedHTML, current.CleanupVerified
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

// buildReadmeDraft 根据原文与同版译文生成安全的中文草稿。
func buildReadmeDraft(tx *gorm.DB, row *storage.RepoReadmeDocument) error {
	row.ChineseHTML, row.Complete = "", false
	if len(row.SourceMarkdown) > 0 {
		data := readmeBody(row.SourceMarkdown)
		var original []byte
		row.Language = language(prose(data))
		if row.Language != consts.LanguageChinese && row.Language != consts.LanguageUndetermined {
			var saved storage.RepoReadmeTranslation
			cols := storage.RepoReadmeTranslationColumns
			if err := tx.Find(&saved, cols.RepoID+"=? AND "+cols.ReadmeSHA+"=? AND "+cols.ReadmeBytes+"=? AND "+cols.Complete+"=1",
				row.RepoID, row.ReadmeSHA, row.ReadmeBytes).Error; err != nil {
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
			html, err := renderReadme(data, row.SourceURL, original)
			if err != nil {
				return err
			}
			row.ChineseHTML, row.Complete = html, strings.TrimSpace(html) != ""
		}
	}
	return nil
}

func (e *Engine) rememberReadme(ctx context.Context, job storage.Job, source gh.ReadmeResult, fullName string) error {
	return e.Queue.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owns(tx, job); err != nil {
			return err
		}
		return saveReadme(tx, *job.RepoID, source, fullName, job.ID)
	})
}

// savedReadme 只恢复已验证且长度完整的快照，迁移前的旧记录由下次处理补齐。
func savedReadme(document storage.RepoReadmeDocument) (gh.ReadmeResult, bool) {
	valid := document.SourceRepoName != "" && document.ReadmeSHA != "" && document.ReadmeBytes > 0 &&
		len(document.SourceMarkdown) == document.ReadmeBytes
	return gh.ReadmeResult{Body: document.SourceMarkdown, SHA: document.ReadmeSHA, ETag: document.SourceETag,
		URL: document.SourceURL, Path: document.SourcePath, Truncated: document.Truncated}, valid
}

// verifyReadmeRepository 复核发布资格和仓库地址，无需再次下载正文。
func (e *Engine) verifyReadmeRepository(ctx context.Context, id int64, fullName string, policy gh.Policy) (*sdk.Repository, error) {
	remote, err := e.publicRepo(ctx, id, policy)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(remote.GetFullName(), fullName) {
		return nil, &gh.Failure{Code: consts.CodeGitHubReadmeSourceChanged}
	}
	return remote, nil
}

func readmeAddressChanged(err error) bool {
	var failure *gh.Failure
	return errors.As(err, &failure) && failure.Code == consts.CodeGitHubReadmeSourceChanged
}

// verifyReadmeVersion 在发布前核对文件 SHA 及仓库身份，避免正文更新或仓库转私时发布旧内容。
func (e *Engine) verifyReadmeVersion(ctx context.Context, repoID int64, fullName string, snapshot gh.ReadmeResult, policy gh.Policy) (*sdk.Repository, error) {
	owner, name, _ := strings.Cut(fullName, "/")
	sha, shaErr := e.GitHub.ReadmeSHA(ctx, owner, name, snapshot.Path, policy)
	remote, err := e.verifyReadmeRepository(ctx, repoID, fullName, policy)
	if err != nil {
		return nil, err
	}
	if shaErr != nil {
		return nil, shaErr
	}
	if sha != snapshot.SHA {
		return nil, &gh.Failure{Code: consts.CodeGitHubReadmeSourceChanged}
	}
	return remote, nil
}

// invalidateReadme 撤下已确认过期的快照和导读，清除条件请求基线并尽快重新检查来源。
func invalidateReadme(tx *gorm.DB, repoID int64, sha string, now int64) error {
	if err := tx.Delete(&storage.RepoReadmeDocument{}, "repo_id = ? AND readme_sha = ?", repoID, sha).Error; err != nil {
		return err
	}
	if err := tx.Delete(&storage.RepoReadmeTranslation{}, "repo_id = ? AND readme_sha = ?", repoID, sha).Error; err != nil {
		return err
	}
	if err := tx.Model(&storage.AISummary{}).Where("repo_id = ? AND readme_sha = ?", repoID, sha).
		Update("summary_status", consts.StatusStale).Error; err != nil {
		return err
	}
	if err := tx.Delete(&storage.RepoAITopics{}, "repo_id = ?", repoID).Error; err != nil {
		return err
	}
	return tx.Model(&storage.SummaryCheck{}).Where("repo_id = ?", repoID).
		Updates(map[string]any{"readme_etag": nil, "next_check_at": now}).Error
}
