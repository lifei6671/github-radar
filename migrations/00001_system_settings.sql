-- +goose Up
-- 预留安装状态与系统设置，初始化时不写入业务数据或 R2 凭据。
CREATE TABLE system_settings (
    key TEXT PRIMARY KEY NOT NULL,
    value_json TEXT NOT NULL CHECK (json_valid(value_json)),
    updated_at INTEGER NOT NULL
);
