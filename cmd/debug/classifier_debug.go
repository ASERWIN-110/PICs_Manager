//go:build ignore
// +build ignore

// ^^^ 在运行 go run classifier_debug.go 之前，请注释掉上面这两行

package main

import (
	"PICs_Manager/pkg/scanner" // 确认这是您正确的模块路径
	"log"
	"os"
	"path/filepath"
)

// ========================================================================
//
//	用户配置区域
//
// ========================================================================
// 步骤 1: 将此路径修改为您自己存放测试图片的文件夹路径！
const userSourceDir = "F:/Test/Test_NewFiles" // <--- 在这里修改为您的源文件路径

// 目标文件夹将由程序自动创建和清理，无需修改。
const destDir = "_debug_classifier_dest"

// ========================================================================

func main() {
	log.Println("======================================================")
	log.Println("===     Classifier 模块调试程序启动     ===")
	log.Println("===      (使用您自定义的测试数据)      ===")
	log.Println("======================================================")

	// 1. 准备测试环境，并从您的目录中发现所有文件
	filesToProcess := prepareTestEnvironment()
	// defer cleanup() // 确保程序退出时，只清理自动生成的目标文件夹

	if len(filesToProcess) == 0 {
		log.Printf("在源目录 %s 中没有找到任何文件。请检查路径。", userSourceDir)
		return
	}
	log.Printf("在源目录中发现了 %d 个文件，准备进行分类...", len(filesToProcess))

	// 2. 从配置中获取的模式 (硬编码用于测试)
	patterns := []string{
		`^(.*?)_(\d+)_p(\d+)_(\d+)(\.[a-zA-Z0-9_]+)?$`,
		`^(.*?)_(\d+)_p(\d+)(\.[a-zA-Z0-9_]+)?$`,
		`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`,
		`^(.*?)_pg(\d+)_(\d+)(\.[a-zA-Z0-9_]+)?$`,
		`^(.*?)_(\d+)_p(\d+).(\.[a-zA-Z0-9_]+)?$`,
	}
	workerCount := 12 // 您可以按需调整并发数

	// 3. 初始化分类器
	classifier, err := scanner.NewClassifier(destDir, patterns, workerCount)
	if err != nil {
		log.Fatalf("创建 Classifier 失败: %v", err)
	}
	defer classifier.Close()

	// 4. 执行分类和移动
	log.Println("\n--- 调用 Classifier.ClassifyAndMove() ---")
	seriesNames, fileNames, err := classifier.ClassifyAndMove(filesToProcess)
	if err != nil {
		log.Fatalf("执行 ClassifyAndMove 失败: %v", err)
	}
	log.Println("--- Classifier.ClassifyAndMove() 执行完毕 ---")

	// 5. 显示结果
	log.Println("\n=============================================")
	log.Println("===              执行结果              ===")
	log.Println("=============================================")
	log.Printf("任务完成！共处理了 %d 个文件，创建或更新了 %d 个系列文件夹。", len(fileNames), len(seriesNames))
	log.Println("--- 创建的系列名 ---")
	for _, name := range seriesNames {
		log.Printf("  -> %s", name)
	}
	log.Println("--- 处理的文件列表 ---")
	for _, name := range fileNames {
		log.Printf("  -> %s", name)
	}
	log.Printf("\n请检查程序目录下新生成的 '%s' 文件夹，验证文件是否被正确分类移动。", destDir)
}

// prepareTestEnvironment 准备测试环境，现在它会扫描用户目录而不是创建文件
func prepareTestEnvironment() []string {
	log.Println("\n--- 正在准备测试环境 ---")

	// 1. 验证用户源目录是否存在
	if _, err := os.Stat(userSourceDir); os.IsNotExist(err) {
		log.Fatalf("错误：您指定的源目录 '%s' 不存在！请修改 'userSourceDir' 常量。", userSourceDir)
	}
	log.Printf("将使用源目录: %s", userSourceDir)

	// 2. 清理并重建目标目录，确保每次运行都是全新的开始
	if err := os.RemoveAll(destDir); err != nil {
		log.Fatalf("清理旧的目标目录 %s 失败: %v", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Fatalf("创建新的目标目录 %s 失败: %v", destDir, err)
	}
	log.Printf("已创建空的临时目标目录: %s", destDir)

	// 3. 遍历源目录，收集所有文件路径
	// 注意：这里我们创建了一个文件副本列表，因为原始文件将被移动
	var filesToProcess []string
	var tempFilesDir = filepath.Join(destDir, "_temp_source_copy")
	must(os.MkdirAll(tempFilesDir, 0755))

	err := filepath.WalkDir(userSourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			// 将文件复制到临时目录进行处理，以避免直接移动您的原始文件
			sourceFile, err := os.ReadFile(path)
			if err != nil {
				log.Printf("警告：读取文件 %s 失败，跳过: %v", path, err)
				return nil
			}
			tempPath := filepath.Join(tempFilesDir, d.Name())
			if err := os.WriteFile(tempPath, sourceFile, 0644); err != nil {
				log.Printf("警告：复制文件到临时目录失败，跳过: %v", err)
				return nil
			}
			filesToProcess = append(filesToProcess, tempPath)
		}
		return nil
	})
	if err != nil {
		log.Fatalf("遍历源目录 %s 时出错: %v", userSourceDir, err)
	}

	return filesToProcess
}

// cleanup 只清理程序创建的目标目录，保证用户数据安全
func cleanup() {
	log.Println("\n--- 正在清理临时文件 ---")
	if err := os.RemoveAll(destDir); err != nil {
		log.Printf("清理目标目录 %s 失败: %v", destDir, err)
	} else {
		log.Printf("成功删除临时目标目录: %s", destDir)
	}
	// os.Remove("classifier.log")
	log.Println("--- 清理完毕 ---")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
