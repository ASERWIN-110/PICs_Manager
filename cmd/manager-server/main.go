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
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	slog.Info("任务管理器创建成功")

	router := api.RegisterRoutesWithRunStore(taskManager, db, runStore)

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
