-- +goose Up
CREATE TABLE repo_embeddings (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE CASCADE,
    model_key TEXT NOT NULL,
    source_fingerprint TEXT NOT NULL,
    vector BLOB CHECK (vector IS NULL OR (length(vector) BETWEEN 4 AND 32768 AND length(vector) % 4 = 0)),
    next_check_at INTEGER NOT NULL
);
CREATE INDEX idx_repo_embeddings_model ON repo_embeddings(model_key);

-- +goose Down
DROP TABLE repo_embeddings;
