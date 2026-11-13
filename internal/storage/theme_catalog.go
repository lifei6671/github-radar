package storage

import (
	"encoding/json"

	"gorm.io/gorm"
)

const ThemeCatalogKey = "project_themes.catalog_key"

const ThemeCatalogKeySQL = `COALESCE((SELECT json_extract(value_json,'$') FROM system_settings WHERE key='` + ThemeCatalogKey + `'),'initial')`

// 指定所有者的专题以仓库归属为准，不参与主题词或 AI 分类。
const ThemeUsesTopicsSQL = `(json_array_length(project_themes.owners_json)=0)`

// TechnicalPurposeVersion 标记由技术用途提示词审核过的模型结果。
const TechnicalPurposeVersion = 1
const CurrentPurposeSQL = `purpose_version = 1`

// TagVocabularyVersion 区分受控词表结果和升级前的自由标签；旧专题继续可读。
const TagVocabularyVersion = 2
const CurrentTagVocabularySQL = `tag_vocab_version = 2`

// 技术专题只使用模型显式选择的有效目录项。组织专题沿用管理员指定的所有者。
const ProjectThemeMatchSQL = `(lower(repositories.owner) IN (SELECT value FROM json_each(project_themes.owners_json))
 OR (` + ThemeUsesTopicsSQL + ` AND
 EXISTS (SELECT 1 FROM repo_ai_topics ai, json_each(ai.theme_slugs_json) chosen
   WHERE ai.repo_id=repositories.repo_id AND ai.` + CurrentPurposeSQL + `
   AND ai.theme_catalog_key=` + ThemeCatalogKeySQL + ` AND chosen.value=project_themes.slug)))`

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
