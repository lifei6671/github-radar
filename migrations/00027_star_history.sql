-- +goose Up
CREATE TABLE repo_star_history_weeks (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id) ON DELETE CASCADE,
    week_timestamp INTEGER NOT NULL,
    days_json TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (repo_id, week_timestamp)
);

CREATE TABLE repo_star_history_status (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE CASCADE,
    checked_at INTEGER NOT NULL DEFAULT 0,
    fetched_at INTEGER NOT NULL DEFAULT 0,
    last_error_code TEXT,
    breakout_at INTEGER,
    confirmed_at INTEGER,
    breakout_json TEXT NOT NULL DEFAULT ''
);

ALTER TABLE latest_metrics ADD COLUMN breakout_json TEXT NOT NULL DEFAULT '';
ALTER TABLE repositories ADD COLUMN breakout_grade_json TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE repositories DROP COLUMN breakout_grade_json;
ALTER TABLE latest_metrics DROP COLUMN breakout_json;
DROP TABLE repo_star_history_status;
DROP TABLE repo_star_history_weeks;
