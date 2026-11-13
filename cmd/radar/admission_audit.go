package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/metricstore"
	"github.com/lifei6671/github-radar/internal/summary"
)

// runAdmissionAudit 只读打开 SQLite，可输出连续日期及独立人工榜单的对照结果。
func runAdmissionAudit(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("admission-audit", flag.ContinueOnError)
	configPath := flags.String("config", "", "部署 YAML 路径")
	dbPath := flags.String("db", "", "直接指定已迁移的 SQLite 文件")
	from := flags.String("from", time.Now().UTC().Format(time.DateOnly), "首个 UTC 日期")
	days := flags.Int("days", 1, "统计天数，最多 30")
	sample := flags.String("sample", "", "逗号分隔的人工对照仓库数字 ID")
	simulate := flags.Bool("simulate", false, "追加一次只读的监控席位模拟")
	queue := flags.Bool("queue", false, "在回滚事务内模拟一轮 AI 入队和存量清理")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *days < 1 || *days > 30 || flags.NArg() != 0 {
		return errors.New("请指定 1 至 30 天")
	}
	start, err := time.Parse(time.DateOnly, *from)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	path := *dbPath
	if path == "" {
		path = cfg.Storage.Path
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	pool, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		return err
	}
	var ids []int64
	for _, raw := range strings.Split(*sample, ",") {
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			return errors.New("人工样本需要正整数 GitHub 仓库 ID")
		}
		ids = append(ids, id)
	}
	type daily struct {
		storage.AdmissionAudit
		Manual []storage.AdmissionAuditSample `json:"manualSample,omitempty"`
	}
	result := struct {
		Days       []daily          `json:"days"`
		Simulation map[string]any   `json:"monitorSimulation,omitempty"`
		Queue      map[string]int64 `json:"queueSimulation,omitempty"`
	}{}
	for i := range *days {
		at := start.AddDate(0, 0, i)
		report, err := storage.ReadAdmissionAudit(ctx, db, at)
		if err != nil {
			return err
		}
		var manual []storage.AdmissionAuditSample
		if len(ids) != 0 {
			manual, err = storage.CompareAdmissionSamples(db.WithContext(ctx), at, ids)
			if err != nil {
				return err
			}
		}
		result.Days = append(result.Days, daily{AdmissionAudit: report, Manual: manual})
	}
	now := start.AddDate(0, 0, *days)
	if real := time.Now(); real.Before(now) {
		now = real
	}
	if *simulate {
		if snapshot, readErr := (settings.Store{DB: db}).Get(ctx); readErr == nil {
			cfg = cfg.WithRuntime(snapshot.Settings)
		} else if !errors.Is(readErr, gorm.ErrRecordNotFound) {
			return readErr
		}
		// 模拟的是副本当前状态，不将当前仓库集合伪装成历史截面。
		result.Simulation, err = metrics.ObservationReplay(ctx, metricstore.Store{DB: db}, cfg, time.Now())
		if err != nil {
			return err
		}
	}
	if *queue {
		// 只读主连接保持不变；另开临时事务，成功后也回滚，绝不领取任务或访问上游。
		result.Queue, err = simulateAdmissionQueue(ctx, absolute, cfg, now)
		if err != nil {
			return err
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// simulateAdmissionQueue 运行真实排队入口，比较回滚前后任务数量。
func simulateAdmissionQueue(ctx context.Context, path string, cfg config.Config, now time.Time) (map[string]int64, error) {
	pool, err := storage.OpenBootstrap(ctx, path)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		return nil, err
	}
	if snapshot, err := (settings.Store{DB: db}).Get(ctx); err == nil {
		cfg = cfg.WithRuntime(snapshot.Settings)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	rollback := errors.New("模拟完成，回滚")
	out := map[string]int64{}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pending := func() *gorm.DB {
			return tx.Model(&storage.Job{}).Where("type IN ? AND priority < 200 AND status IN ?",
				[]string{summary.JobType, summary.TranslationJob}, []string{"pending", "failed"})
		}
		var before, after, rejected, added, canceled int64
		if err := pending().Count(&before).Error; err != nil {
			return err
		}
		cfg.AI.Summary.LegacyAdmission = false
		if err := (summary.Queue{DB: tx, AI: cfg.AI, LeaseSeconds: cfg.Scheduler.LeaseSeconds}).Schedule(ctx, now); err != nil {
			return err
		}
		if err := pending().Count(&after).Error; err != nil {
			return err
		}
		if err := pending().Where("created_at = ?", now.UnixMilli()).Count(&added).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.Job{}).Where("type IN ? AND status = ? AND updated_at = ? AND last_error_code IN ?",
			[]string{summary.JobType, summary.TranslationJob}, "canceled", now.UnixMilli(),
			[]string{"not_public", "no_value_signal", "no_readable_source", "verified_no_readable_source"}).Count(&canceled).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.AdmissionDaily{}).Distinct("repo_id").Where("decided_at = ? AND state IN ?", now.UnixMilli(), []string{"defer", "reject"}).Count(&rejected).Error; err != nil {
			return err
		}
		out["before"], out["after"], out["added"], out["filtered"], out["canceled"] = before, after, added, rejected, canceled
		return rollback
	})
	if errors.Is(err, rollback) {
		err = nil
	}
	return out, err
}
