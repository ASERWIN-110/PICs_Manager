package main

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/api"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/logger"
	"PICs_Manager/pkg/runstate"
	"PICs_Manager/pkg/scanner"
	"PICs_Manager/pkg/security"
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := config.LoadConfig("."); err != nil {
		log.Fatalf("FATAL: 无法加载配置: %v", err)
	}
	if err := logger.InitLogger(); err != nil {
		log.Fatalf("FATAL: 无法初始化日志: %v", err)
	}
	slog.Info("应用启动")
	defer slog.Info("应用关闭")

	appCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var db database.Store
	var err error
	db, err = mongo.NewStore(appCtx, config.C)
	if err != nil {
		slog.Error("FATAL: 无法连接到数据库", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := db.Close(context.Background()); err != nil {
			slog.Error("关闭数据库连接失败", "error", err)
		}
	}()
	if err := db.EnsureIndexes(appCtx); err != nil {
		_ = db.Close(context.Background())
		slog.Error("FATAL: 无法创建/验证数据库索引", "error", err)
		os.Exit(1)
	}
	slog.Info("数据库连接成功并已验证索引")

	orchestrator, err := scanner.NewOrchestrator(config.C, db)
	if err != nil {
		_ = db.Close(context.Background())
		slog.Error("FATAL: 无法创建扫描与处理协调器", "error", err)
		os.Exit(1)
	}
	slog.Info("扫描器协调器创建成功")

	runStore, err := runstate.NewStore(config.C.Logger.Path)
	if err != nil {
		_ = db.Close(context.Background())
		slog.Error("FATAL: 无法初始化运行状态存储", "error", err)
		os.Exit(1)
	}
	taskManager := task.NewManagerWithRunStore(orchestrator, config.C, runStore)
	if err := taskManager.RecoverUnfinishedRuns(appCtx); err != nil {
		_ = db.Close(context.Background())
		slog.Error("FATAL: 无法恢复运行状态", "error", err)
		os.Exit(1)
	}
	if removed, err := runStore.Prune(appCtx, config.C.RunRetention.MaxRuns, time.Duration(config.C.RunRetention.MaxAgeDays)*24*time.Hour); err != nil {
		_ = db.Close(context.Background())
		slog.Error("FATAL: 无法清理旧运行状态", "error", err)
		os.Exit(1)
	} else if removed > 0 {
		slog.Info("已清理旧运行状态", "removed", removed)
	}
	slog.Info("任务管理器创建成功")

	var authStore *security.Store
	if config.C.Security.Enabled {
		authStore, err = security.NewStore(config.SecurityStorePath(config.C))
		if err != nil {
			_ = db.Close(context.Background())
			slog.Error("FATAL: 无法初始化设备绑定存储", "error", err)
			os.Exit(1)
		}
		slog.Info("设备绑定已启用", "store", config.SecurityStorePath(config.C), "requireViewerForRead", config.C.Security.RequireViewerForRead)
	}

	router := api.RegisterRoutesWithServices(taskManager, db, runStore, authStore)

	server := &http.Server{
		Addr:         config.C.Server.Port,
		Handler:      router,
		ReadTimeout:  config.C.Server.Timeout,
		WriteTimeout: config.C.Server.Timeout,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("HTTP服务器正在启动...", "地址", config.C.Server.Port)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()
	startScheduler(appCtx, taskManager)

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("无法启动HTTP服务器", "error", err)
			_ = db.Close(context.Background())
			os.Exit(1)
		}
	case <-appCtx.Done():
		stopSignals()
		slog.Info("收到关闭信号，正在停止 HTTP 服务器")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP服务器关闭失败", "error", err)
			return
		}
		if err := taskManager.Shutdown(shutdownCtx); err != nil {
			slog.Error("任务管理器关闭失败", "error", err)
		}
		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP服务器关闭时返回错误", "error", err)
		}
	}
}

func startScheduler(ctx context.Context, taskManager *task.Manager) {
	if config.C == nil || taskManager == nil || !config.C.Scheduler.Enabled {
		return
	}
	interval, err := time.ParseDuration(config.C.Scheduler.Interval)
	if err != nil || interval <= 0 {
		slog.Error("调度器未启动：scheduler.interval 无效", "interval", config.C.Scheduler.Interval, "error", err)
		return
	}
	mode := strings.TrimSpace(config.C.Scheduler.Mode)
	if mode == "" {
		mode = strings.TrimSpace(config.C.Scanner.Mode)
	}
	if mode == "" {
		mode = "full"
	}
	slog.Info("调度器已启动", "interval", interval, "mode", mode, "runOnStartup", config.C.Scheduler.RunOnStartup)
	go func() {
		if config.C.Scheduler.RunOnStartup {
			startScheduledScan(taskManager, mode)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				startScheduledScan(taskManager, mode)
			}
		}
	}()
}

func startScheduledScan(taskManager *task.Manager, fallbackMode string) {
	if config.C == nil {
		return
	}
	mode := strings.TrimSpace(config.C.Scheduler.Mode)
	if mode == "" {
		mode = fallbackMode
	}
	path := strings.TrimSpace(config.C.Scanner.ScanPath)
	if path == "" {
		slog.Warn("跳过调度扫描：scanner.scanPath 为空")
		return
	}
	taskID, err := taskManager.StartNewScanTask(path, mode)
	if err != nil {
		if errors.Is(err, task.ErrTaskConflict) {
			slog.Info("跳过调度扫描：已有维护任务运行", "error", err)
			return
		}
		slog.Error("启动调度扫描失败", "error", err)
		return
	}
	slog.Info("调度扫描已启动", "taskID", taskID, "path", path, "mode", mode)
}
