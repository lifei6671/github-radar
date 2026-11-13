-- +goose Up
ALTER TABLE discovery_rules ADD COLUMN min_stars INTEGER NOT NULL DEFAULT 0
    CHECK (min_stars >= 0 AND (source_type = 'search' OR min_stars = 0));
UPDATE discovery_rules SET min_stars = 200 WHERE source_type = 'search';
-- 查询范围变化后旧分页偏移已失效，按新规则从第一页开始。
UPDATE discovery_rules
SET next_run_at = min(next_run_at, unixepoch() * 1000)
WHERE source_type = 'search' AND enabled = 1 AND id IN (
    SELECT CAST(json_extract(payload_json, '$.ruleId') AS INTEGER) FROM jobs
    WHERE type = 'discover_search' AND status IN ('pending', 'running', 'failed')
);
UPDATE jobs
SET status = 'canceled', lease_until = NULL, lease_token = NULL,
    last_error_code = 'source_rule_changed', updated_at = unixepoch() * 1000
WHERE type = 'discover_search' AND status IN ('pending', 'running', 'failed');

-- +goose Down
ALTER TABLE discovery_rules DROP COLUMN min_stars;
