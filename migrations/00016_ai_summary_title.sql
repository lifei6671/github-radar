-- +goose Up
ALTER TABLE ai_summaries ADD COLUMN title_zh TEXT;

-- +goose Down
ALTER TABLE ai_summaries DROP COLUMN title_zh;
