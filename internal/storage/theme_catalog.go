package storage

import (
	"encoding/json"
	"slices"
	"strings"

	"gorm.io/gorm"
)

const ThemeCatalogKey = "project_themes.catalog_key"

const ThemeCatalogKeySQL = `COALESCE((SELECT json_extract(value_json,'$') FROM system_settings WHERE key='` + ThemeCatalogKey + `'),'initial')`

// 指定所有者的专题以仓库归属为准，不参与主题词或 AI 分类。
const ThemeUsesTopicsSQL = `(json_array_length(project_themes.owners_json)=0)`
const HasTopicThemesSQL = `EXISTS (SELECT 1 FROM project_themes WHERE ` + ThemeUsesTopicsSQL + `)`

// GitHub Topics 与每个专题的规范标识、匹配词和别名做精确匹配。
const ThemeTermMatchSQL = `(lower(term.value)=project_themes.slug
 OR lower(term.value) IN (SELECT value FROM json_each(project_themes.topics_json))
 OR lower(term.value) IN (SELECT value FROM json_each(project_themes.aliases_json)))`

const NativeThemeMatchSQL = `(` + ThemeUsesTopicsSQL + ` AND EXISTS (SELECT 1 FROM json_each(repositories.topics_json) term WHERE ` + ThemeTermMatchSQL + `))`
const HasNativeThemeSQL = `EXISTS (SELECT 1 FROM project_themes WHERE ` + NativeThemeMatchSQL + `)`

// 现有 AI Topics 和 AI 导读标签在回填完成前可以按新词表重匹配。
const ProjectThemeMatchSQL = `(lower(repositories.owner) IN (SELECT value FROM json_each(project_themes.owners_json))
 OR ` + NativeThemeMatchSQL + ` OR (` + ThemeUsesTopicsSQL + ` AND NOT ` + HasNativeThemeSQL + ` AND (
 EXISTS (SELECT 1 FROM repo_ai_topics ai, json_each(ai.theme_slugs_json) chosen
   WHERE ai.repo_id=repositories.repo_id AND ai.theme_catalog_key=` + ThemeCatalogKeySQL + ` AND chosen.value=project_themes.slug)
 OR (
  NOT EXISTS (SELECT 1 FROM repo_ai_topics ai WHERE ai.repo_id=repositories.repo_id AND ai.theme_catalog_key=` + ThemeCatalogKeySQL + `)
  AND (
   EXISTS (SELECT 1 FROM repo_ai_topics ai, json_each(ai.topics_json) term
     WHERE ai.repo_id=repositories.repo_id AND ` + ThemeTermMatchSQL + `)
   OR EXISTS (SELECT 1 FROM ai_summaries ai, json_each(ai.value_tags_json) term
     WHERE ai.repo_id=repositories.repo_id AND ai.summary_status='ready' AND ` + ThemeTermMatchSQL + `)
  )
 )
)))`

type ThemeTerm struct {
	Slug, Name, Description string
	Terms                   []string
}

type ThemeCatalog struct {
	Key   string
	Terms []ThemeTerm
}

func ReadThemeCatalog(db *gorm.DB) (ThemeCatalog, error) {
	var rows []struct {
		ProjectTheme
		CatalogKey string
	}
	if err := db.Table(ProjectThemeTable).Select("project_themes.*, " + ThemeCatalogKeySQL + " AS catalog_key").
		Order("slug").Scan(&rows).Error; err != nil {
		return ThemeCatalog{}, err
	}
	catalog := ThemeCatalog{Key: "initial", Terms: make([]ThemeTerm, 0, len(rows))}
	for _, row := range rows {
		catalog.Key = row.CatalogKey
		var owners []string
		if err := json.Unmarshal([]byte(row.OwnersJSON), &owners); err != nil {
			return ThemeCatalog{}, err
		}
		if len(owners) > 0 {
			continue
		}
		var keywords, aliases []string
		if err := json.Unmarshal([]byte(row.TopicsJSON), &keywords); err != nil {
			return ThemeCatalog{}, err
		}
		if err := json.Unmarshal([]byte(row.AliasesJSON), &aliases); err != nil {
			return ThemeCatalog{}, err
		}
		catalog.Terms = append(catalog.Terms, ThemeTerm{Slug: row.Slug, Name: row.Name, Description: row.Description,
			Terms: append(append([]string{row.Slug}, keywords...), aliases...)})
	}
	return catalog, nil
}

func (c ThemeCatalog) HasNative(topics []string) bool {
	for _, topic := range topics {
		for _, theme := range c.Terms {
			if slices.Contains(theme.Terms, strings.ToLower(topic)) {
				return true
			}
		}
	}
	return false
}

func (c ThemeCatalog) NeedsAI(topics []string) bool {
	return len(topics) == 0 || (len(c.Terms) > 0 && !c.HasNative(topics))
}
