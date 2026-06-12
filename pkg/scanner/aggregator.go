package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/runstate"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
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

type archiveTask struct {
	sourcePath    string
	folderName    string
	finalBasePath string
}

type LibraryAggregator interface {
	AggregateAndArchive(stagingPath, finalLibraryPath, quarantinePath string) (map[string]string, error)
	Close()
}
type configBasedAggregator struct {
	seriesGroupRules []compiledRule
	numWorkers       int
	mediaRoots       map[string]struct{}
	recorder         runstate.Recorder
	logger           *log.Logger
	logFile          *os.File
}

func NewAggregatorWithMediaRoots(logDir string, rules []config.SeriesGroupRule, workerCount int, mediaRoots []string, recorders ...runstate.Recorder) (LibraryAggregator, error) {
	var recorder runstate.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	logFilePath := filepath.Join(logDir, aggregatorLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化聚合器日志: %w", err)
	}
	logger := log.New(file, "AGGREGATE: ", log.LstdFlags|log.Lshortfile)
	workerCount = defaultWorkerCount(workerCount)
	compiledRules := make([]compiledRule, 0, len(rules))
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("无效的系列分组模式 '%s': %w", rule.Name, err)
		}
		compiledRules = append(compiledRules, compiledRule{Name: rule.Name, Re: re})
	}
	roots := make(map[string]struct{}, len(mediaRoots))
	for _, root := range mediaRoots {
		root = strings.TrimSpace(root)
		if root != "" {
			roots[root] = struct{}{}
		}
	}
	return &configBasedAggregator{
		seriesGroupRules: compiledRules, numWorkers: workerCount, mediaRoots: roots, recorder: recorder, logger: logger, logFile: file,
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
	for mediaRoot := range a.mediaRoots {
		mediaBase := filepath.Join(finalLibraryPath, mediaRoot)
		if _, statErr := os.Stat(mediaBase); statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, statErr
		}
		moved, unmoved, err := a.phase3_aggregateWithinArchiveFolders(mediaBase, quarantinePath)
		if err != nil {
			return nil, err
		}
		for src, dest := range moved {
			groupMoved[src] = dest
		}
		for src := range unmoved {
			groupUnMoved[src] = true
		}
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
		} else if _, ok := a.mediaRoots[name]; ok {
			if !entry.IsDir() {
				return fmt.Errorf("库结构不健康: 媒体根 '%s' 应为目录，但有文件存在", name)
			}
		} else if !isIgnoredFilesystemEntry(name) {
			return fmt.Errorf("库结构不健康：顶层目录包含了非法的文件夹 '%s'", name)
		}
	}
	if err := a.prepareArchiveDirs(finalLibraryPath); err != nil {
		return err
	}
	for mediaRoot := range a.mediaRoots {
		if err := a.prepareArchiveDirs(filepath.Join(finalLibraryPath, mediaRoot)); err != nil {
			return err
		}
	}
	return nil
}

func (a *configBasedAggregator) prepareArchiveDirs(basePath string) error {
	for _, char := range archiveChars {
		if err := os.MkdirAll(filepath.Join(basePath, string(char)), 0755); err != nil {
			a.logger.Printf("警告：无法创建归档目录 %s: %v", filepath.Join(basePath, string(char)), err)
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

	archiveTasks := make([]archiveTask, 0, len(entries))
	var structureErrs []error
	for _, entry := range entries {
		if entry.IsDir() {
			entryPath := filepath.Join(stagingPath, entry.Name())
			if _, ok := a.mediaRoots[entry.Name()]; ok {
				children, err := os.ReadDir(entryPath)
				if err != nil {
					structureErrs = append(structureErrs, fmt.Errorf("无法读取媒体中转目录 %s: %w", entryPath, err))
					continue
				}
				for _, child := range children {
					childPath := filepath.Join(entryPath, child.Name())
					if child.IsDir() {
						archiveTasks = append(archiveTasks, archiveTask{
							sourcePath:    childPath,
							folderName:    child.Name(),
							finalBasePath: filepath.Join(finalLibraryPath, entry.Name()),
						})
						continue
					}
					if !isIgnoredFilesystemEntry(child.Name()) {
						structureErrs = append(structureErrs, fmt.Errorf("媒体中转目录包含非目录条目 %s", childPath))
					}
				}
				continue
			}
			archiveTasks = append(archiveTasks, archiveTask{
				sourcePath:    entryPath,
				folderName:    entry.Name(),
				finalBasePath: finalLibraryPath,
			})
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
	tasks := make(chan archiveTask, len(archiveTasks))
	movedSet := make(map[string]string)
	unMovedSet := make(map[string]bool)
	var mu sync.Mutex

	for i := 0; i < a.numWorkers; i++ {
		wg.Add(1)
		go a.archiveWorker(&wg, quarantinePath, tasks, movedSet, unMovedSet, &mu)
	}
	for _, task := range archiveTasks {
		tasks <- task
	}
	close(tasks)
	wg.Wait()
	return movedSet, unMovedSet, nil
}
func (a *configBasedAggregator) archiveWorker(wg *sync.WaitGroup, quarantinePath string, tasks <-chan archiveTask, movedSet map[string]string, unMovedSet map[string]bool, mu *sync.Mutex) {
	defer wg.Done()
	for task := range tasks {
		folderName := task.folderName
		oldPath, err := filepath.Abs(task.sourcePath)
		if err != nil {
			sourcePath := task.sourcePath
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
		newPath := filepath.Join(task.finalBasePath, archiveDirName, folderName)

		mu.Lock()
		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
			a.logger.Printf("错误: 创建归档父目录 %s 失败: %v", filepath.Dir(newPath), err)
			unMovedSet[oldPath] = true
			mu.Unlock()
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			a.logger.Printf("归档目标 '%s' 已存在，合并中转目录。", newPath)
			a.record(runstate.Event{Phase: "archive", Action: "dir_before_merge", Source: oldPath, Target: newPath})
			if err := a.mergeDirectoryContents(oldPath, newPath, quarantinePath); err != nil {
				a.logger.Printf("错误: 合并归档目录 %s -> %s 失败: %v", oldPath, newPath, err)
				unMovedSet[oldPath] = true
				a.record(runstate.Event{Phase: "archive", Action: "dir_after_merge", Source: oldPath, Target: newPath, Status: "failed", Error: err.Error()})
			} else {
				a.logger.Printf("归档合并: %s -> %s", oldPath, newPath)
				movedSet[oldPath] = newPath
				a.record(runstate.Event{Phase: "archive", Action: "dir_after_merge", Source: oldPath, Target: newPath, Status: "merged"})
			}
		} else {
			a.record(runstate.Event{Phase: "archive", Action: "dir_before_move", Source: oldPath, Target: newPath})
			if err := os.Rename(oldPath, newPath); err != nil {
				a.logger.Printf("错误: 归档移动 %s 失败: %v", oldPath, err)
				unMovedSet[oldPath] = true
				a.record(runstate.Event{Phase: "archive", Action: "dir_after_move", Source: oldPath, Target: newPath, Status: "failed", Error: err.Error()})
			} else {
				a.logger.Printf("归档移动: %s -> %s", oldPath, newPath)
				movedSet[oldPath] = newPath
				a.record(runstate.Event{Phase: "archive", Action: "dir_after_move", Source: oldPath, Target: newPath, Status: "moved"})
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
		a.record(runstate.Event{Phase: "aggregate", Action: "dir_before_merge", Source: src, Target: dest})
		if err := a.mergeDirectoryContents(src, dest, quarantinePath); err != nil {
			a.logger.Printf("错误: 聚合合并 %s -> %s 失败: %v", src, dest, err)
			unMovedSet[src] = true
			quarantineDest := filepath.Join(quarantinePath, fmt.Sprintf("%s_%d", filepath.Base(src), time.Now().UnixNano()))
			if quarantinePath != "" {
				if err := os.MkdirAll(quarantinePath, 0755); err != nil {
					a.logger.Printf("错误: 创建隔离目录 '%s' 失败: %v", quarantinePath, err)
				} else if err := os.Rename(src, quarantineDest); err != nil {
					a.logger.Printf("错误: 隔离文件夹 '%s' 失败: %v", src, err)
				} else {
					a.record(runstate.Event{Phase: "aggregate", Action: "dir_quarantined", Source: src, Target: quarantineDest, Status: "quarantined"})
				}
			}
			a.record(runstate.Event{Phase: "aggregate", Action: "dir_after_merge", Source: src, Target: dest, Status: "failed", Error: err.Error()})
		} else {
			a.logger.Printf("聚合合并: %s -> %s", src, dest)
			movedSet[src] = dest
			a.record(runstate.Event{Phase: "aggregate", Action: "dir_after_merge", Source: src, Target: dest, Status: "merged"})
		}
	} else {
		a.record(runstate.Event{Phase: "aggregate", Action: "dir_before_move", Source: src, Target: dest})
		if err := os.Rename(src, dest); err != nil {
			a.logger.Printf("错误: 聚合移动 %s 失败: %v", src, err)
			unMovedSet[src] = true
			a.record(runstate.Event{Phase: "aggregate", Action: "dir_after_move", Source: src, Target: dest, Status: "failed", Error: err.Error()})
		} else {
			a.logger.Printf("聚合移动: %s -> %s", src, dest)
			movedSet[src] = dest
			a.record(runstate.Event{Phase: "aggregate", Action: "dir_after_move", Source: src, Target: dest, Status: "moved"})
		}
	}
}

func (a *configBasedAggregator) mergeDirectoryContents(srcDir, destDir, quarantinePath string) error {
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
			if err := a.mergeDirectoryContents(srcPath, destPath, quarantinePath); err != nil {
				return err
			}
			continue
		}

		a.record(runstate.Event{Phase: "archive", Action: "file_before_merge", Source: srcPath, Target: destPath})
		finalPath, _, duplicate, err := resolveSameNameTarget(srcPath, destPath, isSameNameSourcePath(srcPath))
		if err != nil {
			a.record(runstate.Event{Phase: "archive", Action: "file_after_merge", Source: srcPath, Target: destPath, Status: "failed", Error: err.Error()})
			return err
		}
		status := "moved"
		if duplicate {
			status = "duplicate_removed"
		} else if isSameNameSourcePath(finalPath) {
			status = "same_name"
		}
		a.record(runstate.Event{Phase: "archive", Action: "file_after_merge", Source: srcPath, Target: finalPath, Status: status})
	}

	a.record(runstate.Event{Phase: "archive", Action: "dir_before_remove", Source: srcDir})
	if err := os.Remove(srcDir); err != nil {
		a.record(runstate.Event{Phase: "archive", Action: "dir_after_remove", Source: srcDir, Status: "failed", Error: err.Error()})
		return err
	}
	a.record(runstate.Event{Phase: "archive", Action: "dir_after_remove", Source: srcDir, Status: "removed"})
	return nil
}

func (a *configBasedAggregator) record(event runstate.Event) {
	if a.recorder.Store == nil || a.recorder.RunID == "" {
		return
	}
	a.recorder.Event(context.Background(), event)
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
