package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"

	"github.com/lifei6671/github-radar/internal/api"
	"github.com/lifei6671/github-radar/internal/backup"
	"github.com/lifei6671/github-radar/internal/collector"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/maintenance"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
	"github.com/lifei6671/github-radar/internal/trending"
	"github.com/lifei6671/github-radar/internal/webui"
	"github.com/lifei6671/github-radar/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], logger)
	stop()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		logger.Error("雷达运行失败", "error", err)
		os.Exit(1)
	}
}

// run 在监听端口前完成配置校验和迁移，也支持单独执行离线迁移。
func run(ctx context.Context, args []string, logger *slog.Logger) error {
	command := consts.CommandServer
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	if command != consts.CommandServer && command != consts.CommandMigrate && command != consts.CommandCollectOnce && command != consts.CommandMaintenanceOnce && command != consts.CommandBackup {
		return fmt.Errorf("未知命令 %q，请使用 server、migrate、collect-once、maintenance-once 或 backup", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "", "YAML 配置文件路径，省略时使用默认配置")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("命令仅接受 -config 选项")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var adminToken string
	if command == consts.CommandServer || command == consts.CommandBackup {
		adminToken, err = cfg.AdminToken()
		if err != nil {
			return err
		}
	}
	if command == consts.CommandBackup {
		return runBackup(ctx, cfg.Server.Listen, adminToken, logger)
	}

	if command == consts.CommandMigrate {
		pool, applied, err := openMigrated(ctx, cfg.Storage.Path)
		if err != nil {
			return err
		}
		defer pool.Close()
		logger.Info("SQLite 迁移完成", "applied", applied)
		return nil
	}

	cfg, applied, err := prepareDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	logger.Info("SQLite 迁移完成", "applied", applied)

	pool, err := storage.Open(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		return fmt.Errorf("初始化 GORM: %w", err)
	}
	maintainer := maintenance.Service{DB: db, Cfg: cfg, Logger: logger}
	if command == consts.CommandMaintenanceOnce {
		return maintainer.Once(ctx, time.Now().UTC())
	}
	githubToken := strings.TrimSpace(os.Getenv(cfg.GitHub.TokenEnv))
	var engine *collector.Engine
	var readmes summary.GitHubSource
	if githubToken == "" {
		if command == consts.CommandCollectOnce {
			return fmt.Errorf("采集需要环境变量 %s", cfg.GitHub.TokenEnv)
		}
		logger.Warn("GitHub 采集待配置", "missingEnv", cfg.GitHub.TokenEnv)
	} else {
		client, err := gh.New(db, cfg.GitHub, githubToken)
		if err != nil {
			return err
		}
		readmes = client
		engine = &collector.Engine{
			Queue:  collector.Queue{DB: db, Cfg: cfg.Scheduler},
			GitHub: client, Trending: trending.New(time.Duration(cfg.GitHub.TimeoutSeconds) * time.Second),
			Cfg: cfg, Logger: logger,
		}
	}
	if command == consts.CommandCollectOnce {
		return engine.CollectOnce(ctx)
	}
	var provider summary.Provider
	var translate func(context.Context, string) summary.ModelResult
	if missing := cfg.MissingAISettings(); len(missing) != 0 {
		logger.Warn("AI 摘要待配置", "missing", missing)
	} else if cfg.AI.Enabled && readmes != nil {
		chat := summary.NewChatProvider(cfg.AI, strings.TrimSpace(os.Getenv(cfg.AI.APIKeyEnv)))
		provider, translate = chat, chat.Translate
	}
	summarizer := summary.Engine{Queue: summary.Queue{DB: db, AI: cfg.AI, LeaseSeconds: cfg.Scheduler.LeaseSeconds},
		GitHub: readmes, Provider: provider, Translate: translate, Logger: logger, Tick: time.Duration(cfg.Scheduler.TickMinutes) * time.Minute}

	group, serviceCtx := errgroup.WithContext(ctx)
	workers := lifecycle.NewWorkers(serviceCtx, func(ctx context.Context) error {
		group, workerCtx := errgroup.WithContext(ctx)
		group.Go(func() error { return maintainer.RunMetrics(workerCtx) })
		group.Go(func() error { return maintainer.RunMaintenance(workerCtx) })
		group.Go(func() error { return summarizer.Run(workerCtx) })
		if engine != nil {
			group.Go(func() error { return engine.Run(workerCtx) })
		}
		return group.Wait()
	})
	manager, err := backup.New(serviceCtx, pool, db, cfg, workers)
	if err != nil {
		return fmt.Errorf("初始化备份服务: %w", err)
	}
	if manager.Initialized() {
		workers.Resume()
	}
	gin.SetMode(gin.ReleaseMode)
	router := api.NewRouter(db, pool, adminToken, logger, cfg, &summarizer, manager)
	webui.Register(router, os.DirFS("web/dist"))
	server := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      router,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeoutSeconds) * time.Second,
	}
	group.Go(func() error { return serve(serviceCtx, server, logger) })
	group.Go(func() error { return manager.Run(serviceCtx) })
	group.Go(func() error {
		select {
		case <-serviceCtx.Done():
			return nil
		case err := <-workers.Failure():
			return err
		}
	})
	err = group.Wait()
	manager.Wait()
	workers.Wait()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// prepareDatabase 用临时连接完成迁移和首次导入，再由调用方用数据库参数打开业务连接池。
func prepareDatabase(ctx context.Context, cfg config.Config) (config.Config, int, error) {
	pool, applied, err := openMigrated(ctx, cfg.Storage.Path)
	if err != nil {
		return config.Config{}, 0, err
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		return config.Config{}, 0, fmt.Errorf("初始化配置连接: %w", err)
	}
	snapshot, err := (settings.Store{DB: db}).Initialize(ctx, cfg)
	if err != nil {
		return config.Config{}, 0, fmt.Errorf("加载数据库配置: %w", err)
	}
	return cfg.WithRuntime(snapshot.Settings), applied, nil
}

// openMigrated 只升级表结构，离线迁移不会消耗首次业务配置导入机会。
func openMigrated(ctx context.Context, path string) (*sql.DB, int, error) {
	pool, err := storage.OpenBootstrap(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	applied, err := migrations.Up(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, 0, err
	}
	return pool, applied, nil
}

// serve 收到退出信号时等待 HTTP 请求结束，再由调用方关闭数据库。
func serve(ctx context.Context, server *http.Server, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("监听 HTTP 地址: %w", err)
	}
	logger.Info("HTTP 服务已启动", "listen", listener.Addr().String())

	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case err = <-done:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), server.WriteTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("停止 HTTP 服务: %w", err)
		}
		err = <-done
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP 服务运行异常: %w", err)
	}
	return nil
}
