-- +goose Up
-- 屏蔽由管理员维护，与 GitHub 返回的禁用状态分开。
ALTER TABLE repositories ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0 CHECK(blocked IN (0,1));

-- +goose Down
ALTER TABLE repositories DROP COLUMN blocked;
