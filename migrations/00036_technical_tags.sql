-- +goose Up
-- 新列保存通过词表校验的标识，旧自由文本保留作历史数据。
ALTER TABLE ai_summaries ADD COLUMN value_tag_slugs_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE repo_ai_topics ADD COLUMN tag_vocab_version INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE repo_ai_topics DROP COLUMN tag_vocab_version;
ALTER TABLE ai_summaries DROP COLUMN value_tag_slugs_json;
