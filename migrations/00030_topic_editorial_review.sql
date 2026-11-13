-- +goose Up
ALTER TABLE ai_summaries ADD COLUMN editorial_brief_json TEXT NOT NULL DEFAULT '';
ALTER TABLE topic_candidates ADD COLUMN score_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE topic_candidates ADD COLUMN score_details_json TEXT NOT NULL DEFAULT '';
ALTER TABLE topic_candidates ADD COLUMN editorial_review_json TEXT NOT NULL DEFAULT '';
ALTER TABLE topic_candidates ADD COLUMN release_key TEXT NOT NULL DEFAULT '';

CREATE TABLE topic_review_history (
    id INTEGER PRIMARY KEY,
    topic_id INTEGER NOT NULL REFERENCES topic_candidates(id) ON DELETE CASCADE,
    archived_at INTEGER NOT NULL,
    snapshot_json TEXT NOT NULL
);
CREATE INDEX idx_topic_review_history ON topic_review_history(topic_id, id);

-- +goose Down
DROP TABLE topic_review_history;
ALTER TABLE topic_candidates DROP COLUMN release_key;
ALTER TABLE topic_candidates DROP COLUMN editorial_review_json;
ALTER TABLE topic_candidates DROP COLUMN score_details_json;
ALTER TABLE topic_candidates DROP COLUMN score_version;
ALTER TABLE ai_summaries DROP COLUMN editorial_brief_json;
