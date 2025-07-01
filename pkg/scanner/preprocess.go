package scanner

import (
	"PICs_Manager/pkg/hasher"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const (
	preprocessLogFileName = "preprocessor_corruption.log"
	maxRepairAttempts     = 5
)

// fileGroup 用于组织一个“文件家族”
type fileGroup struct {
	basePath      string
	numberedFiles map[int]string
}

// ImagePreprocessor 接口不变
type ImagePreprocessor interface {
	ProcessDirectory(rootDir string) ([]string, error)
	Close()
}

type defaultPreprocessor struct {
	numWorkers int
	logger     *log.Logger
	logFile    *os.File
}

// NewPreprocessor 构造函数不变
func NewPreprocessor(logDir string, workerCount int) (ImagePreprocessor, error) {
	logFilePath := filepath.Join(logDir, preprocessLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化预处理器日志: %w", err)
	}
	logger := log.New(file, "PREPROCESS: ", log.LstdFlags|log.Lshortfile)
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}
	logger.Printf("预处理器初始化成功，并发数: %d", workerCount)
	return &defaultPreprocessor{numWorkers: workerCount, logger: logger, logFile: file}, nil
}

// Close 方法不变
func (p *defaultPreprocessor) Close() {
	if p.logFile != nil {
		p.logger.Println("================== 预处理任务结束 ==================")
		p.logFile.Close()
	}
}

// ProcessDirectory 的主体流程不变
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

// scanAndGroupFiles 函数逻辑不变
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

// reconciliationWorker (核心修改)
// 内部逻辑简化，调用专门的修复函数
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
			// 场景A：基础文件损坏，调用专门的修复函数
			p.findAndExecuteRepair(group)
		} else {
			// 场景B：基础文件健康，执行去重逻辑
			p.logger.Printf("去重模式: 基础文件 '%s' 健康。", filepath.Base(group.basePath))
			baseHash, err := hasher.CalculateSHA256(group.basePath)
			if err != nil {
				p.logger.Printf("错误: 计算基础文件哈希失败: %v", err)
				continue
			}
			for _, numberedPath := range group.numberedFiles {
				numberedHash, err := hasher.CalculateSHA256(numberedPath)
				if err != nil {
					p.logger.Printf("警告: 计算副本 '%s' 哈希失败: %v", filepath.Base(numberedPath), err)
					continue
				}
				if baseHash == numberedHash {
					p.logger.Printf("  -> 内容哈希相同，删除冗余副本 '%s'", filepath.Base(numberedPath))
					os.Remove(numberedPath)
				} else {
					p.logger.Printf("  -> 内容哈希不同，保留独立文件 '%s'", filepath.Base(numberedPath))
				}
			}
		}
	}
}

// findAndExecuteRepair (新增)
// 实现了您指定的、更健壮的迭代查找修复逻辑
func (p *defaultPreprocessor) findAndExecuteRepair(group *fileGroup) {
	p.logger.Printf("修复模式: 基础文件 '%s' 损坏。", filepath.Base(group.basePath))

	baseName := strings.TrimSuffix(filepath.Base(group.basePath), filepath.Ext(group.basePath))
	ext := filepath.Ext(group.basePath)
	dir := filepath.Dir(group.basePath)

	// 从 (1) 开始，迭代查找健康的副本
	for i := 1; i <= maxRepairAttempts; i++ {
		candidateName := fmt.Sprintf("%s (%d)%s", baseName, i, ext)
		candidatePath := filepath.Join(dir, candidateName)

		// 检查候选文件是否存在于我们已扫描的列表中
		// (这是一个小优化，避免了不必要的 os.Stat 调用)
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

		// 检查候选文件是否健康
		if !isImageFileDamaged(candidatePath) {
			p.logger.Printf("  -> 找到健康副本 '%s'，执行修复...", candidateName)
			if err := os.Remove(group.basePath); err != nil && !os.IsNotExist(err) {
				p.logger.Printf("错误: 删除损坏的基础文件失败: %v", err)
				return
			}
			if err := os.Rename(candidatePath, group.basePath); err != nil {
				p.logger.Printf("错误: 重命名修复文件失败: %v", err)
				return
			}
			p.logger.Printf("  -> ✅ 文件修复成功: '%s' 已被 '%s' 替换。", filepath.Base(group.basePath), candidateName)
			return // 修复成功，立即返回
		} else {
			p.logger.Printf("  -> 候选文件 %s 已损坏，继续寻找下一个...", candidateName)
		}
	}
	p.logger.Printf("  -> 未能为 '%s' 找到任何健康的修复副本。", filepath.Base(group.basePath))
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
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	default:
		return false
	}
}
