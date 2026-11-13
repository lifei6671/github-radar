package reports

import (
	"strings"
	"testing"
	"time"
)

func TestEditorialRequiresContentTitlesAndKnownCitations(t *testing.T) {
	evidence := []Evidence{{ID: 1, Project: "owner/skills"}, {ID: 2, Project: "owner/workflows"}}
	valid := `{"headline":"把自动化经验变成可复用技能","lead":"两个项目分别处理技能编排和本地验证，适合需要反复执行同一套任务的团队。","stories":[{"title":"复用脚本与本地验证","body":"两个项目覆盖任务执行和验证步骤，可用于减少反复手工操作。","repoIds":[1,2]}]}`
	if result, ids, ok := ParseEditorial(valid, evidence); !ok || len(ids) != 2 || len(result.Stories) != 1 {
		t.Fatalf("无法读取有出处的报刊稿件: %+v %v", result, ids)
	}
	for _, invalid := range []string{
		strings.Replace(valid, "把自动化经验变成可复用技能", "owner/skills", 1),
		strings.Replace(valid, "[1,2]", "[1,3]", 1),
		strings.Replace(valid, "[1,2]", "[1,1]", 1),
		strings.Replace(valid, "[1,2]", "[]", 1),
	} {
		if _, _, ok := ParseEditorial(invalid, evidence); ok {
			t.Fatalf("错误稿件不应通过: %s", invalid)
		}
	}
}

func TestPreviewPromptAndEditorialRejectTrendExtrapolation(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 19, 0, 0, time.UTC)
	prompt, evidence := EditorPrompt(Plan{Kind: Weekly, Key: "2026-W46", Status: Preview},
		[]Evidence{{ID: 1, Project: "owner/tools", Guide: "说明工具如何在本地执行任务。", Date: "2026-11-12"}},
		1, at, at.Add(time.Hour), 2048)
	if len(evidence) != 1 || !strings.Contains(prompt, "覆盖 1 个观测日") ||
		!strings.Contains(prompt, "本次选材涉及 1 个观测日") ||
		!strings.Contains(prompt, "2026-11-12 11:19") || !strings.Contains(prompt, "禁止整期概括") ||
		strings.Contains(prompt, "2026-W46") || len(prompt)+len(EditorInstructions)+128 > 2048 {
		t.Fatalf("预览必须只提供实际观测范围: %s", prompt)
	}
	neutral := Editorial{Headline: "本地工具如何保护数据", Lead: "项目可离线分析统计趋势，支持本地配置。",
		Stories: []Story{{Title: "音频留在本机", Body: "任务正在从本地文件读取配置，项目提供本地模型选项。"}}}
	if !neutral.SafePreview() {
		t.Fatal("有证据的中性文案应保留")
	}
	for _, claim := range []string{"AI 正从云端转向本地", "这是本周的主要趋势", "技能项目越来越多", "整个趋势正在发生", "项目迎来新一波热潮", "开源社区的注意力已发生变化"} {
		unsafe := neutral
		unsafe.Headline = claim
		if unsafe.SafePreview() {
			t.Fatalf("预览不应发布推断: %q", claim)
		}
	}
	neutral.Stories[0].Body = "这月项目正在转向本地部署。"
	if neutral.SafePreview() {
		t.Fatal("趋势校验也应覆盖选题正文")
	}
}
