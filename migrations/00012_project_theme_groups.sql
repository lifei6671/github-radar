-- +goose Up
-- 分组独立维护，既有专题保留标识与展示配置。
CREATE TABLE project_theme_groups (
    slug TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    position INTEGER NOT NULL
) WITHOUT ROWID;

INSERT INTO project_theme_groups (slug, name, description, position) VALUES
    ('technology', '技术方向', '按技术领域追踪开源项目与重要进展。', 0),
    ('content', '内容方向', '按项目用途浏览工具和工作流。', 1);

ALTER TABLE project_themes ADD COLUMN group_slug TEXT NOT NULL DEFAULT 'technology';
UPDATE project_themes SET group_slug = 'content' WHERE slug IN ('terminal-tools', 'webhook-tools');

-- +goose Down
ALTER TABLE project_themes DROP COLUMN group_slug;
DROP TABLE project_theme_groups;
