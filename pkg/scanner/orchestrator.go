package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/database"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type Orchestrator struct {
	Preprocessor ImagePreprocessor
	Classifier   SeriesClassifier
	Ingestor     MetadataIngestor
	Aggregator   LibraryAggregator
}

func NewOrchestrator(cfg *config.Config, dbStore database.Store) (*Orchestrator, error) {
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

	os.RemoveAll(cfg.Scanner.StagingPath)
	os.RemoveAll(cfg.Scanner.QuarantinePath)

	// 2. 依次创建所有模块，并传入 logDir

	preprocessor, err := NewPreprocessor(logDir, cfg.Scanner.WorkerCount)
	if err != nil {
		return nil, fmt.Errorf("创建 Orchestrator 失败: %w", err)
	}

	classifier, err := NewClassifier(logDir, cfg.Scanner.StagingPath, cfg.Scanner.FilePatterns, cfg.Scanner.WorkerCount)
	if err != nil {
		return nil, fmt.Errorf("创建 Orchestrator 失败: %w", err)
	}

	aggregator, err := NewAggregator(logDir, cfg.Scanner.SeriesGroupRules, cfg.Scanner.WorkerCount)
	if err != nil {
		return nil, fmt.Errorf("创建 Orchestrator 失败: %w", err)
	}

	ingestor, err := NewIngestor(logDir, dbStore, cfg.Scanner.WorkerCount, cfg.Scanner.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("创建 Orchestrator 失败: %w", err)
	}

	orchestrator := &Orchestrator{
		Preprocessor: preprocessor,
		Classifier:   classifier,
		Aggregator:   aggregator,
		Ingestor:     ingestor,
	}

	log.Println("扫描协调器初始化成功。")
	return orchestrator, nil
}

func (o *Orchestrator) RunFullScan(cfg config.ScannerConfig) {
	log.Println("--- 任务开始：准备路径并启动扫描 ---")

	absScanPath, err := filepath.Abs(cfg.ScanPath)
	absBackupPath, _ := filepath.Abs(cfg.BackupPath)
	if err != nil {
		log.Fatalf("错误：无法获取扫描路径的绝对路径 '%s': %v", cfg.ScanPath, err)
	}
	absStagingPath, err := filepath.Abs(cfg.StagingPath)
	if err != nil {
		log.Fatalf("错误：无法获取中转站路径的绝对路径 '%s': %v", cfg.StagingPath, err)
	}
	absFinalLibraryPath, err := filepath.Abs(cfg.FinalLibraryPath)
	if err != nil {
		log.Fatalf("错误：无法获取最终库路径的绝对路径 '%s': %v", cfg.FinalLibraryPath, err)
	}

	absQuarantinePath, err := filepath.Abs(cfg.QuarantinePath)
	if err != nil {
		log.Fatalf("错误: 无法获取隔离区路径的绝对路径 '%s': %v", cfg.QuarantinePath, err)
	}

	for _, path := range []string{absStagingPath, absFinalLibraryPath, absBackupPath, absQuarantinePath} {
		if err := os.MkdirAll(path, 0755); err != nil {
			log.Fatalf("错误: 无法创建目录 %s: %v", path, err)
		}
	}

	defer o.Preprocessor.Close()
	defer o.Classifier.Close()
	defer o.Aggregator.Close()
	defer o.Ingestor.Close()

	log.Printf("--- 阶段 1/4: 预处理 ---")
	healthyFiles, err := o.Preprocessor.ProcessDirectory(absScanPath)
	if err != nil {
		log.Fatalf("预处理阶段发生致命错误: %v", err)
	}
	if len(healthyFiles) == 0 {
		log.Println("没有找到可处理的新文件，任务结束。")
		return
	}

	log.Printf("--- 阶段 2/4: 分类到中转站 ---")
	createdSeries, processedFileNames, err := o.Classifier.ClassifyAndMove(healthyFiles)
	if err != nil {
		log.Printf("分类和移动阶段出现错误: %v", err)
	}
	log.Printf("--- 分类阶段完毕，处理了 %d 个文件，涉及 %d 个系列 ---", len(processedFileNames), len(createdSeries))

	log.Printf("--- 阶段 3/4: 聚合与归档 ---")
	changelog, err := o.Aggregator.AggregateAndArchive(absStagingPath, absFinalLibraryPath)
	if err != nil {
		log.Printf("执行聚合归档步骤时出错: %v", err)
	}
	log.Printf("--- 归档阶段完毕，生成变更日志，共 %d 项变更 ---", len(changelog))

	log.Println("--- 阶段 4/4: 数据库同步 ---")
	overwritten, err := o.Ingestor.Sync(context.Background(), absFinalLibraryPath, createdSeries, processedFileNames, changelog)
	if err != nil {
		log.Printf("数据库同步时出错: %v", err)
	}
	if len(overwritten) > 0 {
		log.Printf("警告：在操作过程中，检测到 %d 个文件可能被覆盖，详情请查看 ingestor.log", len(overwritten))

	}

	log.Println("🎉 全库扫描任务完成。")
}
