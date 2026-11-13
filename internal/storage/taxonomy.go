package storage

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/lifei6671/github-radar/docs"
)

// TaxonomyTerm 表示不直接进入项目分类映射的受控词项。
type TaxonomyTerm struct {
	ID         string `yaml:"id" json:"id"`
	Name       string `yaml:"name" json:"name"`
	Definition string `yaml:"definition" json:"definition"`
}

type taxonomyDocument struct {
	Version      string         `yaml:"version"`
	Domains      []TaxonomyTerm `yaml:"domains"`
	Capabilities []TechnicalTag `yaml:"capabilities"`
	Forms        []TaxonomyTerm `yaml:"forms"`
	Industries   []TaxonomyTerm `yaml:"industries"`
	Traits       []TaxonomyTerm `yaml:"traits"`
}

// 正式词表直接从 YAML 嵌入，不生成第二份容易漂移的配置文件。
var loadTaxonomy = sync.OnceValues(func() (taxonomyDocument, error) {
	var doc taxonomyDocument
	if err := yaml.Unmarshal(docs.TechnicalTaxonomy, &doc); err != nil {
		return doc, fmt.Errorf("解析内置技术词表: %w", err)
	}
	seen := make(map[string]bool)
	if doc.Version == "" || len(doc.Capabilities) == 0 {
		return doc, fmt.Errorf("内置词表缺少版本或能力")
	}
	for _, axis := range [][]TaxonomyTerm{doc.Domains, doc.Forms, doc.Industries, doc.Traits} {
		for _, term := range axis {
			if !technicalTagSlug.MatchString(term.ID) || seen[term.ID] || strings.TrimSpace(term.Name) == "" || strings.TrimSpace(term.Definition) == "" {
				return doc, fmt.Errorf("无效或重复的技术词项 %q", term.ID)
			}
			seen[term.ID] = true
		}
	}
	for _, tag := range doc.Capabilities {
		if seen[tag.Slug] || tag.Definition == "" || !hasTerm(doc.Domains, tag.Domain) {
			return doc, fmt.Errorf("内置能力 %q 的标识、定义或领域无效", tag.Slug)
		}
		seen[tag.Slug] = true
	}
	for _, id := range legacyCapabilityIDs {
		if !slices.ContainsFunc(doc.Capabilities, func(tag TechnicalTag) bool { return tag.Slug == id }) {
			return doc, fmt.Errorf("旧技术标识映射目标 %q 不存在", id)
		}
	}
	_, err := newTechnicalTags(doc.Capabilities, doc.Domains)
	return doc, err
})

func hasTerm(terms []TaxonomyTerm, id string) bool {
	return slices.ContainsFunc(terms, func(term TaxonomyTerm) bool { return term.ID == id })
}

// TaxonomyMetadata 给后台表单及分类结果提供同一版本的领域、产品形态。
func TaxonomyMetadata() (version string, domains, forms []TaxonomyTerm, err error) {
	doc, err := loadTaxonomy()
	return doc.Version, slices.Clone(doc.Domains), slices.Clone(doc.Forms), err
}

// ReservedTaxonomyID 不能把领域、形态、行业或特点的标识复用为能力。
func ReservedTaxonomyID(id string) bool {
	doc, _ := loadTaxonomy()
	return legacyCapabilityIDs[id] != "" || hasTerm(doc.Domains, id) || hasTerm(doc.Forms, id) || hasTerm(doc.Industries, id) || hasTerm(doc.Traits, id)
}

var builtinCapabilities = sync.OnceValues(func() (TechnicalTags, error) {
	doc, err := loadTaxonomy()
	if err != nil {
		return TechnicalTags{}, err
	}
	return newTechnicalTags(doc.Capabilities, doc.Domains)
})

// cloneTechnicalTags 避免调用方对后台草稿或候选排序的修改污染共享内置词表。
func cloneTechnicalTags(c TechnicalTags) TechnicalTags {
	c.Items = slices.Clone(c.Items)
	for i := range c.Items {
		c.Items[i].Aliases = slices.Clone(c.Items[i].Aliases)
	}
	return c
}

var oldTechnicalTags = sync.OnceValues(func() (TechnicalTags, error) {
	var items []TechnicalTag
	if err := json.Unmarshal(legacyBuiltinTechnicalTags, &items); err != nil {
		return TechnicalTags{}, err
	}
	return newTechnicalTags(items, nil)
})

func isBuiltinCapability(id string) bool {
	cat, err := builtinCapabilities()
	return err == nil && cat.bySlug[id] != ""
}

// upgradeLegacyTags 只把管理员实际改过的词条叠加到新版内置能力；旧设置原文仍在原键下。
func upgradeLegacyTags(items []TechnicalTag) (TechnicalTags, error) {
	old, err := oldTechnicalTags()
	if err != nil {
		return TechnicalTags{}, err
	}
	legacy, err := newTechnicalTags(items, nil)
	if err != nil {
		return TechnicalTags{}, err
	}
	builtin, err := builtinCapabilities()
	if err != nil {
		return TechnicalTags{}, err
	}
	merged := slices.Clone(builtin.Items)
	for _, item := range legacy.Items {
		prior := slices.IndexFunc(old.Items, func(t TechnicalTag) bool { return t.Slug == item.Slug })
		if prior >= 0 && old.Items[prior].Name == item.Name && slices.Equal(old.Items[prior].Aliases, item.Aliases) {
			continue
		}
		if canonical := legacyCapabilityIDs[item.Slug]; canonical != "" {
			item.Slug = canonical
		}
		if ReservedTaxonomyID(item.Slug) {
			continue
		}
		pos := slices.IndexFunc(merged, func(t TechnicalTag) bool { return t.Slug == item.Slug })
		// 一个旧词条发生冲突时仅保留同名正式词，其他兼容项照常导入。
		if pos >= 0 {
			item.Domain, item.Definition = merged[pos].Domain, merged[pos].Definition
			for _, alias := range merged[pos].Aliases {
				if len(item.Aliases) < 12 && !slices.Contains(item.Aliases, alias) {
					item.Aliases = append(item.Aliases, alias)
				}
			}
			candidate := slices.Clone(merged)
			candidate[pos] = item
			if _, err := NewTechnicalTags(candidate); err == nil {
				merged = candidate
			}
		} else {
			candidate := append(slices.Clone(merged), item)
			if _, err := NewTechnicalTags(candidate); err == nil {
				merged = candidate
			}
		}
	}
	return NewTechnicalTags(merged)
}

// 只转换能够保持语义的旧 ID；其余历史 ID 保持原义，等待重新分类。
var legacyCapabilityIDs = map[string]string{
	"graphics-editor": "vector-graphics", "vector-editor": "vector-graphics", "image-editor": "image-editing",
	"text-to-speech": "speech-synthesis", "tts": "speech-synthesis", "text-to-image": "image-generation", "text-to-video": "video-generation",
	"workflow": "workflow-automation", "structured-extraction": "data-extraction", "automated-testing": "test-automation",
	"mcp": "mcp-server", "model-context-protocol": "mcp-server",
}

// CompatibleCapabilityIDs 同时支持旧结果匹配新规则、新结果匹配旧规则。
func CompatibleCapabilityIDs(id string) []string {
	canonical := id
	if replacement := legacyCapabilityIDs[id]; replacement != "" {
		canonical = replacement
	}
	ids := []string{canonical}
	for old, replacement := range legacyCapabilityIDs {
		if replacement == canonical {
			ids = append(ids, old)
		}
	}
	slices.Sort(ids)
	return ids
}
