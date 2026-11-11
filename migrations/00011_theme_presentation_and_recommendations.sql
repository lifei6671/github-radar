-- +goose Up
-- 旧专题保持无图标，旧摘要在下一次符合规则的生成时补推荐理由。
ALTER TABLE project_themes ADD COLUMN icon TEXT NOT NULL DEFAULT '';
ALTER TABLE project_themes ADD COLUMN icon_color TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_summaries ADD COLUMN recommendation_reason TEXT;

-- +goose Down
ALTER TABLE ai_summaries DROP COLUMN recommendation_reason;
ALTER TABLE project_themes DROP COLUMN icon_color;
ALTER TABLE project_themes DROP COLUMN icon;
