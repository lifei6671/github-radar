-- +goose Up
ALTER TABLE repositories ADD COLUMN below_grade_since INTEGER;
CREATE INDEX idx_snapshots_repo_observed ON repo_snapshots(repo_id, observed_at, sample_slot);
CREATE INDEX idx_search_observed ON search_observations(observed_at);
CREATE INDEX idx_trending_slot ON trending_observations(observed_slot);
CREATE INDEX idx_jobs_retention ON jobs(status, updated_at);

-- 锚点保存实际观测时间及同毫秒内的计划槽，用于稳定选择首末样本。
CREATE TABLE metrics_hourly (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    hour_start INTEGER NOT NULL,
    stars_open INTEGER NOT NULL, stars_close INTEGER NOT NULL,
    open_observed_at INTEGER NOT NULL, close_observed_at INTEGER NOT NULL,
    open_slot INTEGER NOT NULL, close_slot INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    delta INTEGER, quality TEXT NOT NULL CHECK(quality IN ('exact','estimated','insufficient')),
    PRIMARY KEY(repo_id,hour_start)
) WITHOUT ROWID;
CREATE INDEX idx_metrics_hourly_start ON metrics_hourly(hour_start,repo_id);

CREATE TABLE metrics_daily (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    day TEXT NOT NULL,
    stars_open INTEGER NOT NULL, stars_close INTEGER NOT NULL,
    open_observed_at INTEGER NOT NULL, close_observed_at INTEGER NOT NULL,
    open_slot INTEGER NOT NULL, close_slot INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    delta INTEGER, quality TEXT NOT NULL CHECK(quality IN ('exact','estimated','insufficient')),
    hotness_score REAL, rank_in_day INTEGER,
    PRIMARY KEY(repo_id,day)
) WITHOUT ROWID;
CREATE INDEX idx_metrics_daily_day ON metrics_daily(day,repo_id);

CREATE TABLE latest_metrics (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id),
    delta_1h INTEGER, delta_24h INTEGER, delta_7d INTEGER, delta_30d INTEGER,
    relative_growth REAL, acceleration REAL, persistence_score REAL,
    daily_concentration REAL,
    trending_frequency INTEGER NOT NULL, trending_streak INTEGER NOT NULL, trending_rank INTEGER,
    trending_score REAL, hotness_score REAL,
    candidate_burst INTEGER NOT NULL,
    data_quality TEXT NOT NULL CHECK(data_quality IN ('exact','estimated','insufficient')),
    window_details_json TEXT NOT NULL CHECK(json_valid(window_details_json)),
    calculated_at INTEGER NOT NULL
);
CREATE INDEX idx_metrics_hotness ON latest_metrics(hotness_score DESC,repo_id);
CREATE INDEX idx_metrics_exact_growth ON latest_metrics(delta_24h DESC,repo_id);

-- 待聚合表保存尚未合并的增量，过期源数据删除后仍可接收迟到样本。
CREATE TABLE metric_rollup_pending (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id),
    resolution TEXT NOT NULL CHECK(resolution IN ('hourly','daily')),
    bucket_start INTEGER NOT NULL,
    stars_open INTEGER NOT NULL, stars_close INTEGER NOT NULL,
    open_observed_at INTEGER NOT NULL, close_observed_at INTEGER NOT NULL,
    open_slot INTEGER NOT NULL, close_slot INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY(repo_id,resolution,bucket_start)
) WITHOUT ROWID;
CREATE INDEX idx_metric_pending_due ON metric_rollup_pending(resolution,bucket_start,repo_id);

-- 旧库按真实观测时刻生成待合并的小时锚点。
INSERT INTO metric_rollup_pending
    (repo_id,resolution,bucket_start,stars_open,stars_close,open_observed_at,close_observed_at,open_slot,close_slot,sample_count)
SELECT repo_id,'hourly',bucket_start,
       MAX(CASE WHEN first_row=1 THEN stars END),MAX(CASE WHEN last_row=1 THEN stars END),
       MIN(observed_at),MAX(observed_at),
       MAX(CASE WHEN first_row=1 THEN sample_slot END),MAX(CASE WHEN last_row=1 THEN sample_slot END),COUNT(*)
FROM (
    SELECT *, observed_at/3600000*3600000 AS bucket_start,
        ROW_NUMBER() OVER (PARTITION BY repo_id,observed_at/3600000 ORDER BY observed_at,sample_slot) AS first_row,
        ROW_NUMBER() OVER (PARTITION BY repo_id,observed_at/3600000 ORDER BY observed_at DESC,sample_slot DESC) AS last_row
    FROM repo_snapshots
)
GROUP BY repo_id,bucket_start;

-- +goose StatementBegin
CREATE TRIGGER capture_hourly_sample AFTER INSERT ON repo_snapshots BEGIN
    INSERT INTO metric_rollup_pending
        (repo_id,resolution,bucket_start,stars_open,stars_close,open_observed_at,close_observed_at,open_slot,close_slot,sample_count)
    VALUES (NEW.repo_id,'hourly',NEW.observed_at/3600000*3600000,NEW.stars,NEW.stars,
            NEW.observed_at,NEW.observed_at,NEW.sample_slot,NEW.sample_slot,1)
    ON CONFLICT(repo_id,resolution,bucket_start) DO UPDATE SET
        stars_open=CASE WHEN (excluded.open_observed_at,excluded.open_slot)<(open_observed_at,open_slot) THEN excluded.stars_open ELSE stars_open END,
        open_slot=CASE WHEN (excluded.open_observed_at,excluded.open_slot)<(open_observed_at,open_slot) THEN excluded.open_slot ELSE open_slot END,
        open_observed_at=MIN(open_observed_at,excluded.open_observed_at),
        stars_close=CASE WHEN (excluded.close_observed_at,excluded.close_slot)>(close_observed_at,close_slot) THEN excluded.stars_close ELSE stars_close END,
        close_slot=CASE WHEN (excluded.close_observed_at,excluded.close_slot)>(close_observed_at,close_slot) THEN excluded.close_slot ELSE close_slot END,
        close_observed_at=MAX(close_observed_at,excluded.close_observed_at),
        sample_count=sample_count+1;
END;
-- +goose StatementEnd

-- 完整批次包括空榜。旧库只回填有真实成功时间的最新批次。
CREATE TABLE trending_batches (
    source_key TEXT NOT NULL, window TEXT NOT NULL,
    observed_slot INTEGER NOT NULL, observed_at INTEGER NOT NULL,
    PRIMARY KEY(source_key,window,observed_slot)
) WITHOUT ROWID;
CREATE INDEX idx_trending_batches_age ON trending_batches(observed_at);
INSERT INTO trending_batches(source_key,window,observed_slot,observed_at)
SELECT substr(key,10,length(key)-9-length(window)-1),window,
       json_extract(value,'$.observedSlot'),json_extract(value,'$.observedAt')
FROM (
    SELECT key,value,
           CASE WHEN key LIKE '%:daily' THEN 'daily' WHEN key LIKE '%:weekly' THEN 'weekly' ELSE 'monthly' END AS window
    FROM maintenance_state
    WHERE key LIKE 'trending:%' AND json_valid(value)
)
WHERE json_type(value,'$.observedAt')='integer' AND json_type(value,'$.observedSlot')='integer';
