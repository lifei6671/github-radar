-- +goose Up
CREATE TABLE daily_reports (
    issue_date TEXT PRIMARY KEY NOT NULL,
    window_end INTEGER NOT NULL,
    observed_from INTEGER NOT NULL,
    observed_to INTEGER NOT NULL,
    generated_at INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE daily_report_items (
    issue_date TEXT NOT NULL REFERENCES daily_reports(issue_date) ON DELETE CASCADE,
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    section TEXT NOT NULL CHECK(section IN ('discoveries','trending')),
    summary_zh TEXT NOT NULL,
    title_zh TEXT,
    observed_at INTEGER NOT NULL,
    rank INTEGER,
    previous_rank INTEGER,
    PRIMARY KEY (issue_date,position),
    UNIQUE (issue_date,repo_id)
) WITHOUT ROWID;
CREATE INDEX idx_daily_report_items_repo ON daily_report_items(repo_id);

-- +goose Down
DROP TABLE daily_report_items;
DROP TABLE daily_reports;
