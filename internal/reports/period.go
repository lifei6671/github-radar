package reports

import (
	"fmt"
	"strconv"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
)

type Kind string

const (
	Daily   Kind = "daily"
	Weekly  Kind = "weekly"
	Monthly Kind = "monthly"
)

func (k Kind) Key(day time.Time) string {
	day = day.In(beijing)
	switch k {
	case Weekly:
		year, week := day.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	case Monthly:
		return day.Format("2006-01")
	default:
		return day.Format(time.DateOnly)
	}
}

const (
	Published = "published"
	Preview   = "preview"
)

func ObservationDate(at int64) string {
	return time.UnixMilli(at).In(beijing).Format(time.DateOnly)
}

// PublicationPolicy 统计周期的实际观测日，等下一期日报在北京时间 08:00 截稿。
func (k Kind) PublicationPolicy(key string, rules config.ReportPublication) (cutoff time.Time, requiredDays int) {
	from, end, valid := k.Bounds(key)
	if !valid || k == Daily {
		return time.Time{}, 1
	}
	requiredDays = rules.WeeklyMinDays
	if k == Monthly {
		days := int(end.Sub(from).Hours() / 24)
		requiredDays = (days*rules.MonthlyMinCoverage + 99) / 100
	}
	return end.Add(8 * time.Hour), requiredDays
}

func (k Kind) PublicationStatus(key string, days int, now time.Time, rules config.ReportPublication) string {
	cutoff, required := k.PublicationPolicy(key, rules)
	if k == Daily || (!now.Before(cutoff) && days >= required) {
		return Published
	}
	return Preview
}

// Bounds 返回以北京时间排期的起止日期，结束日期为开区间。
func (k Kind) Bounds(key string) (start, end time.Time, valid bool) {
	switch k {
	case Daily:
		day, err := time.ParseInLocation(time.DateOnly, key, beijing)
		return day, day.AddDate(0, 0, 1), err == nil
	case Monthly:
		month, err := time.ParseInLocation("2006-01", key, beijing)
		return month, month.AddDate(0, 1, 0), err == nil && month.Year() > 0
	case Weekly:
		if len(key) != 8 || key[4:6] != "-W" {
			return
		}
		year, yearErr := strconv.Atoi(key[:4])
		week, weekErr := strconv.Atoi(key[6:])
		if yearErr != nil || weekErr != nil || year < 1 || week < 1 || week > 53 {
			return
		}
		jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, beijing)
		monday := jan4.AddDate(0, 0, -(int(jan4.Weekday())+6)%7+(week-1)*7)
		isoYear, isoWeek := monday.ISOWeek()
		return monday, monday.AddDate(0, 0, 7), key == fmt.Sprintf("%04d-W%02d", isoYear, isoWeek)
	}
	return
}
