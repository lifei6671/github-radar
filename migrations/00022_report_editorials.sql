-- +goose Up
CREATE TABLE report_editorials (
    kind TEXT NOT NULL CHECK(kind IN ('daily','weekly','monthly')),
    issue_key TEXT NOT NULL,
    issue_number INTEGER NOT NULL CHECK(issue_number > 0),
    fingerprint TEXT NOT NULL,
    headline TEXT NOT NULL,
    lead TEXT NOT NULL,
    stories_json TEXT NOT NULL CHECK(json_valid(stories_json)),
    repo_ids_json TEXT NOT NULL CHECK(json_valid(repo_ids_json)),
    source_issues INTEGER NOT NULL,
    observed_from INTEGER NOT NULL,
    observed_to INTEGER NOT NULL,
    generated_at INTEGER NOT NULL,
    PRIMARY KEY(kind, issue_key),
    UNIQUE(kind, issue_number)
) WITHOUT ROWID;
CREATE INDEX idx_jobs_compose_report ON jobs(status,available_at,priority DESC,id) WHERE type='compose_report';

-- +goose Down
DROP INDEX idx_jobs_compose_report;
DROP TABLE report_editorials;
