//go:build ignore
// +build ignore

// ^^^ 在运行 go run reconciler_debug.go 之前，请注释掉上面这两行

package main

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/hasher"
	"PICs_Manager/pkg/scanner"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	finalDir      = "_debug_reconcile_final"
	backupRootDir = "_debug_reconcile_backup"
	quarantineDir = "_debug_reconcile_quarantine"
)

var timestampedBackupPath string

func main() {
	log.Println("=============================================")
	log.Println("===    Reconciler 模块集成测试启动 (真实DB)   ===")
	log.Println("=============================================")

	if err := config.LoadConfig("../../"); err != nil {
		log.Fatalf("无法加载配置文件: %v", err)
	}

	ctx := context.Background()
	dbStore, err := mongo.NewStore(ctx, config.C)
	if err != nil {
		log.Fatalf("连接到 MongoDB 失败: %v", err)
	}

	setupTestEnvironment(ctx, dbStore)
	defer cleanupTestEnvironment(ctx, dbStore)

	ingestor, err := scanner.NewIngestor(dbStore, config.C.Scanner.WorkerCount, config.C.Scanner.BatchSize)
	if err != nil {
		log.Fatalf("创建 Ingestor 失败: %v", err)
	}
	defer ingestor.Close()

	// 【核心修复】在调用 NewReconciler 时，传入第三个参数 config.C.Scanner.WorkerCount
	reconciler, err := scanner.NewReconciler(dbStore, ingestor, config.C.Scanner.WorkerCount)
	if err != nil {
		log.Fatalf("创建 Reconciler 失败: %v", err)
	}
	// 假设 Reconciler 也有 Close 方法
	// defer reconciler.Close()

	log.Println("\n--- 正在构建模拟的流水线结果报告 ---")
	pipelineResults := buildMockResults()

	log.Println("\n--- 调用 Reconciler.Reconcile() ---")
	if err := reconciler.Reconcile(ctx, pipelineResults); err != nil {
		log.Fatalf("执行 Reconcile 失败: %v", err)
	}
	log.Println("--- Reconciler.Reconcile() 执行完毕 ---")

	log.Println("\n=============================================")
	log.Println("===              执行结果验证             ===")
	log.Println("=============================================")
	verifyResults(ctx, dbStore)
}

func setupTestEnvironment(ctx context.Context, db database.Store) {
	log.Println("\n--- 正在设置测试环境 ---")
	cleanupTestEnvironment(ctx, db)
	for _, dir := range []string{finalDir, backupRootDir, quarantineDir} {
		must(os.MkdirAll(dir, 0755))
	}

	originalSeriesPath, _ := filepath.Abs(filepath.Join(finalDir, "SeriesToOverwrite"))
	originalFilePath := filepath.Join(originalSeriesPath, "image.jpg")
	originalContent := "this is the original, correct content"
	mustCreateDummyFile(originalFilePath, originalContent)
	originalHash, _ := hasher.CalculateSHA256(originalFilePath)

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	timestampedBackupPath = filepath.Join(backupRootDir, timestamp)
	must(copyDir(originalSeriesPath, filepath.Join(timestampedBackupPath, "SeriesToOverwrite")))
	log.Printf("已将原始系列备份到: %s", timestampedBackupPath)

	series, _ := db.Series().FindOrCreateByName(ctx, "SeriesToOverwrite", originalSeriesPath)
	image := &models.Image{
		ID: primitive.NewObjectID(), SeriesID: series.ID,
		FileName: "image.jpg", FilePath: originalFilePath, FileHash: originalHash,
	}
	_, err := db.Images().CreateBatch(ctx, []*models.Image{image})
	must(err)
	log.Println("数据库已预置为“原始”状态。")

	corruptedContent := "this is the new, corrupted content"
	must(os.WriteFile(originalFilePath, []byte(corruptedContent), 0644))
	log.Printf("已在最终目录中模拟了一次文件覆盖: %s", originalFilePath)
	log.Println("--- “事故现场”测试环境设置完毕 ---")
}

func buildMockResults() *scanner.PipelineResults {
	overwrittenFilePath, _ := filepath.Abs(filepath.Join(finalDir, "SeriesToOverwrite", "image.jpg"))
	absQuarantineDir, _ := filepath.Abs(quarantineDir)
	absFinalDir, _ := filepath.Abs(finalDir)

	return &scanner.PipelineResults{
		OverwrittenFiles: []string{overwrittenFilePath},
		FileNameDuplicates: []scanner.DuplicateInfo{
			{NewFilePath: "some_new_file.jpg", FileName: "image.jpg",
				SeriesName: "SeriesToOverwrite", ExistingPath: overwrittenFilePath},
		},
		HashDuplicates:   []scanner.DuplicateInfo{},
		Changelog:        make(map[string]string), // 在这个简单测试中，changelog可以为空
		LatestBackupPath: timestampedBackupPath,
		QuarantinePath:   absQuarantineDir,
		FinalLibraryPath: absFinalDir,
	}
}

func verifyResults(ctx context.Context, db database.Store) {
	log.Println("--- 开始验证恢复结果 ---")

	expectedQuarantinedPath := filepath.Join(quarantineDir, "SeriesToOverwrite")
	if _, err := os.Stat(expectedQuarantinedPath); os.IsNotExist(err) {
		log.Fatalf("[验证失败] 未在隔离区找到被隔离的系列文件夹: %s", expectedQuarantinedPath)
	}
	log.Println("[验证成功] “被污染”的系列已被正确隔离。")

	restoredFilePath, _ := filepath.Abs(filepath.Join(finalDir, "SeriesToOverwrite", "image.jpg"))
	if _, err := os.Stat(restoredFilePath); os.IsNotExist(err) {
		log.Fatalf("[验证失败] 未在最终目录中找到恢复后的文件: %s", restoredFilePath)
	}
	log.Println("[验证成功] 原始系列已从备份中正确恢复。")

	restoredContent, _ := os.ReadFile(restoredFilePath)
	expectedHash := hasher.CalculateSHA256FromBytes(restoredContent)
	imgFromDB, err := db.Images().GetByFilePath(ctx, restoredFilePath)
	if err != nil || imgFromDB == nil {
		log.Fatalf("[验证失败] 无法在数据库中找到恢复后的文件记录: %s", restoredFilePath)
	}
	if imgFromDB.FileHash != expectedHash {
		log.Fatalf("[验证失败] 数据库中的文件哈希未被校准！预期: %s, 实际: %s", expectedHash, imgFromDB.FileHash)
	}
	log.Println("[验证成功] 数据库记录已通过“再入库”被成功校准。")

	fmt.Println("\n✅ Reconciler 模块集成测试成功完成！")
}

func cleanupTestEnvironment(ctx context.Context, db database.Store) {
	log.Println("\n--- 正在清理环境 ---")
	os.RemoveAll(finalDir)
	os.RemoveAll(backupRootDir)
	os.RemoveAll(quarantineDir)
	os.Remove("reconciler.log")
	if db != nil {
		if err := db.DropAllCollections(ctx); err != nil {
			log.Printf("警告：清理数据库集合失败: %v", err)
		}
	}
	log.Println("--- 环境清理完毕 ---")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func mustCreateDummyFile(path string, content string) {
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, []byte(content), 0644))
}
func mustReadFile(path string) []byte {
	bytes, err := os.ReadFile(path)
	must(err)
	return bytes
}

func copyDir(src, dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(src, path)
		destPath := filepath.Join(dest, relPath)
		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		destFile, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer destFile.Close()
		_, err = io.Copy(destFile, srcFile)
		return err
	})
}
