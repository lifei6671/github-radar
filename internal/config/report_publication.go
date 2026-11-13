package config

import "errors"

type ReportPublication struct {
	WeeklyMinDays      int `json:"weeklyMinDays"`
	MonthlyMinCoverage int `json:"monthlyMinCoverage"`
}

func DefaultReportPublication() ReportPublication {
	return ReportPublication{WeeklyMinDays: 5, MonthlyMinCoverage: 75}
}

func (r ReportPublication) validate() error {
	if r.WeeklyMinDays < 1 || r.WeeklyMinDays > 7 {
		return errors.New("周报最少观测天数必须是 1 到 7")
	}
	if r.MonthlyMinCoverage < 1 || r.MonthlyMinCoverage > 100 {
		return errors.New("月报最低覆盖率必须是 1% 到 100%")
	}
	return nil
}
