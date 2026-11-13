-- +goose Up
-- 旧主题词可能包含附带功能，必须经过新规则判断后才能参与分类。
ALTER TABLE repo_ai_topics ADD COLUMN purpose_version INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE repo_ai_topics DROP COLUMN purpose_version;
