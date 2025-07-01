package scanner

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

const (
	classifierLogFileName = "classifier.log"
)

// 将用于并发结果传递的结构体定义在函数外部，使其成为一个明确的类型。
type classificationResult struct {
	seriesName string
	fileName   string
}

type SeriesClassifier interface {
	ClassifyAndMove(healthyFiles []string) (seriesNames []string, fileNames []string, err error)
	Close()
}

// regexClassifier
type regexClassifier struct {
	destPath    string
	fileRegexps []*regexp.Regexp
	numWorkers  int
	logger      *log.Logger
	logFile     *os.File
}

func NewClassifier(logDir string, destPath string, patterns []string, workerCount int) (SeriesClassifier, error) {
	logFilePath := filepath.Join(logDir, classifierLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化分类器日志: %w", err)
	}
	logger := log.New(file, "CLASSIFY: ", log.LstdFlags|log.Lshortfile)
	compiledRegexps := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("无效的文件匹配模式 '%s': %w", p, err)
		}
		compiledRegexps = append(compiledRegexps, re)
	}
	effectiveWorkerCount := workerCount
	if effectiveWorkerCount <= 0 {
		effectiveWorkerCount = runtime.NumCPU()
		logger.Printf("workerCount 未设置或为0, 自动使用CPU核心数: %d", effectiveWorkerCount)
	} else {
		logger.Printf("使用配置中的 workerCount: %d", effectiveWorkerCount)
	}
	logger.Println("================== 新的分类任务开始 ==================")
	return &regexClassifier{
		destPath:    destPath,
		fileRegexps: compiledRegexps,
		numWorkers:  effectiveWorkerCount,
		logger:      logger,
		logFile:     file,
	}, nil
}

func (c *regexClassifier) Close() {
	if c.logFile != nil {
		c.logger.Println("================== 分类任务结束，关闭日志文件 ==================")
		c.logFile.Close()
	}
}

// ClassifyAndMove
// 创建通道时使用classificationResult 类型
func (c *regexClassifier) ClassifyAndMove(healthyFiles []string) ([]string, []string, error) {
	var wg sync.WaitGroup
	tasks := make(chan string, c.numWorkers)
	results := make(chan classificationResult, len(healthyFiles))

	for i := 0; i < c.numWorkers; i++ {
		wg.Add(1)
		go c.worker(&wg, tasks, results)
	}

	for _, path := range healthyFiles {
		tasks <- path
	}
	close(tasks)

	wg.Wait()
	close(results)

	uniqueSeriesNames := make(map[string]struct{})
	processedFileNames := make([]string, 0, len(healthyFiles))
	for res := range results {
		uniqueSeriesNames[res.seriesName] = struct{}{}
		processedFileNames = append(processedFileNames, res.fileName)
	}

	finalSeriesNames := make([]string, 0, len(uniqueSeriesNames))
	for name := range uniqueSeriesNames {
		finalSeriesNames = append(finalSeriesNames, name)
	}

	return finalSeriesNames, processedFileNames, nil
}

// worker
// 函数参数中明确使用 chan<- classificationResult 类型
func (c *regexClassifier) worker(wg *sync.WaitGroup, tasks <-chan string, results chan<- classificationResult) {
	defer wg.Done()
	for filePath := range tasks {
		fileName := filepath.Base(filePath)
		seriesName := c.extractSeriesName(fileName)

		if seriesName == "" {
			c.logger.Printf("文件无法分类，跳过: %s", fileName)
			continue
		}

		targetDir := filepath.Join(c.destPath, seriesName)
		targetFile := filepath.Join(targetDir, fileName)

		if err := os.MkdirAll(targetDir, 0755); err != nil {
			c.logger.Printf("错误：无法创建系列目录 %s: %v", targetDir, err)
			continue
		}

		if err := os.Rename(filePath, targetFile); err != nil {
			c.logger.Printf("错误：无法移动文件 %s -> %s: %v", filePath, targetFile, err)
			continue
		}

		c.logger.Printf("文件已移动: %s -> %s", fileName, targetDir)

		results <- classificationResult{seriesName: seriesName, fileName: fileName}
	}
}

func (c *regexClassifier) extractSeriesName(fileName string) string {
	for _, re := range c.fileRegexps {
		matches := re.FindStringSubmatch(fileName)
		if len(matches) > 1 {
			return sanitizeName(matches[1])
		}
	}
	return ""
}

func sanitizeName(name string) string {
	replacer := strings.NewReplacer("<", " ", ">", " ", ":", " ", "\"", " ", "/", " ", "\\", " ", "|", " ", "?", " ", "*", " ")
	sanitized := replacer.Replace(name)
	sanitized = strings.TrimSpace(sanitized)
	sanitized = strings.TrimRight(sanitized, ". ")
	return strings.TrimSpace(sanitized)
}
