package main

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/maintenance"
	"PICs_Manager/pkg/scanner"
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {
	// --- 1. 定义命令行参数 ---
	action := flag.String("action", "", "要执行的操作: scan, list-series, list-images, search")
	seriesID := flag.String("series-id", "", "用于 list-images 或其他系列特定操作的ID")
	query := flag.String("query", "", "用于 search 操作的搜索关键词")
	page := flag.Int("page", 1, "分页页码")
	limit := flag.Int("limit", 20, "每页数量")

	flag.Parse()

	if *action == "" {
		fmt.Println("错误: 必须提供 -action 参数。")
		flag.Usage() // 打印所有可用参数的帮助信息
		os.Exit(1)
	}

	// --- 2. 初始化应用核心组件 ---
	// 假设 config.yaml 在项目根目录
	if err := config.LoadConfig("."); err != nil {
		log.Fatalf("FATAL: 无法加载配置: %v", err)
	}
	// slog 的初始化可以更简单，这里我们直接使用默认配置
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	var db database.Store
	var err error
	db, err = mongo.NewStore(context.Background(), config.C)
	if err != nil {
		slog.Error("FATAL: 无法连接到数据库", "error", err)
		os.Exit(1)
	}
	if err := db.EnsureIndexes(context.Background()); err != nil {
		slog.Error("FATAL: 无法创建/验证数据库索引", "error", err)
		os.Exit(1)
	}

	orchestrator, err := scanner.NewOrchestrator(config.C, db)
	if err != nil {
		slog.Error("FATAL: 无法创建扫描与处理协调器", "error", err)
		os.Exit(1)
	}

	maintenanceModule, err := maintenance.NewMaintenance(config.C.Logger.Path, config.C.Scanner.WorkerCount)
	if err != nil {
		slog.Error("FATAL: 无法创建维护模块", "error", err)
		os.Exit(1)
	}

	// --- 3. 根据 action 参数执行相应的功能 ---
	ctx := context.Background()
	switch *action {
	case "scan":
		slog.Info("开始执行完整的扫描、整理、入库流水线任务...")
		orchestrator.RunFullScan(config.C.Scanner)
		slog.Info("批量导入已执行完毕。")

	case "create-manifest":
		slog.Info("开始生成文件系统清单...")
		finalLibraryPath, _ := filepath.Abs(config.C.Scanner.FinalLibraryPath)
		backupPath, _ := filepath.Abs(config.C.Scanner.BackupPath)
		if err := maintenanceModule.GenerateFileManifest(ctx, finalLibraryPath, backupPath); err != nil {
			slog.Error("生成文件清单失败", "error", err)
		} else {
			slog.Info("文件清单生成成功！")
		}

	case "dump-database":
		slog.Info("开始执行数据库压缩备份...")
		backupPath, _ := filepath.Abs(config.C.Scanner.BackupPath)
		if err := maintenanceModule.BackupDatabase(ctx, config.C.Database.URI, config.C.Database.Name, backupPath); err != nil {
			slog.Error("数据库备份失败", "error", err)
		} else {
			slog.Info("数据库备份成功！")
		}

	case "list-series":
		fmt.Println("--- 获取系列列表 ---")
		series, total, err := db.Series().List(ctx, *page, *limit)
		if err != nil {
			slog.Error("获取系列列表失败", "error", err)
			return
		}
		fmt.Printf("总共找到 %d 个系列 (正在显示第 %d 页，每页 %d 个):\n", total, *page, *limit)
		for _, s := range series {
			fmt.Printf("ID: %s\n  Name: %s\n  Path: %s\n  ImageCount: %d\n  Thumbnail: %t\n\n",
				s.ID.Hex(), s.Name, s.Path, s.ImageCount, s.Thumbnail != "")
		}

	case "list-images":
		if *seriesID == "" {
			fmt.Println("错误: list-images 操作需要提供 -series-id 参数。")
			return
		}
		objID, err := primitive.ObjectIDFromHex(*seriesID)
		if err != nil {
			fmt.Printf("错误: 无效的 series-id 格式: %v\n", err)
			return
		}
		fmt.Printf("--- 获取系列 '%s' 下的图片列表 ---\n", *seriesID)
		images, total, err := db.Images().ListBySeriesID(ctx, objID, *page, *limit)
		if err != nil {
			slog.Error("获取图片列表失败", "error", err)
			return
		}
		fmt.Printf("总共找到 %d 张图片 (正在显示第 %d 页，每页 %d 个):\n", total, *page, *limit)
		for _, img := range images {
			fmt.Printf("  ID: %s, FileName: %s\n", img.ID.Hex(), img.FileName)
		}

	case "search":
		if *query == "" {
			fmt.Println("错误: search 操作需要提供 -query 参数。")
			return
		}
		fmt.Printf("--- 搜索系列名包含 '%s' 的系列 ---\n", *query)
		// 注意：我们之前实现的是按图片文件名搜索，这里改为按系列名搜索可能更有用
		series, total, err := db.Series().SearchByName(ctx, *query, *page, *limit)
		if err != nil {
			slog.Error("搜索系列失败", "error", err)
			return
		}
		fmt.Printf("总共找到 %d 个匹配的系列 (正在显示第 %d 页，每页 %d 个):\n", total, *page, *limit)
		for _, s := range series {
			fmt.Printf("  ID: %s, Name: %s, Path: %s\n", s.ID.Hex(), s.Name, s.Path)
		}

	default:
		fmt.Printf("错误: 未知的 action '%s'\n", *action)
		flag.Usage()
	}
}
