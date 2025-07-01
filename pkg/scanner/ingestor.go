package scanner

import (
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/hasher"
	"PICs_Manager/pkg/thumbnailer"
	"bytes"
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// MetadataIngestor 定义了数据入库器的行为接口
type MetadataIngestor interface {
	Sync(ctx context.Context, finalLibraryPath string, createdSeries, processedFileNames []string, changelog map[string]string) (overwrittenFiles []string, err error)
	Close()
}

type mongoIngestor struct {
	dbStore    database.Store
	logger     *log.Logger
	logFile    *os.File
	numWorkers int
	batchSize  int
}

const ingestorLogFileName = "ingestor.log"

// NewIngestor 创建一个新的入库器实例
func NewIngestor(logDir string, dbStore database.Store, workerCount, batchSize int) (MetadataIngestor, error) {
	logFilePath := filepath.Join(logDir, ingestorLogFileName)
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化入库器日志: %w", err)
	}
	logger := log.New(file, "INGEST: ", log.LstdFlags|log.Lshortfile)

	if batchSize <= 0 {
		batchSize = 100
	}
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}

	return &mongoIngestor{
		dbStore:    dbStore,
		logger:     logger,
		logFile:    file,
		numWorkers: workerCount,
		batchSize:  batchSize,
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

	// 3. 阶段二：批量处理图片，并检测覆盖
	m.logger.Printf("--- 阶段 2/4: 处理图片并检测覆盖 ---")
	overwrittenFiles, err := m.processAllImages(ctx, seriesPathsToProcess, seriesCache)
	if err != nil {
		return nil, fmt.Errorf("处理图片时失败: %w", err)
	}

	// 4. 阶段三： 更新 Series 的元数据
	m.logger.Println("--- 阶段 3/4: 更新系列元数据 (ImageCount, Thumbnail) ---")
	if err := m.updateAllSeriesMetadata(ctx, seriesCache); err != nil {
		m.logger.Printf("警告: 更新系列元数据失败: %v", err)
		// 通常这是一个非致命错误，只记录日志即可
	}

	// 5. 阶段四：最终验证
	m.logger.Println("--- 阶段 4/4: 执行最终验证查询 ---")
	m.logger.Printf("接收到 %d 个系列名，%d 个文件名。", len(createdSeries), len(processedFileNames))
	m.logger.Println("--- 数据库同步完成 ---")
	return overwrittenFiles, nil
}

// collectFinalSeriesPaths (基于“靶向扫描”思路的实现)
func (m *mongoIngestor) collectFinalSeriesPaths(finalLibraryPath string, changelog map[string]string) []string {
	pathSet := make(map[string]struct{})
	m.logger.Println("正在根据 changelog 解析需要处理的最终系列路径...")

	// 我们只关心 changelog 中的最终目标路径
	for _, newPath := range changelog {
		// 检查路径是否存在
		info, err := os.Stat(newPath)
		if err != nil {
			// 如果路径不存在，可能是因为它是一个被合并后删除的空目录，或者是一个文件。跳过。
			continue
		}

		if !info.IsDir() {
			continue // 只关心目录
		}

		folderName := filepath.Base(newPath)

		// 判断这个路径本身是聚合父目录，还是一个独立的系列目录
		if strings.HasSuffix(folderName, aggSuffix) {
			// 场景A: 这是一个聚合父目录，我们需要处理它内部的所有子目录
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
			// 场景B: 这是一个独立的系列目录
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

// processAllSeries 并发地对所有系列路径执行 FindOrCreateByName，并返回一个路径到模型的缓存
func (m *mongoIngestor) processAllSeries(ctx context.Context, seriesPaths []string) (map[string]*models.Series, error) {
	if len(seriesPaths) == 0 {
		return make(map[string]*models.Series), nil
	}

	m.logger.Printf("准备批量处理 %d 个系列...", len(seriesPaths))

	// --- 步骤 1: 准备并执行批量 Upsert ---
	var seriesWrites []mongo.WriteModel
	seriesNames := make([]string, len(seriesPaths))

	// 在内存中准备好所有的写入模型
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

	// 一次性提交所有写入操作
	if err := m.dbStore.Series().BulkWrite(ctx, seriesWrites); err != nil {
		m.logger.Printf("错误: 批量写入Series失败: %v", err)
		return nil, err
	}
	m.logger.Printf("批量 Upsert 操作完成，共处理 %d 个系列。", len(seriesWrites))

	// --- 步骤 2: 批量查询结果，构建缓存 ---
	m.logger.Println("批量查询 Upsert 结果以构建缓存...")
	foundSeries, notFound, err := m.dbStore.Series().FindManyByNames(ctx, seriesNames)
	if err != nil {
		return nil, fmt.Errorf("批量查询系列结果失败: %w", err)
	}
	if len(notFound) > 0 {
		// 理论上，Upsert之后不应该有找不到的情况，如果出现则说明有严重问题
		m.logger.Printf("严重错误: Upsert后查询系列时，有 %d 个系列未找到: %v", len(notFound), notFound)
	}

	// 构建以最终路径为键的缓存
	cache := make(map[string]*models.Series)
	// 创建一个 name -> series 的临时map以便快速查找
	seriesByName := make(map[string]*models.Series, len(foundSeries))
	for i := range foundSeries {
		seriesByName[foundSeries[i].Name] = &foundSeries[i]
	}

	// 遍历原始路径列表来构建最终的 cache，确保key是正确的路径
	for _, path := range seriesPaths {
		seriesName := filepath.Base(path)
		seriesName = strings.TrimSuffix(seriesName, aggSuffix)
		if series, ok := seriesByName[seriesName]; ok {
			// 更新一下缓存中系列对象的路径为最新的路径，因为可能存在一个系列名对应多个源路径的情况
			sCopy := *series
			sCopy.Path = path
			cache[path] = &sCopy
		}
	}

	m.logger.Printf("系列信息缓存构建完成，共缓存 %d 个系列。", len(cache))
	return cache, nil
}

type imageJob struct {
	filePath string
	series   *models.Series
}
type imageResult struct {
	writeModel      mongo.WriteModel
	overwrittenPath string
}

// processAllImages 启动一个工作池来并发地处理所有系列下的所有图片
func (m *mongoIngestor) processAllImages(ctx context.Context, seriesPaths []string, seriesCache map[string]*models.Series) ([]string, error) {
	var wg sync.WaitGroup
	jobs := make(chan imageJob, m.batchSize*m.numWorkers)
	results := make(chan imageResult, m.batchSize*m.numWorkers)

	for i := 0; i < m.numWorkers; i++ {
		wg.Add(1)
		go m.imageWorker(&wg, ctx, jobs, results)
	}

	go func() {
		for _, seriesPath := range seriesPaths {
			series, ok := seriesCache[seriesPath]
			if !ok {
				continue
			}
			files, _ := os.ReadDir(seriesPath)
			for _, file := range files {
				if !file.IsDir() {
					jobs <- imageJob{filePath: filepath.Join(seriesPath, file.Name()), series: series}
				}
			}
		}
		close(jobs)
	}()

	var allOverwritten []string
	var writesBatch []mongo.WriteModel
	done := make(chan struct{})

	go func() {
		for res := range results {
			if res.writeModel != nil {
				writesBatch = append(writesBatch, res.writeModel)
			}
			if res.overwrittenPath != "" {
				allOverwritten = append(allOverwritten, res.overwrittenPath)
			}
			if len(writesBatch) >= m.batchSize {
				if err := m.dbStore.Images().BulkWrite(ctx, writesBatch); err != nil {
					m.logger.Printf("错误: 批量写入图片失败: %v", err)
				}
				writesBatch = []mongo.WriteModel{}
			}
		}
		if len(writesBatch) > 0 {
			if err := m.dbStore.Images().BulkWrite(ctx, writesBatch); err != nil {
				m.logger.Printf("错误: 批量写入图片失败: %v", err)
			}
		}
		done <- struct{}{}
	}()

	wg.Wait()
	close(results)
	<-done

	return allOverwritten, nil
}

// imageWorker 是处理单张图片的工人
func (m *mongoIngestor) imageWorker(wg *sync.WaitGroup, ctx context.Context, jobs <-chan imageJob, results chan<- imageResult) {
	defer wg.Done()
	for job := range jobs {
		filePath := job.filePath
		fileName := filepath.Base(job.filePath)

		// 1. 高效地打开文件一次
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			m.logger.Printf("错误: 无法读取文件 %s: %v", filePath, err)
			continue
		}

		// 2. 直接从内存数据计算 SHA256
		// 我们先计算SHA256，因为即使图片损坏，这个哈希也是有意义的，可以用于日志记录
		fileHash := hasher.CalculateSHA256FromBytes(fileBytes)

		// 3. 解码图片，这既是计算需要，也是最核心的损坏检查
		img, _, decodeErr := image.Decode(bytes.NewReader(fileBytes))

		if decodeErr != nil {
			// 如果解码失败，说明文件已损坏
			m.logger.Printf("严重错误: 文件 %s 确认已损坏，无法解码 (错误: %v)。将执行删除操作。", filePath, decodeErr)

			// 尝试删除这个损坏的物理文件
			deleteErr := os.Remove(filePath)
			if deleteErr != nil {
				m.logger.Printf("错误: 删除损坏的文件 %s 失败: %v", filePath, deleteErr)
			} else {
				m.logger.Printf("成功删除损坏的文件: %s", filePath)
			}

			// 终止对这个文件的处理，不将它送入结果通道，从而实现“不入库”
			continue
		}

		// 只有在解码成功后，才继续计算 pHash 和 thumbnail
		var pHash, thumbnail string
		if img != nil {
			pHash = hasher.CalculatePerceptualHashFromImage(img)
			thumbnail, _ = thumbnailer.CreateBase64(img, 200, 200)
		}

		if fileHash == "" {
			m.logger.Printf("错误: 计算SHA256失败，跳过文件 %s", filePath)
			continue
		}

		// 4. 准备 Upsert 操作
		series, err := m.dbStore.Series().FindOrCreateByName(ctx, filepath.Base(filepath.Dir(job.filePath)), job.filePath)

		if err != nil {
			m.logger.Printf("错误: 无法为 %s 找到或创建系列: %v", filePath, err)
			continue
		}

		filter := bson.M{
			"seriesId": series.ID,
			"fileName": fileName,
		}
		update := bson.M{
			// $set: 无论找到与否，都应该更新这些可能会变动的信息
			"$set": bson.M{
				"filePath":       filePath,
				"fileHash":       fileHash,
				"perceptualHash": pHash,
				"thumbnail":      thumbnail,
				"updatedAt":      time.Now(),
			},
			// $setOnInsert: 只有在首次插入时，才设置这些“出生”信息
			"$setOnInsert": bson.M{
				"_id":       primitive.NewObjectID(),
				"seriesId":  series.ID,
				"fileName":  fileName,
				"createdAt": time.Now(),
			},
		}
		model := mongo.NewUpdateOneModel().SetFilter(filter).SetUpsert(true).SetUpdate(update)

		results <- imageResult{writeModel: model}
	}
}

// updateAllSeriesMetadata
// 并发地更新所有受影响系列的元数据
func (m *mongoIngestor) updateAllSeriesMetadata(ctx context.Context, seriesCache map[string]*models.Series) error {
	var wg sync.WaitGroup
	// 任务是需要更新元数据的 Series 对象
	tasks := make(chan *models.Series, len(seriesCache))
	// 结果是准备好批量写入的数据库指令
	results := make(chan mongo.WriteModel, len(seriesCache))

	// 启动元数据更新工人
	for i := 0; i < m.numWorkers; i++ {
		wg.Add(1)
		go m.metadataUpdateWorker(&wg, ctx, tasks, results)
	}

	// 分发任务
	for _, series := range seriesCache {
		tasks <- series
	}
	close(tasks)

	// 收集所有需要执行的 UPDATE 指令
	var writes []mongo.WriteModel
	resultWg := sync.WaitGroup{}
	resultWg.Add(1)
	go func() {
		defer resultWg.Done()
		for model := range results {
			writes = append(writes, model)
		}
	}()

	wg.Wait()
	close(results)
	resultWg.Wait()

	// 一次性批量更新所有 Series
	if len(writes) > 0 {
		m.logger.Printf("准备批量更新 %d 个系列的元数据...", len(writes))
		return m.dbStore.Series().BulkWrite(ctx, writes)
	}

	m.logger.Println("没有需要更新的系列元数据。")
	return nil
}

// metadataUpdateWorker
// 是处理单个系列元数据更新的工人
func (m *mongoIngestor) metadataUpdateWorker(wg *sync.WaitGroup, ctx context.Context, tasks <-chan *models.Series, results chan<- mongo.WriteModel) {
	defer wg.Done()
	for series := range tasks {
		// 1. 获取最新的图片数量
		count, err := m.dbStore.Images().CountBySeriesID(ctx, series.ID)
		if err != nil {
			m.logger.Printf("错误: 无法统计系列 '%s' 的图片数量: %v", series.Name, err)
			continue
		}

		// 2. 获取第一张图片作为封面
		var thumbnail string
		firstImage, err := m.dbStore.Images().GetFirstImage(ctx, series.ID)
		if err != nil {
			m.logger.Printf("错误: 无法获取系列 '%s' 的封面图片: %v", series.Name, err)
		}
		if firstImage != nil {
			thumbnail = firstImage.Thumbnail // 使用图片的缩略图
		}

		// 3. 只有在数据发生变化时才准备更新指令
		if series.ImageCount != int(count) || series.Thumbnail != thumbnail {
			m.logger.Printf("系列的元数据已变更: %s (图片数: %d -> %d)", series.Name, series.ImageCount, count)
			filter := bson.M{"_id": series.ID}
			update := bson.M{"$set": bson.M{
				"imageCount": count,
				"thumbnail":  thumbnail,
				"updatedAt":  time.Now(),
			}}
			model := mongo.NewUpdateOneModel().SetFilter(filter).SetUpdate(update)
			results <- model
		}
	}
}
