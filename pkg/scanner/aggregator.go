package scanner

import (
	"PICs_Manager/config"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mozillazg/go-unidecode"
)

const (
	aggregatorLogFileName = "aggregator.log"
	aggSuffix             = "_agg"
	archiveChars          = "ABCDEFGHIJKLMNOPQRSTUVWXYZ#"
)

type compiledRule struct {
	Name string
	Re   *regexp.Regexp
}
type LibraryAggregator interface {
	AggregateAndArchive(stagingPath, finalLibraryPath string) (map[string]string, error)
	Close()
}
type configBasedAggregator struct {
	seriesGroupRules []compiledRule
	numWorkers       int
	logger           *log.Logger
	logFile          *os.File
}

func NewAggregator(logDir string, rules []config.SeriesGroupRule, workerCount int) (LibraryAggregator, error) {
	logFilePath := filepath.Join(logDir, aggregatorLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化聚合器日志: %w", err)
	}
	logger := log.New(file, "AGGREGATE: ", log.LstdFlags|log.Lshortfile)
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}
	compiledRules := make([]compiledRule, 0, len(rules))
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("无效的系列分组模式 '%s': %w", rule.Name, err)
		}
		compiledRules = append(compiledRules, compiledRule{Name: rule.Name, Re: re})
	}
	return &configBasedAggregator{
		seriesGroupRules: compiledRules, numWorkers: workerCount, logger: logger, logFile: file,
	}, nil
}

func (a *configBasedAggregator) Close() {
	if a.logFile != nil {
		a.logger.Println("--- 聚合归档任务结束 ---")
		a.logFile.Close()
	}
}

// AggregateAndArchive (核心重构) - 实现了全新的三段式工作流 + changelog计算
func (a *configBasedAggregator) AggregateAndArchive(stagingPath, finalLibraryPath string) (map[string]string, error) {
	a.logger.Println("================== 新的聚合归档任务开始 ==================")

	if err := a.phase1_checkAndPrepareStructure(finalLibraryPath); err != nil {
		return nil, err
	}

	archiveMoved, _, err := a.phase2_archiveStagingFolders(stagingPath, finalLibraryPath)
	if err != nil {
		return nil, err
	}

	groupMoved, groupUnMoved, err := a.phase3_aggregateWithinArchiveFolders(finalLibraryPath, config.C.Scanner.QuarantinePath)
	if err != nil {
		return nil, err
	}

	// --- 最终 Changelog 计算 ---
	finalChangelog := make(map[string]string)
	// 1. achieveMoved ∪ groupMoved
	for src, dest := range archiveMoved {
		finalChangelog[src] = dest
	}
	for src, dest := range groupMoved {
		finalChangelog[src] = dest
	}

	// 2. achieveMoved ∩ groupUnMoved
	intersection := make(map[string]bool)
	for src := range archiveMoved {
		if _, exists := groupUnMoved[src]; exists {
			intersection[src] = true
		}
	}

	// 3. 执行减法
	for src := range intersection {
		delete(finalChangelog, src)
	}

	a.logger.Printf("聚合归档完成，最终生成 %d 项有效路径变更。", len(finalChangelog))
	return finalChangelog, nil
}

// --- 阶段一：库结构健康检查 ---
func (a *configBasedAggregator) phase1_checkAndPrepareStructure(finalLibraryPath string) error {
	a.logger.Println("--- 阶段 1/4: 检查并准备最终库结构 ---")
	// 确保最终库的根目录存在
	if err := os.MkdirAll(finalLibraryPath, 0755); err != nil {
		return err
	}
	expectedDirs := make(map[string]bool)
	for _, r := range archiveChars {
		expectedDirs[string(r)] = false
	}
	if _, err := os.Stat(finalLibraryPath); os.IsNotExist(err) {
		if err := os.MkdirAll(finalLibraryPath, 0755); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(finalLibraryPath)
	if err != nil {
		return fmt.Errorf("无法读取最终库目录: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := expectedDirs[name]; ok {
			if !entry.IsDir() {
				return fmt.Errorf("库结构不健康: '%s' 应为归档目录，但有文件存在", name)
			}
			expectedDirs[name] = true
		} else if !strings.HasPrefix(name, ".") && !strings.EqualFold(name, "Thumbs.db") {
			return fmt.Errorf("库结构不健康：顶层目录包含了非法的文件夹 '%s'", name)
		}
	}
	// 预先创建所有归档分类目录
	for _, char := range archiveChars {
		// 【核心修复】使用标准的 if err != nil 错误处理
		if err := os.MkdirAll(filepath.Join(finalLibraryPath, string(char)), 0755); err != nil {
			a.logger.Printf("警告：无法创建归档目录 %s: %v", string(char), err)
			return err // 如果无法创建基础目录，则中止
		}
	}
	return nil
}

// --- 阶段二：归档中转站文件夹 ---
func (a *configBasedAggregator) phase2_archiveStagingFolders(stagingPath, finalLibraryPath string) (map[string]string, map[string]bool, error) {
	a.logger.Println("--- 阶段 1/3: 归档中转站内容 ---")
	entries, err := os.ReadDir(stagingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var wg sync.WaitGroup
	tasks := make(chan string, len(entries))
	movedSet := make(map[string]string)
	unMovedSet := make(map[string]bool)
	var mu sync.Mutex

	for i := 0; i < a.numWorkers; i++ {
		wg.Add(1)
		go a.archiveWorker(&wg, stagingPath, finalLibraryPath, tasks, movedSet, unMovedSet, &mu)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			tasks <- entry.Name()
		}
	}
	close(tasks)
	wg.Wait()
	return movedSet, unMovedSet, nil
}
func (a *configBasedAggregator) archiveWorker(wg *sync.WaitGroup, stagingPath, finalLibraryPath string, tasks <-chan string, movedSet map[string]string, unMovedSet map[string]bool, mu *sync.Mutex) {
	defer wg.Done()
	for folderName := range tasks {
		oldPath, _ := filepath.Abs(filepath.Join(stagingPath, folderName))
		firstChar := findFirstAlphaNum(unidecode.Unidecode(folderName))
		archiveDirName := "#"
		if firstChar >= 'A' && firstChar <= 'Z' {
			archiveDirName = string(firstChar)
		}
		newPath := filepath.Join(finalLibraryPath, archiveDirName, folderName)

		mu.Lock()
		if _, err := os.Stat(newPath); err == nil {
			a.logger.Printf("归档冲突: 目标 '%s' 已存在，跳过移动。", newPath)
			unMovedSet[oldPath] = true
		} else {
			if err := os.Rename(oldPath, newPath); err != nil {
				a.logger.Printf("错误: 归档移动 %s 失败: %v", oldPath, err)
				unMovedSet[oldPath] = true
			} else {
				a.logger.Printf("归档移动: %s -> %s", oldPath, newPath)
				movedSet[oldPath] = newPath
			}
		}
		mu.Unlock()
	}
}

// --- 阶段三：在最终库内进行聚合 ---
func (a *configBasedAggregator) phase3_aggregateWithinArchiveFolders(finalLibraryPath, quarantinePath string) (map[string]string, map[string]bool, error) {
	a.logger.Println("--- 阶段 3/3: 在最终库内执行聚合 ---")
	var wg sync.WaitGroup
	archiveDirs, _ := os.ReadDir(finalLibraryPath)
	tasks := make(chan string, len(archiveDirs))
	movedSet := make(map[string]string)
	unMovedSet := make(map[string]bool)
	var mu sync.Mutex
	for i := 0; i < a.numWorkers; i++ {
		wg.Add(1)
		go a.aggregationWorker(&wg, tasks, quarantinePath, movedSet, unMovedSet, &mu)
	}
	for _, dir := range archiveDirs {
		if dir.IsDir() && len(dir.Name()) == 1 {
			tasks <- filepath.Join(finalLibraryPath, dir.Name())
		}
	}
	close(tasks)
	wg.Wait()
	return movedSet, unMovedSet, nil
}
func (a *configBasedAggregator) aggregationWorker(wg *sync.WaitGroup, tasks <-chan string, quarantinePath string, movedSet map[string]string, unMovedSet map[string]bool, mu *sync.Mutex) {
	defer wg.Done()
	for archivePath := range tasks {
		seriesEntries, err := os.ReadDir(archivePath)
		if err != nil || len(seriesEntries) < 2 {
			continue
		}
		var seriesPaths []string
		for _, entry := range seriesEntries {
			if entry.IsDir() {
				seriesPaths = append(seriesPaths, filepath.Join(archivePath, entry.Name()))
			}
		}
		if len(seriesPaths) < 2 {
			continue
		}

		groups := a.groupSeries(seriesPaths)
		for groupName, members := range groups {
			if len(members) < 2 {
				continue
			}
			var existingAggDir string
			var nonAggMembers []string
			for _, p := range members {
				if strings.HasSuffix(filepath.Base(p), aggSuffix) {
					existingAggDir = p
				} else {
					nonAggMembers = append(nonAggMembers, p)
				}
			}
			targetAggDir := existingAggDir
			if targetAggDir == "" {
				targetAggDir = filepath.Join(archivePath, sanitizeName(groupName)+aggSuffix)
			}
			if err := os.MkdirAll(targetAggDir, 0755); err != nil {
				a.logger.Printf("错误：无法创建聚合目录 %s: %v", targetAggDir, err)
				continue // 如果无法创建，则中止对这个组的处理
			}
			for _, memberPath := range nonAggMembers {
				newPath := filepath.Join(targetAggDir, filepath.Base(memberPath))
				a.groupMove(memberPath, newPath, quarantinePath, movedSet, unMovedSet, mu)
			}
		}
	}
}

// --- 辅助函数 ---
func (a *configBasedAggregator) groupSeries(seriesPaths []string) map[string][]string {
	groups := make(map[string][]string)
	for _, seriesPath := range seriesPaths {
		folderName := filepath.Base(seriesPath)
		var groupName string
		baseName := strings.TrimSuffix(folderName, aggSuffix)
		for _, rule := range a.seriesGroupRules {
			matches := rule.Re.FindStringSubmatch(baseName)
			if len(matches) > 1 {
				for i, n := range rule.Re.SubexpNames() {
					if n == "group" && i < len(matches) {
						groupName = matches[i]
						break
					}
				}
			}
			if groupName != "" {
				break
			}
		}
		if groupName != "" {
			groups[groupName] = append(groups[groupName], seriesPath)
		}
	}
	return groups
}

func (a *configBasedAggregator) groupMove(src, dest string, quarantinePath string, movedSet map[string]string, unMovedSet map[string]bool, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(dest); err == nil {
		a.logger.Printf("聚合冲突: 目标 '%s' 已存在，隔离源文件夹。", dest)
		unMovedSet[src] = true
		// 移动到隔离区
		quarantineDest := filepath.Join(quarantinePath, fmt.Sprintf("%s_%d", filepath.Base(src), time.Now().UnixNano()))
		if err := os.Rename(src, quarantineDest); err != nil {
			a.logger.Printf("错误: 隔离文件夹 '%s' 失败: %v", src, err)
		}
	} else {
		if err := os.Rename(src, dest); err != nil {
			a.logger.Printf("错误: 聚合移动 %s 失败: %v", src, err)
			unMovedSet[src] = true
		} else {
			a.logger.Printf("聚合移动: %s -> %s", src, dest)
			movedSet[src] = dest
		}
	}
}

func findFirstAlphaNum(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToUpper(r)
		}
	}
	return '#'
}
