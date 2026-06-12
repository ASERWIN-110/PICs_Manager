package scanner

import (
	"PICs_Manager/config"
	"errors"
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
	AggregateAndArchive(stagingPath, finalLibraryPath, quarantinePath string) (map[string]string, error)
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

func (a *configBasedAggregator) AggregateAndArchive(stagingPath, finalLibraryPath, quarantinePath string) (map[string]string, error) {
	a.logger.Println("================== 新的聚合归档任务开始 ==================")

	if err := a.phase1_checkAndPrepareStructure(finalLibraryPath); err != nil {
		return nil, err
	}

	archiveMoved, _, err := a.phase2_archiveStagingFolders(stagingPath, finalLibraryPath, quarantinePath)
	if err != nil {
		return nil, err
	}

	groupMoved, groupUnMoved, err := a.phase3_aggregateWithinArchiveFolders(finalLibraryPath, quarantinePath)
	if err != nil {
		return nil, err
	}

	finalChangelog := make(map[string]string)
	for src, dest := range archiveMoved {
		finalChangelog[src] = dest
	}
	for src, dest := range groupMoved {
		finalChangelog[src] = dest
	}

	intersection := make(map[string]bool)
	for src := range archiveMoved {
		if _, exists := groupUnMoved[src]; exists {
			intersection[src] = true
		}
	}

	for src := range intersection {
		delete(finalChangelog, src)
	}

	a.logger.Printf("聚合归档完成，最终生成 %d 项有效路径变更。", len(finalChangelog))
	return finalChangelog, nil
}

func (a *configBasedAggregator) phase1_checkAndPrepareStructure(finalLibraryPath string) error {
	a.logger.Println("--- 检查并准备最终库结构 ---")
	if err := os.MkdirAll(finalLibraryPath, 0755); err != nil {
		return err
	}
	expectedDirs := make(map[string]bool)
	for _, r := range archiveChars {
		expectedDirs[string(r)] = false
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
		} else if !isIgnoredFilesystemEntry(name) {
			return fmt.Errorf("库结构不健康：顶层目录包含了非法的文件夹 '%s'", name)
		}
	}
	for _, char := range archiveChars {
		if err := os.MkdirAll(filepath.Join(finalLibraryPath, string(char)), 0755); err != nil {
			a.logger.Printf("警告：无法创建归档目录 %s: %v", string(char), err)
			return err
		}
	}
	return nil
}

func (a *configBasedAggregator) phase2_archiveStagingFolders(stagingPath, finalLibraryPath, quarantinePath string) (map[string]string, map[string]bool, error) {
	a.logger.Println("--- 归档中转站内容 ---")
	entries, err := os.ReadDir(stagingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	folderNames := make([]string, 0, len(entries))
	var structureErrs []error
	for _, entry := range entries {
		if entry.IsDir() {
			folderNames = append(folderNames, entry.Name())
			continue
		}
		if !isIgnoredFilesystemEntry(entry.Name()) {
			structureErrs = append(structureErrs, fmt.Errorf("中转站包含非目录条目 %s", filepath.Join(stagingPath, entry.Name())))
		}
	}
	if len(structureErrs) > 0 {
		return nil, nil, errors.Join(structureErrs...)
	}

	var wg sync.WaitGroup
	tasks := make(chan string, len(folderNames))
	movedSet := make(map[string]string)
	unMovedSet := make(map[string]bool)
	var mu sync.Mutex

	for i := 0; i < a.numWorkers; i++ {
		wg.Add(1)
		go a.archiveWorker(&wg, stagingPath, finalLibraryPath, quarantinePath, tasks, movedSet, unMovedSet, &mu)
	}
	for _, folderName := range folderNames {
		tasks <- folderName
	}
	close(tasks)
	wg.Wait()
	return movedSet, unMovedSet, nil
}
func (a *configBasedAggregator) archiveWorker(wg *sync.WaitGroup, stagingPath, finalLibraryPath, quarantinePath string, tasks <-chan string, movedSet map[string]string, unMovedSet map[string]bool, mu *sync.Mutex) {
	defer wg.Done()
	for folderName := range tasks {
		oldPath, err := filepath.Abs(filepath.Join(stagingPath, folderName))
		if err != nil {
			sourcePath := filepath.Join(stagingPath, folderName)
			a.logger.Printf("错误: 解析归档源路径 %s 失败: %v", sourcePath, err)
			mu.Lock()
			unMovedSet[sourcePath] = true
			mu.Unlock()
			continue
		}
		firstChar := findFirstAlphaNum(unidecode.Unidecode(folderName))
		archiveDirName := "#"
		if firstChar >= 'A' && firstChar <= 'Z' {
			archiveDirName = string(firstChar)
		}
		newPath := filepath.Join(finalLibraryPath, archiveDirName, folderName)

		mu.Lock()
		if _, err := os.Stat(newPath); err == nil {
			a.logger.Printf("归档目标 '%s' 已存在，合并中转目录。", newPath)
			if err := mergeDirectoryContents(oldPath, newPath, quarantinePath); err != nil {
				a.logger.Printf("错误: 合并归档目录 %s -> %s 失败: %v", oldPath, newPath, err)
				unMovedSet[oldPath] = true
			} else {
				a.logger.Printf("归档合并: %s -> %s", oldPath, newPath)
				movedSet[oldPath] = newPath
			}
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

func (a *configBasedAggregator) phase3_aggregateWithinArchiveFolders(finalLibraryPath, quarantinePath string) (map[string]string, map[string]bool, error) {
	a.logger.Println("--- 在最终库内执行聚合 ---")
	var wg sync.WaitGroup
	archiveDirs, err := os.ReadDir(finalLibraryPath)
	if err != nil {
		return nil, nil, fmt.Errorf("无法读取最终库归档目录: %w", err)
	}
	tasks := make(chan string, len(archiveDirs))
	errs := make(chan error, len(archiveDirs))
	movedSet := make(map[string]string)
	unMovedSet := make(map[string]bool)
	var mu sync.Mutex
	for i := 0; i < a.numWorkers; i++ {
		wg.Add(1)
		go a.aggregationWorker(&wg, tasks, quarantinePath, movedSet, unMovedSet, &mu, errs)
	}
	for _, dir := range archiveDirs {
		if dir.IsDir() && len(dir.Name()) == 1 {
			tasks <- filepath.Join(finalLibraryPath, dir.Name())
		}
	}
	close(tasks)
	wg.Wait()
	close(errs)
	var readErrs []error
	for err := range errs {
		readErrs = append(readErrs, err)
	}
	return movedSet, unMovedSet, errors.Join(readErrs...)
}
func (a *configBasedAggregator) aggregationWorker(wg *sync.WaitGroup, tasks <-chan string, quarantinePath string, movedSet map[string]string, unMovedSet map[string]bool, mu *sync.Mutex, errs chan<- error) {
	defer wg.Done()
	for archivePath := range tasks {
		seriesEntries, err := os.ReadDir(archivePath)
		if err != nil {
			errs <- fmt.Errorf("无法读取归档目录 %s: %w", archivePath, err)
			continue
		}
		if len(seriesEntries) < 2 {
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
				continue
			}
			for _, memberPath := range nonAggMembers {
				newPath := filepath.Join(targetAggDir, filepath.Base(memberPath))
				a.groupMove(memberPath, newPath, quarantinePath, movedSet, unMovedSet, mu)
			}
		}
	}
}

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
		a.logger.Printf("聚合目标 '%s' 已存在，合并源文件夹。", dest)
		if err := mergeDirectoryContents(src, dest, quarantinePath); err != nil {
			a.logger.Printf("错误: 聚合合并 %s -> %s 失败: %v", src, dest, err)
			unMovedSet[src] = true
			quarantineDest := filepath.Join(quarantinePath, fmt.Sprintf("%s_%d", filepath.Base(src), time.Now().UnixNano()))
			if quarantinePath != "" {
				if err := os.MkdirAll(quarantinePath, 0755); err != nil {
					a.logger.Printf("错误: 创建隔离目录 '%s' 失败: %v", quarantinePath, err)
				} else if err := os.Rename(src, quarantineDest); err != nil {
					a.logger.Printf("错误: 隔离文件夹 '%s' 失败: %v", src, err)
				}
			}
		} else {
			a.logger.Printf("聚合合并: %s -> %s", src, dest)
			movedSet[src] = dest
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

func mergeDirectoryContents(srcDir, destDir, quarantinePath string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		destPath := filepath.Join(destDir, entry.Name())
		if entry.IsDir() {
			if err := mergeDirectoryContents(srcPath, destPath, quarantinePath); err != nil {
				return err
			}
			continue
		}

		if _, _, _, err := resolveSameNameTarget(srcPath, destPath, isSameNameSourcePath(srcPath)); err != nil {
			return err
		}
	}

	return os.Remove(srcDir)
}

func findFirstAlphaNum(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToUpper(r)
		}
	}
	return '#'
}

func isIgnoredFilesystemEntry(name string) bool {
	return strings.HasPrefix(name, ".") || strings.EqualFold(name, "Thumbs.db")
}
