// 文件: cmd/manager-server/main.go
package main

import (
	"PICs_Manager/config" // 使用您根目录下的config包
	"PICs_Manager/internal/api"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/logger"
	"PICs_Manager/pkg/scanner"
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	// --- 1. 初始化 ---
	if err := config.LoadConfig("."); err != nil {
		log.Fatalf("FATAL: 无法加载配置: %v", err)
	}
	// [修正] 根据错误提示“实参过多”，InitLogger很可能不需要参数，
	// 而是直接在内部使用全局的 config.C。
	if err := logger.InitLogger(); err != nil {
		log.Fatalf("FATAL: 无法初始化日志: %v", err)
	}
	slog.Info("应用启动")
	defer slog.Info("应用关闭")

	// --- 2. 连接数据库 ---
	var db database.Store
	var err error
	// [修正] 根据错误提示，NewStore 函数期望接收整个配置对象 (*config.Config)，
	// 而不是其中的一部分 (config.C.Database)。
	db, err = mongo.NewStore(context.Background(), config.C)
	if err != nil {
		slog.Error("FATAL: 无法连接到数据库", "error", err)
		os.Exit(1)
	}
	if err := db.EnsureIndexes(context.Background()); err != nil {
		slog.Error("FATAL: 无法创建/验证数据库索引", "error", err)
		os.Exit(1)
	}
	slog.Info("数据库连接成功并已验证索引")

	// --- 3. 创建核心服务实例 ---
	// 使用全局 config.C
	orchestrator, err := scanner.NewOrchestrator(config.C, db)
	if err != nil {
		slog.Error("FATAL: 无法创建扫描与处理协调器", "error", err)
		os.Exit(1)
	}
	slog.Info("扫描器协调器创建成功")

	// 将创建好的扫描器实例和配置实例注入到任务管理器中
	taskManager := task.NewManager(orchestrator, config.C)
	slog.Info("任务管理器创建成功")

	// --- 4. 设置并启动HTTP服务器 ---
	router := api.RegisterRoutes(taskManager, db)

	server := &http.Server{
		Addr:         config.C.Server.Port,
		Handler:      router,
		ReadTimeout:  config.C.Server.Timeout, // 直接使用 time.Duration 类型
		WriteTimeout: config.C.Server.Timeout, // 直接使用 time.Duration 类型
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("HTTP服务器正在启动...", "地址", config.C.Server.Port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("无法启动HTTP服务器", "error", err)
		os.Exit(1)
	}
}
