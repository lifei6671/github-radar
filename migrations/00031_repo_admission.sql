-- +goose Up
ALTER TABLE repositories ADD COLUMN derived_project INTEGER NOT NULL DEFAULT 0;
-- 分开保存采集决策和 AI 预检，避免两条流水线互相覆盖。
CREATE TABLE repo_admission_decisions (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id) ON DELETE CASCADE,
    operation TEXT NOT NULL,
    state TEXT NOT NULL,
    reason TEXT NOT NULL,
    lane TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '{}',
    next_check_at INTEGER NOT NULL DEFAULT 0,
    policy_version INTEGER NOT NULL DEFAULT 1,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (repo_id, operation)
);
CREATE INDEX idx_repo_admission_due ON repo_admission_decisions(operation, next_check_at, repo_id);

-- +goose Down
DROP TABLE repo_admission_decisions;
ALTER TABLE repositories DROP COLUMN derived_project;
