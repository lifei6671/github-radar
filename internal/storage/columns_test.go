package storage

import (
	"reflect"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// TestColumnMappings 防止查询使用的字段名与 GORM 显式映射发生漂移。
func TestColumnMappings(t *testing.T) {
	tests := []struct{ model, columns any }{
		{AICall{}, AICallColumns},
		{AISummary{}, AISummaryColumns},
		{CategorySetting{}, CategorySettingColumns},
		{DailyMetric{}, DailyMetricColumns},
		{DiscoveryRule{}, DiscoveryRuleColumns},
		{HourlyMetric{}, HourlyMetricColumns},
		{Job{}, JobColumns},
		{LatestMetric{}, LatestMetricColumns},
		{MaintenanceState{}, MaintenanceStateColumns},
		{PendingMetric{}, PendingMetricColumns},
		{ProjectTheme{}, ProjectThemeColumns},
		{ProjectThemeGroup{}, ProjectThemeGroupColumns},
		{RateLimit{}, RateLimitColumns},
		{RepoAITopics{}, RepoAITopicsColumns},
		{RepoReadmeTranslation{}, RepoReadmeTranslationColumns},
		{RepoSnapshot{}, RepoSnapshotColumns},
		{Repository{}, RepositoryColumns},
		{SearchObservation{}, SearchObservationColumns},
		{SummaryCheck{}, SummaryCheckColumns},
		{SystemSetting{}, SystemSettingColumns},
		{TopicCandidate{}, TopicCandidateColumns},
		{TrendingBatchRecord{}, TrendingBatchRecordColumns},
		{TrendingObservation{}, TrendingObservationColumns},
	}
	for _, test := range tests {
		t.Run(reflect.TypeOf(test.model).Name(), func(t *testing.T) {
			mapped, err := schema.Parse(test.model, &sync.Map{}, schema.NamingStrategy{})
			if err != nil {
				t.Fatal(err)
			}
			columns := reflect.ValueOf(test.columns)
			count := 0
			for _, column := range reflect.VisibleFields(columns.Type()) {
				if column.Anonymous {
					continue
				}
				count++
				field := mapped.FieldsByName[column.Name]
				if field == nil {
					t.Errorf("GORM 缺少字段 %s", column.Name)
					continue
				}
				if got := columns.FieldByIndex(column.Index).String(); got != field.DBName {
					t.Errorf("%s 映射为 %q，GORM 映射为 %q", column.Name, got, field.DBName)
				}
			}
			if want := len(mapped.FieldsByDBName); count != want {
				t.Errorf("字段数为 %d，GORM 映射为 %d", count, want)
			}
		})
	}
}
