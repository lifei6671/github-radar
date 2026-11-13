-- +goose Up
ALTER TABLE project_themes ADD COLUMN aliases_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(aliases_json));
ALTER TABLE repo_ai_topics ADD COLUMN theme_slugs_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(theme_slugs_json));
ALTER TABLE repo_ai_topics ADD COLUMN theme_catalog_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE repo_ai_topics DROP COLUMN theme_catalog_key;
ALTER TABLE repo_ai_topics DROP COLUMN theme_slugs_json;
ALTER TABLE project_themes DROP COLUMN aliases_json;
