//go:build ignore
// +build ignore

// ^^^ 在运行 go run preprocess_debug.go 之前，请注释掉上面这两行

package main

import (
	"PICs_Manager/pkg/hasher"
	"PICs_Manager/pkg/scanner"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

const (
	testRootDir = "_debug_preprocess_test"
)

func main() {
	log.Println("=============================================")
	log.Println("===   Preprocessor 模块调试程序启动   ===")
	log.Println("===    (测试新的文件家族整理逻辑)    ===")
	log.Println("=============================================")

	// 1. 设置测试环境，并获取用于验证的“正确”哈希值
	expectedRepairHash := setupTestEnvironment()
	defer cleanupTestEnvironment()

	// 2. 初始化 Preprocessor
	preprocessor, err := scanner.NewPreprocessor(4)
	if err != nil {
		log.Fatalf("创建 Preprocessor 失败: %v", err)
	}
	defer preprocessor.Close()

	// 3. 执行核心整理逻辑
	log.Printf("\n--- 调用 Preprocessor.ProcessDirectory(%s) ---\n", testRootDir)
	_, err = preprocessor.ProcessDirectory(testRootDir)
	if err != nil {
		log.Fatalf("Preprocessor 执行失败: %v", err)
	}
	log.Println("--- Preprocessor.ProcessDirectory 执行完毕 ---")

	// 4. 验证最终的文件系统状态
	fmt.Println()
	log.Println("=============================================")
	log.Println("===              执行结果验证             ===")
	log.Println("=============================================")
	verifyResults(expectedRepairHash) // 将正确的哈希传入验证函数
}

// setupTestEnvironment 创建测试场景，并返回“健康修复文件”的真实哈希值
func setupTestEnvironment() (expectedRepairHash string) {
	log.Println("\n--- 正在设置测试环境 ---")
	cleanupTestEnvironment()
	must(os.MkdirAll(testRootDir, 0755))
	os.Remove("preprocessor_corruption.log")

	// 场景 1: “修复”
	createCorruptedFile(filepath.Join(testRootDir, "repair_me.jpg"))
	healthyFixerPath := filepath.Join(testRootDir, "repair_me (1).jpg")
	createValidJPEG(healthyFixerPath, "healthy content for repair")
	// 【核心修改】在创建后，立即计算其真实哈希，作为验证标准
	expectedRepairHash, _ = hasher.CalculateSHA256(healthyFixerPath)
	log.Println("-> 已创建 [修复] 场景 (JPG)")

	// 场景 2: “去重”
	createValidPNG(filepath.Join(testRootDir, "deduplicate_me.png"), "identical content")
	createValidPNG(filepath.Join(testRootDir, "deduplicate_me (1).png"), "identical content")
	log.Println("-> 已创建 [去重] 场景 (PNG)")

	// 场景 3: “保留”
	createValidJPEG(filepath.Join(testRootDir, "keep_me.jpg"), "content A")
	createValidJPEG(filepath.Join(testRootDir, "keep_me (1).jpg"), "content B")
	log.Println("-> 已创建 [保留] 场景 (JPG)")

	// 场景 4: 独立的、非图片文件
	must(os.WriteFile(filepath.Join(testRootDir, "standalone.txt"), []byte("not an image"), 0644))
	log.Println("-> 已创建 [独立] 场景")

	log.Println("--- 测试环境设置完毕 ---")
	return expectedRepairHash
}

// verifyResults (核心修正)
// 接收正确的哈希值作为验证标准
func verifyResults(expectedHash string) {
	var finalFiles []string
	filepath.WalkDir(testRootDir, func(path string, d os.DirEntry, err error) error {
		if !d.IsDir() {
			finalFiles = append(finalFiles, d.Name())
		}
		return nil
	})
	sort.Strings(finalFiles)

	expectedFiles := []string{
		"deduplicate_me.png", "keep_me (1).jpg", "keep_me.jpg", "repair_me.jpg", "standalone.txt",
	}
	sort.Strings(expectedFiles)

	log.Println("--- 开始验证最终文件列表 ---")
	if !reflect.DeepEqual(finalFiles, expectedFiles) {
		log.Fatalf("[验证失败]\n  预期文件列表: %v\n  实际文件列表: %v", expectedFiles, finalFiles)
	}
	log.Println("[验证成功] 最终文件列表与预期一致！")

	// 使用正确的哈希值进行内容验证
	repairedPath := filepath.Join(testRootDir, "repair_me.jpg")
	repairedHash, err := hasher.CalculateSHA256(repairedPath)
	if err != nil {
		log.Fatalf("[验证失败] 无法读取被修复的文件 '%s'", repairedPath)
	}

	if repairedHash != expectedHash {
		log.Fatalf("[验证失败] 'repair_me.jpg' 的内容未被正确修复！哈希不匹配。")
	}
	log.Println("[验证成功] 'repair_me.jpg' 的内容已正确修复！")

	fmt.Println("\n✅ Preprocessor 模块新逻辑测试成功完成！")
}

func cleanupTestEnvironment() {
	log.Println("\n--- 正在清理测试环境 ---")
	os.RemoveAll(testRootDir)
	os.Remove("preprocessor_corruption.log")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func createValidPNG(path string, content string) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 1))
	hash := hasher.CalculateSHA256FromBytes([]byte(content))
	c := color.RGBA{R: hash[0], G: hash[1], B: hash[2], A: 255}
	for i := 0; i < 10; i++ {
		img.Set(i, 0, c)
	}
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	must(png.Encode(f, img))
}
func createValidJPEG(path string, content string) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 1))
	hash := hasher.CalculateSHA256FromBytes([]byte(content))
	c := color.RGBA{R: hash[0], G: hash[1], B: hash[2], A: 255}
	for i := 0; i < 10; i++ {
		img.Set(i, 0, c)
	}
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	must(jpeg.Encode(f, img, &jpeg.Options{Quality: 90}))
}
func createCorruptedFile(path string) {
	must(os.WriteFile(path, []byte("this is a corrupted image file"), 0644))
}
