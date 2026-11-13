package summary

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/github-radar/internal/storage"
)

// TestTechnicalTaxonomySeparatesMCPRoles 覆盖主用途证据、角色、形态和多能力边界。
func TestTechnicalTaxonomySeparatesMCPRoles(t *testing.T) {
	engine, _, _, _ := fixture(t)
	vocabulary, err := storage.ReadTechnicalTags(engine.Queue.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(vocabulary.Items) != 219 || vocabulary.Domain("vector-graphics") != "media-creation" {
		t.Fatalf("正式能力词表未加载: %d", len(vocabulary.Items))
	}
	catalog := storage.ThemeCatalog{Terms: []storage.ThemeTerm{{Slug: "mcp-servers"}, {Slug: "graphics", Terms: []string{"graphics-editor"}}}}
	for _, sample := range []struct {
		name, raw, topics, themes, tags string
	}{
		{"ZigZag", `{"mcp_role":"integration","primary_technology":"vector-graphics","primary_quote":"Edit SVG artwork.","theme_slugs":["mcp-servers","graphics"],"value_tags":["mcp-server","mcp-client","desktop-app","finance","ai-agent","desktop-framework","vector-graphics","image-processing"],"forms":["desktop-app","finance"]}`,
			`["vector-graphics"]`, `["graphics"]`, `["vector-graphics","image-processing"]`},
		{"GitHub MCP Server", `{"mcp_role":"server","primary_technology":"mcp-server","primary_quote":"provides MCP tools","theme_slugs":["mcp-servers"],"value_tags":["git-workflow","ai-agent","tool-calling"]}`,
			`["mcp-server"]`, `["mcp-servers"]`, `["git-workflow","tool-calling"]`},
		{"Playwright MCP", `{"mcp_role":"server","primary_technology":"mcp-server","primary_quote":"provides MCP tools","theme_slugs":[],"value_tags":["browser-automation"]}`,
			`["mcp-server"]`, `["mcp-servers"]`, `["browser-automation"]`},
		{"Firecrawl MCP", `{"mcp_role":"server","primary_technology":"mcp-server","primary_quote":"provides MCP tools","theme_slugs":[],"value_tags":["web-scraping","data-extraction"]}`,
			`["mcp-server"]`, `["mcp-servers"]`, `["web-scraping","data-extraction"]`},
		{"MCP Inspector", `{"mcp_role":"client","primary_technology":"mcp-client","primary_quote":"connects to MCP servers","theme_slugs":["mcp-servers"],"value_tags":["mcp-server"]}`,
			`["mcp-client"]`, `[]`, `["mcp-client"]`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			sent := source{technicalTags: vocabulary, formsSent: true, description: "Edit SVG artwork. This project provides MCP tools or connects to MCP servers."}
			classified, issue := readTaxonomyResult(sample.raw, sent)
			if issue != "" || classified.topics() != sample.topics || classified.cardTags() != sample.tags {
				t.Fatalf("分类不符: %+v %s", classified, issue)
			}
			themes, valid := resultThemeSlugs(sample.raw, classified.topics(), catalog)
			if !valid || themes != sample.themes {
				t.Fatalf("专题=%s，期望=%s", themes, sample.themes)
			}
			if sample.name == "ZigZag" && (!reflect.DeepEqual(classified.Forms, []string{"desktop-app"}) || !reflect.DeepEqual(classified.Domains, []string{"media-creation"})) {
				t.Fatalf("轴没有正确分离: %+v", classified)
			}
		})
	}
	for _, raw := range []string{
		`{"mcp_role":"integration","primary_technology":"mcp-server","primary_quote":"Edit SVG artwork","value_tags":[]}`,
		`{"mcp_role":"server","primary_technology":"mcp-client","primary_quote":"Edit SVG artwork","value_tags":[]}`,
		`{"mcp_role":"none","primary_technology":"graphics-editor","primary_quote":"Edit SVG artwork","value_tags":[]}`,
		`{"mcp_role":"none","primary_technology":"vector-graphics","primary_quote":"invented wording","value_tags":[]}`,
	} {
		if _, issue := readTaxonomyResult(raw, source{technicalTags: vocabulary, body: "Edit SVG artwork"}); issue == "" {
			t.Fatalf("接受了无效分类: %s", raw)
		}
	}
	// 简介明确自称服务端时可以核对原文；客户端说明中提及 MCP 服务端不会触发服务端分类。
	for _, check := range []struct{ description, role, primary, want string }{
		{"Official Example MCP server, a search tool", "server", "web-scraping", "mcp-server"},
		{"Deterministic testing tools for MCP servers", "client", "mcp-client", "mcp-client"},
	} {
		raw := `{"mcp_role":"` + check.role + `","primary_technology":"` + check.primary + `","value_tags":[]}`
		got, issue := readTaxonomyResult(raw, source{technicalTags: vocabulary, description: check.description, fullName: "example/example-mcp-server"})
		if issue != "" || got.Primary != check.want || !strings.Contains(check.description, got.PrimaryQuote) || got.PrimaryQuote == "" {
			t.Fatalf("简介证据验收失败: %+v %s", got, issue)
		}
	}
	for _, about := range []string{"Not an MCP server", "Monitor your MCP server", "Client for MCP server", "Desktop editor with MCP server", "支持 MCP server", "并非 MCP server"} {
		if declaresMCPServer(about, "example/example-mcp-server") {
			t.Fatalf("不能把附带功能或否定用语当作服务端自称: %s", about)
		}
	}
	input := source{fullName: "example/editor", language: "en", description: "Vector graphics and SVG editor", technicalTags: vocabulary}
	prompt, sent := input.prompt(2048, false, true, false)
	if len(prompt)+len(systemPrompt)+128 > 2048 || sent.description == "" || !sent.technicalTags.Has("vector-graphics") || sent.technicalTags.Has("game-engine") {
		t.Fatalf("小输入预算的证据或词表边界无效: %s", prompt)
	}
	full, sent := input.prompt(131072, true, true, true)
	if len(sent.technicalTags.Items) != 219 || !strings.Contains(full, "产品形态") {
		t.Fatal("默认预算必须包含完整词表")
	}
}
