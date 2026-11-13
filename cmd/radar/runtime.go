package main

import (
	"context"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/api"
	"github.com/lifei6671/github-radar/internal/collector"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/embedding"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/maintenance"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/collectionstore"
	"github.com/lifei6671/github-radar/internal/summary"
	"github.com/lifei6671/github-radar/internal/trending"
)

// runtimeController 发布不可变的服务快照，保存与发布按同一顺序完成。
type runtimeController struct {
	boot           config.Config
	store          settings.Store
	logger         *slog.Logger
	mu             sync.Mutex
	active         atomic.Pointer[runtimeGroup]
	restartPending atomic.Bool
	workers        *lifecycle.Workers
}

type runtimeGroup struct {
	services   api.RuntimeServices
	collector  *collector.Engine
	maintainer maintenance.Service
	embedding  *embedding.Worker
}

func newRuntimeController(ctx context.Context, db *gorm.DB, cfg config.Config, logger *slog.Logger) (*runtimeController, error) {
	r := &runtimeController{boot: cfg, store: settings.Store{DB: db}, logger: logger}
	initial, err := newRuntimeGroup(db, cfg, logger, nil)
	if err != nil {
		return nil, err
	}
	r.active.Store(initial)
	initial.logConfiguration()
	r.workers = lifecycle.NewWorkers(ctx, func(ctx context.Context) error { return r.active.Load().run(ctx) })
	return r, nil
}

func (r *runtimeController) Current() *api.RuntimeServices { return &r.active.Load().services }

func (r *runtimeController) RestartRequired() bool { return r.restartPending.Load() }

func (r *runtimeController) SaveRuntime(ctx context.Context, revision int64, values config.Runtime) (settings.Snapshot, error) {
	if err := values.Validate(); err != nil {
		return settings.Snapshot{}, &settings.Invalid{Err: err}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := r.prepare(values)
	if err != nil {
		return settings.Snapshot{}, err
	}
	snapshot, err := r.store.SaveRuntime(ctx, revision, values)
	if err != nil {
		return settings.Snapshot{}, err
	}
	r.publish(next, values)
	return snapshot, nil
}

// RestoreRuntime 在备份恢复验证完成后、业务请求与 Worker 重新开放前应用配置。
func (r *runtimeController) RestoreRuntime(values config.Runtime) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := r.prepare(values)
	if err == nil {
		r.publish(next, values)
	}
	return err
}

func (r *runtimeController) prepare(values config.Runtime) (*runtimeGroup, error) {
	cfg := r.boot.WithLiveRuntime(values)
	current := r.Current()
	if reflect.DeepEqual(cfg.RuntimeSettings(), current.Config.RuntimeSettings()) {
		return nil, nil
	}
	var queryVectors *embedding.QueryVectors
	if cfg.Embedding == current.Config.Embedding {
		queryVectors = current.QueryVectors
	}
	return newRuntimeGroup(r.store.DB, cfg, r.logger, queryVectors)
}

func (r *runtimeController) publish(next *runtimeGroup, saved config.Runtime) {
	r.restartPending.Store(r.boot.NeedsRestart(saved))
	if next != nil {
		r.active.Store(next)
		r.workers.Refresh()
		next.logConfiguration()
	}
}

func newCollector(db *gorm.DB, cfg config.Config, logger *slog.Logger, background bool) (*collector.Engine, *gh.Client, error) {
	token := cfg.GitHubToken()
	if token == "" {
		return nil, nil, nil
	}
	client, err := gh.New(db, cfg.GitHub, token)
	if err != nil {
		return nil, nil, err
	}
	queue := collectionstore.Queue{DB: db, Cfg: cfg.Scheduler}
	if background {
		queue.Pacing = storage.JobPacing{
			{Key: "search", Types: []string{consts.JobDiscoverSearch}, Gap: 2 * time.Second,
				Resource: gh.Search, Window: time.Minute, Budget: cfg.GitHub.SearchSoftBudgetPerMin, Burst: 1},
			{Key: "trending", Types: []string{consts.JobDiscoverTrending}, Gap: 2 * time.Second},
			{Key: "sampling", Types: []string{consts.JobRefreshRepo}, Gap: time.Second,
				Resource: gh.RepoRefreshResource, Window: time.Hour, Budget: cfg.GitHub.RepoSoftBudgetPerHour},
		}
	}
	return &collector.Engine{
		Queue:  queue,
		GitHub: client, Trending: trending.New(time.Duration(cfg.GitHub.TimeoutSeconds) * time.Second),
		Cfg: cfg, Logger: logger,
	}, client, nil
}

func newRuntimeGroup(db *gorm.DB, cfg config.Config, logger *slog.Logger, queryVectors *embedding.QueryVectors) (*runtimeGroup, error) {
	collectorEngine, client, err := newCollector(db, cfg, logger, true)
	if err != nil {
		return nil, err
	}
	pacing := storage.JobPacing{
		{Key: "readme", Types: []string{summary.JobType, summary.TranslationJob}, Gap: 2 * time.Second,
			Resource: gh.ReadmeCheckResource, Window: time.Hour, Budget: cfg.AI.Summary.GitHubBudget},
		{Key: "reports", Types: []string{summary.ReportJob}, Gap: 2 * time.Second},
	}
	summarizer := &summary.Engine{
		Queue:  summary.Queue{DB: db, AI: cfg.AI, LeaseSeconds: cfg.Scheduler.LeaseSeconds, Pacing: pacing},
		Logger: logger, Tick: time.Duration(cfg.Scheduler.TickMinutes) * time.Minute,
	}
	if client != nil {
		summarizer.GitHub = client
	}
	if cfg.AI.Enabled && len(cfg.MissingAISettings()) == 0 {
		chat := summary.NewChatProvider(cfg.AI, cfg.AIAPIKey())
		summarizer.WriteReport = chat.WriteReport
		if client != nil {
			summarizer.Provider, summarizer.Translate = chat, chat.Translate
		}
	}
	key := strings.TrimSpace(os.Getenv(cfg.Embedding.APIKeyEnv))
	if queryVectors == nil {
		queryVectors = embedding.NewQueryVectors(cfg.Embedding, key)
	}
	group := &runtimeGroup{
		services:  api.RuntimeServices{Config: cfg, Readmes: summarizer, QueryVectors: queryVectors},
		collector: collectorEngine, maintainer: maintenance.Service{DB: db, Cfg: cfg, Logger: logger},
	}
	if summarizer.WriteReport != nil {
		group.maintainer.PlanReports = summarizer.Queue.ScheduleReports
	}
	if cfg.Embedding.Enabled && key != "" {
		model := embedding.NewProvider(cfg.Embedding, key)
		group.embedding = &embedding.Worker{DB: db, Config: cfg.Embedding, Embed: model.Embed, Readme: summarizer.EmbeddingReadme, Logger: logger}
	}
	return group, nil
}

func (r *runtimeGroup) logConfiguration() {
	cfg, logger := r.services.Config, r.maintainer.Logger
	if r.collector == nil {
		logger.Warn("GitHub 采集待配置", "yamlField", "credentials.github_token", "env", cfg.GitHub.TokenEnv)
	}
	if missing := cfg.MissingAISettings(); len(missing) != 0 {
		logger.Warn("AI 摘要待配置", "missing", missing)
	}
	if cfg.Embedding.Enabled && r.embedding == nil {
		logger.Warn("相似项目向量待配置", "missingEnv", cfg.Embedding.APIKeyEnv)
	}
}

func (r *runtimeGroup) run(ctx context.Context) error {
	group, workerCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return r.maintainer.RunMetrics(workerCtx) })
	group.Go(func() error { return r.maintainer.RunMaintenance(workerCtx) })
	group.Go(func() error { return r.services.Readmes.Run(workerCtx) })
	if r.embedding != nil {
		group.Go(func() error { return r.embedding.Run(workerCtx) })
	}
	if r.collector != nil {
		group.Go(func() error { return r.collector.Run(workerCtx) })
	}
	return group.Wait()
}
