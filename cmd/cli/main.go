package main

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/maintenance"
	"PICs_Manager/pkg/runstate"
	"PICs_Manager/pkg/scanner"
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {
	action := flag.String("action", "", "操作: scan, create-manifest, health-report, dump-database, rebuild-database, list-series, list-media, search, stats, list-runs, show-run, run-journal, verify-run")
	seriesID := flag.String("series-id", "", "用于 list-media 的系列ID")
	mediaTypeFlag := flag.String("media-type", "image", "用于 list-media 的媒体类型，例如 image, video, audio, text")
	runID := flag.String("run-id", "", "用于 show-run, run-journal 的运行ID")
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

	case "health-report":
		if err := runHealthReport(ctx, &cfg); err != nil {
			slog.Error("生成目录健康报告失败", "error", err)
			os.Exit(1)
		}

	case "dump-database":
		if err := runDatabaseBackup(ctx, &cfg); err != nil {
			slog.Error("数据库备份失败", "error", err)
			os.Exit(1)
		}

	case "rebuild-database":
		if err := runDatabaseRebuild(ctx, &cfg); err != nil {
			slog.Error("数据库补齐失败", "error", err)
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
			return listMedia(ctx, db, *seriesID, *mediaTypeFlag, *page, *limit, *cursor)
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

	case "list-runs":
		if err := listRuns(ctx, &cfg, *limit); err != nil {
			slog.Error("获取运行记录失败", "error", err)
			os.Exit(1)
		}

	case "show-run":
		if err := showRun(ctx, &cfg, *runID); err != nil {
			slog.Error("获取运行记录失败", "error", err)
			os.Exit(1)
		}

	case "run-journal":
		if err := printRunJournal(ctx, &cfg, *runID); err != nil {
			slog.Error("获取运行日志失败", "error", err)
			os.Exit(1)
		}

	case "verify-run":
		if err := verifyRunRecovery(ctx, &cfg, *runID); err != nil {
			slog.Error("运行恢复校验失败", "error", err)
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
	runStore, err := runstate.NewStore(cfg.Logger.Path)
	if err != nil {
		return err
	}
	runID := uuid.New().String()
	if err := runStore.AcquireLock(runID); err != nil {
		return err
	}
	defer func() {
		if err := runStore.ReleaseLock(); err != nil {
			slog.Error("释放维护任务锁失败", "runID", runID, "error", err)
		}
	}()
	if err := runStore.Create(ctx, runstate.Run{
		ID:       runID,
		Status:   runstate.StatusRunning,
		Mode:     cfg.Scanner.Mode,
		Phase:    "running",
		ScanPath: cfg.Scanner.ScanPath,
		Counts:   map[string]int64{},
	}); err != nil {
		return err
	}
	_ = runStore.AppendEvent(ctx, runstate.Event{
		RunID:      runID,
		Phase:      "running",
		Action:     "cli_scan_started",
		Status:     string(runstate.StatusRunning),
		Checkpoint: true,
	})
	ctx = runstate.WithRecorder(ctx, runstate.Recorder{Store: runStore, RunID: runID})
	slog.Info("开始扫描", "runID", runID, "mode", cfg.Scanner.Mode, "scanPath", cfg.Scanner.ScanPath, "workers", cfg.Scanner.WorkerCount)
	if err := orchestrator.RunFullScanContext(ctx, cfg.Scanner); err != nil {
		endTime := time.Now()
		_ = runStore.Update(context.Background(), runID, func(run *runstate.Run) {
			run.Status = runstate.StatusFailed
			run.EndedAt = &endTime
			run.ErrorSummary = append(run.ErrorSummary, err.Error())
		})
		_ = runStore.AppendEvent(context.Background(), runstate.Event{
			RunID:      runID,
			Phase:      "finished",
			Action:     "cli_scan_finished",
			Status:     string(runstate.StatusFailed),
			Error:      err.Error(),
			Checkpoint: true,
		})
		return err
	}
	endTime := time.Now()
	_ = runStore.AppendEvent(context.Background(), runstate.Event{
		RunID:      runID,
		Phase:      "finished",
		Action:     "cli_scan_finished",
		Status:     string(runstate.StatusCompleted),
		Checkpoint: true,
	})
	return runStore.Update(context.Background(), runID, func(run *runstate.Run) {
		run.Status = runstate.StatusCompleted
		run.EndedAt = &endTime
	})
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

func runHealthReport(ctx context.Context, cfg *config.Config) error {
	unsupportedCount, err := scanner.CountUnsupportedFiles(ctx, cfg.Scanner.ScanPath, cfg.Scanner)
	if err != nil {
		return err
	}
	report, err := scanner.WriteDirectoryHealthReport(
		ctx,
		cfg.Scanner.FinalLibraryPath,
		cfg.Scanner.QuarantinePath,
		cfg.Scanner.BackupPath,
		cfg.Scanner.MaxFilesPerDir,
		fmt.Sprintf("manual-%d", time.Now().UnixNano()),
		unsupportedCount,
	)
	if err != nil {
		return err
	}
	fmt.Printf("health report generated directories=%d files=%d sameName=%d quarantine=%d unsupported=%d warnings=%d\n",
		report.Directories, report.Files, report.SameNameFiles, report.QuarantineFiles, report.UnsupportedFiles, len(report.Warnings))
	return nil
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

func runDatabaseRebuild(ctx context.Context, cfg *config.Config) error {
	db := mustConnectDB(ctx, cfg)
	defer closeDB(ctx, db)
	runStore, err := runstate.NewStore(cfg.Logger.Path)
	if err != nil {
		return err
	}
	runID := uuid.New().String()
	if err := runStore.AcquireLock(runID); err != nil {
		return err
	}
	defer func() {
		if err := runStore.ReleaseLock(); err != nil {
			slog.Error("释放维护任务锁失败", "runID", runID, "error", err)
		}
	}()
	if err := runStore.Create(ctx, runstate.Run{
		ID:       runID,
		Status:   runstate.StatusRunning,
		Mode:     "rebuild-database",
		Phase:    "database_rebuild",
		ScanPath: cfg.Scanner.FinalLibraryPath,
		Counts:   map[string]int64{},
	}); err != nil {
		return err
	}
	recorder := runstate.Recorder{Store: runStore, RunID: runID}
	recorder.Event(ctx, runstate.Event{Phase: "database_rebuild", Action: "database_rebuild_started", Status: string(runstate.StatusRunning), Checkpoint: true})

	ingestor, err := scanner.NewIngestor(cfg.Logger.Path, db, cfg.Scanner, cfg.Scanner.WorkerCount, cfg.Scanner.BatchSize)
	if err != nil {
		return err
	}
	defer ingestor.Close()
	stats, err := ingestor.RebuildFromLibrary(ctx, cfg.Scanner.FinalLibraryPath)
	endTime := time.Now()
	if err != nil {
		_ = runStore.Update(context.Background(), runID, func(run *runstate.Run) {
			run.Status = runstate.StatusFailed
			run.EndedAt = &endTime
			run.ErrorSummary = append(run.ErrorSummary, err.Error())
		})
		recorder.Event(context.Background(), runstate.Event{Phase: "database_rebuild", Action: "database_rebuild_finished", Status: string(runstate.StatusFailed), Error: err.Error(), Checkpoint: true})
		return err
	}
	recorder.Phase(context.Background(), "database_rebuild_done", map[string]int64{"series": int64(stats.Series), "media": int64(stats.Media)})
	recorder.Event(context.Background(), runstate.Event{Phase: "database_rebuild_done", Action: "database_rebuild_finished", Status: string(runstate.StatusCompleted), Checkpoint: true})
	if err := runStore.Update(context.Background(), runID, func(run *runstate.Run) {
		run.Status = runstate.StatusCompleted
		run.EndedAt = &endTime
	}); err != nil {
		return err
	}
	fmt.Printf("database rebuild completed runID=%s series=%d media=%d\n", runID, stats.Series, stats.Media)
	return nil
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

func listMedia(ctx context.Context, db database.Store, seriesID, mediaTypeFlag string, page, limit int, cursor string) error {
	if strings.TrimSpace(seriesID) == "" {
		return fmt.Errorf("list-media 需要 -series-id")
	}
	mediaTypeFlag = strings.TrimSpace(mediaTypeFlag)
	if mediaTypeFlag == "" {
		mediaTypeFlag = "image"
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
		return db.Media(mediaTypeFlag).ListBySeriesIDCursor(ctx, objID, cursor, limit)
	})
	if err != nil {
		return err
	}
	fmt.Printf("媒体类型: %s  总数: %d  页码: %d  每页: %d\n", mediaTypeFlag, total, page, limit)
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

func listRuns(ctx context.Context, cfg *config.Config, limit int) error {
	_, limit = normalizePagination(1, limit)
	store, err := runstate.NewStore(cfg.Logger.Path)
	if err != nil {
		return err
	}
	runs, err := store.List(ctx, limit)
	if err != nil {
		return err
	}
	for _, run := range runs {
		fmt.Printf("%s  status=%s  phase=%s  mode=%s  started=%s\n  scanPath=%s\n",
			run.ID, run.Status, run.Phase, run.Mode, run.StartedAt.Format(time.RFC3339), run.ScanPath)
		if len(run.ErrorSummary) > 0 {
			fmt.Printf("  error=%s\n", strings.Join(run.ErrorSummary, " | "))
		}
	}
	return nil
}

func showRun(ctx context.Context, cfg *config.Config, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("show-run 需要 -run-id")
	}
	store, err := runstate.NewStore(cfg.Logger.Path)
	if err != nil {
		return err
	}
	run, err := store.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("运行记录不存在: %s", runID)
	}
	fmt.Printf("id=%s status=%s phase=%s mode=%s\n", run.ID, run.Status, run.Phase, run.Mode)
	fmt.Printf("scanPath=%s\nstarted=%s updated=%s\n", run.ScanPath, run.StartedAt.Format(time.RFC3339), run.UpdatedAt.Format(time.RFC3339))
	if run.EndedAt != nil {
		fmt.Printf("ended=%s\n", run.EndedAt.Format(time.RFC3339))
	}
	for key, value := range run.Counts {
		fmt.Printf("count.%s=%d\n", key, value)
	}
	if len(run.ErrorSummary) > 0 {
		fmt.Printf("errors=%s\n", strings.Join(run.ErrorSummary, " | "))
	}
	return nil
}

func printRunJournal(ctx context.Context, cfg *config.Config, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run-journal 需要 -run-id")
	}
	store, err := runstate.NewStore(cfg.Logger.Path)
	if err != nil {
		return err
	}
	events, err := store.Journal(ctx, runID)
	if err != nil {
		return err
	}
	for _, event := range events {
		fmt.Printf("%s phase=%s action=%s status=%s source=%s target=%s error=%s\n",
			event.Time.Format(time.RFC3339), event.Phase, event.Action, event.Status, event.Source, event.Target, event.Error)
	}
	return nil
}

func verifyRunRecovery(ctx context.Context, cfg *config.Config, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("verify-run 需要 -run-id")
	}
	store, err := runstate.NewStore(cfg.Logger.Path)
	if err != nil {
		return err
	}
	run, err := store.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("运行记录不存在: %s", runID)
	}
	events, err := store.Journal(ctx, runID)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("run %s 没有 journal，无法校验恢复依据", runID)
	}
	var lastCheckpoint *runstate.Event
	failedEvents := 0
	fileEvents := 0
	dirEvents := 0
	dbEvents := 0
	for idx := range events {
		if events[idx].Checkpoint {
			event := events[idx]
			lastCheckpoint = &event
		}
		if strings.Contains(events[idx].Action, "file_") || strings.Contains(events[idx].Action, "media_") {
			fileEvents++
		}
		if strings.Contains(events[idx].Action, "dir_") {
			dirEvents++
		}
		if strings.Contains(events[idx].Action, "bulk_write") || strings.Contains(events[idx].Action, "database_") {
			dbEvents++
		}
		if events[idx].Status == "failed" || events[idx].Error != "" {
			failedEvents++
		}
	}
	if lastCheckpoint == nil {
		return fmt.Errorf("run %s 没有 checkpoint，无法校验恢复依据", runID)
	}
	report, err := scanner.WriteDirectoryHealthReport(ctx, cfg.Scanner.FinalLibraryPath, cfg.Scanner.QuarantinePath, cfg.Scanner.BackupPath, cfg.Scanner.MaxFilesPerDir, "verify-"+runID)
	if err != nil {
		return err
	}
	recoveryStatus := "complete"
	if run.Status == runstate.StatusInterrupted || run.Status == runstate.StatusStopped || run.Status == runstate.StatusPaused || run.Status == runstate.StatusFailed {
		recoveryStatus = "recoverable_with_review"
	}
	if failedEvents > 0 || len(report.Warnings) > 0 {
		recoveryStatus = "needs_attention"
	}
	fmt.Printf("run=%s status=%s phase=%s lastCheckpoint=%s/%s recoveryStatus=%s finalFiles=%d sameName=%d quarantine=%d warnings=%d journalEvents=%d fileEvents=%d dirEvents=%d dbEvents=%d failedEvents=%d\n",
		run.ID, run.Status, run.Phase, lastCheckpoint.Phase, lastCheckpoint.Action, recoveryStatus, report.Files, report.SameNameFiles, report.QuarantineFiles, len(report.Warnings), len(events), fileEvents, dirEvents, dbEvents, failedEvents)
	if recoveryStatus != "complete" {
		fmt.Println("recoveryHint=re-run scan in classifyOnly or run rebuild-database for DB reconciliation after inspecting the health report")
	}
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
