package summary

import (
	"encoding/json"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

// 仅为可直接复用已验证正文的主题回填放开 GitHub 排队限制。
func cachedThemeRepos(db *gorm.DB) *gorm.DB {
	return db.Model(&storage.Repository{}).Select("repositories.repo_id").
		Joins("JOIN ai_summaries s ON s.repo_id = repositories.repo_id AND s.summary_status = ?", consts.StatusReady).
		Joins("JOIN repo_readme_documents d ON d.repo_id = s.repo_id AND d.readme_sha = s.readme_sha").
		Joins("JOIN summary_checks c ON c.repo_id = s.repo_id AND c.last_seen_readme_sha = d.readme_sha").
		Where("d.complete = ? AND TRIM(d.chinese_html) <> ''", true).
		Where(storage.PublicRepositoryCondition).
		Where(storage.HasTopicThemesSQL+" AND NOT "+storage.HasNativeThemeSQL).
		Where("NOT EXISTS (?)", db.Model(&storage.RepoAITopics{}).Select("1").
			Where("repo_ai_topics.repo_id = repositories.repo_id AND repo_ai_topics.theme_catalog_key = "+storage.ThemeCatalogKeySQL))
}

// 有版本对应的已发布中文 README 时，旧项目补分类直接复用它。
func cachedTopicSource(db *gorm.DB, repo storage.Repository, old storage.AISummary, check storage.SummaryCheck) (source, bool, error) {
	var doc storage.RepoReadmeDocument
	err := db.Select("repo_id", "readme_sha", "chinese_html").Where("repo_id=? AND readme_sha=? AND complete=1", repo.RepoID, old.ReadmeSHA).Find(&doc).Error
	if err != nil || doc.RepoID == 0 || check.LastSeenReadmeSHA == nil || *check.LastSeenReadmeSHA != doc.ReadmeSHA {
		return source{}, false, err
	}
	var text strings.Builder
	tokens := html.NewTokenizer(strings.NewReader(doc.ChineseHTML))
	for token := tokens.Next(); token != html.ErrorToken; token = tokens.Next() {
		if token == html.TextToken {
			text.Write(tokens.Text())
		}
	}
	body := strings.TrimSpace(text.String())
	description := ""
	if repo.Description != nil {
		description = strings.Join(strings.Fields(*repo.Description), " ")
	}
	return source{fullName: repo.FullName, fingerprint: old.SourceFingerprint, sha: doc.ReadmeSHA,
		body: body, description: description, language: consts.LanguageChinese, githubTopics: repo.GitHubTopics()}, true, nil
}

func cachedTopicSourceCurrent(tx *gorm.DB, repoID int64, input source) (bool, error) {
	var current storage.Repository
	err := tx.Where(storage.PublicRepositoryCondition).Find(&current, "repositories.repo_id=? AND repositories.full_name=?", repoID, input.fullName).Error
	if err != nil || current.RepoID == 0 {
		return false, err
	}
	description := ""
	if current.Description != nil {
		description = strings.Join(strings.Fields(*current.Description), " ")
	}
	if description != input.description {
		return false, nil
	}
	var count int64
	err = tx.Model(&storage.RepoReadmeDocument{}).Where("repo_id=? AND readme_sha=? AND complete=1", repoID, input.sha).Count(&count).Error
	return count == 1, err
}

func themePrompt(catalog storage.ThemeCatalog, budget int) string {
	if len(catalog.Terms) == 0 {
		return ""
	}
	slugs := make([]string, 0, len(catalog.Terms))
	for _, theme := range catalog.Terms {
		slugs = append(slugs, theme.Slug)
	}
	const label = "主题词表（规范标识）：\n"
	prefix := label + strings.Join(slugs, ", ") + "\n"
	if len(prefix) > budget {
		// 极大词表仍由服务端对全部条目匹配通用主题词。
		return ""
	}
	perTheme := (budget - len(prefix)) / len(catalog.Terms)
	var prompt strings.Builder
	prompt.WriteString(prefix)
	for _, theme := range catalog.Terms {
		detail := theme.Name + " " + theme.Description + " [" + strings.Join(theme.Terms, " / ") + "]"
		if available := perTheme - len(theme.Slug) - 2; available > 12 {
			prompt.WriteString(theme.Slug + " " + limitText(detail, available) + "\n")
		}
	}
	return prompt.String()
}

func resultThemeSlugs(raw, topicsJSON string, catalog storage.ThemeCatalog) (string, bool) {
	var result struct {
		Slugs []string `json:"theme_slugs"`
	}
	parsed := json.Unmarshal([]byte(raw), &result) == nil
	var topics []string
	_ = json.Unmarshal([]byte(topicsJSON), &topics)
	selected := make([]string, 0, 5)
	valid := result.Slugs != nil && len(result.Slugs) == 0
	for _, term := range catalog.Terms {
		explicit := slices.Contains(result.Slugs, term.Slug)
		valid = valid || explicit
		if len(selected) < 5 && (explicit || slices.ContainsFunc(topics, func(topic string) bool {
			return slices.Contains(term.Terms, topic)
		})) {
			selected = append(selected, term.Slug)
		}
	}
	encoded, _ := json.Marshal(selected)
	return string(encoded), (parsed && valid) || len(catalog.Terms) == 0
}
