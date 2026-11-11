-- +goose Up
-- 分类由后台维护。topics 保留为有序 JSON 列表，查询时按当前规则匹配。
CREATE TABLE categories (
    slug TEXT PRIMARY KEY NOT NULL CHECK (slug <> 'other'),
    name TEXT NOT NULL,
    topics_json TEXT NOT NULL CHECK (json_valid(topics_json) AND json_type(topics_json) = 'array'),
    position INTEGER NOT NULL
) WITHOUT ROWID;
