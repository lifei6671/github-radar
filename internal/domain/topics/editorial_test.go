package topics

import (
	"testing"
	"time"
)

// 核对五维上限、推荐顺序与人工实测前后的建议区间。
func TestEditorialAssessmentRanksReaderValueAndDemo(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	hot := Signals{RepoID: 1, Hotness: new(99.0), Growth: new(int64(200)), Description: new("底层项目")}
	useful := Signals{RepoID: 2, Hotness: new(15.0), Growth: new(int64(12)), Features: []string{"导入", "转换", "导出"},
		Audience: []string{"视频作者"}, GitHubCreatedAt: new(now.UnixMilli()),
		Brief: Brief{ReaderBenefit: "让非技术用户批量整理字幕", Demo: "拖入两个字幕文件并导出", Steps: []string{"验证导出结果"}}}
	result := Recommend([]Signals{hot, useful}, nil, 2, 200, now)
	if len(result) != 2 || result[0].RepoID != 2 || result[1].RepoID != 1 {
		t.Fatalf("五维评分应让有读者价值的项目排在高热但难演示的项目之前: %+v", result)
	}
	assessment := result[0].Assessment()
	if len(assessment.Dimensions) != 5 || assessment.Dimensions[3].Points != 0 ||
		assessment.Dimensions[3].Confidence != "pending" {
		t.Fatalf("仅有模型建议时不能计实测分: %+v", assessment)
	}
	verified := assessment.WithReview(Review{Verification: "verified", Evidence: "确认批量导出完成"})
	if verified.Dimensions[3].Points != 20 || Suggestion(verified.Total()) != "prepare" ||
		Suggestion(80) != "prepare" || Suggestion(65) != "candidate" || Suggestion(64.99) != "observe" {
		t.Fatalf("评分或建议边界错误: %+v", verified)
	}
}
