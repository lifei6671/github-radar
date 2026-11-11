package api

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/lifei6671/github-radar/internal/storage"
)

// 用设计容量验证列表只返回当前页，游标在相同排序值的仓库间稳定推进。
func TestListFiveThousandRepositoriesByCursor(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	rows := make([]storage.Repository, 5000)
	for i := range rows {
		name := fmt.Sprintf("repo-%04d", i+1)
		rows[i] = storage.Repository{RepoID: int64(i + 1), Owner: "fixture", Name: name,
			FullName: "fixture/" + name, NameIsCurrent: true, TopicsJSON: "[]", State: "active", Grade: "C", Stars: 10}
	}
	if err := db.CreateInBatches(&rows, 150).Error; err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/repos?sort=stars&limit=30"
	first := request(t, router, "GET", path, "", "admin", 200)
	second := request(t, router, "GET", path+"&cursor="+url.QueryEscape(first["nextCursor"].(string)), "", "admin", 200)
	for page, body := range []map[string]any{first, second} {
		items := body["items"].([]any)
		if len(items) != 30 || body["nextCursor"] == nil {
			t.Fatalf("第 %d 页返回数量或游标有误", page+1)
		}
		for i, entry := range items {
			if entry.(map[string]any)["id"] != float64(page*30+i+1) {
				t.Fatalf("第 %d 页出现重复或漏项", page+1)
			}
		}
	}
}
