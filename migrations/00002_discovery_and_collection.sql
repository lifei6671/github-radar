-- +goose Up
-- 数字 ID 是仓库身份。历史名称可以保留，只有当前地址参与唯一约束。
CREATE TABLE repositories (
    repo_id INTEGER PRIMARY KEY,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    full_name TEXT NOT NULL,
    name_is_current INTEGER NOT NULL DEFAULT 1 CHECK(name_is_current IN (0,1)),
    html_url TEXT NOT NULL,
    description TEXT,
    topics_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(topics_json)),
    primary_language TEXT,
    license_spdx_id TEXT,
    license_name TEXT,
    stars INTEGER NOT NULL DEFAULT 0,
    forks INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    disabled INTEGER NOT NULL DEFAULT 0,
    grade TEXT NOT NULL DEFAULT 'A' CHECK(grade IN ('S','A','B','C')),
    starred_by_user INTEGER NOT NULL DEFAULT 0,
    followed_at INTEGER,
    state TEXT NOT NULL DEFAULT 'candidate'
      CHECK(state IN ('candidate','active','paused','unavailable','address_pending')),
    github_created_at INTEGER,
    github_pushed_at INTEGER,
    discovered_at INTEGER NOT NULL,
    last_discovered_at INTEGER NOT NULL,
    last_checked_at INTEGER,
    last_success_at INTEGER,
    first_not_found_at INTEGER,
    next_fetch_at INTEGER NOT NULL,
    metadata_etag TEXT,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_repos_current_fullname
    ON repositories(full_name COLLATE NOCASE) WHERE name_is_current=1;
CREATE INDEX idx_repos_schedule ON repositories(state,next_fetch_at,grade);
CREATE INDEX idx_repos_stars ON repositories(stars DESC);

CREATE TABLE repo_snapshots (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    sample_slot INTEGER NOT NULL,
    observed_at INTEGER NOT NULL,
    stars INTEGER NOT NULL,
    forks INTEGER NOT NULL,
    PRIMARY KEY(repo_id,sample_slot)
) WITHOUT ROWID;
CREATE INDEX idx_samples_observed ON repo_snapshots(observed_at);

CREATE TABLE trending_observations (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    source_key TEXT NOT NULL,
    window TEXT NOT NULL CHECK(window IN ('daily','weekly','monthly')),
    observed_slot INTEGER NOT NULL,
    rank INTEGER NOT NULL,
    reported_stars INTEGER,
    PRIMARY KEY(repo_id,source_key,window,observed_slot)
) WITHOUT ROWID;
CREATE INDEX idx_trending_batch ON trending_observations(source_key,window,observed_slot,rank);

CREATE TABLE search_observations (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    source_key TEXT NOT NULL,
    observed_slot INTEGER NOT NULL,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY(repo_id,source_key,observed_slot)
) WITHOUT ROWID;

CREATE TABLE discovery_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type TEXT NOT NULL,
    source_key TEXT NOT NULL,
    query TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    interval_minutes INTEGER NOT NULL,
    next_run_at INTEGER NOT NULL,
    priority INTEGER NOT NULL,
    UNIQUE(source_type,source_key)
);
CREATE INDEX idx_discovery_due ON discovery_rules(enabled,next_run_at);

CREATE TABLE jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    repo_id INTEGER REFERENCES repositories(repo_id),
    dedup_key TEXT NOT NULL UNIQUE,
    payload_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(payload_json)),
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
      CHECK(status IN ('pending','running','succeeded','failed','dead','unknown','canceled')),
    available_at INTEGER NOT NULL,
    lease_until INTEGER,
    lease_token TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    last_error_code TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_jobs_ready ON jobs(status,available_at,priority DESC);
CREATE INDEX idx_jobs_lease ON jobs(status,lease_until);
CREATE INDEX idx_jobs_repo ON jobs(repo_id,type,status);

CREATE TABLE rate_limits (
    resource TEXT PRIMARY KEY NOT NULL,
    limit_total INTEGER NOT NULL,
    remaining INTEGER NOT NULL,
    reset_at INTEGER NOT NULL,
    paused_until INTEGER,
    observed_at INTEGER NOT NULL,
    local_window INTEGER NOT NULL,
    local_requests INTEGER NOT NULL,
    limited_count INTEGER NOT NULL,
    secondary_streak INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE maintenance_state (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;
