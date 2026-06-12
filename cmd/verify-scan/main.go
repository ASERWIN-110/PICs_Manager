package main

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/database/mongo"
	"PICs_Manager/pkg/hasher"
	"PICs_Manager/pkg/scanner"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"

	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type mediaTypeRule struct {
	typ        string
	extensions map[string]struct{}
}

type manifest struct {
	totalFiles      int
	supportedFiles  int
	unsupported     int
	corruptedImages int
	hashCounts      map[string]int
}

func main() {
	source := flag.String("source", "", "源数据目录，例如 test_img 或 Pictures")
	workDir := flag.String("workdir", "", "验证工作目录；为空时使用 /tmp 下的新目录")
	mode := flag.String("mode", "classifyOnly", "扫描模式: classifyOnly 或 full")
	mongoURI := flag.String("mongo-uri", "", "MongoDB URI，仅 full 模式需要；为空时使用 config/env")
	dbName := flag.String("db-name", "", "验证数据库名；为空时自动生成")
	workers := flag.Int("workers", 4, "扫描 worker 数")
	copyMode := flag.String("copy-mode", "hardlink", "数据集准备方式: hardlink 或 copy")
	resetDB := flag.Bool("reset-db", false, "full 模式启动前删除验证数据库集合；需要 MongoDB drop 权限")
	keep := flag.Bool("keep", false, "保留工作目录")
	flag.Parse()

	if *source == "" {
		log.Fatal("必须提供 -source")
	}
	if *mode != "classifyOnly" && *mode != "full" {
		log.Fatalf("无效 mode %q", *mode)
	}

	if err := config.LoadConfig("."); err != nil {
		log.Fatalf("加载 config.yaml 失败: %v", err)
	}

	sourceAbs, err := filepath.Abs(*source)
	if err != nil {
		log.Fatalf("解析 source 失败: %v", err)
	}
	info, err := os.Stat(sourceAbs)
	if err != nil {
		log.Fatalf("读取 source 失败: %v", err)
	}
	if !info.IsDir() {
		log.Fatalf("source 必须是目录: %s", sourceAbs)
	}

	if *workDir == "" {
		*workDir = filepath.Join(os.TempDir(), fmt.Sprintf("pics-verify-%s-%d", filepath.Base(sourceAbs), time.Now().UnixNano()))
	}
	workAbs, err := filepath.Abs(*workDir)
	if err != nil {
		log.Fatalf("解析 workdir 失败: %v", err)
	}
	if err := os.MkdirAll(workAbs, 0755); err != nil {
		log.Fatalf("创建 workdir 失败: %v", err)
	}
	if !*keep {
		defer os.RemoveAll(workAbs)
	}

	paths := verifyPaths{
		root:       workAbs,
		scan:       filepath.Join(workAbs, "scan"),
		staging:    filepath.Join(workAbs, "staging"),
		library:    filepath.Join(workAbs, "library"),
		backup:     filepath.Join(workAbs, "backup"),
		quarantine: filepath.Join(workAbs, "quarantine"),
		logs:       filepath.Join(workAbs, "logs"),
	}
	if *copyMode != "hardlink" && *copyMode != "copy" {
		log.Fatalf("无效 copy-mode %q", *copyMode)
	}
	if err := copyTree(sourceAbs, paths.scan, *copyMode); err != nil {
		log.Fatalf("复制数据集失败: %v", err)
	}

	cfg := *config.C
	cfg.Logger.Path = paths.logs
	if strings.TrimSpace(*mongoURI) != "" {
		cfg.Database.URI = *mongoURI
	}
	if *dbName == "" {
		*dbName = fmt.Sprintf("pics_verify_%d", time.Now().UnixNano())
	}
	cfg.Database.Name = *dbName
	cfg.Scanner.Mode = *mode
	cfg.Scanner.ScanPath = paths.scan
	cfg.Scanner.StagingPath = paths.staging
	cfg.Scanner.FinalLibraryPath = paths.library
	cfg.Scanner.BackupPath = paths.backup
	cfg.Scanner.QuarantinePath = paths.quarantine
	cfg.Scanner.WorkerCount = *workers

	rules := buildMediaTypeRules(cfg.Scanner)
	before, err := buildManifest(sourceAbs, rules, *workers)
	if err != nil {
		log.Fatalf("构建源 manifest 失败: %v", err)
	}
	fmt.Printf("source=%s total=%d supported=%d unsupported=%d corruptedImages=%d\n",
		sourceAbs, before.totalFiles, before.supportedFiles, before.unsupported, before.corruptedImages)

	var db database.Store
	if *mode == "full" {
		db, err = mongo.NewStore(context.Background(), &cfg)
		if err != nil {
			log.Fatalf("连接 MongoDB 失败: %v", err)
		}
		defer db.Close(context.Background())
		if *resetDB {
			if err := db.DropAllCollections(context.Background()); err != nil {
				log.Fatalf("重置验证数据库失败: %v", err)
			}
		}
		if err := db.EnsureIndexes(context.Background()); err != nil {
			log.Fatalf("创建验证数据库索引失败: %v", err)
		}
	}

	start := time.Now()
	orchestrator, err := scanner.NewOrchestrator(&cfg, db)
	if err != nil {
		log.Fatalf("创建扫描器失败: %v", err)
	}
	if err := orchestrator.RunFullScanContext(context.Background(), cfg.Scanner); err != nil {
		log.Fatalf("扫描执行失败: %v", err)
	}
	elapsed := time.Since(start)

	after, err := buildOutputManifest(paths.library, paths.quarantine, rules, *workers)
	if err != nil {
		log.Fatalf("构建输出 manifest 失败: %v", err)
	}
	if err := compareHashCounts(before.hashCounts, after.hashCounts); err != nil {
		log.Fatalf("文件内容集合不一致: %v", err)
	}

	libraryManifest, err := buildManifest(paths.library, rules, *workers)
	if err != nil {
		log.Fatalf("构建最终库 manifest 失败: %v", err)
	}
	quarantineManifest, err := buildManifest(paths.quarantine, rules, *workers)
	if err != nil {
		log.Fatalf("构建隔离区 manifest 失败: %v", err)
	}

	if quarantineManifest.supportedFiles < before.corruptedImages {
		log.Fatalf("损坏图片隔离数量不足: 预检测 %d, 隔离区支持媒体文件 %d", before.corruptedImages, quarantineManifest.supportedFiles)
	}

	if *mode == "full" {
		dbCount, seriesCount, err := countDatabaseMedia(context.Background(), db)
		if err != nil {
			log.Fatalf("统计数据库失败: %v", err)
		}
		if dbCount != libraryManifest.supportedFiles {
			log.Fatalf("数据库记录数和最终库文件数不一致: db=%d library=%d", dbCount, libraryManifest.supportedFiles)
		}
		diagnostics, err := db.Diagnostics(context.Background())
		if err != nil {
			log.Fatalf("读取数据库诊断失败: %v", err)
		}
		if diagnostics.MediaCount != int64(libraryManifest.supportedFiles) {
			log.Fatalf("数据库诊断媒体数和最终库文件数不一致: diagnostics=%d library=%d", diagnostics.MediaCount, libraryManifest.supportedFiles)
		}
		if diagnostics.MediaCount != int64(dbCount) {
			log.Fatalf("数据库诊断媒体数和按系列累计数不一致: diagnostics=%d seriesSum=%d", diagnostics.MediaCount, dbCount)
		}
		if diagnostics.SeriesCount != int64(seriesCount) {
			log.Fatalf("数据库诊断系列数和按系列读取数不一致: diagnostics=%d series=%d", diagnostics.SeriesCount, seriesCount)
		}
		if diagnostics.ImagesWithPHash != diagnostics.ImagesWithPHashBuckets || diagnostics.ImagesMissingPHashBuckets != 0 {
			log.Fatalf("图片 pHash bucket 覆盖不完整: pHash=%d buckets=%d missing=%d",
				diagnostics.ImagesWithPHash, diagnostics.ImagesWithPHashBuckets, diagnostics.ImagesMissingPHashBuckets)
		}
		if diagnostics.SeriesWithThumbnail != diagnostics.SeriesWithThumbnailFlag || diagnostics.SeriesMissingThumbnailFlag != 0 {
			log.Fatalf("系列缩略图标记不完整: thumbnails=%d flags=%d missingFlag=%d",
				diagnostics.SeriesWithThumbnail, diagnostics.SeriesWithThumbnailFlag, diagnostics.SeriesMissingThumbnailFlag)
		}
		fmt.Printf("database series=%d media=%d imageMedia=%d imagesWithPHash=%d imagesWithPHashBuckets=%d seriesWithThumbnail=%d seriesWithThumbnailFlag=%d db=%s\n",
			diagnostics.SeriesCount,
			diagnostics.MediaCount,
			diagnostics.ImageMediaCount,
			diagnostics.ImagesWithPHash,
			diagnostics.ImagesWithPHashBuckets,
			diagnostics.SeriesWithThumbnail,
			diagnostics.SeriesWithThumbnailFlag,
			cfg.Database.Name)
	}

	fmt.Printf("result mode=%s workers=%d elapsed=%s library=%d quarantine=%d preserved=%d workdir=%s\n",
		*mode, *workers, elapsed.Round(time.Millisecond), libraryManifest.supportedFiles, quarantineManifest.supportedFiles, after.supportedFiles, workAbs)
}

type verifyPaths struct {
	root       string
	scan       string
	staging    string
	library    string
	backup     string
	quarantine string
	logs       string
}

func copyTree(src, dst string, mode string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if mode == "hardlink" {
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.Link(path, target); err != nil {
				if errors.Is(err, syscall.EXDEV) {
					return copyFile(path, target, info.Mode())
				}
				return err
			}
			return nil
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	inClosed := false
	defer func() {
		if !inClosed {
			_ = in.Close()
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	outClosed := false
	defer func() {
		if !outClosed {
			_ = out.Close()
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		outClosed = true
		return err
	}
	outClosed = true
	if err := in.Close(); err != nil {
		inClosed = true
		return err
	}
	inClosed = true
	return nil
}

func buildOutputManifest(library, quarantine string, rules []mediaTypeRule, workers int) (manifest, error) {
	combined := manifest{hashCounts: make(map[string]int)}
	for _, root := range []string{library, quarantine} {
		part, err := buildManifest(root, rules, workers)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return combined, err
		}
		combined.totalFiles += part.totalFiles
		combined.supportedFiles += part.supportedFiles
		combined.unsupported += part.unsupported
		combined.corruptedImages += part.corruptedImages
		for hash, count := range part.hashCounts {
			combined.hashCounts[hash] += count
		}
	}
	return combined, nil
}

func buildManifest(root string, rules []mediaTypeRule, workers int) (manifest, error) {
	m := manifest{hashCounts: make(map[string]int)}
	if workers <= 0 {
		workers = 1
	}

	type fileJob struct {
		path      string
		mediaType string
	}
	jobs := make(chan fileJob, workers*2)
	errs := make(chan error, workers)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errDone := make(chan []error, 1)
	go func() {
		var allErrs []error
		for err := range errs {
			if err != nil {
				allErrs = append(allErrs, err)
			}
		}
		errDone <- allErrs
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				corrupted := false
				if job.mediaType == "image" && imageDamaged(job.path) {
					corrupted = true
				}
				hash, err := hasher.CalculateSHA256(job.path)
				if err != nil {
					errs <- err
					continue
				}

				mu.Lock()
				if corrupted {
					m.corruptedImages++
				}
				m.hashCounts[hash]++
				mu.Unlock()
			}
		}()
	}

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		m.totalFiles++
		mediaType := detectMediaType(path, rules)
		if mediaType == "" {
			m.unsupported++
			return nil
		}
		m.supportedFiles++
		jobs <- fileJob{path: path, mediaType: mediaType}
		return nil
	})
	close(jobs)
	wg.Wait()
	close(errs)
	allErrs := <-errDone

	if walkErr != nil {
		return m, walkErr
	}
	if len(allErrs) > 0 {
		return m, errors.Join(allErrs...)
	}
	return m, nil
}

func compareHashCounts(expected, actual map[string]int) error {
	var missing []string
	var extra []string
	for hash, count := range expected {
		if actual[hash] != count {
			missing = append(missing, fmt.Sprintf("%s expected=%d actual=%d", hash, count, actual[hash]))
		}
	}
	for hash, count := range actual {
		if expected[hash] != count {
			extra = append(extra, fmt.Sprintf("%s expected=%d actual=%d", hash, expected[hash], count))
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("missingOrChanged=%v extra=%v", firstN(missing, 10), firstN(extra, 10))
	}
	return nil
}

func firstN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

func countDatabaseMedia(ctx context.Context, db database.Store) (mediaCount int, seriesCount int, err error) {
	diagnostics, err := db.Diagnostics(ctx)
	if err != nil {
		return 0, 0, err
	}
	return int(diagnostics.MediaCount), int(diagnostics.SeriesCount), nil
}

func buildMediaTypeRules(scannerCfg config.ScannerConfig) []mediaTypeRule {
	mediaConfigs := append([]config.MediaTypeConfig(nil), scannerCfg.MediaTypes...)
	hasImage := false
	for i := range mediaConfigs {
		if strings.EqualFold(mediaConfigs[i].Type, "image") {
			hasImage = true
			if len(mediaConfigs[i].Extensions) == 0 {
				mediaConfigs[i].Extensions = []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}
			}
		}
	}
	if !hasImage {
		mediaConfigs = append(mediaConfigs, config.MediaTypeConfig{
			Type:       "image",
			Extensions: []string{".jpg", ".jpeg", ".png", ".gif", ".webp"},
		})
	}

	rules := make([]mediaTypeRule, 0, len(mediaConfigs))
	for _, mediaConfig := range mediaConfigs {
		rule := mediaTypeRule{typ: strings.TrimSpace(mediaConfig.Type), extensions: make(map[string]struct{})}
		for _, ext := range mediaConfig.Extensions {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext == "" {
				continue
			}
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			rule.extensions[ext] = struct{}{}
		}
		if rule.typ != "" && len(rule.extensions) > 0 {
			rules = append(rules, rule)
		}
	}
	return rules
}

func detectMediaType(path string, rules []mediaTypeRule) string {
	ext := strings.ToLower(filepath.Ext(path))
	for _, rule := range rules {
		if _, ok := rule.extensions[ext]; ok {
			return rule.typ
		}
	}
	return ""
}

func imageDamaged(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer file.Close()
	_, _, err = image.Decode(file)
	return err != nil
}
