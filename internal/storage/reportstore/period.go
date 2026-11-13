package reportstore

import (
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/reports"
)

// periodProjects 按采集时间选取周期证据，同一仓库保留其最佳上榜记录或最新发现。
func periodProjects(tx *gorm.DB, kind reports.Kind, key string, now time.Time) ([]PublicItem, error) {
	from, to, _ := kind.Bounds(key)
	size := 10
	if kind == reports.Monthly {
		size = 15
	}
	candidates := publicItems(tx).Where("daily_report_items.observed_at >= ? AND daily_report_items.observed_at < ? AND daily_report_items.observed_at <= ?",
		from.UnixMilli(), to.UnixMilli(), now.UnixMilli()).Select(`daily_report_items.*, repositories.full_name, repositories.html_url,
		row_number() OVER (PARTITION BY daily_report_items.repo_id
			ORDER BY rank NULLS LAST, observed_at DESC, issue_date DESC, position) AS occurrence`)
	ranked := tx.Table("(?) AS chosen", candidates).Where("occurrence=1").Select(`chosen.*,
		row_number() OVER (PARTITION BY section ORDER BY rank NULLS LAST, observed_at DESC, repo_id) AS section_rank`)
	var items []PublicItem
	err := tx.Table("(?) AS ranked", ranked).Where("section_rank<=?", size).
		Select("issue_date, position, repo_id, section, summary_zh, title_zh, observed_at, rank, full_name, html_url").
		Order("rank NULLS LAST, observed_at DESC, repo_id").Scan(&items).Error
	return items, err
}
