//go:build ignore
// +build ignore

// ^^^ 在运行 go run debug_backup.go 之前，请注释掉上面这两行

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
	"log"
	"os"
	"path/filepath"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	scanDir   = "_debug_backup_scan"
	finalDir  = "_debug_backup_final"
	backupDir = "_debug_backup_target"
)

func main() {
	log.Println("=============================================")
	log.Println("===     Backup 模块集成测试启动 (真实DB)    ===")
	log.Println("=============================================")

	// 1. 加载配置并连接数据库
	if err := config.LoadConfig("../../"); err != nil {
		log.Fatalf("无法加载配置文件: %v", err)
	}
	ctx := context.Background()
	dbStore, err := mongo.NewStore(ctx, config.C)
	if err != nil {
		log.Fatalf("连接到 MongoDB 失败: %v", err)
	}

	// 2. 设置测试环境
	setupTestEnvironment(ctx, dbStore)
	defer cleanupTestEnvironment(ctx, dbStore)

	// 3. 初始化 Backup 模块
	backupModule, err := scanner.NewBackup(dbStore, config.C.Scanner.WorkerCount)
	if err != nil {
		log.Fatalf("创建 Backup 模块失败: %v", err)
	}
	// backup.go 当前没有 Close 方法，如果未来添加，需要在此 defer

	// 4. 执行核心的预备份逻辑
	log.Println("\n--- 调用 Backup.BackupForNewFiles() ---")
	fileNameDups, hashDups, actualBackupPath, err := backupModule.BackupForNewFiles(ctx, scanDir, backupDir)
	if err != nil {
		log.Fatalf("执行 BackupForNewFiles 失败: %v", err)
	}
	log.Println("--- Backup.BackupForNewFiles() 执行完毕 ---")

	// 5. 验证结果
	log.Println("\n=============================================")
	log.Println("===              执行结果验证             ===")
	log.Println("=============================================")
	verifyResults(fileNameDups, hashDups, actualBackupPath)
}

// setupTestEnvironment 创建文件并预置数据库记录
func setupTestEnvironment(ctx context.Context, db database.Store) {
	log.Println("\n--- 正在设置测试环境 ---")
	cleanupTestEnvironment(ctx, db)
	for _, dir := range []string{scanDir, finalDir, backupDir} {
		must(os.MkdirAll(dir, 0755))
	}

	// 1. 在“最终库”中创建已存在的系列和图片
	seriesA_path, _ := filepath.Abs(filepath.Join(finalDir, "SeriesA"))
	existingCatPath := filepath.Join(seriesA_path, "cat.jpg")
	mustCreateDummyFile(existingCatPath, "this is a cat")

	log.Println("正在向测试数据库预置数据...")
	seriesA, _ := db.Series().FindOrCreateByName(ctx, "SeriesA", seriesA_path)
	catHash, _ := hasher.CalculateSHA256(existingCatPath)
	imageCat := &models.Image{
		ID: primitive.NewObjectID(), SeriesID: seriesA.ID,
		FileName: "cat.jpg", FilePath: existingCatPath, FileHash: catHash,
	}
	_, err := db.Images().CreateBatch(ctx, []*models.Image{imageCat})
	must(err)
	log.Println("数据库预置数据完成。")

	// 2. 在“新文件入口”创建待处理的文件
	log.Println("正在创建待扫描的测试文件...")
	// Case 1: 内容重复，文件名不同 (应触发哈希重复报告)
	must(os.WriteFile(filepath.Join(scanDir, "new_cat_same_content.jpg"), mustReadFile(existingCatPath), 0644))
	// Case 2: 文件名重复，内容不同 (应触发文件名重复报告，并触发备份)
	must(os.WriteFile(filepath.Join(scanDir, "cat.jpg"), []byte("this is NOT a cat"), 0644))
	// Case 3: 全新的文件 (不应触发任何报告和备份)
	must(os.WriteFile(filepath.Join(scanDir, "a_new_dog.png"), []byte("a completely new dog image"), 0644))

	log.Println("--- 测试环境设置完毕 ---")
}

// verifyResults 验证备份报告和备份文件
func verifyResults(fileNameDups, hashDups []scanner.DuplicateInfo, actualBackupPath string) {
	log.Println("--- 开始验证 [文件名重复] 报告 ---")
	if len(fileNameDups) != 1 {
		log.Fatalf("[验证失败] 文件名重复报告应包含 1 条记录，实际有 %d 条", len(fileNameDups))
	}
	if !strings.HasSuffix(fileNameDups[0].NewFilePath, "cat.jpg") || fileNameDups[0].SeriesName != "SeriesA" {
		log.Fatalf("[验证失败] 文件名重复报告内容不正确: %+v", fileNameDups[0])
	}
	log.Println("[验证成功] 文件名重复报告内容正确。")

	log.Println("\n--- 开始验证 [内容哈希重复] 报告 ---")
	if len(hashDups) != 1 {
		log.Fatalf("[验证失败] 内容哈希重复报告应包含 1 条记录，实际有 %d 条", len(hashDups))
	}
	if !strings.HasSuffix(hashDups[0].NewFilePath, "new_cat_same_content.jpg") || hashDups[0].SeriesName != "SeriesA" {
		log.Fatalf("[验证失败] 内容哈希重复报告内容不正确: %+v", hashDups[0])
	}
	log.Println("[验证成功] 内容哈希重复报告内容正确。")

	log.Println("\n--- 开始验证文件系统备份 ---")
	if actualBackupPath == "" {
		log.Fatal("[验证失败] Backup 模块未返回有效的备份路径。")
	}
	seriesA_backup_path := filepath.Join(actualBackupPath, "SeriesA")
	if _, err := os.Stat(seriesA_backup_path); os.IsNotExist(err) {
		log.Fatalf("[验证失败] 未在备份目录中找到 'SeriesA' 的备份: %s", seriesA_backup_path)
	}
	log.Println("[验证成功] 文件系统备份正确。")

	fmt.Println("\n✅ Backup 模块集成测试成功完成！")
}

// cleanup 清理所有临时文件和数据库记录
func cleanupTestEnvironment(ctx context.Context, db database.Store) {
	log.Println("\n--- 正在清理环境 ---")
	os.RemoveAll(scanDir)
	os.RemoveAll(finalDir)
	os.RemoveAll(backupDir)
	os.Remove("backup.log")
	if db != nil {
		if err := db.DropAllCollections(ctx); err != nil {
			log.Printf("警告：清理数据库集合失败: %v", err)
		}
	}
	log.Println("--- 环境清理完毕 ---")
}

// --- 辅助函数 ---
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
