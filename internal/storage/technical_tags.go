package storage

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
)

const TechnicalTagsKey = "ai.technical_tags.v2"
const legacyTechnicalTagsKey = "ai.technical_tags.v1"

//go:embed technical_tags.json
var legacyBuiltinTechnicalTags []byte

var technicalTagSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// TechnicalTag 用稳定标识保存模型选择，名称和别名可独立维护。
type TechnicalTag struct {
	Slug       string   `json:"slug" yaml:"id"`
	Name       string   `json:"name" yaml:"name"`
	Aliases    []string `json:"aliases" yaml:"aliases"`
	Domain     string   `json:"domain" yaml:"domain"`
	Definition string   `json:"definition" yaml:"definition"`
}

// TechnicalTags 同时支持按标识读取展示名和按模型输出查找规范标识。
type TechnicalTags struct {
	Items  []TechnicalTag
	bySlug map[string]string
	lookup map[string]string
	domain map[string]string
}

func tagKey(value string) string { return strings.ToLower(strings.Join(strings.Fields(value), " ")) }

// NewTechnicalTags 拒绝歧义别名和不适合作为模型词表的多行内容。
func NewTechnicalTags(items []TechnicalTag) (TechnicalTags, error) {
	doc, err := loadTaxonomy()
	if err != nil {
		return TechnicalTags{}, err
	}
	return newTechnicalTags(items, doc.Domains)
}

func newTechnicalTags(items []TechnicalTag, domains []TaxonomyTerm) (TechnicalTags, error) {
	if items == nil || len(items) > 512 {
		return TechnicalTags{}, fmt.Errorf("技术标签须为数组，最多 512 项")
	}
	c := TechnicalTags{Items: make([]TechnicalTag, 0, len(items)), bySlug: make(map[string]string), lookup: make(map[string]string), domain: make(map[string]string)}
	clean := func(value string) (string, bool) {
		if strings.ContainsFunc(value, unicode.IsControl) {
			return "", false
		}
		value = strings.Join(strings.Fields(value), " ")
		return value, value != "" && utf8.RuneCountInString(value) <= 48
	}
	for _, item := range items {
		slug := strings.ToLower(strings.TrimSpace(item.Slug))
		name, ok := clean(item.Name)
		if !ok || len(slug) > 48 || !technicalTagSlug.MatchString(slug) || len(item.Aliases) > 12 {
			return TechnicalTags{}, fmt.Errorf("技术标签 %q 的标识、名称或别名数量无效", slug)
		}
		item.Definition = strings.TrimSpace(item.Definition)
		if (item.Domain != "" && !hasTerm(domains, item.Domain)) || utf8.RuneCountInString(item.Definition) > 140 || strings.ContainsFunc(item.Definition, unicode.IsControl) {
			return TechnicalTags{}, fmt.Errorf("技术标签 %q 的领域或定义无效", slug)
		}
		if _, duplicate := c.bySlug[slug]; duplicate {
			return TechnicalTags{}, fmt.Errorf("技术标签标识 %q 重复", slug)
		}
		aliases := make([]string, 0, len(item.Aliases))
		seen := map[string]bool{slug: true, tagKey(name): true}
		for _, value := range item.Aliases {
			alias, ok := clean(value)
			if !ok {
				return TechnicalTags{}, fmt.Errorf("技术标签 %q 有无效别名", slug)
			}
			if key := tagKey(alias); !seen[key] {
				seen[key] = true
				aliases = append(aliases, alias)
			}
		}
		for key := range seen {
			if previous, duplicate := c.lookup[key]; duplicate {
				return TechnicalTags{}, fmt.Errorf("技术标签词 %q 同时属于 %q 和 %q", key, previous, slug)
			}
			c.lookup[key] = slug
		}
		c.bySlug[slug] = name
		c.domain[slug] = item.Domain
		c.Items = append(c.Items, TechnicalTag{Slug: slug, Name: name, Aliases: aliases, Domain: item.Domain, Definition: item.Definition})
	}
	return c, nil
}

// ReadTechnicalTags 首次使用内置词表；管理员保存后以数据库配置为准，空词表同样有效。
func ReadTechnicalTags(db *gorm.DB) (TechnicalTags, error) {
	var row SystemSetting
	err := db.Find(&row, "key = ?", TechnicalTagsKey).Error
	if err != nil {
		return TechnicalTags{}, err
	}
	var items []TechnicalTag
	if row.Key != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), &items); err != nil {
			return TechnicalTags{}, fmt.Errorf("解析技术标签词表: %w", err)
		}
		return NewTechnicalTags(items)
	}
	if err := db.Find(&row, "key = ?", legacyTechnicalTagsKey).Error; err != nil {
		return TechnicalTags{}, err
	}
	if row.Key != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), &items); err != nil {
			return TechnicalTags{}, fmt.Errorf("解析旧技术标签词表: %w", err)
		}
		return upgradeLegacyTags(items)
	}
	builtin, err := builtinCapabilities()
	return cloneTechnicalTags(builtin), err
}

// Resolve 将标识、名称或别名统一为标识。未知词返回空串。
func (c TechnicalTags) Resolve(value string) string { return c.lookup[tagKey(value)] }

// Has 供模型结果校验，只接收实际发出的规范 ID。
func (c TechnicalTags) Has(id string) bool { return c.bySlug[id] != "" }

// Domain 从已通过校验的能力派生领域。
func (c TechnicalTags) Domain(id string) string { return c.domain[id] }

// Labels 只展示当前词表里的标识，删除和改名无需更新历史仓库。
func (c TechnicalTags) Labels(raw string) []string {
	var slugs []string
	_ = json.Unmarshal([]byte(raw), &slugs)
	labels := make([]string, 0, min(3, len(slugs)))
	for _, slug := range slugs {
		if name := c.StoredLabel(slug); name != "" && !slices.Contains(labels, name) {
			labels = append(labels, name)
			if len(labels) == 3 {
				break
			}
		}
	}
	return labels
}

// StoredLabel 支持历史标识；未找到严格等义的新词时只显示原名，不发给模型。
func (c TechnicalTags) StoredLabel(id string) string {
	if name := c.bySlug[id]; name != "" {
		return name
	}
	if id == "mcp" || id == "model-context-protocol" {
		return c.bySlug["mcp-server"]
	}
	if canonical := legacyCapabilityIDs[id]; canonical != "" {
		return c.bySlug[canonical]
	}
	legacy, err := oldTechnicalTags()
	if err != nil || isBuiltinCapability(id) {
		return ""
	}
	return legacy.bySlug[id]
}

// FilterIDs 返回可参与公开筛选的现行及历史标识，与 StoredLabel 使用同一兼容规则。
func (c TechnicalTags) FilterIDs(value string) []string {
	if id := c.Resolve(value); id != "" {
		return CompatibleCapabilityIDs(id)
	}
	if id := legacyCapabilityIDs[tagKey(value)]; id != "" && c.Has(id) {
		return CompatibleCapabilityIDs(id)
	}
	legacy, err := oldTechnicalTags()
	if err == nil {
		if id := legacy.Resolve(value); id != "" && c.StoredLabel(id) != "" {
			return CompatibleCapabilityIDs(id)
		}
	}
	return nil
}

// IsMCPTag 防止另取别名后把附带的 MCP 接入写成主要技术或卡片标签。
func (c TechnicalTags) IsMCPTag(slug string) bool {
	return strings.Contains(slug, "mcp") || slug == "model-context-protocol" ||
		strings.Contains(strings.ToLower(c.bySlug[slug]), "mcp") ||
		(slug != "" && (slug == c.Resolve("mcp") || slug == c.Resolve("model-context-protocol")))
}
