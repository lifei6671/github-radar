-- +goose Up
-- 每个仓库保留命中的 Search 规则，以及是否有过 Trending 记录。
-- 发布判定依赖这份来源记录，观测明细仍按原有期限清理。
CREATE TABLE repo_discovery_sources (
    repo_id INTEGER NOT NULL REFERENCES repositories(repo_id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('search','trending')),
    source_key TEXT NOT NULL CHECK (source_type = 'search' OR source_key = ''),
    PRIMARY KEY (repo_id,source_type,source_key)
) WITHOUT ROWID;

INSERT INTO repo_discovery_sources (repo_id,source_type,source_key)
SELECT DISTINCT repo_id,'search',source_key FROM search_observations;
INSERT INTO repo_discovery_sources (repo_id,source_type,source_key)
SELECT DISTINCT repo_id,'trending','' FROM trending_observations;

-- +goose StatementBegin
CREATE TRIGGER capture_repo_search_source AFTER INSERT ON search_observations BEGIN
    INSERT INTO repo_discovery_sources (repo_id,source_type,source_key)
    VALUES (NEW.repo_id,'search',NEW.source_key)
    ON CONFLICT(repo_id,source_type,source_key) DO NOTHING;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER capture_repo_trending_source AFTER INSERT ON trending_observations BEGIN
    INSERT INTO repo_discovery_sources (repo_id,source_type,source_key)
    VALUES (NEW.repo_id,'trending','')
    ON CONFLICT(repo_id,source_type,source_key) DO NOTHING;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER capture_repo_trending_source;
DROP TRIGGER capture_repo_search_source;
DROP TABLE repo_discovery_sources;
