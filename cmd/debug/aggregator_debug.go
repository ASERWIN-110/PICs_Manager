//go:build ignore
// +build ignore

// ^^^ 在运行 go run aggregator_debug.go 之前，请注释掉上面这两行

package main

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/scanner" // 确认这是您正确的模块路径
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

const (
	stagingDir = "_debug_agg_staging"
	finalDir   = "_debug_agg_final"
)

func main() {
	log.Println("======================================================")
	log.Println("===     State-Aware Aggregator 模块调试程序启动    ===")
	log.Println("===        (使用独立的、完备的测试场景)        ===")
	log.Println("======================================================")

	setupTestEnvironment()
	defer cleanupTestEnvironment()

	seriesRules := []config.SeriesGroupRule{
		{Name: "前置序号", Pattern: `^\s*\(\d+\)\s*(?P<group>.*)`},
		{Name: "括号", Pattern: `^[『「《[(【（](?P<group>.*?)[』」》)\]】）]`},
		{Name: "文本+数字", Pattern: `^(?P<group>.+?)\s*(\d+)$`},
	}
	workerCount := 12

	aggregator, err := scanner.NewAggregator(seriesRules, workerCount)
	if err != nil {
		log.Fatalf("创建 Aggregator 失败: %v", err)
	}
	defer aggregator.Close()

	log.Println("\n--- 调用 Aggregator.AggregateAndArchive() ---")
	log.Printf("--- 输入: staging='%s', final='%s' ---", stagingDir, finalDir)
	log.Println("--- 详细日志将被写入到 aggregator.log 文件中 ---")

	changelog, err := aggregator.AggregateAndArchive(stagingDir, finalDir)
	if err != nil {
		log.Fatalf("执行 AggregateAndArchive 失败: %v", err)
	}
	log.Println("--- Aggregator.AggregateAndArchive() 执行完毕 ---")

	log.Println("\n=============================================")
	log.Println("===              执行结果验证             ===")
	log.Println("=============================================")
	verifyResults(changelog)

	fmt.Println("\n✅ 测试成功完成！所有验证均已通过。")
}

func setupTestEnvironment() {
	log.Println("\n--- 正在设置测试环境 ---")
	cleanupTestEnvironment()
	must(os.MkdirAll(stagingDir, 0755))
	must(os.MkdirAll(finalDir, 0755))
	log.Printf("已创建空的测试目录: '%s' 和 '%s'", stagingDir, finalDir)

	mustCreateDummySeries(finalDir, "My Story 1")
	mustCreateDummySeries(stagingDir, "My Story 2")
	mustCreateDummySeries(stagingDir, "A")
	must(os.MkdirAll(filepath.Join(finalDir, "Conflict Story"), 0755))
	must(os.WriteFile(filepath.Join(finalDir, "Conflict Story", "original_file.txt"), []byte("..."), 0644))
	mustCreateDummySeries(stagingDir, "Conflict Story 1")
	mustCreateDummySeries(stagingDir, "Conflict Story 2")
	mustCreateDummySeries(stagingDir, "[Request] Series")
	mustCreateDummySeries(stagingDir, "【中文】系列")

	log.Println("--- 测试环境设置完毕 ---")
}

// verifyResults (核心修复)
// 修正了验证路径
func verifyResults(changelog map[string]string) {
	log.Println("--- 开始验证文件系统最终状态 ---")
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		log.Fatalf("[验证失败] 读取中转站 '%s' 失败: %v", stagingDir, err)
	}
	if len(entries) > 0 {
		log.Fatalf("[验证失败] 中转站 '%s' 在操作后没有被清空！", stagingDir)
	}
	log.Printf("[验证成功] 中转站 '%s' 已被清空。", stagingDir)

	log.Println("\n--- 开始验证关键路径 ---")
	// 验证聚合（现在带有 _agg 后缀）
	checkPathExists(filepath.Join(finalDir, "M", "My Story_agg", "My Story 1"))
	checkPathExists(filepath.Join(finalDir, "M", "My Story_agg", "My Story 2"))
	// 验证归档冲突
	checkPathExists(filepath.Join(finalDir, "A", "A_agg"))
	// 验证聚合冲突
	checkPathExists(filepath.Join(finalDir, "C", "Conflict Story_agg", "Conflict Story 1"))
	checkPathExists(filepath.Join(finalDir, "C", "Conflict Story_agg", "Conflict Story"))
	// 验证智能归档
	checkPathExists(filepath.Join(finalDir, "R", "[Request] Series"))
	checkPathExists(filepath.Join(finalDir, "Z", "【中文】系列"))

	log.Println("\n--- Aggregator 返回的变更日志 (Changelog) ---")
	keys := make([]string, 0, len(changelog))
	for k := range changelog {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		log.Printf("  %s  ->  %s\n", k, changelog[k])
	}
}

func checkPathExists(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Fatalf("[验证失败] 期望的路径不存在: %s", path)
	}
	log.Printf("[验证成功] 路径存在: %s", path)
}

func cleanupTestEnvironment() {
	log.Println("\n--- 正在清理测试环境 ---")
	os.RemoveAll(stagingDir)
	os.RemoveAll(finalDir)
	//os.Remove("aggregator.log")
	log.Println("--- 测试环境清理完毕 ---")
}

func mustCreateDummySeries(baseDir, name string) {
	dir := filepath.Join(baseDir, name)
	must(os.MkdirAll(dir, 0755))
	must(os.WriteFile(filepath.Join(dir, "dummy_file.txt"), []byte("content"), 0644))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
