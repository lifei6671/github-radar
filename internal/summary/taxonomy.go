package summary

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lifei6671/github-radar/internal/storage"
)

var identityWords = regexp.MustCompile(`[\p{L}\p{N}]+`)

// declaresMCPServer 只接受简介的自称：泛称服务端，或仓库/所有者品牌加服务端。
func declaresMCPServer(identity, fullName string) bool {
	words := identityWords.FindAllString(strings.ToLower(identity), -1)
	if len(words) < 2 || words[len(words)-2] != "mcp" || words[len(words)-1] != "server" {
		return false
	}
	owner, repo, _ := strings.Cut(strings.ToLower(fullName), "/")
	brand := func(parts []string) string {
		return strings.Join(slices.DeleteFunc(parts, func(w string) bool {
			return w == "official" || w == "mcp" || w == "server" || w == "s"
		}), "")
	}
	prefix := brand(words[:len(words)-2])
	return prefix == "" || prefix == brand(identityWords.FindAllString(owner, -1)) || prefix == brand(identityWords.FindAllString(repo, -1))
}

// taxonomyResult 保存结构化分类及可核对的主用途证据，旧分类消费者仍只读取单个主能力。
type taxonomyResult struct {
	Version      string   `json:"taxonomyVersion"`
	Primary      string   `json:"primaryCapability"`
	Capabilities []string `json:"capabilities"`
	Domains      []string `json:"domains"`
	Forms        []string `json:"forms"`
	MCPRole      string   `json:"mcpRole"`
	PrimaryQuote string   `json:"primaryQuote,omitempty"`
	NeedsReview  bool     `json:"needsReview"`
}

// verifiedPrimaryQuote 只承认本轮实际送给模型的简介或 README 中的连续短引文。
func verifiedPrimaryQuote(quote string, sent source) string {
	quote = strings.Join(strings.Fields(strings.Trim(quote, "\"'“”` ")), " ")
	if n := utf8.RuneCountInString(quote); n < 6 || n > 140 {
		return ""
	}
	for _, text := range []string{sent.description, sent.body} {
		if strings.Contains(strings.Join(strings.Fields(text), " "), quote) {
			return quote
		}
	}
	// 模型偶尔在逐字摘录后补句号；只保存去除该标点后确实存在的原文。
	if trimmed := strings.TrimRight(quote, ".。！!？?"); trimmed != quote && utf8.RuneCountInString(trimmed) >= 6 {
		for _, text := range []string{sent.description, sent.body} {
			if strings.Contains(strings.Join(strings.Fields(text), " "), trimmed) {
				return trimmed
			}
		}
	}
	// 模型可能改变大小写。仅在找到同一连续片段时返回输入中的原始拼写。
	for _, text := range []string{sent.description, sent.body} {
		original := []rune(strings.Join(strings.Fields(text), " "))
		folded := []rune(strings.Map(unicode.ToLower, string(original)))
		needle := []rune(strings.Map(unicode.ToLower, strings.TrimRight(quote, ".。！!？?")))
		if len(needle) < 6 {
			continue
		}
		for i := 0; i+len(needle) <= len(folded); i++ {
			if slices.Equal(folded[i:i+len(needle)], needle) {
				return string(original[i : i+len(needle)])
			}
		}
	}
	return ""
}

// readTaxonomyResult 将主用途、卡片能力和产品形态分开核验。模型不能决定技术领域或行业分类。
func readTaxonomyResult(raw string, sent source) (taxonomyResult, string) {
	var output struct {
		Primary     json.RawMessage `json:"primary_technology"`
		Role        string          `json:"mcp_role"`
		Tags        []string        `json:"value_tags"`
		Forms       []string        `json:"forms"`
		Quote       string          `json:"primary_quote"`
		NeedsReview bool            `json:"needs_review"`
	}
	version, _, forms, err := storage.TaxonomyMetadata()
	if err != nil || json.Unmarshal([]byte(raw), &output) != nil {
		return taxonomyResult{}, "taxonomy_invalid_json"
	}
	if len(output.Primary) == 0 {
		return taxonomyResult{}, "taxonomy_missing_fields"
	}
	var primary string
	if json.Unmarshal(output.Primary, &primary) != nil {
		return taxonomyResult{}, "taxonomy_invalid_primary"
	}
	result := taxonomyResult{Version: version, MCPRole: output.Role, NeedsReview: output.NeedsReview,
		Capabilities: []string{}, Domains: []string{}, Forms: []string{}}
	catalog := sent.technicalTags
	if primary != "" && !catalog.Has(primary) {
		return taxonomyResult{}, "taxonomy_unknown_primary"
	}
	quote := verifiedPrimaryQuote(output.Quote, sent)
	// GitHub 简介明确将项目称为 MCP 服务端时，它可作为直接、可复查的角色证据。
	description := strings.TrimSpace(sent.description)
	about := strings.ToLower(description)
	// 只识别简介首段的自称，不将 “client for MCP servers” 等描述当成服务端。
	identity := description
	if i := strings.IndexAny(identity, ":,：，"); i >= 0 {
		identity = identity[:i]
	}
	serverAbout := declaresMCPServer(identity, sent.fullName)
	if serverAbout && catalog.Has("mcp-server") && (output.Role == "server" || output.Role == "none" || output.Role == "integration") && primary != "mcp-client" {
		output.Role, result.MCPRole = "server", "server"
		primary = "mcp-server"
		// 明确的首段声明比模型回传的任意摘录更直接。
		quote = verifiedPrimaryQuote(identity, sent)
	}
	switch output.Role {
	case "server":
		if !catalog.Has("mcp-server") || (primary != "mcp-server" && (!serverAbout || primary == "mcp-client")) {
			return taxonomyResult{}, "taxonomy_server_primary_mismatch"
		}
		primary = "mcp-server"
		if quote == "" && serverAbout {
			quote = verifiedPrimaryQuote(strings.TrimSpace(limitText(sent.description, 140)), sent)
		}
	case "client":
		if primary != "mcp-client" {
			return taxonomyResult{}, "taxonomy_client_primary_mismatch"
		}
		if quote == "" && strings.Contains(about, "mcp server") &&
			(strings.Contains(about, "testing") || strings.Contains(about, "inspect") || strings.Contains(about, "mcp client")) {
			quote = verifiedPrimaryQuote(strings.TrimSpace(limitText(sent.description, 140)), sent)
		}
	case "integration", "none":
		if catalog.IsMCPTag(primary) {
			return taxonomyResult{}, "taxonomy_mcp_role_mismatch"
		}
	default:
		return taxonomyResult{}, "taxonomy_invalid_role"
	}
	result.Primary = primary
	if primary != "" {
		result.PrimaryQuote = quote
		if result.PrimaryQuote == "" {
			return taxonomyResult{}, "taxonomy_unverified_quote"
		}
	}
	if primary == "" {
		result.NeedsReview = true
	} else {
		result.Capabilities = append(result.Capabilities, primary)
	}
	for _, id := range output.Tags {
		if !catalog.Has(id) || catalog.IsMCPTag(id) || slices.Contains(result.Capabilities, id) {
			continue
		}
		// MCP 接入、调用或被调用本身都不提供智能体能力。
		if output.Role != "none" && (id == "ai-agent" || id == "coding-agent" || id == "browser-agent") &&
			primary != "ai-agent" && primary != "coding-agent" && primary != "browser-agent" {
			continue
		}
		if id == "desktop-framework" && primary != id && slices.Contains(output.Forms, "desktop-app") {
			continue
		}
		result.Capabilities = append(result.Capabilities, id)
		if len(result.Capabilities) == 5 {
			break
		}
	}
	for _, id := range result.Capabilities {
		if domain := catalog.Domain(id); domain != "" && !slices.Contains(result.Domains, domain) {
			result.Domains = append(result.Domains, domain)
			if len(result.Domains) == 2 {
				break
			}
		}
	}
	for _, id := range output.Forms {
		if sent.formsSent && slices.ContainsFunc(forms, func(term storage.TaxonomyTerm) bool { return term.ID == id }) && !slices.Contains(result.Forms, id) {
			result.Forms = append(result.Forms, id)
			if len(result.Forms) == 2 {
				break
			}
		}
	}
	return result, ""
}

func (r taxonomyResult) topics() string {
	if r.Primary == "" {
		return "[]"
	}
	data, _ := json.Marshal([]string{r.Primary})
	return string(data)
}

func (r taxonomyResult) cardTags() string {
	ids := r.Capabilities
	// 服务端主能力已作为分类展示，卡片补充它对外提供的具体能力。
	if r.MCPRole == "server" && len(ids) > 0 {
		ids = ids[1:]
	}
	data, _ := json.Marshal(ids[:min(3, len(ids))])
	return string(data)
}
