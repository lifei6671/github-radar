package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

type ProjectTheme struct {
	GroupSlug   string   `json:"groupSlug"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	AnyTopics   []string `json:"anyTopics"`
	Icon        string   `json:"icon"`
	IconColor   string   `json:"iconColor"`
}

var projectThemeIcons = []string{
	consts.ThemeIconBot, consts.ThemeIconCodeXML, consts.ThemeIconDatabase, consts.ThemeIconFlask,
	consts.ThemeIconGlobe, consts.ThemeIconLayers, consts.ThemeIconPlug, consts.ThemeIconTerminal,
	consts.ThemeIconVideo, consts.ThemeIconWrench,
	consts.ThemeIconFolder, consts.ThemeIconCurrency, consts.ThemeIconBook, consts.ThemeIconStudy,
	consts.ThemeIconPencil, consts.ThemeIconMusic, consts.ThemeIconPalette, consts.ThemeIconCase,
	consts.ThemeIconChart, consts.ThemeIconBranch, consts.ThemeIconShield, consts.ThemeIconScan,
	consts.ThemeIconPlane, consts.ThemeIconBrain, consts.ThemeIconHeart, consts.ThemeIconLeaf,
	consts.ThemeIconRocket, consts.ThemeIconNetwork, consts.ThemeIconBox, consts.ThemeIconSparkles,
	consts.ThemeIconActivity, consts.ThemeIconAlarmClock, consts.ThemeIconArchive, consts.ThemeIconAtom,
	consts.ThemeIconAudioLines, consts.ThemeIconBinary, consts.ThemeIconBlocks, consts.ThemeIconBolt,
	consts.ThemeIconBookMarked, consts.ThemeIconBraces, consts.ThemeIconBug, consts.ThemeIconCable,
	consts.ThemeIconCalendarDays, consts.ThemeIconCamera, consts.ThemeIconChartLine, consts.ThemeIconCheckCircle2,
	consts.ThemeIconCircuitBoard, consts.ThemeIconClipboardList, consts.ThemeIconCloud, consts.ThemeIconCloudDownload,
	consts.ThemeIconCode, consts.ThemeIconCog, consts.ThemeIconCompass, consts.ThemeIconContainer,
	consts.ThemeIconCpu, consts.ThemeIconDices, consts.ThemeIconEarth, consts.ThemeIconFeather,
	consts.ThemeIconFileCode, consts.ThemeIconFileText, consts.ThemeIconFingerprint, consts.ThemeIconFlag,
	consts.ThemeIconFlame, consts.ThemeIconFolderGit2, consts.ThemeIconGauge, consts.ThemeIconGitCommitHorizontal,
	consts.ThemeIconGitPullRequest, consts.ThemeIconGithub, consts.ThemeIconHammer, consts.ThemeIconHardDrive,
	consts.ThemeIconHeadphones, consts.ThemeIconImage, consts.ThemeIconKeyRound, consts.ThemeIconKeyboard,
	consts.ThemeIconLaptop, consts.ThemeIconLibrary, consts.ThemeIconLink2, consts.ThemeIconListChecks,
	consts.ThemeIconLockKeyhole, consts.ThemeIconMap, consts.ThemeIconMapPin, consts.ThemeIconMegaphone,
	consts.ThemeIconMessageSquare, consts.ThemeIconMic, consts.ThemeIconMonitor, consts.ThemeIconMousePointer2,
	consts.ThemeIconPackage, consts.ThemeIconPaperclip, consts.ThemeIconPuzzle, consts.ThemeIconRadio,
	consts.ThemeIconRefreshCw, consts.ThemeIconRoute, consts.ThemeIconRss, consts.ThemeIconSearch,
	consts.ThemeIconServer, consts.ThemeIconShare2, consts.ThemeIconShieldCheck, consts.ThemeIconSmartphone,
	consts.ThemeIconSquareStack, consts.ThemeIconStar, consts.ThemeIconTestTubeDiagonal, consts.ThemeIconTimer,
	consts.ThemeIconTriangleAlert, consts.ThemeIconUnplug, consts.ThemeIconWebhook, consts.ThemeIconWifi,
	consts.ThemeIconWorkflow, consts.ThemeIconZap,
}

var projectThemeColors = []string{
	consts.ThemeColorCyan, consts.ThemeColorGreen, consts.ThemeColorAmber, consts.ThemeColorTomato, consts.ThemeColorSlate,
	consts.ThemeColorOrange, consts.ThemeColorBlue, consts.ThemeColorViolet, consts.ThemeColorPink,
}

type ProjectThemeSettings struct {
	Revision int64          `json:"revision"`
	Items    []ProjectTheme `json:"items"`
}

func (s Store) ProjectThemeSettings(ctx context.Context) (ProjectThemeSettings, error) {
	result := ProjectThemeSettings{Items: []ProjectTheme{}}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := read(tx)
		if err != nil {
			return err
		}
		result.Revision = snapshot.Revision
		var rows []storage.ProjectTheme
		if err := tx.Order(storage.ProjectThemeColumns.Position + ", " + storage.ProjectThemeColumns.Slug).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			item := ProjectTheme{Slug: row.Slug, Name: row.Name, Description: row.Description, Icon: row.Icon, IconColor: row.IconColor, GroupSlug: row.GroupSlug}
			if err := json.Unmarshal([]byte(row.TopicsJSON), &item.AnyTopics); err != nil {
				return err
			}
			result.Items = append(result.Items, item)
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return result, err
}

// SaveProjectThemes 与其他后台配置共用修订号，原子替换管理员维护的专题目录。
func (s Store) SaveProjectThemes(ctx context.Context, revision int64, items []ProjectTheme) (ProjectThemeSettings, error) {
	rows := make([]storage.ProjectTheme, 0, len(items))
	result := ProjectThemeSettings{Items: make([]ProjectTheme, 0, len(items))}
	slugs := make(map[string]bool, len(items))
	for position, item := range items {
		item.Name = strings.TrimSpace(item.Name)
		item.Description = strings.TrimSpace(item.Description)
		if !config.ValidSourceSlug(item.Slug) || slugs[item.Slug] || item.Name == "" || item.Description == "" || len(item.AnyTopics) == 0 {
			return ProjectThemeSettings{}, &Invalid{Err: fmt.Errorf("专题 %q 须有唯一标识、名称、简介和匹配词", item.Slug)}
		}
		if item.Icon != "" && !slices.Contains(projectThemeIcons, item.Icon) ||
			item.IconColor != "" && !slices.Contains(projectThemeColors, item.IconColor) {
			return ProjectThemeSettings{}, &Invalid{Err: fmt.Errorf("专题 %q 的图标或颜色无效", item.Slug)}
		}
		if item.Icon == "" {
			item.IconColor = ""
		}
		slugs[item.Slug] = true
		topics := make([]string, 0, len(item.AnyTopics))
		seen := make(map[string]bool, len(item.AnyTopics))
		for _, topic := range item.AnyTopics {
			topic = strings.ToLower(strings.TrimSpace(topic))
			if !config.ValidSourceSlug(topic) {
				return ProjectThemeSettings{}, &Invalid{Err: fmt.Errorf("专题 %q 的匹配词 %q 无效", item.Slug, topic)}
			}
			if !seen[topic] {
				seen[topic] = true
				topics = append(topics, topic)
			}
		}
		item.AnyTopics = topics
		data, err := json.Marshal(topics)
		if err != nil {
			return ProjectThemeSettings{}, err
		}
		rows = append(rows, storage.ProjectTheme{
			Slug: item.Slug, Name: item.Name, Description: item.Description, TopicsJSON: string(data),
			Position: position, Icon: item.Icon, IconColor: item.IconColor,
		})
		result.Items = append(result.Items, item)
	}
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, _ *Snapshot) error {
		if len(rows) > 0 {
			groups, err := ReadProjectThemeGroups(tx)
			if err != nil {
				return err
			}
			allowed := make(map[string]bool, len(groups))
			for _, group := range groups {
				allowed[group.Slug] = true
			}
			var existing []storage.ProjectTheme
			if err := tx.Select(storage.ProjectThemeColumns.Slug, storage.ProjectThemeColumns.GroupSlug).Find(&existing).Error; err != nil {
				return err
			}
			current := make(map[string]string, len(existing))
			for _, theme := range existing {
				current[theme.Slug] = theme.GroupSlug
			}
			for i, item := range result.Items {
				group := item.GroupSlug
				// 旧请求没有方向字段，已有专题沿用其归属，新专题进入首个方向。
				if group == "" {
					group = current[item.Slug]
					if group == "" && len(groups) > 0 {
						group = groups[0].Slug
					}
				}
				if !allowed[group] {
					return &Invalid{Err: fmt.Errorf("专题 %q 的方向不存在", item.Slug)}
				}
				rows[i].GroupSlug, result.Items[i].GroupSlug = group, group
			}
		}
		if err := tx.Where("1 = 1").Delete(&storage.ProjectTheme{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			return tx.CreateInBatches(&rows, 100).Error
		}
		return nil
	})
	result.Revision = snapshot.Revision
	return result, err
}

type ProjectThemeGroupSettings struct {
	Revision int64                       `json:"revision"`
	Items    []storage.ProjectThemeGroup `json:"items"`
}

// ReadProjectThemeGroups 返回公开目录与后台共用的方向顺序。
func ReadProjectThemeGroups(db *gorm.DB) ([]storage.ProjectThemeGroup, error) {
	items := []storage.ProjectThemeGroup{}
	err := db.Order(storage.ProjectThemeGroupColumns.Position).Find(&items).Error
	return items, err
}

func (s Store) ProjectThemeGroups(ctx context.Context) (ProjectThemeGroupSettings, error) {
	var result ProjectThemeGroupSettings
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := read(tx)
		if err != nil {
			return err
		}
		result.Revision = snapshot.Revision
		result.Items, err = ReadProjectThemeGroups(tx)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	return result, err
}

// SaveProjectThemeGroups 禁止删除仍有专题归属的方向。
func (s Store) SaveProjectThemeGroups(ctx context.Context, revision int64, items []storage.ProjectThemeGroup) (ProjectThemeGroupSettings, error) {
	allowed := make(map[string]bool, len(items))
	for i := range items {
		item := &items[i]
		item.Name = strings.TrimSpace(item.Name)
		item.Description = strings.TrimSpace(item.Description)
		if !config.ValidSourceSlug(item.Slug) || allowed[item.Slug] || item.Name == "" || item.Description == "" {
			return ProjectThemeGroupSettings{}, &Invalid{Err: fmt.Errorf("主题方向 %q 须有唯一标识、名称和简介", item.Slug)}
		}
		allowed[item.Slug] = true
		item.Position = i
	}
	snapshot, err := s.update(ctx, revision, func(tx *gorm.DB, _ *Snapshot) error {
		var used []string
		if err := tx.Model(&storage.ProjectTheme{}).Distinct().Pluck(storage.ProjectThemeColumns.GroupSlug, &used).Error; err != nil {
			return err
		}
		for _, group := range used {
			if !allowed[group] {
				return &Invalid{Err: fmt.Errorf("方向 %q 下还有专题，请先将专题移到其他方向", group)}
			}
		}
		if err := tx.Where("1 = 1").Delete(&storage.ProjectThemeGroup{}).Error; err != nil {
			return err
		}
		if len(items) > 0 {
			return tx.CreateInBatches(&items, 100).Error
		}
		return nil
	})
	return ProjectThemeGroupSettings{Revision: snapshot.Revision, Items: items}, err
}
