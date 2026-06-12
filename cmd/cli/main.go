package main

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
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
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {
	action := flag.String("action", "", "操作: scan, create-manifest, dump-database, list-series, list-media, search, stats")
	seriesID := flag.String("series-id", "", "用于 list-media 的系列ID")
	query := flag.String("query", "", "用于 search 的系列名关键词")
	mode := flag.String("mode", "", "扫描模式: full, classifyOnly")
	scanPath := flag.String("scan-path", "", "覆盖 scanner.scanPath")
	libraryPath := flag.String("library-path", "", "覆盖 scanner.finalLibraryPath")
	backupPath := flag.String("backup-path", "", "覆盖 scanner.backupPath")
	workers := flag.Int("workers", -1, "覆盖 scanner.workerCount；-1 表示使用配置")
	batchSize := flag.Int("batch-size", -1, "覆盖 scanner.batchSize；-1 表示使用配置")
	page := flag.Int("page", 1, "分页页码")
	limit := flag.Int("limit", 20, "每页数量")
	cursor := flag.String("cursor", "", "分页游标；用于 list-series, list-media, search 的下一页")
	flag.Parse()

	if strings.TrimSpace(*action) == "" {
		fmt.Fprintln(os.Stderr, "错误: 必须提供 -action 参数。")
		flag.Usage()
		os.Exit(2)
	}

	if err := config.LoadConfig("."); err != nil {
		log.Fatalf("FATAL: 无法加载配置: %v", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	ctx := context.Background()
	cfg := *config.C
	applyCLIOverrides(&cfg, *mode, *scanPath, *libraryPath, *backupPath, *workers, *batchSize)
	if err := config.ValidateConfig(cfg); err != nil {
		slog.Error("配置无效", "error", err)
		os.Exit(2)
	}

	switch *action {
	case "scan":
		if err := runScan(ctx, &cfg); err != nil {
			slog.Error("扫描失败", "error", err)
			os.Exit(1)
		}

	case "create-manifest":
		if err := runManifest(ctx, &cfg); err != nil {
			slog.Error("生成文件清单失败", "error", err)
			os.Exit(1)
		}

	case "dump-database":
		if err := runDatabaseBackup(ctx, &cfg); err != nil {
			slog.Error("数据库备份失败", "error", err)
			os.Exit(1)
		}

	case "list-series":
		if err := withDB(ctx, &cfg, func(db database.Store) error {
			return listSeries(ctx, db, *page, *limit, *cursor)
		}); err != nil {
			slog.Error("获取系列列表失败", "error", err)
			os.Exit(1)
		}

	case "list-media":
		if err := withDB(ctx, &cfg, func(db database.Store) error {
			return listMedia(ctx, db, *seriesID, *page, *limit, *cursor)
		}); err != nil {
			slog.Error("获取媒体列表失败", "error", err)
			os.Exit(1)
		}

	case "search":
		if err := withDB(ctx, &cfg, func(db database.Store) error {
			return searchSeries(ctx, db, *query, *page, *limit, *cursor)
		}); err != nil {
			slog.Error("搜索系列失败", "error", err)
			os.Exit(1)
		}

	case "stats":
		if err := withDB(ctx, &cfg, func(db database.Store) error {
			return printStats(ctx, db)
		}); err != nil {
			slog.Error("统计失败", "error", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "错误: 未知 action %q\n", *action)
		flag.Usage()
		os.Exit(2)
	}
}

func applyCLIOverrides(cfg *config.Config, mode, scanPath, libraryPath, backupPath string, workers, batchSize int) {
	if mode != "" {
		cfg.Scanner.Mode = mode
	}
	if scanPath != "" {
		cfg.Scanner.ScanPath = scanPath
	}
	if libraryPath != "" {
		cfg.Scanner.FinalLibraryPath = libraryPath
	}
	if backupPath != "" {
		cfg.Scanner.BackupPath = backupPath
	}
	if workers >= 0 {
		cfg.Scanner.WorkerCount = workers
	}
	if batchSize >= 0 {
		cfg.Scanner.BatchSize = batchSize
	}
}

func runScan(ctx context.Context, cfg *config.Config) error {
	var db database.Store
	if scanRequiresDatabase(cfg.Scanner.Mode) {
		db = mustConnectDB(ctx, cfg)
		defer closeDB(ctx, db)
	}
	orchestrator, err := scanner.NewOrchestrator(cfg, db)
	if err != nil {
		return err
	}
	slog.Info("开始扫描", "mode", cfg.Scanner.Mode, "scanPath", cfg.Scanner.ScanPath, "workers", cfg.Scanner.WorkerCount)
	return orchestrator.RunFullScanContext(ctx, cfg.Scanner)
}

func scanRequiresDatabase(mode string) bool {
	return strings.TrimSpace(mode) != "classifyOnly"
}

func runManifest(ctx context.Context, cfg *config.Config) error {
	maint, err := maintenance.NewMaintenance(cfg.Logger.Path, cfg.Scanner.WorkerCount)
	if err != nil {
		return err
	}
	defer closeMaintenance(maint)
	libraryPath, err := filepath.Abs(cfg.Scanner.FinalLibraryPath)
	if err != nil {
		return err
	}
	backupPath, err := filepath.Abs(cfg.Scanner.BackupPath)
	if err != nil {
		return err
	}
	slog.Info("生成文件清单", "library", libraryPath, "output", backupPath)
	return maint.GenerateFileManifest(ctx, libraryPath, backupPath)
}

func runDatabaseBackup(ctx context.Context, cfg *config.Config) error {
	maint, err := maintenance.NewMaintenance(cfg.Logger.Path, cfg.Scanner.WorkerCount)
	if err != nil {
		return err
	}
	defer closeMaintenance(maint)
	backupPath, err := filepath.Abs(cfg.Scanner.BackupPath)
	if err != nil {
		return err
	}
	slog.Info("执行数据库备份", "database", cfg.Database.Name, "output", backupPath)
	return maint.BackupDatabase(ctx, cfg.Database.URI, cfg.Database.Name, backupPath)
}

func closeMaintenance(maint maintenance.Maintenance) {
	if maint == nil {
		return
	}
	if err := maint.Close(); err != nil {
		slog.Error("关闭维护模块日志失败", "error", err)
	}
}

func withDB(ctx context.Context, cfg *config.Config, run func(database.Store) error) error {
	db := mustConnectDB(ctx, cfg)
	defer closeDB(ctx, db)
	return run(db)
}

func mustConnectDB(ctx context.Context, cfg *config.Config) database.Store {
	db, err := mongo.NewStore(ctx, cfg)
	if err != nil {
		slog.Error("FATAL: 无法连接到数据库", "error", err)
		os.Exit(1)
	}
	if err := db.EnsureIndexes(ctx); err != nil {
		_ = db.Close(context.Background())
		slog.Error("FATAL: 无法创建/验证数据库索引", "error", err)
		os.Exit(1)
	}
	return db
}

func closeDB(ctx context.Context, db database.Store) {
	if db == nil {
		return
	}
	if err := db.Close(ctx); err != nil {
		slog.Error("关闭数据库连接失败", "error", err)
	}
}

func listSeries(ctx context.Context, db database.Store, page, limit int, cursor string) error {
	page, limit = normalizePagination(page, limit)
	if strings.TrimSpace(cursor) != "" {
		page = 1
	}
	series, total, nextCursor, err := fetchCursorPage(ctx, page, cursor, func(cursor string) ([]models.Series, int64, string, error) {
		return db.Series().ListCursor(ctx, cursor, limit)
	})
	if err != nil {
		return err
	}
	fmt.Printf("系列总数: %d  页码: %d  每页: %d\n", total, page, limit)
	printNextCursor(nextCursor)
	for _, item := range series {
		fmt.Printf("%s  media=%d  name=%s\n  path=%s\n", item.ID.Hex(), item.ImageCount, item.Name, item.Path)
	}
	return nil
}

func listMedia(ctx context.Context, db database.Store, seriesID string, page, limit int, cursor string) error {
	if strings.TrimSpace(seriesID) == "" {
		return fmt.Errorf("list-media 需要 -series-id")
	}
	objID, parseErr := primitive.ObjectIDFromHex(seriesID)
	if parseErr != nil {
		return fmt.Errorf("无效 series-id: %w", parseErr)
	}
	page, limit = normalizePagination(page, limit)
	if strings.TrimSpace(cursor) != "" {
		page = 1
	}
	media, total, nextCursor, err := fetchCursorPage(ctx, page, cursor, func(cursor string) ([]models.Image, int64, string, error) {
		return db.Images().ListBySeriesIDCursor(ctx, objID, cursor, limit)
	})
	if err != nil {
		return err
	}
	fmt.Printf("媒体总数: %d  页码: %d  每页: %d\n", total, page, limit)
	printNextCursor(nextCursor)
	for _, item := range media {
		fmt.Printf("%s  type=%s  file=%s\n  path=%s\n  sha256=%s\n",
			item.ID.Hex(), mediaType(item), item.FileName, item.FilePath, item.FileHash)
	}
	return nil
}

func searchSeries(ctx context.Context, db database.Store, query string, page, limit int, cursor string) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("search 需要 -query")
	}
	page, limit = normalizePagination(page, limit)
	if strings.TrimSpace(cursor) != "" {
		page = 1
	}
	series, total, nextCursor, err := fetchCursorPage(ctx, page, cursor, func(cursor string) ([]models.Series, int64, string, error) {
		return db.Series().SearchByNameCursor(ctx, query, cursor, limit)
	})
	if err != nil {
		return err
	}
	fmt.Printf("匹配系列: %d  页码: %d  每页: %d\n", total, page, limit)
	printNextCursor(nextCursor)
	for _, item := range series {
		fmt.Printf("%s  media=%d  name=%s\n  path=%s\n", item.ID.Hex(), item.ImageCount, item.Name, item.Path)
	}
	return nil
}

type cursorPageFunc[T any] func(cursor string) ([]T, int64, string, error)

func fetchCursorPage[T any](ctx context.Context, page int, initialCursor string, fetch cursorPageFunc[T]) ([]T, int64, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, "", err
	}
	cursor := strings.TrimSpace(initialCursor)
	var (
		items      []T
		total      int64
		nextCursor string
		err        error
	)
	for currentPage := 1; currentPage <= page; currentPage++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, "", err
		}
		items, total, nextCursor, err = fetch(cursor)
		if err != nil {
			return nil, 0, "", err
		}
		if currentPage == page {
			return items, total, nextCursor, nil
		}
		if nextCursor == "" {
			return nil, total, "", nil
		}
		cursor = nextCursor
	}
	return items, total, nextCursor, nil
}

func printNextCursor(cursor string) {
	if cursor != "" {
		fmt.Printf("nextCursor: %s\n", cursor)
	}
}

func printStats(ctx context.Context, db database.Store) error {
	diagnostics, err := db.Diagnostics(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("series=%d seriesWithThumbnail=%d seriesWithThumbnailFlag=%d seriesMissingThumbnailFlag=%d media=%d imageMedia=%d imagesWithPHash=%d imagesWithPHashBuckets=%d imagesMissingPHashBuckets=%d\n",
		diagnostics.SeriesCount,
		diagnostics.SeriesWithThumbnail,
		diagnostics.SeriesWithThumbnailFlag,
		diagnostics.SeriesMissingThumbnailFlag,
		diagnostics.MediaCount,
		diagnostics.ImageMediaCount,
		diagnostics.ImagesWithPHash,
		diagnostics.ImagesWithPHashBuckets,
		diagnostics.ImagesMissingPHashBuckets,
	)
	fmt.Printf("seriesIndexes=%s\n", strings.Join(diagnostics.SeriesIndexes, ","))
	fmt.Printf("imageIndexes=%s\n", strings.Join(diagnostics.ImageIndexes, ","))
	return nil
}

func normalizePagination(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 1000 {
		limit = 1000
	}
	return page, limit
}

func mediaType(image models.Image) string {
	if image.MediaType == "" {
		return "image"
	}
	return image.MediaType
}
