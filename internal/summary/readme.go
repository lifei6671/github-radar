package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

var readmeMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))

// readReadme 按仓库 ID 验证公开性和当前地址后临时获取 README，不写入原文。
func (e *Engine) readReadme(ctx context.Context, repo storage.Repository, display bool) (gh.ReadmeResult, error) {
	policy := gh.Policy{RepositoryID: repo.RepoID, Grade: repo.Grade, ReadmeBudget: e.Queue.AI.Summary.GitHubBudget, ReadmeView: display}
	remote, err := e.publicRepo(ctx, repo.RepoID, policy)
	if err != nil {
		return gh.ReadmeResult{}, err
	}
	fullName := remote.GetFullName()
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok {
		return gh.ReadmeResult{}, &gh.Failure{Code: consts.CodeGitHubInvalidRepository}
	}
	result, err := e.GitHub.Readme(ctx, owner, name, "", e.Queue.AI.Summary.ReadmeMaxBytes, policy)
	if err != nil || result.Missing {
		return result, err
	}
	remote, err = e.publicRepo(ctx, repo.RepoID, policy)
	if err != nil {
		return gh.ReadmeResult{}, err
	}
	if result.NotModified || !strings.EqualFold(remote.GetFullName(), fullName) {
		return gh.ReadmeResult{}, &gh.Failure{Code: consts.CodeGitHubReadmeSourceChanged}
	}
	return result, nil
}

type readmePart struct {
	start, end int
	text       string
}

func readmeBody(data []byte) []byte {
	return bytes.ToValidUTF8(data, nil)
}

// readmeParts 按 Markdown 文本节点提取可翻译片段，代码、媒体及链接地址留在原文结构中。
func readmeParts(data []byte) []readmePart {
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
			if strings.ContainsFunc(content, func(r rune) bool { return unicode.In(r, unicode.Latin) }) {
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

func renderReadme(data []byte, sourceURL string) (string, error) {
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
	OriginalHTML     string `json:"originalHtml,omitempty"`
	ChineseHTML      string `json:"chineseHtml,omitempty"`
	Complete         bool   `json:"complete"`
	Truncated        bool   `json:"truncated"`
	TranslationJobID *int64 `json:"translationJobId,omitempty"`
}

// ReadmeView 只发布与本次确认的公开 README SHA 一致的译文。
func (e *Engine) ReadmeView(ctx context.Context, repo storage.Repository) (ReadmeView, error) {
	if e.GitHub == nil {
		return ReadmeView{State: consts.StatusUnavailable}, nil
	}
	source, err := e.readReadme(ctx, repo, true)
	if err != nil {
		// 公开读取也可能先于定时采集发现仓库已撤回。
		if withdraw := withdrawRepository(err, repo.RepoID); withdraw != nil {
			if saveErr := withdraw(e.Queue.DB.WithContext(ctx)); saveErr != nil {
				e.Logger.Warn("保存 GitHub 仓库可见性失败", "repoId", repo.RepoID, "error", saveErr)
			}
		} else if err == ErrRepoUnavailable {
			// 上游身份响应异常时保留详情，只有已确认撤回才返回 404。
			err = &gh.Failure{Code: consts.CodeGitHubInvalidRepository}
		}
		return ReadmeView{}, err
	}
	if source.Missing || len(source.Body) == 0 {
		return ReadmeView{State: consts.StatusMissing}, nil
	}
	data := readmeBody(source.Body)
	view := ReadmeView{State: consts.StatusReady, Language: language(prose(data)), SourceURL: source.URL, Truncated: source.Truncated}
	if view.Language == consts.LanguageChinese {
		view.ChineseHTML, err = renderReadme(data, source.URL)
		view.Complete = true
		return view, err
	}
	view.OriginalHTML, err = renderReadme(data, source.URL)
	if err != nil {
		return ReadmeView{}, err
	}
	var saved storage.RepoReadmeTranslation
	if err := e.Queue.DB.WithContext(ctx).Find(&saved, storage.RepoReadmeTranslationColumns.RepoID+"=? AND "+storage.RepoReadmeTranslationColumns.ReadmeSHA+"=? AND "+storage.RepoReadmeTranslationColumns.ReadmeBytes+"=?", repo.RepoID, source.SHA, len(source.Body)).Error; err != nil {
		return ReadmeView{}, err
	}
	if saved.RepoID != 0 {
		var translations []string
		if err := json.Unmarshal([]byte(saved.PartsJSON), &translations); err != nil {
			return ReadmeView{}, err
		}
		parts := readmeParts(data)
		if len(translations) <= len(parts) && slices.ContainsFunc(translations, containsChinese) {
			view.ChineseHTML, err = renderReadme(translatedMarkdown(data, parts, translations), source.URL)
			view.Complete = saved.Complete && len(translations) == len(parts)
		}
	}
	return view, err
}
