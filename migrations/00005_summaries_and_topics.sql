-- +goose Up
-- 只存放经校验的中文摘要、精简标签及来源指纹。
CREATE TABLE ai_summaries (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id),
    readme_sha TEXT,
    source_fingerprint TEXT NOT NULL,
    source_quality TEXT NOT NULL CHECK(source_quality IN ('sufficient','incomplete_source','insufficient_source')),
    source_language TEXT NOT NULL,
    summary_zh TEXT NOT NULL,
    features_json TEXT NOT NULL CHECK(json_valid(features_json)),
    audience_json TEXT NOT NULL CHECK(json_valid(audience_json)),
    value_tags_json TEXT NOT NULL CHECK(json_valid(value_tags_json)),
    source_readme_url TEXT,
    model_provider TEXT NOT NULL,
    model_name TEXT NOT NULL,
    prompt_version INTEGER NOT NULL,
    summary_status TEXT NOT NULL CHECK(summary_status IN ('ready','stale')),
    generated_at INTEGER NOT NULL
);
CREATE TABLE summary_checks (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id),
    readme_etag TEXT,
    last_seen_readme_sha TEXT,
    source_fingerprint TEXT NOT NULL,
    source_language TEXT NOT NULL,
    last_checked_at INTEGER NOT NULL,
    next_check_at INTEGER NOT NULL,
    last_error_code TEXT
);
CREATE INDEX idx_summary_checks_due ON summary_checks(next_check_at);

CREATE UNIQUE INDEX idx_jobs_summary_unresolved ON jobs(repo_id)
    WHERE type='refresh_readme_summary' AND status IN ('pending','running','failed','unknown');
CREATE INDEX idx_jobs_summary_ready ON jobs(status,available_at,priority DESC,id)
    WHERE type='refresh_readme_summary';

-- 调用台账与任务使用同一数据库；未知调用由管理员关闭。
CREATE TABLE ai_calls (
    call_key TEXT PRIMARY KEY NOT NULL,
    job_id INTEGER NOT NULL REFERENCES jobs(id),
    provider TEXT NOT NULL,
    provider_request_id TEXT,
    origin TEXT NOT NULL CHECK(origin IN ('auto','manual')),
    status TEXT NOT NULL CHECK(status IN ('reserved','sending','succeeded','failed','unknown')),
    reserved_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    input_tokens INTEGER,
    output_tokens INTEGER,
    estimated_cost REAL,
    duration_ms INTEGER,
    error_code TEXT
);
CREATE INDEX idx_ai_calls_budget ON ai_calls(origin,reserved_at);
CREATE INDEX idx_ai_calls_job ON ai_calls(job_id);
CREATE UNIQUE INDEX idx_ai_calls_unresolved ON ai_calls(job_id)
    WHERE status IN ('reserved','sending','unknown');

CREATE TABLE topic_candidates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    angle_key TEXT NOT NULL,
    title_zh TEXT NOT NULL,
    reader_value TEXT NOT NULL,
    source_url TEXT NOT NULL,
    source_fingerprint TEXT,
    reason TEXT NOT NULL,
    topic_score REAL NOT NULL,
    hotness_score REAL,
    growth_24h INTEGER,
    status TEXT NOT NULL CHECK(status IN ('candidate','shortlisted','accepted','ignored','published')),
    feedback TEXT,
    reviewed_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(repo_id,angle_key)
);
CREATE INDEX idx_topic_candidates_status ON topic_candidates(status,id DESC);
CREATE INDEX idx_topic_candidates_age ON topic_candidates(updated_at)
    WHERE status='candidate' AND reviewed_at IS NULL;
