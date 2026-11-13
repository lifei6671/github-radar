-- +goose Up
-- 保存每次调用的具体校验原因，历史记录保留原错误码。
ALTER TABLE ai_calls ADD COLUMN output_issue TEXT DEFAULT NULL;

-- +goose Down
ALTER TABLE ai_calls DROP COLUMN output_issue;
