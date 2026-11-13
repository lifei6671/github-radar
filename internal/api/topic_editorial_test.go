package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/storage"
)

// 覆盖旧分保存、带风险的人工复核以及仅后台可见的证据。
func TestEditorialTopicReviewRequiresEvidence(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	repo := storage.Repository{RepoID: 700, Owner: "owner", Name: "editorial", FullName: "owner/editorial",
		NameIsCurrent: true, Grade: "B", State: "active", TopicsJSON: "[]", HTMLURL: "https://github.com/owner/editorial", DiscoveredAt: now}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	summary := storage.AISummary{RepoID: repo.RepoID, SourceQuality: "sufficient", SourceLanguage: "en", SourceFingerprint: "readme:one",
		FeaturesJSON: `["执行命令"]`, AudienceJSON: `["开发者"]`, ValueTagsJSON: "[]", SummaryStatus: "ready",
		EditorialBriefJSON: `{"demo":"导出文件","steps":["检查输出"],"risks":["code_execution"]}`}
	if err := db.Create(&summary).Error; err != nil {
		t.Fatal(err)
	}
	legacy := storage.TopicCandidate{RepoID: repo.RepoID, AngleKey: "project", Status: "candidate", TopicScore: 89,
		TitleZh: "已有旧版选题", SourceURL: repo.HTMLURL, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/topics/%d", legacy.ID)
	request(t, router, http.MethodPatch, path, `{"status":"accepted"}`, "admin", 400)
	scored := request(t, router, http.MethodPatch, path, `{"rescore":true}`, "admin", 200)
	if scored["scoreVersion"] != float64(2) || scored["status"] != "candidate" {
		t.Fatalf("旧版需可重新评分: %+v", scored)
	}
	request(t, router, http.MethodPatch, path, `{"status":"accepted","verification":"verified"}`, "admin", 400)
	revision := scored["assessment"].(map[string]any)["revision"].(string)
	reviewed := request(t, router, http.MethodPatch, path, fmt.Sprintf(
		`{"status":"accepted","verification":"verified","verificationEvidence":"隔离环境中复现成功","overrideReason":"符合读者需求","assessmentRevision":%q,"expectedUpdatedAt":%q}`, revision, scored["updatedAt"]), "admin", 200)
	if reviewed["status"] != "accepted" || reviewed["review"].(map[string]any)["override"] != "符合读者需求" ||
		reviewed["topicScore"].(float64) <= scored["topicScore"].(float64) {
		t.Fatalf("实测和人工决定没有生效: %+v", reviewed)
	}
	request(t, router, http.MethodPatch, path, fmt.Sprintf(`{"status":"ignored","expectedUpdatedAt":%q}`, scored["updatedAt"]), "admin", 409)
	request(t, router, http.MethodGet, path+"/evidence", "", "", 401)
	history := request(t, router, http.MethodGet, path+"/evidence", "", "admin", 200)["history"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["topicScore"] != float64(89) {
		t.Fatalf("旧分丢失: %+v", history)
	}
	request(t, router, http.MethodPatch, path, `{"verification":"pending"}`, "admin", 400)
	if err := db.Model(&summary).Update("summary_status", "stale").Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPatch, path, `{"status":"published"}`, "admin", 400)
	if err := db.Model(&summary).Update("summary_status", "ready").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&summary).Update("source_fingerprint", "readme:two").Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPatch, path, fmt.Sprintf(
		`{"verification":"verified","verificationEvidence":"对旧版本再次实测","assessmentRevision":%q,"expectedUpdatedAt":%q}`, revision, reviewed["updatedAt"]), "admin", 400)
	request(t, router, http.MethodPatch, path, `{"status":"published"}`, "admin", 400)
	request(t, router, http.MethodPatch, path, `{"status":"shortlisted"}`, "admin", 200)
	rescored := request(t, router, http.MethodPatch, path, `{"rescore":true}`, "admin", 200)
	revision = rescored["assessment"].(map[string]any)["revision"].(string)
	request(t, router, http.MethodPatch, path, fmt.Sprintf(
		`{"status":"published","verification":"verified","verificationEvidence":"对新版本重新复现成功","assessmentRevision":%q,"expectedUpdatedAt":%q}`, revision, rescored["updatedAt"]), "admin", 200)
	request(t, router, http.MethodPatch, path, `{"status":"candidate"}`, "admin", 200)
	if err := db.Model(&summary).Update("summary_status", "stale").Error; err != nil {
		t.Fatal(err)
	}
	check := storage.SummaryCheck{RepoID: repo.RepoID, SourceFingerprint: "readme:three"}
	if err := db.Create(&check).Error; err != nil {
		t.Fatal(err)
	}
	stale := request(t, router, http.MethodPatch, path, `{"rescore":true}`, "admin", 200)
	staleRevision := stale["assessment"].(map[string]any)["revision"].(string)
	if err := db.Model(&check).Update("source_fingerprint", "readme:four").Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPatch, path, fmt.Sprintf(
		`{"status":"published","verification":"verified","verificationEvidence":"旧资料已复现","assessmentRevision":%q,"expectedUpdatedAt":%q}`, staleRevision, stale["updatedAt"]), "admin", 400)
	// README 失效先于新指纹落盘时，旧的就绪摘要也不再是同一份核验资料。
	if err := db.Model(&summary).Updates(map[string]any{"summary_status": "ready", "editorial_brief_json": "", "source_fingerprint": "readme:four"}).Error; err != nil {
		t.Fatal(err)
	}
	current := request(t, router, http.MethodPatch, path, `{"rescore":true}`, "admin", 200)
	currentRevision := current["assessment"].(map[string]any)["revision"].(string)
	request(t, router, http.MethodPatch, path, fmt.Sprintf(
		`{"verification":"verified","verificationEvidence":"当前就绪资料已复现","assessmentRevision":%q,"expectedUpdatedAt":%q}`, currentRevision, current["updatedAt"]), "admin", 200)
	if err := db.Model(&summary).Update("summary_status", "stale").Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPatch, path, `{"status":"published"}`, "admin", 400)
}
