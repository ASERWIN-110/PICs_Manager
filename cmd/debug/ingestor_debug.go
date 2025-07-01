//go:build ignore
// +build ignore

// ^^^ 在运行 go run ingestor_debug.go 之前，请注释掉上面这两行

package main

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/scanner"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

const (
	finalDir = "_debug_ingestor_final"
)

func main() {
	log.Println("=============================================")
	log.Println("===   Ingestor 模块集成测试启动 (真实DB)   ===")
	log.Println("=============================================")

	if err := config.LoadConfig("../../"); err != nil {
		log.Fatalf("无法加载配置文件: %v", err)
	}

	effectiveWorkerCount := config.C.Scanner.WorkerCount
	if effectiveWorkerCount <= 0 {
		effectiveWorkerCount = runtime.NumCPU()
	}
	log.Printf("配置的并发数: %d (实际运行: %d), 批处理大小: %d", config.C.Scanner.WorkerCount, effectiveWorkerCount, config.C.Scanner.BatchSize)

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

	log.Println("\n--- 调用 Ingestor.Sync() ---")

	// 模拟 Aggregator 产出的 changelog
	mockChangelog := map[string]string{
		"some_old_path_for_A":            filepath.Join(finalDir, "A"),
		"some_old_path_for_My_Story_agg": filepath.Join(finalDir, "My Story_agg"),
	}

	_, err = ingestor.Sync(ctx, finalDir, nil, nil, mockChangelog)
	if err != nil {
		log.Fatalf("执行 Sync 失败: %v", err)
	}
	log.Println("--- Ingestor.Sync() 执行完毕 ---")

	log.Println("\n=============================================")
	log.Println("===              执行结果验证             ===")
	log.Println("=============================================")
	verifyResults(ctx, dbStore)
}

// setupTestEnvironment (核心修改)
// 创建了更真实的、嵌套的聚合目录结构
func setupTestEnvironment(ctx context.Context, db database.Store) {
	log.Println("\n--- 正在设置测试环境 ---")
	cleanupTestEnvironment(ctx, db)

	must(os.MkdirAll(finalDir, 0755))

	// 场景1: 独立的、未被聚合的系列 "A"
	mustCreateDummyFile(filepath.Join(finalDir, "A", "photo_A.jpg"))

	// 场景2: 已被聚合的系列 "My Story"，其父目录带有 _agg 后缀
	mustCreateDummyFile(filepath.Join(finalDir, "My Story_agg", "My Story 1", "photo_M1.jpg"))
	mustCreateDummyFile(filepath.Join(finalDir, "My Story_agg", "My Story 2", "photo_M2.jpg"))

	log.Println("--- 测试环境设置完毕 ---")
}

// verifyResults (核心修改)
// 更新了对图片总数和各系列图片数的验证
func verifyResults(ctx context.Context, db database.Store) {
	log.Println("--- 开始验证数据库状态 ---")

	allSeries, err := db.Series().GetAllSeries(ctx)
	if err != nil {
		log.Fatalf("[验证失败] 查询所有系列时出错: %v", err)
	}
	if len(allSeries) != 3 {
		log.Fatalf("[验证失败] 应有 3 个系列入库(A, My Story 1, My Story 2)，实际有 %d 个", len(allSeries))
	}
	log.Println("[验证成功] 系列入库数量正确 (3个)。")

	// 由于图片是最终的验证单元，我们直接验证图片
	var allImages []models.Image
	for _, s := range allSeries {
		images, _, _ := db.Images().ListBySeriesID(ctx, s.ID, 1, 100)
		allImages = append(allImages, images...)
	}

	if len(allImages) != 3 {
		log.Fatalf("[验证失败] 应有 3 张图片入库，实际总共有 %d 张", len(allImages))
	}
	log.Println("[验证成功] 图片入库总数正确 (3张)。")

	// 验证元数据更新
	seriesA, _ := db.Series().GetByName(ctx, "A")
	seriesM1, _ := db.Series().GetByName(ctx, "My Story 1")
	seriesM2, _ := db.Series().GetByName(ctx, "My Story 2")

	if seriesA.ImageCount != 1 {
		log.Fatalf("[验证失败] 'A' 系列的 ImageCount 应为1，实际为 %d", seriesA.ImageCount)
	}
	if seriesM1.ImageCount != 1 {
		log.Fatalf("[验证失败] 'My Story 1' 系列的 ImageCount 应为1，实际为 %d", seriesM1.ImageCount)
	}
	if seriesM2.ImageCount != 1 {
		log.Fatalf("[验证失败] 'My Story 2' 系列的 ImageCount 应为1，实际为 %d", seriesM2.ImageCount)
	}
	log.Println("[验证成功] 所有系列的 ImageCount 已被正确更新。")

	fmt.Println("\n✅ Ingestor 模块集成测试成功完成！")
}

func cleanupTestEnvironment(ctx context.Context, db database.Store) {
	log.Println("\n--- 正在清理环境 ---")
	os.RemoveAll(finalDir)
	os.Remove("ingestor.log")
	if db != nil {
		if err := db.DropAllCollections(ctx); err != nil {
			log.Printf("警告：清理数据库集合失败: %v", err)
		}
	}
	log.Println("--- 环境清理完毕 ---")
}

func mustCreateDummyFile(path string) {
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, []byte("dummy content for test"), 0644))
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
