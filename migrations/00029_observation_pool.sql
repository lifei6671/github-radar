-- +goose Up
ALTER TABLE repositories ADD COLUMN observation_probe_signal_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repositories ADD COLUMN observation_probe_due_at INTEGER NOT NULL DEFAULT 0;

CREATE TABLE repo_observation_transitions (
    id INTEGER PRIMARY KEY,
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id) ON DELETE CASCADE,
    occurred_at INTEGER NOT NULL,
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    from_grade TEXT NOT NULL,
    to_grade TEXT NOT NULL,
    reason TEXT NOT NULL,
    evidence_json TEXT NOT NULL
);
CREATE INDEX idx_repo_observation_transitions ON repo_observation_transitions(repo_id, occurred_at, id);

-- +goose Down
DROP TABLE repo_observation_transitions;
ALTER TABLE repositories DROP COLUMN observation_probe_due_at;
ALTER TABLE repositories DROP COLUMN observation_probe_signal_at;
