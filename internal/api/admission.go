package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lifei6671/github-radar/internal/storage"
)

// admissionAudit 提供管理员只读回测数据，日期参数为 UTC。
func (a *collectionAPI) admissionAudit(c *gin.Context) {
	at := time.Now().UTC()
	if day := c.Query("day"); day != "" {
		var err error
		at, err = time.Parse(time.DateOnly, day)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_day"})
			return
		}
	}
	report, err := storage.ReadAdmissionAudit(c.Request.Context(), a.db, at)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}
