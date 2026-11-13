package collection

import (
	"net/http"
	"strings"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

type RepositoryState struct {
	FullName, State                               string
	Blocked, Disabled, NameIsCurrent              bool
	MetadataETag                                  *string
	LastSuccessAt, LastCheckedAt, FirstNotFoundAt *int64
}

func (r RepositoryState) CanMonitor() bool {
	return !r.Blocked && !r.Disabled && r.NameIsCurrent &&
		(r.State == consts.StatusCandidate || r.State == consts.StatusPaused)
}

func (r RepositoryState) CanSample(manual bool) bool {
	return !r.Blocked && (manual || r.State == consts.StatusActive)
}

func (r RepositoryState) HasBaseline() bool {
	return r.LastSuccessAt != nil && r.MetadataETag != nil
}

func (r RepositoryState) ConditionalETag() string {
	if r.HasBaseline() && r.FirstNotFoundAt == nil && r.State != consts.StatusUnavailable {
		return *r.MetadataETag
	}
	return ""
}

func (r RepositoryState) MatchesBaseline(baseline RepositoryState) bool {
	return r.HasBaseline() && baseline.HasBaseline() &&
		*r.MetadataETag == *baseline.MetadataETag && *r.LastSuccessAt == *baseline.LastSuccessAt
}

// CanPublishObservation keeps late samples from overwriting newer metadata or reviving a withdrawn repository.
func (r RepositoryState) CanPublishObservation(requestName string, startedAt, observedAt int64) bool {
	return (r.LastSuccessAt == nil || *r.LastSuccessAt < observedAt) &&
		strings.EqualFold(r.FullName, requestName) &&
		(r.FirstNotFoundAt == nil || r.LastCheckedAt == nil || *r.LastCheckedAt < startedAt)
}

func (r RepositoryState) ConfirmedMissing(status int, observedAt int64) bool {
	return status == http.StatusGone ||
		(r.FirstNotFoundAt != nil && observedAt-*r.FirstNotFoundAt >= time.Hour.Milliseconds())
}

func RetryAt(id int64, attempts int, now time.Time) int64 {
	return now.Add(time.Duration(1<<min(attempts, 8))*time.Second + time.Duration(id%17)*time.Second).UnixMilli()
}

// SpreadRetry 错开共享额度恢复后的自动重试，恢复时刻仍是最早允许时间。
func SpreadRetry(until, id int64, manual bool) int64 {
	if until <= 0 || manual {
		return until
	}
	return until + (time.Duration(1+id%11) * time.Second).Milliseconds()
}
