package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/database"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Orchestrator struct {
	db     database.Store
	logDir string
}

func NewOrchestrator(cfg *config.Config, dbStore database.Store) (*Orchestrator, error) {
	if cfg == nil {
		return nil, errors.New("配置未初始化")
	}
	log.Println("初始化扫描协调器 (Orchestrator)...")

	// 1. 创建统一的日志目录
	logDir, err := filepath.Abs(cfg.Logger.Path)
	if err != nil {
		return nil, fmt.Errorf("无法获取日志目录绝对路径: %w", err)
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建日志目录: %w", err)
	}
	log.Printf("所有模块日志将存放在: %s", logDir)

	orchestrator := &Orchestrator{
		db:     dbStore,
		logDir: logDir,
	}

	log.Println("扫描协调器初始化成功。")
	return orchestrator, nil
}

func (o *Orchestrator) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	log.Println("--- 任务开始：准备路径并启动扫描 ---")
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := strings.TrimSpace(cfg.Mode)
	if mode == "" {
		mode = "full"
	}
	if mode != "full" && mode != "classifyOnly" {
		return fmt.Errorf("无效扫描模式 %q，可选值: full, classifyOnly", cfg.Mode)
	}
	if mode == "full" && o.db == nil {
		return errors.New("full 模式需要数据库存储")
	}

	absScanPath, err := filepath.Abs(cfg.ScanPath)
	if err != nil {
		return fmt.Errorf("无法获取扫描路径的绝对路径 %q: %w", cfg.ScanPath, err)
	}
	absBackupPath, err := filepath.Abs(cfg.BackupPath)
	if err != nil {
		return fmt.Errorf("无法获取备份路径的绝对路径 %q: %w", cfg.BackupPath, err)
	}
	absStagingPath, err := filepath.Abs(cfg.StagingPath)
	if err != nil {
		return fmt.Errorf("无法获取中转站路径的绝对路径 %q: %w", cfg.StagingPath, err)
	}
	absFinalLibraryPath, err := filepath.Abs(cfg.FinalLibraryPath)
	if err != nil {
		return fmt.Errorf("无法获取最终库路径的绝对路径 %q: %w", cfg.FinalLibraryPath, err)
	}

	absQuarantinePath, err := filepath.Abs(cfg.QuarantinePath)
	if err != nil {
		return fmt.Errorf("无法获取隔离区路径的绝对路径 %q: %w", cfg.QuarantinePath, err)
	}

	for _, path := range []string{absStagingPath, absFinalLibraryPath, absBackupPath, absQuarantinePath} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("无法创建目录 %s: %w", path, err)
		}
	}

	preprocessor, err := NewPreprocessor(o.logDir, cfg.WorkerCount)
	if err != nil {
		return fmt.Errorf("创建预处理器失败: %w", err)
	}
	defer preprocessor.Close()

	classifier, err := NewClassifier(o.logDir, absStagingPath, cfg)
	if err != nil {
		return fmt.Errorf("创建分类器失败: %w", err)
	}
	defer classifier.Close()

	aggregator, err := NewAggregator(o.logDir, cfg.SeriesGroupRules, cfg.WorkerCount)
	if err != nil {
		return fmt.Errorf("创建聚合器失败: %w", err)
	}
	defer aggregator.Close()

	log.Printf("--- 阶段 1/4: 预处理 ---")
	healthyFiles, err := preprocessor.ProcessDirectory(absScanPath)
	if err != nil {
		return fmt.Errorf("预处理阶段失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mediaFiles, err := supportedMediaFiles(healthyFiles, cfg)
	if err != nil {
		return fmt.Errorf("媒体类型配置无效: %w", err)
	}
	if len(mediaFiles) != len(healthyFiles) {
		log.Printf("跳过 %d 个未配置媒体类型的文件", len(healthyFiles)-len(mediaFiles))
	}
	mediaFiles, corruptedCount, err := quarantineCorruptedImages(ctx, mediaFiles, absQuarantinePath, cfg.WorkerCount)
	if err != nil {
		return fmt.Errorf("损坏图片隔离失败: %w", err)
	}
	if corruptedCount > 0 {
		log.Printf("检测并隔离 %d 个损坏图片", corruptedCount)
	}
	if len(mediaFiles) == 0 {
		log.Println("没有找到可处理的新文件，任务结束。")
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	log.Printf("--- 阶段 2/4: 分类到中转站 ---")
	createdSeries, processedFileNames, err := classifier.ClassifyAndMove(mediaFiles)
	if err != nil {
		return fmt.Errorf("分类和移动阶段失败: %w", err)
	}
	log.Printf("--- 分类阶段完毕，处理了 %d 个文件，涉及 %d 个系列 ---", len(processedFileNames), len(createdSeries))
	if err := ctx.Err(); err != nil {
		return err
	}

	log.Printf("--- 阶段 3/4: 聚合与归档 ---")
	changelog, err := aggregator.AggregateAndArchive(absStagingPath, absFinalLibraryPath, absQuarantinePath)
	if err != nil {
		return fmt.Errorf("聚合归档阶段失败: %w", err)
	}
	log.Printf("--- 归档阶段完毕，生成变更日志，共 %d 项变更 ---", len(changelog))

	if mode == "classifyOnly" {
		log.Println("--- classifyOnly 模式：跳过数据库同步 ---")
		return nil
	}

	log.Println("--- 阶段 4/4: 数据库同步 ---")
	ingestor, err := NewIngestor(o.logDir, o.db, cfg, cfg.WorkerCount, cfg.BatchSize)
	if err != nil {
		return fmt.Errorf("创建入库器失败: %w", err)
	}
	defer ingestor.Close()
	overwritten, err := ingestor.Sync(ctx, absFinalLibraryPath, createdSeries, processedFileNames, changelog)
	if err != nil {
		return fmt.Errorf("数据库同步阶段失败: %w", err)
	}
	if len(overwritten) > 0 {
		log.Printf("警告：在操作过程中，检测到 %d 个文件可能被覆盖，详情请查看 ingestor.log", len(overwritten))

	}

	log.Println("全库扫描任务完成。")
	return nil
}

func quarantineCorruptedImages(ctx context.Context, paths []string, quarantinePath string, workerCount int) ([]string, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if workerCount <= 0 {
		workerCount = 1
	}
	healthy := make([]string, 0, len(paths))
	corruptedDir := filepath.Join(quarantinePath, "corrupted")

	type result struct {
		path      string
		corrupted bool
		err       error
	}

	jobs := make(chan string, workerCount*2)
	results := make(chan result, len(paths))
	var wg sync.WaitGroup
	var moveMu sync.Mutex
	sendResult := func(res result) bool {
		select {
		case results <- res:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					return
				}
				if !isImageExtension(path) || !isImageFileDamaged(path) {
					if !sendResult(result{path: path}) {
						return
					}
					continue
				}

				moveMu.Lock()
				if err := os.MkdirAll(corruptedDir, 0755); err != nil {
					moveMu.Unlock()
					if !sendResult(result{path: path, err: err}) {
						return
					}
					continue
				}
				target, _, err := nextAvailablePath(filepath.Join(corruptedDir, filepath.Base(path)))
				if err == nil {
					err = os.Rename(path, target)
				}
				moveMu.Unlock()

				if err != nil {
					if !sendResult(result{path: path, err: err}) {
						return
					}
					continue
				}
				if !sendResult(result{path: path, corrupted: true}) {
					return
				}
			}
		}()
	}

	var feedErr error
	for _, path := range paths {
		select {
		case jobs <- path:
		case <-ctx.Done():
			feedErr = ctx.Err()
		}
		if feedErr != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	close(results)

	var corruptedCount int
	var errs []error
	for res := range results {
		if res.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", res.path, res.err))
			continue
		}
		if res.corrupted {
			corruptedCount++
			continue
		}
		healthy = append(healthy, res.path)
	}
	if len(errs) > 0 {
		if feedErr != nil {
			errs = append(errs, feedErr)
		}
		return healthy, corruptedCount, errors.Join(errs...)
	}
	if feedErr != nil {
		return healthy, corruptedCount, feedErr
	}

	return healthy, corruptedCount, nil
}
