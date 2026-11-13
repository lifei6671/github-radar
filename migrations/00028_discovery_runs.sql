-- +goose Up
ALTER TABLE discovery_rules ADD COLUMN age_window TEXT NOT NULL DEFAULT '';

CREATE TABLE discovery_rule_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id INTEGER NOT NULL,
    source_type TEXT NOT NULL,
    source_key TEXT NOT NULL,
    planned_at INTEGER NOT NULL,
    pass TEXT NOT NULL,
    created_from INTEGER NOT NULL DEFAULT 0,
    created_until INTEGER NOT NULL DEFAULT 0,
    query TEXT NOT NULL,
    min_stars INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0,
    upstream_total INTEGER,
    planned_shards INTEGER NOT NULL DEFAULT 1,
    completed_shards INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    error_code TEXT,
    UNIQUE(rule_id, planned_at, pass)
);
CREATE INDEX idx_discovery_runs_plan ON discovery_rule_runs(planned_at, id);

CREATE TABLE discovery_rule_run_repos (
    run_id INTEGER NOT NULL REFERENCES discovery_rule_runs(id) ON DELETE CASCADE,
    repo_id INTEGER NOT NULL,
    newly_discovered INTEGER NOT NULL,
    PRIMARY KEY(run_id, repo_id)
);

ALTER TABLE jobs ADD COLUMN discovery_run_id INTEGER;
CREATE INDEX idx_jobs_discovery_run ON jobs(discovery_run_id, status);

-- +goose Down
DROP INDEX idx_jobs_discovery_run;
ALTER TABLE jobs DROP COLUMN discovery_run_id;
DROP TABLE discovery_rule_run_repos;
DROP TABLE discovery_rule_runs;
ALTER TABLE discovery_rules DROP COLUMN age_window;
