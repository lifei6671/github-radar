-- +goose Up
ALTER TABLE project_themes ADD COLUMN owners_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(owners_json));

-- +goose Down
ALTER TABLE project_themes DROP COLUMN owners_json;
