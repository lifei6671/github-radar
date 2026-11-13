-- +goose Up
-- 用途仍存于 topics_json；完整能力、领域、形态和主能力证据独立存放。
ALTER TABLE repo_ai_topics ADD COLUMN taxonomy_json TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE repo_ai_topics DROP COLUMN taxonomy_json;
