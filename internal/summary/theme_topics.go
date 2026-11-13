package summary

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
)

// schedulePurposeBackfill 从已公开导读中补办用途审核，每轮按现有准入和预算使用有限席位。
func (q Queue) schedulePurposeBackfill(tx *gorm.DB, now time.Time, budget aibudget.Service) error {
	remaining, err := budget.Remaining(tx, admission.Maintenance, now)
	if err != nil || remaining == 0 {
		return err
	}
	const priority = 145
	keyPrefix := fmt.Sprintf("auto:technical-tags:%d:%d:", storage.TechnicalPurposeVersion, storage.TagVocabularyVersion)
	var active, due int64
	if err := tx.Model(&storage.Job{}).Where("type = ? AND priority = ? AND status IN ?",
		JobType, priority, []string{consts.StatusPending, consts.StatusRunning}).Count(&active).Error; err != nil {
		return fmt.Errorf("统计回填任务: %w", err)
	}
	if err := tx.Model(&storage.Job{}).Where("type = ? AND priority = ? AND status = ? AND available_at <= ?",
		JobType, priority, consts.StatusFailed, now.UnixMilli()).Count(&due).Error; err != nil {
		return fmt.Errorf("统计到期回填任务: %w", err)
	}
	slots := min(8, remaining) - int(active+due)
	if slots <= 0 {
		return nil
	}
	var repos []storage.Repository
	published := tx.Model(&storage.AISummary{}).Select("repo_id").Where("summary_status = ?", consts.StatusReady)
	reviewed := tx.Model(&storage.RepoAITopics{}).Select("repo_id").Where(storage.CurrentTagVocabularySQL)
	deferred := tx.Model(&storage.AdmissionDecision{}).Select("repo_id").
		Where("operation = ? AND state <> ? AND next_check_at > ?", "topics", admission.Pass, now.UnixMilli())
	occupied := tx.Model(&storage.Job{}).Select("repo_id").Where("type = ? AND repo_id IS NOT NULL", JobType).
		Where("dedup_key LIKE ? OR (priority = ? AND status IN ?) OR status IN ? OR (priority >= ? AND status IN ?)", keyPrefix+"%", priority,
			[]string{consts.StatusPending, consts.StatusFailed},
			[]string{consts.StatusRunning, consts.StatusUnknown}, 200, []string{consts.StatusPending, consts.StatusFailed})
	if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
		Where("repo_id IN (?) AND repo_id NOT IN (?) AND repo_id NOT IN (?) AND repo_id NOT IN (?)", published, reviewed, deferred, occupied).
		Order("repositories.starred_by_user DESC, repositories.stars DESC, repositories.repo_id").
		Limit(slots * 4).Find(&repos).Error; err != nil {
		return fmt.Errorf("读取回填候选: %w", err)
	}
	for _, repo := range repos {
		decision, evidence, err := storage.AIDecision(tx, repo, "topics", false, now)
		if err != nil {
			return err
		}
		if err := storage.SaveAdmission(tx, repo.RepoID, "topics", decision, evidence, now); err != nil {
			return err
		}
		if decision.State != admission.Pass {
			continue
		}
		var job storage.Job
		if err := tx.Where("repo_id = ? AND type = ? AND status IN ? AND priority < 200", repo.RepoID, JobType,
			[]string{consts.StatusPending, consts.StatusFailed}).Order("id").Limit(1).Find(&job).Error; err != nil {
			return err
		}
		if job.ID != 0 {
			var p payload
			if err := json.Unmarshal([]byte(job.PayloadJSON), &p); err != nil {
				return err
			}
			p.TopicsOnly = true
			data, err := json.Marshal(p)
			if err != nil {
				return err
			}
			fields := map[string]any{"priority": priority, "payload_json": string(data)}
			if job.LastErrorCode != nil && *job.LastErrorCode == consts.CodeGitHubLocalBudget {
				fields["available_at"] = now.UnixMilli()
			}
			if err := tx.Model(&job).Updates(fields).Error; err != nil {
				return err
			}
		} else {
			key := fmt.Sprintf("%s%d", keyPrefix, repo.RepoID)
			if _, err := q.insert(tx, JobType, repo.RepoID, key, payload{TopicsOnly: true}, priority, now.UnixMilli()); err != nil {
				return err
			}
		}
		slots--
		if slots == 0 {
			break
		}
	}
	return nil
}

// 仅为可直接复用已验证正文的主题回填放开 GitHub 排队限制。
func cachedThemeRepos(db *gorm.DB) *gorm.DB {
	return db.Model(&storage.Repository{}).Select("repositories.repo_id").
		Joins("JOIN ai_summaries s ON s.repo_id = repositories.repo_id AND s.summary_status = ?", consts.StatusReady).
		Joins("JOIN repo_readme_documents d ON d.repo_id = s.repo_id AND d.readme_sha = s.readme_sha AND d.source_repo_name = repositories.full_name").
		Joins("JOIN summary_checks c ON c.repo_id = s.repo_id AND c.last_seen_readme_sha = d.readme_sha").
		Where("d.readme_bytes > 0 AND LENGTH(d.source_markdown) = d.readme_bytes").
		Where(storage.PublicRepositoryCondition).
		Where("NOT EXISTS (?)", db.Model(&storage.RepoAITopics{}).Select("1").
			Where("repo_ai_topics.repo_id = repositories.repo_id AND "+storage.CurrentTagVocabularySQL+" AND repo_ai_topics.theme_catalog_key = "+storage.ThemeCatalogKeySQL))
}

// 用同版的缓存 README 原文复用旧项目资料，技术引文不能来自中文译文。
func cachedTopicSource(db *gorm.DB, repo storage.Repository, old storage.AISummary, check storage.SummaryCheck) (source, bool, error) {
	var doc storage.RepoReadmeDocument
	err := db.Where("repo_id=? AND readme_sha=? AND source_repo_name=?", repo.RepoID, old.ReadmeSHA, repo.FullName).Find(&doc).Error
	if err != nil || doc.RepoID == 0 || check.LastSeenReadmeSHA == nil || *check.LastSeenReadmeSHA != doc.ReadmeSHA {
		return source{}, false, err
	}
	readme, ok := savedReadme(doc)
	if !ok {
		return source{}, false, nil
	}
	body := prose(readme.Body)
	description := ""
	if repo.Description != nil {
		description = strings.Join(strings.Fields(*repo.Description), " ")
	}
	return source{fullName: repo.FullName, fingerprint: old.SourceFingerprint, sha: doc.ReadmeSHA,
		body: body, description: description, language: language(body), githubTopics: repo.GitHubTopics()}, true, nil
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
	err = tx.Model(&storage.RepoReadmeDocument{}).Where("repo_id=? AND readme_sha=? AND source_repo_name=? AND readme_bytes > 0 AND LENGTH(source_markdown) = readme_bytes", repoID, input.sha, input.fullName).Count(&count).Error
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
		// 极大词表只生成通用技术主题，等待适合的输入预算再补专题。
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
	isMCPServer := slices.Contains(topics, "mcp-server")
	selected := make([]string, 0, 5)
	for _, term := range catalog.Terms {
		if isMCPServer && term.Slug == "mcp-servers" {
			return `["mcp-servers"]`, true
		}
		if slices.Contains(result.Slugs, term.Slug) {
			if term.Slug == "mcp-servers" || !slices.ContainsFunc(topics, func(topic string) bool {
				return slices.ContainsFunc(storage.CompatibleCapabilityIDs(topic), func(id string) bool { return id == term.Slug || slices.Contains(term.Terms, id) })
			}) {
				continue
			}
			if len(selected) == 5 {
				break
			}
			selected = append(selected, term.Slug)
		}
	}
	encoded, _ := json.Marshal(selected)
	// 独立专题可为空或有未知候选；只忽略无效专题，不丢掉已核验的主用途。
	return string(encoded), (parsed && result.Slugs != nil) || len(catalog.Terms) == 0
}

// technicalTagPrompt 优先提供完整词表；小预算下按资料命中排序，并用发出的子集校验。
func technicalTagPrompt(catalog storage.TechnicalTags, budget int, evidence string) (string, storage.TechnicalTags) {
	const label = "技术词表（标识：名称）：\n"
	line := func(item storage.TechnicalTag) string {
		return item.Slug + "：" + item.Name + "。" + item.Definition + "\n"
	}
	total := len(label)
	for _, item := range catalog.Items {
		total += len(line(item))
	}
	items := catalog.Items
	if total > budget {
		items = slices.Clone(items)
		text := strings.ToLower(evidence)
		score := make(map[string]int, len(items))
		for _, item := range items {
			for _, term := range append([]string{strings.ReplaceAll(item.Slug, "-", " "), item.Name}, item.Aliases...) {
				if len(term) >= 4 && strings.Contains(text, strings.ToLower(term)) {
					score[item.Slug] += 2
				}
			}
			if strings.Contains(text, "mcp") && (item.Slug == "mcp-client" || item.Slug == "mcp-server") {
				score[item.Slug] += 1
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return score[items[i].Slug] > score[items[j].Slug] })
	}
	var prompt strings.Builder
	prompt.WriteString(label)
	included := make([]storage.TechnicalTag, 0, len(items))
	for _, item := range items {
		entry := line(item)
		if prompt.Len()+len(entry) > budget {
			continue
		}
		prompt.WriteString(entry)
		included = append(included, item)
	}
	if len(included) == 0 {
		return "", storage.TechnicalTags{}
	}
	if len(included) != len(catalog.Items) {
		// 已校验词表的子集不会引入歧义或无效领域。
		catalog, _ = storage.NewTechnicalTags(included)
	}
	return prompt.String(), catalog
}
