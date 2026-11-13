-- +goose Up
ALTER TABLE report_editorials RENAME TO report_editorials_v22;
CREATE TABLE report_editorials (
    kind TEXT NOT NULL CHECK(kind IN ('daily','weekly','monthly')),
    issue_key TEXT NOT NULL,
    issue_number INTEGER,
    publication_status TEXT NOT NULL CHECK(publication_status IN ('published','preview','legacy')),
    coverage_days INTEGER NOT NULL CHECK(coverage_days >= 0),
    fingerprint TEXT NOT NULL,
    headline TEXT NOT NULL,
    lead TEXT NOT NULL,
    stories_json TEXT NOT NULL CHECK(json_valid(stories_json)),
    repo_ids_json TEXT NOT NULL CHECK(json_valid(repo_ids_json)),
    source_issues INTEGER NOT NULL,
    observed_from INTEGER NOT NULL,
    observed_to INTEGER NOT NULL,
    generated_at INTEGER NOT NULL,
    CHECK((publication_status='published' AND issue_number IS NOT NULL AND issue_number>0)
       OR (publication_status<>'published' AND issue_number IS NULL)),
    PRIMARY KEY(kind, issue_key),
    UNIQUE(kind, issue_number)
) WITHOUT ROWID;
INSERT INTO report_editorials(kind,issue_key,issue_number,publication_status,coverage_days,fingerprint,headline,lead,stories_json,repo_ids_json,source_issues,observed_from,observed_to,generated_at)
SELECT kind,issue_key,CASE WHEN kind='daily' THEN issue_number END,
       CASE WHEN kind='daily' THEN 'published' ELSE 'legacy' END,
       CASE WHEN kind='daily' THEN 1 ELSE 0 END,
       fingerprint,headline,lead,stories_json,repo_ids_json,source_issues,observed_from,observed_to,generated_at
FROM report_editorials_v22;
DROP TABLE report_editorials_v22;
CREATE INDEX idx_daily_report_items_observed ON daily_report_items(observed_at);

-- +goose Down
DROP INDEX idx_daily_report_items_observed;
ALTER TABLE report_editorials RENAME TO report_editorials_v23;
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
INSERT INTO report_editorials(kind,issue_key,issue_number,fingerprint,headline,lead,stories_json,repo_ids_json,source_issues,observed_from,observed_to,generated_at)
SELECT kind,issue_key,issue_number,fingerprint,headline,lead,stories_json,repo_ids_json,source_issues,observed_from,observed_to,generated_at
FROM report_editorials_v23 WHERE publication_status='published';
DROP TABLE report_editorials_v23;
