-- +goose Up
ALTER TABLE repo_readme_documents ADD COLUMN source_markdown BLOB;
ALTER TABLE repo_readme_documents ADD COLUMN source_repo_name TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_readme_documents ADD COLUMN source_etag TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_readme_documents ADD COLUMN source_path TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_readme_documents ADD COLUMN source_job_id INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE repo_readme_documents DROP COLUMN source_job_id;
ALTER TABLE repo_readme_documents DROP COLUMN source_path;
ALTER TABLE repo_readme_documents DROP COLUMN source_etag;
ALTER TABLE repo_readme_documents DROP COLUMN source_repo_name;
ALTER TABLE repo_readme_documents DROP COLUMN source_markdown;
