package collector

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (e *Engine) discoverManual(ctx context.Context, job storage.Job, p collection.Payload) (port.Change, error) {
	owner, name, _ := strings.Cut(p.Repository, "/")
	started := time.Now()
	result, err := e.GitHub.Repository(ctx, owner, name, "", gh.Policy{Resource: gh.Core, Priority: true})
	if err != nil {
		return nil, err
	}
	if result.Repository == nil {
		return nil, &gh.Failure{Code: consts.CodeGitHubInvalidRepository}
	}
	if result.Repository.GetPrivate() || result.Repository.GetDisabled() ||
		(result.Repository.GetVisibility() != "" && result.Repository.GetVisibility() != "public") {
		return nil, &gh.Failure{Code: consts.CodeGitHubGone, Status: http.StatusGone}
	}
	observed := time.Now()
	// 仓库 API 已确认当前名称，写入时以数字 ID 复用改名、转移和屏蔽规则。
	return func(w port.Writer) error { return w.SaveManualDiscovery(job, p, result.Repository, started, observed) }, nil
}
