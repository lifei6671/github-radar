-- +goose Up
-- NULL 表示旧缓存或尚未经过清理；空字符串表示模型确认无可发布正文。
ALTER TABLE repo_readme_documents ADD COLUMN cleaned_html TEXT;
ALTER TABLE repo_readme_documents ADD COLUMN cleanup_verified INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE repo_readme_documents DROP COLUMN cleanup_verified;
ALTER TABLE repo_readme_documents DROP COLUMN cleaned_html;
