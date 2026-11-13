package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

// 验证数字仓库路由、翻页顺序、真实零值、短日数组与次级限流。
func TestStarHistoryPaginationAndLimits(t *testing.T) {
	week := time.Now().AddDate(0, 0, -5).Unix()
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repositories/45/stargazers/history" || r.URL.Query().Get("per_page") != "30" {
			t.Errorf("错误的历史地址: %s", r.URL)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", `<https://api.github.com/repositories/45/stargazers/history?per_page=30&page=2>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"week": week, "days": []any{0, nil, 12}}})
		case "2":
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"week": week - 604800, "days": []int{1, 0, 0, 0, 0, 0, 0}}})
		default:
			w.Header().Set("Retry-After", "15")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	first, err := client.StarHistory(t.Context(), 45, 1, 30, Policy{})
	if err != nil || first.NextPage != 2 || len(first.Weeks) != 1 || len(first.Weeks[0].Days) != 7 ||
		*first.Weeks[0].Days[0] != 0 || first.Weeks[0].Days[1] != nil || first.Weeks[0].Days[6] != nil {
		t.Fatalf("首页数据不符: %+v %v", first, err)
	}
	second, err := client.StarHistory(t.Context(), 45, first.NextPage, 30, Policy{})
	if err != nil || second.NextPage != 0 || second.Weeks[0].Week >= first.Weeks[0].Week {
		t.Fatalf("次页数据不符: %+v %v", second, err)
	}
	_, err = client.StarHistory(t.Context(), 45, 3, 30, Policy{})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != consts.CodeGitHubSecondaryLimit || failure.Until <= time.Now().UnixMilli() {
		t.Fatalf("历史接口须沿用持久退避: %v", err)
	}
}
