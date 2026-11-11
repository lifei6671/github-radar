-- +goose Up
-- 保存当前 README 的译文片段和进度，原文从 GitHub 读取。
CREATE TABLE repo_readme_translations (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE CASCADE,
    readme_sha TEXT NOT NULL,
    readme_bytes INTEGER NOT NULL,
    model_key TEXT NOT NULL,
    parts_json TEXT NOT NULL CHECK(json_valid(parts_json) AND json_type(parts_json) = 'array'),
    complete INTEGER NOT NULL CHECK(complete IN (0, 1))
);

CREATE UNIQUE INDEX idx_jobs_readme_unresolved ON jobs(repo_id)
    WHERE type='translate_readme' AND status IN ('pending','running','failed','unknown');

-- +goose Down
DROP INDEX idx_jobs_readme_unresolved;
DROP TABLE repo_readme_translations;
