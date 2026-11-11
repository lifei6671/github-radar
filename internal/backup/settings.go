package backup

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

const settingsKey = "backup.settings.v1"

type Fault string

func (f Fault) Error() string { return string(f) }

const (
	ErrBusy               Fault = consts.CodeBackupOperationInProgress
	ErrRestoring          Fault = consts.CodeRestoreInProgress
	ErrMaintenance        Fault = consts.CodeRestoring
	ErrAlreadyInitialized Fault = consts.CodeAlreadyInitialized
	ErrSetupRequired      Fault = consts.CodeSetupRequired
	ErrNotConfigured      Fault = consts.CodeR2NotConfigured
	ErrInvalidSettings    Fault = consts.CodeInvalidBackupSettings
	ErrInvalidObject      Fault = consts.CodeInvalidBackupObject
	ErrSession            Fault = consts.CodeSetupSessionExpired
	ErrOperation          Fault = consts.CodeBackupOperationNotFound
	ErrReload             Fault = consts.CodeReloadNotAvailable
	ErrR2                 Fault = consts.CodeR2RequestFailed
	ErrSpace              Fault = consts.CodeBackupInsufficientDisk
	ErrUploadUnknown      Fault = consts.CodeBackupUploadUnconfirmed
	ErrDatabase           Fault = consts.CodeBackupDatabaseError
)

func ErrorCode(err error) string {
	var f Fault
	if errors.As(err, &f) {
		return string(f)
	}
	return string(ErrDatabase)
}

type Schedule struct {
	Hour     int    `json:"hour"`
	Minute   int    `json:"minute"`
	Timezone string `json:"timezone"`
}

type Connection struct {
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

type Settings struct {
	Enabled       bool
	Schedule      Schedule
	RetentionDays int
	R2            Connection
}

type SettingsUpdate struct {
	Enabled       bool        `json:"enabled"`
	Schedule      Schedule    `json:"schedule"`
	RetentionDays int         `json:"retentionDays"`
	R2            *Connection `json:"r2,omitempty"`
}

type SettingsView struct {
	Enabled       bool           `json:"enabled"`
	Schedule      Schedule       `json:"schedule"`
	RetentionDays int            `json:"retentionDays"`
	R2            ConnectionView `json:"r2"`
	NextBackupAt  *time.Time     `json:"nextBackupAt"`
	NextCleanupAt *time.Time     `json:"nextCleanupAt"`
}

type ConnectionView struct {
	Endpoint          string `json:"endpoint"`
	Bucket            string `json:"bucket"`
	Prefix            string `json:"prefix"`
	AccessKeyIDMasked string `json:"accessKeyIdMasked"`
	Configured        bool   `json:"configured"`
}

func defaults() Settings {
	return Settings{Schedule: Schedule{Hour: 3, Minute: 0, Timezone: "Asia/Shanghai"}, RetentionDays: 5,
		R2: Connection{Prefix: "github-radar/backups/"}}
}

// 原有安装流程已经写入 snake_case JSON，API 使用 lowerCamelCase 的独立脱敏结构。
type storedSettings struct {
	Version       int      `json:"version"`
	Enabled       bool     `json:"enabled"`
	Schedule      Schedule `json:"schedule"`
	RetentionDays int      `json:"retention_days"`
	R2            struct {
		Endpoint        string `json:"endpoint"`
		Bucket          string `json:"bucket"`
		Prefix          string `json:"prefix"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	} `json:"r2"`
}

func readSettings(ctx context.Context, db *gorm.DB) (Settings, error) {
	var row storage.SystemSetting
	err := db.WithContext(ctx).Take(&row, storage.SystemSettingColumns.Key+"=?", settingsKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaults(), nil
	}
	if err != nil {
		return Settings{}, ErrDatabase
	}
	var saved storedSettings
	if json.Unmarshal([]byte(row.ValueJSON), &saved) != nil || saved.Version != 1 {
		return Settings{}, ErrInvalidSettings
	}
	s := Settings{Enabled: saved.Enabled, Schedule: saved.Schedule, RetentionDays: saved.RetentionDays,
		R2: Connection{saved.R2.Endpoint, saved.R2.Bucket, saved.R2.Prefix, saved.R2.AccessKeyID, saved.R2.SecretAccessKey}}
	return normalizeSettings(s)
}

func writeSettings(tx *gorm.DB, s Settings) error {
	saved := storedSettings{Version: 1, Enabled: s.Enabled, Schedule: s.Schedule, RetentionDays: s.RetentionDays}
	saved.R2.Endpoint, saved.R2.Bucket, saved.R2.Prefix = s.R2.Endpoint, s.R2.Bucket, s.R2.Prefix
	saved.R2.AccessKeyID, saved.R2.SecretAccessKey = s.R2.AccessKeyID, s.R2.SecretAccessKey
	value, err := json.Marshal(saved)
	if err != nil {
		return ErrDatabase
	}
	return saveSetting(tx, settingsKey, string(value))
}

func saveSetting(tx *gorm.DB, key, value string) error {
	row := storage.SystemSetting{Key: key, ValueJSON: value, UpdatedAt: time.Now().UnixMilli()}
	if err := tx.Save(&row).Error; err != nil {
		return ErrDatabase
	}
	return nil
}

var r2Host = regexp.MustCompile(`^[0-9a-f]{32}(\.[a-z0-9-]+)?\.r2\.cloudflarestorage\.com$`)
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func NormalizeConnection(c Connection) (Connection, error) {
	c.Endpoint, c.Bucket = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Bucket)
	c.Prefix = strings.TrimSpace(c.Prefix)
	if c.Prefix == "" {
		c.Prefix = defaults().R2.Prefix
	}
	c.Prefix = strings.TrimSuffix(c.Prefix, "/") + "/"
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || !r2Host.MatchString(u.Host) || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		!bucketName.MatchString(c.Bucket) || strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == "" ||
		strings.ContainsAny(c.AccessKeyID+c.SecretAccessKey, "\r\n\x00") || len(c.Prefix) > 960 {
		return Connection{}, ErrInvalidSettings
	}
	for _, part := range strings.Split(strings.TrimSuffix(c.Prefix, "/"), "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return Connection{}, ErrInvalidSettings
		}
	}
	c.Endpoint = "https://" + u.Host
	return c, nil
}

func (c Connection) configured() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

func normalizeSettings(s Settings) (Settings, error) {
	if err := validatePlan(s.Schedule, s.RetentionDays); err != nil {
		return Settings{}, err
	}
	if s.R2.configured() {
		var err error
		s.R2, err = NormalizeConnection(s.R2)
		if err != nil {
			return Settings{}, err
		}
	} else if s.Enabled || s.R2.Endpoint != "" || s.R2.Bucket != "" || s.R2.AccessKeyID != "" || s.R2.SecretAccessKey != "" {
		return Settings{}, ErrInvalidSettings
	}
	return s, nil
}

func validatePlan(schedule Schedule, retention int) error {
	if _, err := time.LoadLocation(schedule.Timezone); err != nil || schedule.Timezone == "" || schedule.Timezone == "Local" ||
		schedule.Hour < 0 || schedule.Hour > 23 || schedule.Minute < 0 || schedule.Minute > 59 || retention < 1 || retention > 30 {
		return ErrInvalidSettings
	}
	return nil
}

func (s Settings) view() SettingsView {
	c := s.R2
	masked := ""
	if c.configured() {
		runes := []rune(c.AccessKeyID)
		masked = "••••"
		if len(runes) > 4 {
			masked += string(runes[len(runes)-4:])
		}
	}
	return SettingsView{Enabled: s.Enabled, Schedule: s.Schedule, RetentionDays: s.RetentionDays,
		R2: ConnectionView{c.Endpoint, c.Bucket, c.Prefix, masked, c.configured()}}
}

func (s Settings) location() *time.Location {
	loc, _ := time.LoadLocation(s.Schedule.Timezone)
	return loc
}

func atDate(now time.Time, hour, minute int) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, hour, minute, 0, 0, now.Location())
}

func nextTime(now time.Time, hour, minute int) time.Time {
	when := atDate(now, hour, minute)
	if !when.After(now) {
		when = atDate(now.AddDate(0, 0, 1), hour, minute)
	}
	return when
}
