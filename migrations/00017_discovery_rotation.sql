-- +goose Up
CREATE TABLE discovery_rotation (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE CASCADE,
    last_day INTEGER NOT NULL,
    streak INTEGER NOT NULL CHECK (streak > 0)
);

-- +goose Down
DROP TABLE discovery_rotation;
