package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
)

// StarHistoryPage 只包含 GitHub 的日创建数，和累计 Star 快照无关。
type StarHistoryPage struct {
	Weeks    []metrics.StarHistoryWeekData
	NextPage int
}

// StarHistory 使用 GitHub 分页链接公开使用的数字 ID 路径，避免同名复用和仓库转移造成串库。
func (c *Client) StarHistory(ctx context.Context, id int64, page, size int, p Policy) (StarHistoryPage, error) {
	if id <= 0 || page < 1 || page > metrics.StarHistoryMaxPages || size < 1 || size > metrics.StarHistoryPageSize {
		return StarHistoryPage{}, &Failure{Code: consts.CodeInvalidRange}
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(size)}}
	var rows []struct {
		Week int64    `json:"week"`
		Days []*int64 `json:"days"`
	}
	p.Resource, p.RepositoryID, p.StarHistory = Core, id, true
	now := time.Now().UnixMilli()
	response, err := c.get(ctx, fmt.Sprintf("repositories/%d/stargazers/history?%s", id, query.Encode()), "", p, &rows)
	if err != nil {
		return StarHistoryPage{}, err
	}
	invalid := &Failure{Code: consts.CodeGitHubInvalidResponse, Status: response.StatusCode}
	if rows == nil || len(rows) > size || response.NextPage != 0 && (response.NextPage != page+1 || len(rows) == 0) {
		return StarHistoryPage{}, invalid
	}
	// 记录请求前的时间。响应跨日时保守标记当天未完成，且不依赖未保证的服务端时区。
	result := StarHistoryPage{NextPage: response.NextPage, Weeks: make([]metrics.StarHistoryWeekData, 0, len(rows))}
	for i, row := range rows {
		if row.Week <= 0 || row.Week > now/1000 || len(row.Days) > 7 || i > 0 && (rows[i-1].Week-row.Week < 6*metrics.Day/1000 || rows[i-1].Week-row.Week > 8*metrics.Day/1000) {
			return StarHistoryPage{}, invalid
		}
		for _, count := range row.Days {
			if count != nil && *count < 0 {
				return StarHistoryPage{}, invalid
			}
		}
		days := make([]*int64, 7)
		copy(days, row.Days)
		result.Weeks = append(result.Weeks, metrics.StarHistoryWeekData{Week: row.Week * 1000, Days: days, FetchedAt: now})
	}
	return result, nil
}
