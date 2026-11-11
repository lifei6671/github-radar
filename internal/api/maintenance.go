package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/maintenance"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (a *collectionAPI) requestMaintenance(c *gin.Context) {
	if err := maintenance.Request(c.Request.Context(), a.db, time.Now()); err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": consts.StatusPending})
}

func (a *collectionAPI) storageStatus(c *gin.Context) {
	db := a.db.WithContext(c.Request.Context())
	pool, err := db.DB()
	if err != nil {
		a.fail(c, err)
		return
	}
	var pages, free, size int64
	for _, pragma := range []struct {
		key    string
		target *int64
	}{
		{"page_count", &pages}, {"freelist_count", &free}, {"page_size", &size},
	} {
		if err := pool.QueryRowContext(c.Request.Context(), "PRAGMA "+pragma.key).Scan(pragma.target); err != nil {
			a.fail(c, err)
			return
		}
	}
	var wal int64
	if file, err := os.Stat(a.cfg.Storage.Path + "-wal"); err == nil {
		wal = file.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		a.fail(c, err)
		return
	}
	counts := gin.H{}
	for name, model := range map[string]any{consts.ResolutionRaw: &storage.RepoSnapshot{}, consts.ResolutionHourly: &storage.HourlyMetric{}, consts.ResolutionDaily: &storage.DailyMetric{}} {
		var n int64
		if err := db.Model(model).Count(&n).Error; err != nil {
			a.fail(c, err)
			return
		}
		counts[name] = n
	}
	var markers []storage.MaintenanceState
	if err := db.Where(storage.MaintenanceStateColumns.Key+" IN ?", []string{maintenance.HourlyKey, maintenance.DailyKey, maintenance.CleanupKey, metrics.ComputeKey}).
		Find(&markers).Error; err != nil {
		a.fail(c, err)
		return
	}
	watermarks := gin.H{consts.ResolutionHourly: nil, consts.ResolutionDaily: nil, "cleanedAt": nil, "metricsUpdatedAt": nil}
	for _, marker := range markers {
		switch marker.Key {
		case metrics.ComputeKey:
			watermarks["metricsUpdatedAt"] = iso(marker.UpdatedAt)
		case maintenance.CleanupKey:
			watermarks["cleanedAt"] = iso(marker.UpdatedAt)
		default:
			value, err := strconv.ParseInt(marker.Value, 10, 64)
			if err != nil {
				a.fail(c, err)
				return
			}
			if marker.Key == maintenance.HourlyKey {
				watermarks[consts.ResolutionHourly] = iso(value)
			} else {
				watermarks[consts.ResolutionDaily] = iso(value)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"databaseBytes": pages * size, "walBytes": wal, "pageCount": pages, "freelistCount": free,
		"counts": counts, "watermarks": watermarks})
}
