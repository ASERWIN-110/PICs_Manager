package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/hasher"
	"PICs_Manager/pkg/runstate"
	"PICs_Manager/pkg/thumbnailer"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type MetadataIngestor interface {
	Sync(ctx context.Context, finalLibraryPath string, createdSeries, processedFileNames []string, changelog map[string]string) (overwrittenFiles []string, err error)
	RebuildFromLibrary(ctx context.Context, finalLibraryPath string) (RebuildStats, error)
	Close()
}

type RebuildStats struct {
	Series int
	Media  int
}

type mongoIngestor struct {
	dbStore    database.Store
	recorder   runstate.Recorder
	logger     *log.Logger
	logFile    *os.File
	numWorkers int
	batchSize  int
	scannerCfg config.ScannerConfig
}

const ingestorLogFileName = "ingestor.log"

func NewIngestor(logDir string, dbStore database.Store, scannerCfg config.ScannerConfig, workerCount, batchSize int) (MetadataIngestor, error) {
	if dbStore == nil {
		return nil, errors.New("数据库存储未初始化")
	}
	logFilePath := filepath.Join(logDir, ingestorLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化入库器日志: %w", err)
	}
	logger := log.New(file, "INGEST: ", log.LstdFlags|log.Lshortfile)

	if batchSize <= 0 {
		batchSize = 100
	}
	workerCount = defaultWorkerCount(workerCount)

	return &mongoIngestor{
		dbStore:    dbStore,
		recorder:   recorderFromContext(context.Background()),
		logger:     logger,
		logFile:    file,
		numWorkers: workerCount,
		batchSize:  batchSize,
		scannerCfg: scannerCfg,
	}, nil
}

func (m *mongoIngestor) Close() {
	if m.logFile != nil {
		m.logger.Println("================== 入库任务结束，关闭日志文件 ==================")
		m.logFile.Close()
	}
}

// Sync 实现了将文件系统变更同步到数据库的核心逻辑
func (m *mongoIngestor) Sync(ctx context.Context, finalLibraryPath string, createdSeries, processedFileNames []string, changelog map[string]string) ([]string, error) {
	if recorder, ok := runstate.FromContext(ctx); ok {
		m.recorder = recorder
	}
	m.logger.Println("================== 新的入库任务开始 ==================")
	if m.dbStore == nil {
		m.logger.Println("警告：数据库存储未初始化，跳过。")
		return nil, nil
	}

	// 1. 解析并收集所有需要处理的系列路径
	seriesPathsToProcess := m.collectFinalSeriesPaths(finalLibraryPath, changelog)

	// 2. 阶段一：批量处理系列，并缓存结果
	m.logger.Printf("--- 阶段 1/4: 处理 %d 个系列 ---", len(seriesPathsToProcess))
	seriesCache, err := m.processAllSeries(ctx, seriesPathsToProcess)
	if err != nil {
		return nil, fmt.Errorf("处理系列时失败: %w", err)
	}

	// 3. 阶段二：批量处理媒体，并检测覆盖
	m.logger.Printf("--- 阶段 2/4: 处理媒体并检测覆盖 ---")
	overwrittenFiles, mediaStats, err := m.processAllMedia(ctx, seriesPathsToProcess, seriesCache)
	if err != nil {
		return nil, fmt.Errorf("处理媒体时失败: %w", err)
	}
	m.logger.Printf("媒体处理统计: 待处理 %d, 已准备 %d, 已写入 %d", mediaStats.Scheduled, mediaStats.Prepared, mediaStats.Written)
	if mediaStats.Written < len(processedFileNames) {
		return nil, fmt.Errorf("入库数量少于本次分类成功数量: 分类成功 %d, 入库写入 %d", len(processedFileNames), mediaStats.Written)
	}

	// 4. 阶段三： 更新 Series 的元数据
	m.logger.Println("--- 阶段 3/4: 更新系列元数据 (media count, thumbnail) ---")
	if err := m.updateAllSeriesMetadata(ctx, seriesCache); err != nil {
		m.logger.Printf("警告: 更新系列元数据失败: %v", err)
		// 通常这是一个非致命错误，只记录日志即可
	}

	// 5. 阶段四：最终验证
	m.logger.Println("--- 阶段 4/4: 执行最终验证查询 ---")
	if err := m.reconcileAndValidateSeries(ctx, seriesCache); err != nil {
		return nil, fmt.Errorf("最终数量校验失败: %w", err)
	}
	m.logger.Printf("接收到 %d 个系列名，%d 个文件名。", len(createdSeries), len(processedFileNames))
	m.logger.Println("--- 数据库同步完成 ---")
	return overwrittenFiles, nil
}

func (m *mongoIngestor) RebuildFromLibrary(ctx context.Context, finalLibraryPath string) (RebuildStats, error) {
	if recorder, ok := runstate.FromContext(ctx); ok {
		m.recorder = recorder
	}
	m.logger.Println("================== 从最终库补齐数据库任务开始 ==================")
	if m.dbStore == nil {
		return RebuildStats{}, errors.New("数据库存储未初始化")
	}
	seriesPaths, err := discoverFinalSeriesPaths(ctx, finalLibraryPath, m.scannerCfg)
	if err != nil {
		return RebuildStats{}, err
	}
	m.logger.Printf("发现 %d 个最终库系列目录。", len(seriesPaths))
	seriesCache, err := m.processAllSeries(ctx, seriesPaths)
	if err != nil {
		return RebuildStats{}, fmt.Errorf("处理系列时失败: %w", err)
	}
	_, mediaStats, err := m.processAllMedia(ctx, seriesPaths, seriesCache)
	if err != nil {
		return RebuildStats{}, fmt.Errorf("处理媒体时失败: %w", err)
	}
	if err := m.updateAllSeriesMetadata(ctx, seriesCache); err != nil {
		return RebuildStats{}, fmt.Errorf("更新系列元数据失败: %w", err)
	}
	if err := m.reconcileAndValidateSeries(ctx, seriesCache); err != nil {
		return RebuildStats{}, fmt.Errorf("最终数量校验失败: %w", err)
	}
	return RebuildStats{Series: len(seriesCache), Media: mediaStats.Written}, nil
}

func (m *mongoIngestor) collectFinalSeriesPaths(finalLibraryPath string, changelog map[string]string) []string {
	pathSet := make(map[string]struct{})
	m.logger.Println("正在根据 changelog 解析需要处理的最终系列路径...")

	for _, newPath := range changelog {
		info, err := os.Stat(newPath)
		if err != nil {
			continue
		}

		if !info.IsDir() {
			continue
		}

		folderName := filepath.Base(newPath)
		if strings.HasSuffix(folderName, aggSuffix) {
			subEntries, err := os.ReadDir(newPath)
			if err != nil {
				m.logger.Printf("错误: 无法读取聚合目录 %s: %v", newPath, err)
				continue
			}
			for _, subEntry := range subEntries {
				if subEntry.IsDir() {
					seriesPath := filepath.Join(newPath, subEntry.Name())
					pathSet[seriesPath] = struct{}{}
				}
			}
		} else {
			pathSet[newPath] = struct{}{}
		}
	}

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}

	m.logger.Printf("解析完成，共找到 %d 个需要处理的唯一系列路径。", len(paths))
	return paths
}

func (m *mongoIngestor) processAllSeries(ctx context.Context, seriesPaths []string) (map[string]*models.Series, error) {
	if len(seriesPaths) == 0 {
		return make(map[string]*models.Series), nil
	}

	m.logger.Printf("准备批量处理 %d 个系列...", len(seriesPaths))

	var seriesWrites []mongo.WriteModel
	seriesNames := make([]string, len(seriesPaths))

	for i, path := range seriesPaths {
		seriesName := filepath.Base(path)
		seriesName = strings.TrimSuffix(seriesName, aggSuffix)
		seriesNames[i] = seriesName

		filter := bson.M{"name": seriesName}
		update := bson.M{
			"$set":         bson.M{"path": path, "updatedAt": time.Now()},
			"$setOnInsert": bson.M{"_id": primitive.NewObjectID(), "name": seriesName, "imageCount": 0, "createdAt": time.Now()},
		}
		model := mongo.NewUpdateOneModel().SetFilter(filter).SetUpdate(update).SetUpsert(true)
		seriesWrites = append(seriesWrites, model)
	}

	if err := m.dbStore.Series().BulkWrite(ctx, seriesWrites); err != nil {
		m.logger.Printf("错误: 批量写入Series失败: %v", err)
		return nil, err
	}
	m.logger.Printf("批量 Upsert 操作完成，共处理 %d 个系列。", len(seriesWrites))

	m.logger.Println("批量查询 Upsert 结果以构建缓存...")
	foundSeries, notFound, err := m.dbStore.Series().FindManyByNames(ctx, seriesNames)
	if err != nil {
		return nil, fmt.Errorf("批量查询系列结果失败: %w", err)
	}
	if len(notFound) > 0 {
		m.logger.Printf("严重错误: Upsert后查询系列时，有 %d 个系列未找到: %v", len(notFound), notFound)
	}

	cache := make(map[string]*models.Series)
	seriesByName := make(map[string]*models.Series, len(foundSeries))
	for i := range foundSeries {
		seriesByName[foundSeries[i].Name] = &foundSeries[i]
	}

	for _, path := range seriesPaths {
		seriesName := filepath.Base(path)
		seriesName = strings.TrimSuffix(seriesName, aggSuffix)
		if series, ok := seriesByName[seriesName]; ok {
			sCopy := *series
			sCopy.Path = path
			cache[path] = &sCopy
		}
	}

	m.logger.Printf("系列信息缓存构建完成，共缓存 %d 个系列。", len(cache))
	return cache, nil
}

type mediaJob struct {
	filePath string
	fileName string
	series   *models.Series
}
type mediaResult struct {
	writeModel      mongo.WriteModel
	mediaType       string
	overwrittenPath string
	err             error
}

type mediaProcessStats struct {
	Scheduled int
	Prepared  int
	Written   int
}

type metadataUpdateResult struct {
	writeModel mongo.WriteModel
	err        error
}

// processAllMedia 启动一个工作池来并发地处理所有系列下的媒体文件。
func (m *mongoIngestor) processAllMedia(ctx context.Context, seriesPaths []string, seriesCache map[string]*models.Series) ([]string, mediaProcessStats, error) {
	var wg sync.WaitGroup
	jobs := make(chan mediaJob, m.batchSize*m.numWorkers)
	results := make(chan mediaResult, m.batchSize*m.numWorkers)
	readErrs := make(chan error, len(seriesPaths))
	var scheduledMu sync.Mutex
	scheduled := 0

	for i := 0; i < m.numWorkers; i++ {
		wg.Add(1)
		go m.mediaWorker(&wg, ctx, jobs, results)
	}

	go func() {
		defer close(jobs)
		defer close(readErrs)
		for _, seriesPath := range seriesPaths {
			if err := ctx.Err(); err != nil {
				readErrs <- err
				return
			}
			series, ok := seriesCache[seriesPath]
			if !ok {
				continue
			}
			files, err := mediaFilesInDir(seriesPath, m.scannerCfg)
			if err != nil {
				readErrs <- fmt.Errorf("无法读取系列目录 %s: %w", seriesPath, err)
				continue
			}
			for _, file := range files {
				job := mediaJob{filePath: filepath.Join(seriesPath, file), fileName: file, series: series}
				select {
				case <-ctx.Done():
					readErrs <- ctx.Err()
					return
				case jobs <- job:
				}
				scheduledMu.Lock()
				scheduled++
				scheduledMu.Unlock()
			}
		}
	}()

	var allOverwritten []string
	writeModelsByType := make(map[string][]mongo.WriteModel)
	preparedCount := 0
	done := make(chan []error, 1)

	go func() {
		var resultErrs []error
		for res := range results {
			if res.err != nil {
				resultErrs = append(resultErrs, res.err)
				continue
			}
			if res.writeModel != nil {
				mediaType := strings.TrimSpace(res.mediaType)
				if mediaType == "" {
					mediaType = "unknown"
				}
				writeModelsByType[mediaType] = append(writeModelsByType[mediaType], res.writeModel)
				preparedCount++
			}
			if res.overwrittenPath != "" {
				allOverwritten = append(allOverwritten, res.overwrittenPath)
			}
		}
		done <- resultErrs
	}()

	wg.Wait()
	close(results)
	resultErrs := <-done

	var allErrs []error
	for err := range readErrs {
		allErrs = append(allErrs, err)
	}
	allErrs = append(allErrs, resultErrs...)

	stats := mediaProcessStats{Scheduled: scheduled, Prepared: preparedCount}
	if len(allErrs) > 0 {
		return allOverwritten, stats, errors.Join(allErrs...)
	}
	if stats.Prepared != stats.Scheduled {
		return allOverwritten, stats, fmt.Errorf("入库模型数量不一致: 待处理 %d, 已准备 %d", stats.Scheduled, stats.Prepared)
	}

	mediaTypes := make([]string, 0, len(writeModelsByType))
	for mediaType := range writeModelsByType {
		mediaTypes = append(mediaTypes, mediaType)
	}
	sort.Strings(mediaTypes)
	for _, mediaType := range mediaTypes {
		writeModels := writeModelsByType[mediaType]
		targetStore := m.dbStore.Images()
		if typedStore, ok := m.dbStore.(interface {
			Media(string) database.ImageStore
		}); ok {
			targetStore = typedStore.Media(mediaType)
		}
		for start := 0; start < len(writeModels); start += m.batchSize {
			end := start + m.batchSize
			if end > len(writeModels) {
				end = len(writeModels)
			}
			counts := map[string]int64{"batchSize": int64(end - start)}
			m.record(runstate.Event{Phase: "database_sync", Action: "media_bulk_write_before", Counts: counts, Metadata: map[string]string{"mediaType": mediaType}})
			if err := targetStore.BulkWrite(ctx, writeModels[start:end]); err != nil {
				m.logger.Printf("错误: 批量写入媒体失败: type=%s error=%v", mediaType, err)
				m.record(runstate.Event{Phase: "database_sync", Action: "media_bulk_write_after", Status: "failed", Error: err.Error(), Counts: counts, Metadata: map[string]string{"mediaType": mediaType}})
				return allOverwritten, stats, err
			}
			m.record(runstate.Event{Phase: "database_sync", Action: "media_bulk_write_after", Status: "written", Counts: counts, Metadata: map[string]string{"mediaType": mediaType}})
			stats.Written += end - start
		}
	}

	return allOverwritten, stats, nil
}

// mediaWorker 处理单个媒体文件，并为数据库批量写入准备模型。
func (m *mongoIngestor) mediaWorker(wg *sync.WaitGroup, ctx context.Context, jobs <-chan mediaJob, results chan<- mediaResult) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			m.sendMediaResult(ctx, results, mediaResult{err: ctx.Err()})
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}
			m.processMediaJob(ctx, job, results)
		}
	}
}

func (m *mongoIngestor) processMediaJob(ctx context.Context, job mediaJob, results chan<- mediaResult) {
	filePath := job.filePath
	fileName := job.fileName
	if fileName == "" {
		fileName = filepath.Base(job.filePath)
	}
	mediaType := detectMediaType(filepath.Base(filePath), m.scannerCfg)

	fileHash, err := hasher.CalculateSHA256(filePath)
	if err != nil {
		err = fmt.Errorf("计算SHA256失败 %s: %w", filePath, err)
		m.logger.Printf("错误: %v", err)
		m.sendMediaResult(ctx, results, mediaResult{err: err})
		return
	}

	var pHash, thumbnail string
	if mediaType == "image" {
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			err = fmt.Errorf("无法读取图片文件 %s: %w", filePath, err)
			m.logger.Printf("错误: %v", err)
			m.sendMediaResult(ctx, results, mediaResult{err: err})
			return
		}
		img, _, decodeErr := image.Decode(bytes.NewReader(fileBytes))

		if decodeErr != nil {
			m.logger.Printf("严重错误: 文件 %s 确认已损坏，无法解码 (错误: %v)。保留物理文件并中止入库。", filePath, decodeErr)
			m.sendMediaResult(ctx, results, mediaResult{err: fmt.Errorf("文件已损坏且未入库: %s: %w", filePath, decodeErr)})
			return
		}

		if img != nil {
			pHash = hasher.CalculatePerceptualHashFromImage(img)
			thumbnail, err = thumbnailer.CreateBase64(img, 200, 200)
			if err != nil {
				err = fmt.Errorf("创建缩略图失败 %s: %w", filePath, err)
				m.logger.Printf("错误: %v", err)
				m.sendMediaResult(ctx, results, mediaResult{err: err})
				return
			}
		}
	}
	var pHashBuckets []string
	if pHash != "" {
		pHashBuckets, err = hasher.BuildPerceptualHashBuckets(pHash)
		if err != nil {
			err = fmt.Errorf("构建感知哈希索引桶失败 %s: %w", filePath, err)
			m.logger.Printf("错误: %v", err)
			m.sendMediaResult(ctx, results, mediaResult{err: err})
			return
		}
	}

	if fileHash == "" {
		err := fmt.Errorf("计算SHA256失败，跳过文件 %s", filePath)
		m.logger.Printf("错误: %v", err)
		m.sendMediaResult(ctx, results, mediaResult{err: err})
		return
	}

	if job.series == nil {
		err := fmt.Errorf("文件 %s 缺少系列上下文，跳过", filePath)
		m.logger.Printf("错误: %v", err)
		m.sendMediaResult(ctx, results, mediaResult{err: err})
		return
	}
	series := job.series
	m.record(runstate.Event{Phase: "database_sync", Action: "media_before_upsert", Source: filePath, Metadata: map[string]string{"series": series.Name, "fileName": fileName, "mediaType": mediaType}})

	filter := bson.M{
		"seriesId": series.ID,
		"fileName": fileName,
	}
	update := bson.M{
		"$set": bson.M{
			"filePath":       filePath,
			"fileHash":       fileHash,
			"mediaType":      mediaType,
			"perceptualHash": pHash,
			"pHashBuckets":   pHashBuckets,
			"thumbnail":      thumbnail,
			"hasThumbnail":   thumbnail != "",
			"updatedAt":      time.Now(),
		},
		"$setOnInsert": bson.M{
			"_id":       primitive.NewObjectID(),
			"seriesId":  series.ID,
			"fileName":  fileName,
			"createdAt": time.Now(),
		},
	}
	model := mongo.NewUpdateOneModel().SetFilter(filter).SetUpsert(true).SetUpdate(update)

	m.sendMediaResult(ctx, results, mediaResult{writeModel: model, mediaType: mediaType})
	m.record(runstate.Event{Phase: "database_sync", Action: "media_after_prepare", Source: filePath, Status: "prepared", Metadata: map[string]string{"series": series.Name, "fileName": fileName}})
}

func (m *mongoIngestor) sendMediaResult(ctx context.Context, results chan<- mediaResult, result mediaResult) {
	select {
	case <-ctx.Done():
	case results <- result:
	}
}

// updateAllSeriesMetadata
// 并发地更新所有受影响系列的元数据
func (m *mongoIngestor) updateAllSeriesMetadata(ctx context.Context, seriesCache map[string]*models.Series) error {
	var wg sync.WaitGroup
	tasks := make(chan *models.Series, len(seriesCache))
	results := make(chan metadataUpdateResult, len(seriesCache))

	for i := 0; i < m.numWorkers; i++ {
		wg.Add(1)
		go m.metadataUpdateWorker(&wg, ctx, tasks, results)
	}

	for _, series := range seriesCache {
		tasks <- series
	}
	close(tasks)

	var writes []mongo.WriteModel
	var allErrs []error
	resultWg := sync.WaitGroup{}
	resultWg.Add(1)
	go func() {
		defer resultWg.Done()
		for result := range results {
			if result.err != nil {
				allErrs = append(allErrs, result.err)
				continue
			}
			if result.writeModel != nil {
				writes = append(writes, result.writeModel)
			}
		}
	}()

	wg.Wait()
	close(results)
	resultWg.Wait()

	if len(writes) > 0 {
		m.logger.Printf("准备批量更新 %d 个系列的元数据...", len(writes))
		m.record(runstate.Event{Phase: "database_sync", Action: "series_metadata_bulk_write_before", Counts: map[string]int64{"batchSize": int64(len(writes))}})
		if err := m.dbStore.Series().BulkWrite(ctx, writes); err != nil {
			allErrs = append(allErrs, err)
			m.record(runstate.Event{Phase: "database_sync", Action: "series_metadata_bulk_write_after", Status: "failed", Error: err.Error(), Counts: map[string]int64{"batchSize": int64(len(writes))}})
		} else {
			m.record(runstate.Event{Phase: "database_sync", Action: "series_metadata_bulk_write_after", Status: "written", Counts: map[string]int64{"batchSize": int64(len(writes))}})
		}
	}

	if len(allErrs) > 0 {
		return errors.Join(allErrs...)
	}

	m.logger.Println("没有需要更新的系列元数据。")
	return nil
}

func (m *mongoIngestor) metadataUpdateWorker(wg *sync.WaitGroup, ctx context.Context, tasks <-chan *models.Series, results chan<- metadataUpdateResult) {
	defer wg.Done()
	for series := range tasks {
		count, err := m.dbStore.Images().CountBySeriesID(ctx, series.ID)
		if err != nil {
			err := fmt.Errorf("无法统计系列 %q 的媒体数量: %w", series.Name, err)
			m.logger.Printf("错误: %v", err)
			results <- metadataUpdateResult{err: err}
			continue
		}

		var thumbnail string
		firstMedia, err := m.dbStore.Images().GetFirstThumbnailMedia(ctx, series.ID)
		if err != nil {
			err := fmt.Errorf("无法获取系列 %q 的封面媒体: %w", series.Name, err)
			m.logger.Printf("错误: %v", err)
			results <- metadataUpdateResult{err: err}
			continue
		}
		if firstMedia != nil {
			thumbnail = firstMedia.Thumbnail
		}

		if series.ImageCount != int(count) || series.Thumbnail != thumbnail {
			m.logger.Printf("系列的元数据已变更: %s (媒体数: %d -> %d)", series.Name, series.ImageCount, count)
			filter := bson.M{"_id": series.ID}
			update := bson.M{"$set": bson.M{
				"imageCount":   count,
				"thumbnail":    thumbnail,
				"hasThumbnail": thumbnail != "",
				"updatedAt":    time.Now(),
			}}
			model := mongo.NewUpdateOneModel().SetFilter(filter).SetUpdate(update)
			results <- metadataUpdateResult{writeModel: model}
		}
	}
}

func (m *mongoIngestor) reconcileAndValidateSeries(ctx context.Context, seriesCache map[string]*models.Series) error {
	var allErrs []error
	for seriesPath, series := range seriesCache {
		fsFileNames, err := mediaFileNamesInDir(seriesPath, m.scannerCfg)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("读取系列目录 %s 失败: %w", seriesPath, err))
			continue
		}

		dbMedia, err := m.dbStore.Images().GetAllBySeriesID(ctx, series.ID)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("读取系列 %s 数据库媒体失败: %w", series.Name, err))
			continue
		}

		for _, item := range dbMedia {
			if _, ok := fsFileNames[item.FileName]; ok {
				continue
			}
			m.logger.Printf("删除数据库中已不存在的媒体记录: series=%s file=%s", series.Name, item.FileName)
			m.record(runstate.Event{Phase: "database_sync", Action: "media_before_delete_missing", Source: item.FilePath, Metadata: map[string]string{"series": series.Name, "fileName": item.FileName}})
			if err := m.dbStore.Images().Delete(ctx, item.ID); err != nil {
				allErrs = append(allErrs, fmt.Errorf("删除缺失媒体记录 %s/%s 失败: %w", series.Name, item.FileName, err))
				m.record(runstate.Event{Phase: "database_sync", Action: "media_after_delete_missing", Source: item.FilePath, Status: "failed", Error: err.Error(), Metadata: map[string]string{"series": series.Name, "fileName": item.FileName}})
			} else {
				m.record(runstate.Event{Phase: "database_sync", Action: "media_after_delete_missing", Source: item.FilePath, Status: "deleted", Metadata: map[string]string{"series": series.Name, "fileName": item.FileName}})
			}
		}

		dbCount, err := m.dbStore.Images().CountBySeriesID(ctx, series.ID)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("统计系列 %s 数据库媒体失败: %w", series.Name, err))
			continue
		}
		if int64(len(fsFileNames)) != dbCount {
			allErrs = append(allErrs, fmt.Errorf("系列 %s 数量不一致: 文件系统 %d, 数据库 %d", series.Name, len(fsFileNames), dbCount))
		}
	}

	return errors.Join(allErrs...)
}

func recorderFromContext(ctx context.Context) runstate.Recorder {
	recorder, _ := runstate.FromContext(ctx)
	return recorder
}

func (m *mongoIngestor) record(event runstate.Event) {
	if m.recorder.Store == nil || m.recorder.RunID == "" {
		return
	}
	m.recorder.Event(context.Background(), event)
}

func mediaFileNamesInDir(dir string, scannerCfg config.ScannerConfig) (map[string]struct{}, error) {
	files, err := mediaFilesInDir(dir, scannerCfg)
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{})
	for _, file := range files {
		names[file] = struct{}{}
	}
	return names, nil
}

func mediaFilesInDir(dir string, scannerCfg config.ScannerConfig) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if detectMediaType(entry.Name(), scannerCfg) == "unknown" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func discoverFinalSeriesPaths(ctx context.Context, finalLibraryPath string, scannerCfg config.ScannerConfig) ([]string, error) {
	pathSet := make(map[string]struct{})
	err := filepath.WalkDir(finalLibraryPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == sameNameDirName {
			return filepath.SkipDir
		}
		if path == finalLibraryPath {
			return nil
		}
		files, err := directMediaFilesInDir(path, scannerCfg)
		if err != nil {
			return err
		}
		if len(files) > 0 {
			pathSet[path] = struct{}{}
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func directMediaFilesInDir(dir string, scannerCfg config.ScannerConfig) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if detectMediaType(entry.Name(), scannerCfg) == "unknown" {
			continue
		}
		files = append(files, entry.Name())
	}
	return files, nil
}
