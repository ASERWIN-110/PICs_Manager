package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/hasher"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	classifierLogFileName = "classifier.log"
	sameNameDirName       = ".same-name"
)

type classificationResult struct {
	seriesName string
	fileName   string
	duplicate  bool
	err        error
}

type SeriesClassifier interface {
	ClassifyAndMove(healthyFiles []string) (seriesNames []string, fileNames []string, err error)
	Close()
}

type compiledMediaType struct {
	Type       string
	Extensions map[string]struct{}
	Regexps    []*regexp.Regexp
}

type regexClassifier struct {
	destPath   string
	mediaTypes []compiledMediaType
	moveMu     sync.Mutex
	logger     *log.Logger
	logFile    *os.File
}

func NewClassifier(logDir string, destPath string, scannerCfg config.ScannerConfig) (SeriesClassifier, error) {
	logFilePath := filepath.Join(logDir, classifierLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化分类器日志: %w", err)
	}
	logger := log.New(file, "CLASSIFY: ", log.LstdFlags|log.Lshortfile)
	mediaTypes, err := compileMediaTypes(scannerCfg)
	if err != nil {
		file.Close()
		return nil, err
	}
	logger.Println("================== 新的分类任务开始 ==================")
	return &regexClassifier{
		destPath:   destPath,
		mediaTypes: mediaTypes,
		logger:     logger,
		logFile:    file,
	}, nil
}

func (c *regexClassifier) Close() {
	if c.logFile != nil {
		c.logger.Println("================== 分类任务结束，关闭日志文件 ==================")
		c.logFile.Close()
	}
}

func (c *regexClassifier) ClassifyAndMove(healthyFiles []string) ([]string, []string, error) {
	results := make([]classificationResult, 0, len(healthyFiles))
	orderedFiles := append([]string(nil), healthyFiles...)
	sort.Strings(orderedFiles)

	for _, filePath := range orderedFiles {
		fileName := filepath.Base(filePath)
		seriesName, mediaType, matchErr := c.extractSeriesName(fileName)
		if matchErr != nil {
			err := fmt.Errorf("文件无法分类: %s: %w", fileName, matchErr)
			c.logger.Printf("%v", err)
			results = append(results, classificationResult{fileName: fileName, err: err})
			continue
		}

		res := c.moveClassifiedFile(filePath, fileName, seriesName, mediaType)
		if res.err != nil {
			c.logger.Printf("错误：%v", res.err)
		}
		results = append(results, res)
	}

	uniqueSeriesNames := make(map[string]struct{})
	processedFileNames := make([]string, 0, len(healthyFiles))
	handledCount := 0
	pendingCount := 0
	var resultErrs []error
	for _, res := range results {
		if res.err != nil {
			resultErrs = append(resultErrs, res.err)
			continue
		}
		handledCount++
		if res.duplicate {
			pendingCount++
			continue
		}
		uniqueSeriesNames[res.seriesName] = struct{}{}
		processedFileNames = append(processedFileNames, res.fileName)
	}

	finalSeriesNames := make([]string, 0, len(uniqueSeriesNames))
	for name := range uniqueSeriesNames {
		finalSeriesNames = append(finalSeriesNames, name)
	}

	if len(resultErrs) > 0 {
		return finalSeriesNames, processedFileNames, fmt.Errorf(
			"分类数量不一致: 输入 %d, 成功 %d, 失败 %d: %w",
			len(healthyFiles),
			handledCount,
			len(resultErrs),
			errors.Join(resultErrs...),
		)
	}
	if handledCount != len(healthyFiles) {
		return finalSeriesNames, processedFileNames, fmt.Errorf("分类数量不一致: 输入 %d, 已处理 %d", len(healthyFiles), handledCount)
	}
	if pendingCount > 0 {
		c.logger.Printf("同名冲突或重复文件已自动处理，共 %d 个。", pendingCount)
	}

	return finalSeriesNames, processedFileNames, nil
}

func (c *regexClassifier) moveClassifiedFile(filePath, fileName, seriesName, mediaType string) classificationResult {
	targetDir := filepath.Join(c.destPath, seriesName)

	c.moveMu.Lock()
	defer c.moveMu.Unlock()

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return classificationResult{fileName: fileName, seriesName: seriesName, err: fmt.Errorf("无法创建系列目录 %s: %w", targetDir, err)}
	}

	targetFile := filepath.Join(targetDir, fileName)
	finalPath, finalName, duplicate, err := resolveSameNameTarget(filePath, targetFile, isSameNameSourcePath(filePath))
	if err != nil {
		return classificationResult{fileName: fileName, seriesName: seriesName, err: err}
	}
	if duplicate {
		c.logger.Printf("同名同哈希重复文件已删除: type=%s %s", mediaType, fileName)
		return classificationResult{seriesName: seriesName, fileName: fileName, duplicate: true}
	}
	if isSameNameSourcePath(finalPath) {
		c.logger.Printf("同名不同哈希文件已移入同名分流目录: type=%s %s -> %s", mediaType, fileName, finalPath)
		return classificationResult{seriesName: seriesName, fileName: finalName, duplicate: true}
	}
	c.logger.Printf("文件已移动: type=%s %s -> %s", mediaType, fileName, finalPath)
	return classificationResult{seriesName: seriesName, fileName: finalName}
}

func resolveSameNameTarget(srcPath, desiredPath string, forceConflict bool) (targetPath string, targetName string, duplicate bool, err error) {
	if forceConflict {
		if isSameNameSourcePath(desiredPath) {
			return moveOrDeduplicateExactTarget(srcPath, desiredPath)
		}
		return moveToSameNameTarget(srcPath, desiredPath)
	}

	if _, statErr := os.Stat(desiredPath); statErr != nil {
		if os.IsNotExist(statErr) {
			if err := os.Rename(srcPath, desiredPath); err != nil {
				return "", "", false, fmt.Errorf("无法移动文件 %s -> %s: %w", srcPath, desiredPath, err)
			}
			return desiredPath, filepath.Base(desiredPath), false, nil
		}
		return "", "", false, fmt.Errorf("无法检查目标文件路径 %s: %w", desiredPath, statErr)
	}

	same, err := sameFileHash(srcPath, desiredPath)
	if err != nil {
		return "", "", false, err
	}
	if same {
		if err := os.Remove(srcPath); err != nil {
			return "", "", false, fmt.Errorf("无法删除同名同哈希重复文件 %s: %w", srcPath, err)
		}
		return desiredPath, filepath.Base(desiredPath), true, nil
	}

	return moveToSameNameTarget(srcPath, desiredPath)
}

func moveOrDeduplicateExactTarget(srcPath, desiredPath string) (targetPath string, targetName string, duplicate bool, err error) {
	if _, statErr := os.Stat(desiredPath); statErr != nil {
		if !os.IsNotExist(statErr) {
			return "", "", false, fmt.Errorf("无法检查目标文件路径 %s: %w", desiredPath, statErr)
		}
		if err := os.MkdirAll(filepath.Dir(desiredPath), 0755); err != nil {
			return "", "", false, err
		}
		if err := os.Rename(srcPath, desiredPath); err != nil {
			return "", "", false, fmt.Errorf("无法移动文件 %s -> %s: %w", srcPath, desiredPath, err)
		}
		return desiredPath, filepath.Base(desiredPath), false, nil
	}

	same, err := sameFileHash(srcPath, desiredPath)
	if err != nil {
		return "", "", false, err
	}
	if !same {
		return "", "", false, fmt.Errorf("同名分流目标已存在但内容不同: %s", desiredPath)
	}
	if err := os.Remove(srcPath); err != nil {
		return "", "", false, fmt.Errorf("无法删除同名同哈希重复文件 %s: %w", srcPath, err)
	}
	return desiredPath, filepath.Base(desiredPath), true, nil
}

func moveToSameNameTarget(srcPath, desiredPath string) (targetPath string, targetName string, duplicate bool, err error) {
	hash, err := hasher.CalculateSHA256(srcPath)
	if err != nil {
		return "", "", false, fmt.Errorf("计算文件哈希失败 %s: %w", srcPath, err)
	}
	targetPath = filepath.Join(filepath.Dir(desiredPath), sameNameDirName, sameNameBucket(filepath.Base(desiredPath)), hash, filepath.Base(desiredPath))
	if _, statErr := os.Stat(targetPath); statErr != nil {
		if !os.IsNotExist(statErr) {
			return "", "", false, fmt.Errorf("无法检查同名分流文件路径 %s: %w", targetPath, statErr)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return "", "", false, err
		}
		if err := os.Rename(srcPath, targetPath); err != nil {
			return "", "", false, fmt.Errorf("无法移动同名不同哈希文件 %s -> %s: %w", srcPath, targetPath, err)
		}
		return targetPath, filepath.Base(targetPath), false, nil
	}

	same, err := sameFileHash(srcPath, targetPath)
	if err != nil {
		return "", "", false, err
	}
	if !same {
		return "", "", false, fmt.Errorf("同名分流槽已存在但内容不同: %s", targetPath)
	}
	if err := os.Remove(srcPath); err != nil {
		return "", "", false, fmt.Errorf("无法删除同名同哈希重复文件 %s: %w", srcPath, err)
	}
	return targetPath, filepath.Base(targetPath), true, nil
}

func sameFileHash(a, b string) (bool, error) {
	hashA, err := hasher.CalculateSHA256(a)
	if err != nil {
		return false, fmt.Errorf("计算文件哈希失败 %s: %w", a, err)
	}
	hashB, err := hasher.CalculateSHA256(b)
	if err != nil {
		return false, fmt.Errorf("计算文件哈希失败 %s: %w", b, err)
	}
	return hashA == hashB, nil
}

func sameNameBucket(fileName string) string {
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	if base == "" {
		return "_file"
	}
	return base
}

func isSameNameSourcePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == sameNameDirName {
			return true
		}
	}
	return false
}

func nextAvailablePath(path string) (string, string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return path, filepath.Base(path), nil
		}
		return "", "", err
	}

	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s_dup_%d%s", base, i, ext))
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				return candidate, filepath.Base(candidate), nil
			}
			return "", "", err
		}
	}
}

func (c *regexClassifier) extractSeriesName(fileName string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(fileName))
	for _, mediaType := range c.mediaTypes {
		if _, ok := mediaType.Extensions[ext]; !ok {
			continue
		}
		for _, re := range mediaType.Regexps {
			matches := re.FindStringSubmatch(fileName)
			if len(matches) > 1 {
				seriesName := sanitizeName(matches[1])
				if seriesName == "" {
					return "", mediaType.Type, fmt.Errorf("正则命中的系列名为空或只包含非法路径字符")
				}
				return seriesName, mediaType.Type, nil
			}
		}
		return "", mediaType.Type, fmt.Errorf("扩展名匹配媒体类型 %q，但没有正则命中", mediaType.Type)
	}
	return "", "", fmt.Errorf("不支持的文件扩展名 %q", ext)
}

func compileMediaTypes(scannerCfg config.ScannerConfig) ([]compiledMediaType, error) {
	mediaConfigs := effectiveMediaTypes(scannerCfg)
	compiled := make([]compiledMediaType, 0, len(mediaConfigs))
	for _, mediaConfig := range mediaConfigs {
		mediaType := strings.TrimSpace(mediaConfig.Type)
		if mediaType == "" {
			return nil, fmt.Errorf("媒体类型 type 不能为空")
		}

		extensions := make(map[string]struct{})
		for _, ext := range mediaConfig.Extensions {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext == "" {
				continue
			}
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			extensions[ext] = struct{}{}
		}
		if len(extensions) == 0 {
			return nil, fmt.Errorf("媒体类型 %q 至少需要一个扩展名", mediaType)
		}

		regexps := make([]*regexp.Regexp, 0, len(mediaConfig.FilePatterns))
		for _, pattern := range mediaConfig.FilePatterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("媒体类型 %q 的文件匹配模式无效 '%s': %w", mediaType, pattern, err)
			}
			regexps = append(regexps, re)
		}
		if len(regexps) == 0 {
			return nil, fmt.Errorf("媒体类型 %q 至少需要一个文件匹配正则", mediaType)
		}

		compiled = append(compiled, compiledMediaType{
			Type:       mediaType,
			Extensions: extensions,
			Regexps:    regexps,
		})
	}
	return compiled, nil
}

func effectiveMediaTypes(scannerCfg config.ScannerConfig) []config.MediaTypeConfig {
	mediaTypes := append([]config.MediaTypeConfig(nil), scannerCfg.MediaTypes...)
	hasImage := false
	for i := range mediaTypes {
		if strings.EqualFold(mediaTypes[i].Type, "image") {
			hasImage = true
			if len(mediaTypes[i].FilePatterns) == 0 {
				mediaTypes[i].FilePatterns = scannerCfg.FilePatterns
			}
			if len(mediaTypes[i].Extensions) == 0 {
				mediaTypes[i].Extensions = defaultImageExtensions()
			}
		}
	}
	if !hasImage && len(scannerCfg.FilePatterns) > 0 {
		mediaTypes = append([]config.MediaTypeConfig{{
			Type:         "image",
			Extensions:   defaultImageExtensions(),
			FilePatterns: scannerCfg.FilePatterns,
		}}, mediaTypes...)
	}
	return mediaTypes
}

func defaultImageExtensions() []string {
	return []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}
}

func supportedMediaFiles(paths []string, scannerCfg config.ScannerConfig) ([]string, error) {
	mediaTypes, err := compileMediaTypes(scannerCfg)
	if err != nil {
		return nil, err
	}
	supported := make([]string, 0, len(paths))
	for _, path := range paths {
		ext := strings.ToLower(filepath.Ext(path))
		for _, mediaType := range mediaTypes {
			if _, ok := mediaType.Extensions[ext]; ok {
				supported = append(supported, path)
				break
			}
		}
	}
	return supported, nil
}

func detectMediaType(fileName string, scannerCfg config.ScannerConfig) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	mediaTypes, err := compileMediaTypes(scannerCfg)
	if err != nil {
		if isImageExtension(fileName) {
			return "image"
		}
		return "unknown"
	}
	if len(mediaTypes) == 0 && isImageExtension(fileName) {
		return "image"
	}
	for _, mediaType := range mediaTypes {
		if _, ok := mediaType.Extensions[ext]; ok {
			return mediaType.Type
		}
	}
	return "unknown"
}

func sanitizeName(name string) string {
	replacer := strings.NewReplacer("<", " ", ">", " ", ":", " ", "\"", " ", "/", " ", "\\", " ", "|", " ", "?", " ", "*", " ")
	sanitized := replacer.Replace(name)
	sanitized = strings.TrimSpace(sanitized)
	sanitized = strings.TrimRight(sanitized, ". ")
	return strings.TrimSpace(sanitized)
}
