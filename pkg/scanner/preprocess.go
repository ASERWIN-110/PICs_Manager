package scanner

import (
	"PICs_Manager/pkg/runstate"
	"context"
	"fmt"
	"image"

	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	preprocessLogFileName = "preprocessor_corruption.log"
	maxRepairAttempts     = 5
)

type fileGroup struct {
	basePath      string
	numberedFiles map[int]string
}

type ImagePreprocessor interface {
	ProcessDirectory(rootDir string) ([]string, error)
	Close()
}

type defaultPreprocessor struct {
	numWorkers int
	recorder   runstate.Recorder
	logger     *log.Logger
	logFile    *os.File
}

func NewPreprocessor(logDir string, workerCount int, recorders ...runstate.Recorder) (ImagePreprocessor, error) {
	var recorder runstate.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	logFilePath := filepath.Join(logDir, preprocessLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化预处理器日志: %w", err)
	}
	logger := log.New(file, "PREPROCESS: ", log.LstdFlags|log.Lshortfile)
	workerCount = defaultWorkerCount(workerCount)
	logger.Printf("预处理器初始化成功，并发数: %d", workerCount)
	return &defaultPreprocessor{numWorkers: workerCount, recorder: recorder, logger: logger, logFile: file}, nil
}

func (p *defaultPreprocessor) Close() {
	if p.logFile != nil {
		p.logger.Println("================== 预处理任务结束 ==================")
		p.logFile.Close()
	}
}

func (p *defaultPreprocessor) ProcessDirectory(rootDir string) ([]string, error) {
	p.logger.Println("================== 新的预处理任务开始 ==================")
	p.logger.Println("--- 步骤 1/2: 扫描并分组所有文件 ---")
	groups, err := p.scanAndGroupFiles(rootDir)
	if err != nil {
		return nil, fmt.Errorf("扫描和分组文件失败: %w", err)
	}

	if len(groups) > 0 {
		p.logger.Printf("发现 %d 个文件家族需要整理，开始并发处理...", len(groups))
		var wg sync.WaitGroup
		tasks := make(chan *fileGroup, len(groups))
		for i := 0; i < p.numWorkers; i++ {
			wg.Add(1)
			go p.reconciliationWorker(&wg, tasks)
		}
		for _, group := range groups {
			tasks <- group
		}
		close(tasks)
		wg.Wait()
		p.logger.Println("--- 步骤 2/2: 并发整理完成 ---")
	}

	var finalFiles []string
	err = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			finalFiles = append(finalFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取最终文件列表失败: %w", err)
	}

	p.logger.Printf("预处理完成，最终剩余 %d 个文件。", len(finalFiles))
	return finalFiles, nil
}

func (p *defaultPreprocessor) scanAndGroupFiles(rootDir string) (map[string]*fileGroup, error) {
	groups := make(map[string]*fileGroup)
	re := regexp.MustCompile(`^(.*?)(?: \((\d+)\))?(\.\w+)$`)
	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isImageExtension(path) {
			return nil
		}
		fileName := d.Name()
		matches := re.FindStringSubmatch(fileName)
		if len(matches) < 4 {
			return nil
		}
		baseName, numberStr, ext := matches[1], matches[2], matches[3]
		groupKey := strings.ToLower(filepath.Join(filepath.Dir(path), baseName+ext))
		if _, ok := groups[groupKey]; !ok {
			groups[groupKey] = &fileGroup{numberedFiles: make(map[int]string)}
		}
		if numberStr == "" {
			groups[groupKey].basePath = path
		} else {
			num, _ := strconv.Atoi(numberStr)
			if num > 0 {
				groups[groupKey].numberedFiles[num] = path
			}
		}
		return nil
	})
	return groups, err
}

// reconciliationWorker 保留下载器的“基础文件损坏、编号副本补位”语义，但不删除任何输入内容。
func (p *defaultPreprocessor) reconciliationWorker(wg *sync.WaitGroup, tasks <-chan *fileGroup) {
	defer wg.Done()
	for group := range tasks {
		if len(group.numberedFiles) == 0 {
			continue
		}
		if group.basePath == "" {
			continue
		}

		if isImageFileDamaged(group.basePath) {
			p.findAndExecuteRepair(group)
		} else {
			nums := make([]int, 0, len(group.numberedFiles))
			for num := range group.numberedFiles {
				nums = append(nums, num)
			}
			sort.Ints(nums)
			for _, num := range nums {
				numberedPath := group.numberedFiles[num]
				targetPath, _, duplicate, err := normalizeNumberedCopy(numberedPath, group.basePath)
				if err != nil {
					p.logger.Printf("编号副本 '%s' 整理失败: %v", filepath.Base(numberedPath), err)
					continue
				}
				if duplicate {
					p.logger.Printf("编号副本 '%s' 与基础文件哈希相同，已删除重复文件。", filepath.Base(numberedPath))
					p.record(runstate.Event{Phase: "preprocess", Action: "file_after_preprocess", Source: numberedPath, Target: targetPath, Status: "duplicate_removed"})
					continue
				}
				p.logger.Printf("编号副本 '%s' 与基础文件不同，已整理为 '%s'。", filepath.Base(numberedPath), filepath.Base(targetPath))
				p.record(runstate.Event{Phase: "preprocess", Action: "file_after_preprocess", Source: numberedPath, Target: targetPath, Status: "same_name"})
			}
		}
	}
}

func normalizeNumberedCopy(srcPath, basePath string) (targetPath string, targetName string, duplicate bool, err error) {
	same, err := sameFileHash(srcPath, basePath)
	if err != nil {
		return "", "", false, err
	}
	if same {
		if err := os.Remove(srcPath); err != nil {
			return "", "", false, fmt.Errorf("无法删除同哈希编号副本 %s: %w", srcPath, err)
		}
		return basePath, filepath.Base(basePath), true, nil
	}

	return moveToSameNameTarget(srcPath, basePath)
}

func (p *defaultPreprocessor) findAndExecuteRepair(group *fileGroup) {
	p.logger.Printf("修复模式: 基础文件 '%s' 损坏。", filepath.Base(group.basePath))

	baseName := strings.TrimSuffix(filepath.Base(group.basePath), filepath.Ext(group.basePath))
	ext := filepath.Ext(group.basePath)
	dir := filepath.Dir(group.basePath)

	for i := 1; i <= maxRepairAttempts; i++ {
		candidateName := fmt.Sprintf("%s (%d)%s", baseName, i, ext)
		candidatePath := filepath.Join(dir, candidateName)

		var foundInMap bool
		for _, path := range group.numberedFiles {
			if path == candidatePath {
				foundInMap = true
				break
			}
		}
		if !foundInMap {
			p.logger.Printf("  -> 修复中止: 未在文件组中找到候选文件 %s，停止查找。", candidateName)
			break
		}

		if isImageFileDamaged(candidatePath) {
			p.logger.Printf("  -> 候选文件 %s 已损坏，继续寻找下一个...", candidateName)
			p.record(runstate.Event{Phase: "preprocess", Action: "repair_candidate_damaged", Source: candidatePath, Status: "damaged"})
			continue
		}

		corruptedOriginalPath, _, err := nextAvailablePath(filepath.Join(dir, fmt.Sprintf("%s_corrupted_original%s", baseName, ext)))
		if err != nil {
			p.logger.Printf("错误: 生成损坏原件保留路径失败: %v", err)
			return
		}
		p.record(runstate.Event{Phase: "preprocess", Action: "file_before_repair", Source: group.basePath, Target: corruptedOriginalPath, Status: "move_corrupted_original"})
		if err := os.Rename(group.basePath, corruptedOriginalPath); err != nil {
			p.logger.Printf("错误: 保留损坏基础文件失败: %v", err)
			p.record(runstate.Event{Phase: "preprocess", Action: "file_after_repair", Source: group.basePath, Target: corruptedOriginalPath, Status: "failed", Error: err.Error()})
			return
		}
		p.record(runstate.Event{Phase: "preprocess", Action: "file_before_repair", Source: candidatePath, Target: group.basePath, Status: "promote_candidate"})
		if err := os.Rename(candidatePath, group.basePath); err != nil {
			p.logger.Printf("错误: 重命名修复文件失败: %v", err)
			if rollbackErr := os.Rename(corruptedOriginalPath, group.basePath); rollbackErr != nil {
				p.logger.Printf("严重错误: 修复回滚失败: %v", rollbackErr)
			}
			p.record(runstate.Event{Phase: "preprocess", Action: "file_after_repair", Source: candidatePath, Target: group.basePath, Status: "failed", Error: err.Error()})
			return
		}
		p.record(runstate.Event{Phase: "preprocess", Action: "file_after_repair", Source: candidatePath, Target: group.basePath, Status: "repaired"})
		p.logger.Printf("  -> 文件修复成功: '%s' 已由 '%s' 补位；损坏原件保留为 '%s'。",
			filepath.Base(group.basePath), candidateName, filepath.Base(corruptedOriginalPath))
		return
	}
	p.logger.Printf("  -> 未能为 '%s' 找到任何健康的修复副本。", filepath.Base(group.basePath))
}

func (p *defaultPreprocessor) record(event runstate.Event) {
	if p.recorder.Store == nil || p.recorder.RunID == "" {
		return
	}
	p.recorder.Event(context.Background(), event)
}

// isImageFileDamaged 是一个不带 receiver 的辅助函数版本
func isImageFileDamaged(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer file.Close()
	_, _, err = image.Decode(file)
	return err != nil
}

// isImageExtension 是一个包内可用的辅助函数
func isImageExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}
