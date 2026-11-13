-- +goose Up
CREATE TABLE repo_discovery_events (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id) ON DELETE CASCADE,
    source_type TEXT NOT NULL,
    source_key TEXT NOT NULL,
    source_url TEXT NOT NULL DEFAULT '',
    first_observed_at INTEGER NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, source_type, source_key)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE repo_discovery_events;
