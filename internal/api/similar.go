package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/embedding"
)

// similar 只读已生成的向量，公开接口不返回模型配置、向量或匹配分数。
func (a *collectionAPI) similar(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	items, err := embedding.Similar(a.db.WithContext(c.Request.Context()), runtimeFor(c).Config.Embedding, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
