-- +goose Up
-- 日级证据只保留抽查所需字段，不保存 README 或模型响应。
CREATE TABLE repo_admission_daily (
    day TEXT NOT NULL,
    repo_id INTEGER NOT NULL,
    operation TEXT NOT NULL,
    state TEXT NOT NULL,
    reason TEXT NOT NULL,
    lane TEXT NOT NULL DEFAULT '',
    stars INTEGER NOT NULL,
    age_days INTEGER,
    decided_at INTEGER NOT NULL,
    PRIMARY KEY (day, repo_id, operation)
);
CREATE INDEX idx_repo_admission_daily_state ON repo_admission_daily(day, operation, state);

-- 单独记录成功探测、首篇已发布导读及每日最近一次监控席位。
CREATE TABLE admission_day_totals (
    day TEXT PRIMARY KEY,
    probe_samples INTEGER NOT NULL DEFAULT 0,
    monitored INTEGER,
    observed_at INTEGER
);
CREATE TABLE repo_first_guides (
    repo_id INTEGER PRIMARY KEY,
    ready_at INTEGER NOT NULL
);
CREATE INDEX idx_repo_first_guides_ready ON repo_first_guides(ready_at);
-- 迁移前已有导读不算作新策略生成；历史首篇发布时间未知。
INSERT INTO repo_first_guides(repo_id, ready_at)
SELECT repo_id, 0 FROM ai_summaries WHERE summary_status = 'ready';

-- +goose Down
DROP TABLE repo_first_guides;
DROP TABLE admission_day_totals;
DROP TABLE repo_admission_daily;
