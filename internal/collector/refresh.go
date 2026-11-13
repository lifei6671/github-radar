package collector

import (
	"context"
	"errors"
	"time"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (e *Engine) refresh(ctx context.Context, job storage.Job, p collection.Payload) (port.Change, error) {
	if job.RepoID == nil {
		return nil, errors.New("仓库采集任务缺少 repo_id")
	}
	baseline, err := e.Queue.Repository(ctx, *job.RepoID)
	if err != nil {
		return nil, err
	}
	state := baseline.MonitoringState()
	if !state.CanSample(p.Manual) {
		return nil, nil
	}
	policy := gh.Policy{Resource: gh.Core, RepositoryID: baseline.RepoID, Sample: true, Grade: baseline.Grade, Priority: p.Manual || baseline.Grade == consts.GradeS}
	var result gh.RepositoryResult
	var requestErr error
	started := time.Now()
	if !baseline.NameIsCurrent {
		result, requestErr = e.GitHub.RepositoryByID(ctx, baseline.RepoID, policy)
	} else {
		result, requestErr = e.GitHub.Repository(ctx, baseline.Owner, baseline.Name, state.ConditionalETag(), policy)
	}
	observed := time.Now()
	var failure *gh.Failure
	if requestErr != nil && (!errors.As(requestErr, &failure) || (failure.Status != 404 && failure.Status != 410)) {
		return nil, requestErr
	}
	status := 0
	if failure != nil {
		status = failure.Status
	}
	observation := port.Refresh{Baseline: baseline, Plan: p, Result: result,
		FailureStatus: status, StartedAt: started, ObservedAt: observed}
	return func(w port.Writer) error { return w.SaveRefresh(observation) }, nil
}
