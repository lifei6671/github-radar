-- +goose Up
ALTER TABLE daily_reports ADD COLUMN issue_number INTEGER NOT NULL DEFAULT 0;
WITH numbered AS (
    SELECT issue_date, row_number() OVER (ORDER BY generated_at,issue_date) AS issue_number FROM daily_reports
)
UPDATE daily_reports SET issue_number=(SELECT numbered.issue_number FROM numbered WHERE numbered.issue_date=daily_reports.issue_date);
CREATE UNIQUE INDEX idx_daily_reports_issue_number ON daily_reports(issue_number);

-- +goose Down
DROP INDEX idx_daily_reports_issue_number;
ALTER TABLE daily_reports DROP COLUMN issue_number;
