//go:build ignore
// +build ignore

// ^^^ 在运行此脚本前，请注释掉上面两行

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ========================================================================
//
//	用户配置区域
//
// ========================================================================
// 请将此路径修改为您自己存放测试数据的文件夹路径
const userSourceDir = "F:/Test/Test_Staging" // <--- 在这里修改

const testRootDir = "_debug_aggregation_real_data"
const aggSuffix = "_agg"

// ========================================================================

// Rule 结构体用于存放规则名称和已编译的正则表达式
type Rule struct {
	Name string
	Re   *regexp.Regexp
}

// sanitizeName 清理从正则中提取出的组名
func sanitizeName(name string) string {
	name = regexp.MustCompile(`[\[\]『』《》()（）【】]`).ReplaceAllString(name, " ")
	r := strings.NewReplacer("<", " ", ">", " ", ":", " ", "\"", " ", "/", " ", "\\", " ", "|", " ", "?", " ", "*", " ")
	s := r.Replace(name)
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	s = strings.TrimRight(s, ". ")
	return strings.TrimSpace(s)
}

func main() {
	log.Println("========================================")
	log.Println("===   聚合核心逻辑专项测试程序启动   ===")
	log.Println("===       (使用您自己的真实数据)       ===")
	log.Println("========================================")

	// 1. 设置测试环境，将您的数据安全地复制到临时目录
	setupTestEnvironment()
	// 在这个版本中，我们默认不自动清理，方便您检查结果
	// cleanupTestEnvironment()

	// 2. 准备规则
	rulesToCompile := []struct{ Name, Pattern string }{
		{"前置序号", `^\s*\(\d+\)\s*(?P<group>.*)`},
		{"括号", `^[『「《[(【（](?P<group>.*?)[』」》)\]】）]`},
		{"文本+数字", `^(?P<group>.+?)\s*(\d+)$`},
	}
	var compiledRules []Rule
	for _, r := range rulesToCompile {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			log.Fatalf("编译正则 '%s' 失败: %v", r.Name, err)
		}
		compiledRules = append(compiledRules, Rule{Name: r.Name, Re: re})
	}

	// 3. 准备待处理的系列列表 (从临时的测试目录中读取)
	seriesEntries, _ := os.ReadDir(testRootDir)
	var seriesPaths []string
	for _, entry := range seriesEntries {
		if entry.IsDir() {
			seriesPaths = append(seriesPaths, filepath.Join(testRootDir, entry.Name()))
		}
	}
	log.Printf("准备处理目录下的 %d 个系列", len(seriesPaths))

	// -------------------------------------------------
	// ---  核心分组与聚合逻辑 ---
	// -------------------------------------------------
	groups := make(map[string][]string)
	for _, seriesPath := range seriesPaths {
		folderName := filepath.Base(seriesPath)
		var groupName string
		baseName := strings.TrimSuffix(folderName, aggSuffix)
		for _, rule := range compiledRules {
			matches := rule.Re.FindStringSubmatch(baseName)
			if len(matches) > 1 {
				for i, n := range rule.Re.SubexpNames() {
					if n == "group" && i < len(matches) {
						groupName = sanitizeName(matches[i])
						break
					}
				}
			}
			if groupName != "" {
				break
			}
		}
		if groupName == "" {
			groupName = sanitizeName(baseName)
		}
		if groupName != "" {
			groups[groupName] = append(groups[groupName], seriesPath)
		}
	}

	log.Println("\n--- 分组结果检查点 ---")
	for name, members := range groups {
		if len(members) >= 2 {
			log.Printf("  [可聚合] 组名: '%s', 成员数: %d\n", name, len(members))
		}
	}
	log.Println("----------------------")

	log.Println("\n--- 开始执行聚合移动 ---")
	for groupName, members := range groups {
		if len(members) < 2 {
			continue
		}
		log.Printf("处理聚合组: '%s'", groupName)
		targetAggDir := filepath.Join(testRootDir, sanitizeName(groupName)+aggSuffix)
		must(os.MkdirAll(targetAggDir, 0755))
		log.Printf("  -> 创建/确认聚合目录: %s", targetAggDir)

		for _, memberPath := range members {
			newPath := filepath.Join(targetAggDir, filepath.Base(memberPath))
			log.Printf("  -> 移动: %s -> %s", memberPath, newPath)
			must(os.Rename(memberPath, newPath))
		}
	}
	log.Println("--- 聚合移动完成 ---\n")

	// 4. 最终提示
	log.Println("========================================")
	log.Println("===           测试执行完毕           ===")
	log.Println("========================================")
	log.Printf("请手动检查项目目录下的 '%s' 文件夹，\n查看聚合结果是否符合您的预期。", testRootDir)
	log.Println("您可以反复修改 userSourceDir 并重新运行此脚本进行测试。")
}

func setupTestEnvironment() {
	log.Println("\n--- 正在设置测试环境 ---")
	cleanupTestEnvironment() // 先清理上一次的临时目录

	// 检查用户指定的源目录是否存在
	if _, err := os.Stat(userSourceDir); os.IsNotExist(err) {
		log.Fatalf("错误！您指定的源目录 '%s' 不存在，请修改脚本中的 userSourceDir 常量。", userSourceDir)
	}

	// 创建临时的根目录
	must(os.MkdirAll(testRootDir, 0755))

	// 将用户数据复制到临时目录进行处理，确保原始数据安全
	log.Printf("正在从 '%s' 复制数据到临时目录 '%s'...", userSourceDir, testRootDir)
	must(copyDir(userSourceDir, testRootDir))
	log.Println("--- 测试环境设置完毕 ---")
}

func cleanupTestEnvironment() {
	log.Println("正在清理旧的测试目录...")
	os.RemoveAll(testRootDir)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// copyDir 递归地复制一个目录
func copyDir(src, dest string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, destPath); err != nil {
				return err
			}
		} else {
			srcFile, err := os.Open(srcPath)
			if err != nil {
				return err
			}
			defer srcFile.Close()
			destFile, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer destFile.Close()
			if _, err := io.Copy(destFile, srcFile); err != nil {
				return err
			}
		}
	}
	return nil
}
