package config

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lifei6671/github-radar/configs"
	"github.com/lifei6671/github-radar/internal/consts"
)

// Category 按 GitHub 原始 topics 派生，允许一个仓库同时命中多类。
type Category struct {
	Slug      string   `yaml:"slug" json:"slug"`
	Name      string   `yaml:"name" json:"name"`
	AnyTopics []string `yaml:"any_topics" json:"anyTopics"`
}

func LoadCategories(path string) ([]Category, error) {
	data := configs.DefaultCategories
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取分类配置: %w", err)
		}
	}
	var categories []Category
	if err := yaml.Unmarshal(data, &categories); err != nil {
		return nil, fmt.Errorf("解析分类配置: %w", err)
	}
	return NormalizeCategories(categories)
}

// NormalizeCategories 统一验证默认数据和后台修改，other 始终由查询派生。
func NormalizeCategories(categories []Category) ([]Category, error) {
	result := make([]Category, 0, len(categories))
	seen := map[string]bool{consts.CategoryOther: true}
	for _, c := range categories {
		c.Name = strings.TrimSpace(c.Name)
		if !ValidSourceSlug(c.Slug) || seen[c.Slug] || c.Name == "" || len(c.AnyTopics) == 0 {
			return nil, fmt.Errorf("分类配置含无效或重复分类 %q", c.Slug)
		}
		seen[c.Slug] = true
		topics := make([]string, 0, len(c.AnyTopics))
		topicSeen := make(map[string]bool)
		for _, topic := range c.AnyTopics {
			topic = strings.ToLower(strings.TrimSpace(topic))
			if topic == "" {
				return nil, fmt.Errorf("分类 %q 含空主题", c.Slug)
			}
			if !topicSeen[topic] {
				topics = append(topics, topic)
				topicSeen[topic] = true
			}
		}
		c.AnyTopics = topics
		result = append(result, c)
	}
	return result, nil
}

// Classify 统一供 API 过滤、计数和 DTO 使用。
func Classify(categories []Category, topics []string) []string {
	normalized := make(map[string]bool, len(topics))
	for _, topic := range topics {
		normalized[strings.ToLower(topic)] = true
	}
	var matches []string
	for _, c := range categories {
		for _, topic := range c.AnyTopics {
			if normalized[topic] {
				matches = append(matches, c.Slug)
				break
			}
		}
	}
	if len(matches) == 0 {
		return []string{consts.CategoryOther}
	}
	return matches
}
