package port

import (
	"context"
	"time"

	sdk "github.com/google/go-github/v88/github"

	"github.com/lifei6671/github-radar/internal/domain/collection"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/trending"
)

type Queue interface {
	Initialized(context.Context) (bool, error)
	IsBusy(error) bool
	Rebalance(context.Context, time.Time) error
	Schedule(context.Context, time.Time) error
	Claim(context.Context, time.Time) (*storage.Job, error)
	Renew(context.Context, storage.Job, time.Time) error
	Complete(context.Context, storage.Job, Change) error
	Fail(context.Context, storage.Job, error, time.Time) error
	Interrupt(context.Context, storage.Job) error
	Repository(context.Context, int64) (storage.Repository, error)
	KnownRepository(context.Context, int64, string) (storage.Repository, bool, error)
	IsBlocked(context.Context, int64, string) (bool, error)
	TrendingBatch(context.Context, string, string) (collection.TrendingBatch, bool, error)
}

// Change is applied only inside the transaction that validates and completes the lease.
type Change func(Writer) error

type Writer interface {
	SaveRefresh(Refresh) error
	SaveSearch(Search) error
	SaveTrending(Trending) error
}

type Refresh struct {
	Baseline              storage.Repository
	Plan                  collection.Payload
	Result                gh.RepositoryResult
	FailureStatus         int
	StartedAt, ObservedAt time.Time
}

type Search struct {
	Parent       storage.Job
	Plan         collection.Payload
	Repositories []*sdk.Repository
	Next         []collection.Payload
	ObservedAt   int64
}

type Trending struct {
	Plan       collection.Payload
	Entries    []trending.Entry
	ObservedAt int64
}
