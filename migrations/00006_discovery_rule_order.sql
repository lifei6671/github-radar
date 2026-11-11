-- +goose Up
-- 保留旧后台列表的顺序，展示排序独立于任务优先级。
ALTER TABLE discovery_rules ADD COLUMN display_position INTEGER NOT NULL DEFAULT 0;
WITH ordered AS (
    SELECT id, ROW_NUMBER() OVER (ORDER BY source_type, id) - 1 AS position FROM discovery_rules
)
UPDATE discovery_rules SET display_position = (SELECT position FROM ordered WHERE ordered.id = discovery_rules.id);

-- +goose Down
ALTER TABLE discovery_rules DROP COLUMN display_position;
