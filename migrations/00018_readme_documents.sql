-- +goose Up
CREATE TABLE repo_readme_documents (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE CASCADE,
    readme_sha TEXT NOT NULL,
    readme_bytes INTEGER NOT NULL,
    language TEXT NOT NULL,
    source_url TEXT NOT NULL,
    chinese_html TEXT NOT NULL,
    complete INTEGER NOT NULL,
    truncated INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER withdraw_repo_readme AFTER UPDATE OF blocked,disabled,name_is_current,state,first_not_found_at ON repositories
WHEN NEW.blocked=1 OR NEW.disabled=1 OR NEW.name_is_current=0
    OR NEW.state='unavailable' OR NEW.first_not_found_at IS NOT NULL
BEGIN
    DELETE FROM repo_readme_documents WHERE repo_id=NEW.repo_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER withdraw_repo_readme;
DROP TABLE repo_readme_documents;
