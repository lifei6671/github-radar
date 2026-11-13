-- +goose Up
-- 独立账本不随旧任务清理或仓库删除，恢复时按请求键合并。
CREATE TABLE ai_budget_reservations (
    reservation_key TEXT PRIMARY KEY,
    logical_key TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL CHECK (origin IN ('auto','manual')),
    lane TEXT NOT NULL,
    operation TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    reserved_at INTEGER NOT NULL,
    sending_at INTEGER,
    settled_at INTEGER,
    status TEXT NOT NULL CHECK (status IN ('reserved','sending','succeeded','failed','unknown','unused')),
    input_tokens INTEGER,
    output_tokens INTEGER,
    estimated_cost REAL,
    held_cost REAL NOT NULL DEFAULT 0,
    duration_ms INTEGER
);
CREATE INDEX idx_ai_budget_day ON ai_budget_reservations(sending_at, origin, lane);
CREATE INDEX idx_ai_budget_logical ON ai_budget_reservations(logical_key, status);

-- 旧版只保存预留日，发送状态用更新时间补齐跨日；已确认响应以耗时估计发送点。
INSERT INTO ai_budget_reservations (
    reservation_key, origin, lane, operation, provider, reserved_at, sending_at, settled_at,
    status, input_tokens, output_tokens, estimated_cost, duration_ms
)
SELECT a.call_key, a.origin, CASE WHEN a.origin = 'manual' THEN 'manual' ELSE 'maintenance' END,
    CASE j.type WHEN 'refresh_readme_summary' THEN 'summary' WHEN 'translate_readme' THEN 'translation'
        WHEN 'compose_report' THEN 'report' ELSE j.type END,
    a.provider, a.reserved_at,
    CASE WHEN a.status = 'reserved' OR a.error_code IN ('reservation_unused','reservation_expired','lease_expired') THEN NULL
         WHEN a.status = 'sending' THEN a.updated_at
         WHEN a.duration_ms IS NOT NULL AND a.updated_at - a.duration_ms > a.reserved_at THEN a.updated_at - a.duration_ms
         ELSE a.reserved_at END,
    CASE WHEN a.status IN ('reserved','sending') THEN NULL ELSE a.updated_at END,
    CASE WHEN a.error_code IN ('reservation_unused','reservation_expired','lease_expired') THEN 'unused' ELSE a.status END,
    a.input_tokens, a.output_tokens, a.estimated_cost, a.duration_ms
FROM ai_calls a JOIN jobs j ON j.id = a.job_id;

-- +goose Down
DROP TABLE ai_budget_reservations;
