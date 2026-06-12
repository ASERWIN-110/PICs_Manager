package maintenance

import (
	"PICs_Manager/pkg/hasher"
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	execLookPath = exec.LookPath
	execCommand  = exec.CommandContext
)

// Maintenance 定义了维护工具的接口
type Maintenance interface {
	GenerateFileManifest(ctx context.Context, libraryPath, outputPath string) error
	BackupDatabase(ctx context.Context, dbURI, dbName, outputPath string) error
	Close() error
}

type defaultMaintenance struct {
	logger     *log.Logger
	logFile    *os.File
	numWorkers int
}

// NewMaintenance 创建一个新的维护模块实例
func NewMaintenance(logDir string, workerCount int) (Maintenance, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建维护模块日志目录: %w", err)
	}
	logFilePath := filepath.Join(logDir, "maintenance.log")
	file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return nil, fmt.Errorf("无法初始化维护模块日志: %w", err)
	}
	logger := log.New(file, "MAINTENANCE: ", log.LstdFlags|log.Lshortfile)
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}
	return &defaultMaintenance{
		logger:     logger,
		logFile:    file,
		numWorkers: workerCount,
	}, nil
}

func (m *defaultMaintenance) Close() error {
	if m == nil || m.logFile == nil {
		return nil
	}
	err := m.logFile.Close()
	m.logFile = nil
	return err
}

// GenerateFileManifest 并发地为媒体库生成文件清单
func (m *defaultMaintenance) GenerateFileManifest(ctx context.Context, libraryPath, outputPath string) error {
	m.logger.Println("--- 开始生成文件清单 (File Manifest) ---")
	libraryRoot, err := filepath.Abs(libraryPath)
	if err != nil {
		return fmt.Errorf("无法解析媒体库路径: %w", err)
	}
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("无法创建清单输出目录: %w", err)
	}

	paths, err := collectManifestPaths(ctx, libraryRoot)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	tasks := make(chan string, m.numWorkers)
	results := make(chan manifestEntry, m.numWorkers)
	errs := make(chan error, len(paths))

	for i := 0; i < m.numWorkers; i++ {
		wg.Add(1)
		go m.manifestWorker(ctx, &wg, libraryRoot, tasks, results, errs)
	}

	go func() {
		defer close(tasks)
		for _, path := range paths {
			select {
			case <-ctx.Done():
				return
			case tasks <- path:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
		close(errs)
	}()

	entries := make([]manifestEntry, 0, len(paths))
	for entry := range results {
		entries = append(entries, entry)
	}

	var allErrs []error
	if ctx.Err() != nil {
		allErrs = append(allErrs, ctx.Err())
	}
	for err := range errs {
		allErrs = append(allErrs, err)
	}
	if len(allErrs) > 0 {
		return errors.Join(allErrs...)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelPath < entries[j].RelPath
	})

	manifestPath, err := writeManifestFile(outputPath, entries)
	if err != nil {
		return err
	}

	m.logger.Printf("清单文件已保存到: %s", manifestPath)
	m.logger.Println("--- 文件清单生成完毕 ---")
	return nil
}

type manifestEntry struct {
	RelPath string
	Hash    string
}

func collectManifestPaths(ctx context.Context, libraryRoot string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(libraryRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描媒体库失败: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func writeManifestFile(outputPath string, entries []manifestEntry) (string, error) {
	manifestFileName := fmt.Sprintf("manifest_%s.txt", time.Now().Format("2006-01-02"))
	manifestPath := filepath.Join(outputPath, manifestFileName)
	tmpFile, err := os.CreateTemp(outputPath, ".manifest-*.tmp")
	if err != nil {
		return "", fmt.Errorf("无法创建临时清单文件: %w", err)
	}
	tmpName := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	for _, entry := range entries {
		if _, err := tmpFile.WriteString(fmt.Sprintf("%s *%s\n", entry.Hash, entry.RelPath)); err != nil {
			_ = tmpFile.Close()
			return "", fmt.Errorf("写入清单文件失败: %w", err)
		}
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("同步清单文件失败: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("关闭清单文件失败: %w", err)
	}
	if err := os.Rename(tmpName, manifestPath); err != nil {
		return "", fmt.Errorf("保存清单文件失败: %w", err)
	}
	if err := syncDir(outputPath); err != nil {
		return "", fmt.Errorf("同步清单目录失败: %w", err)
	}
	cleanup = false
	return manifestPath, nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func (m *defaultMaintenance) manifestWorker(ctx context.Context, wg *sync.WaitGroup, libraryRoot string, tasks <-chan string, results chan<- manifestEntry, errs chan<- error) {
	defer wg.Done()
	for path := range tasks {
		if ctx.Err() != nil {
			return
		}
		hash, err := hasher.CalculateSHA256(path)
		if err != nil {
			errs <- fmt.Errorf("计算文件 %s 的哈希失败: %w", path, err)
			continue
		}
		relPath, err := filepath.Rel(libraryRoot, path)
		if err != nil {
			errs <- fmt.Errorf("计算相对路径失败 %s: %w", path, err)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case results <- manifestEntry{RelPath: filepath.ToSlash(relPath), Hash: hash}:
		}
	}
}

// BackupDatabase 调用 mongodump 工具来备份数据库
func (m *defaultMaintenance) BackupDatabase(ctx context.Context, dbURI, dbName, outputPath string) error {
	m.logger.Println("--- 开始执行数据库备份 ---")

	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("无法创建数据库备份输出目录: %w", err)
	}
	if _, err := execLookPath("mongodump"); err != nil {
		m.logger.Println("未找到 mongodump，改用内置 Extended JSON Lines 备份。")
		return m.backupDatabaseNative(ctx, dbURI, dbName, outputPath)
	}

	// 1. 创建输出文件路径
	backupFileName := fmt.Sprintf("db_backup_%s.gz", time.Now().Format("2006-01-02_150405"))
	archiveFile := filepath.Join(outputPath, backupFileName)
	m.logger.Printf("数据库备份文件将被保存到: %s", archiveFile)

	// 2. 构建并执行命令
	cmd := execCommand(ctx, "mongodump",
		"--uri", dbURI,
		"--db", dbName,
		"--archive="+archiveFile,
		"--gzip",
	)

	// 将命令的输出连接到我们的日志，以便实时查看进度和错误
	cmd.Stdout = m.logger.Writer()
	cmd.Stderr = m.logger.Writer()

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("执行 mongodump 失败: %w", err)
	}

	m.logger.Println("--- 数据库备份成功 ---")
	return nil
}

func (m *defaultMaintenance) backupDatabaseNative(ctx context.Context, dbURI, dbName, outputPath string) error {
	if strings.TrimSpace(dbName) == "" {
		return fmt.Errorf("数据库名不能为空")
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(dbURI))
	if err != nil {
		return fmt.Errorf("连接 MongoDB 失败: %w", err)
	}
	defer func() {
		if disconnectErr := client.Disconnect(context.Background()); disconnectErr != nil {
			m.logger.Printf("断开 MongoDB 连接失败: %v", disconnectErr)
		}
	}()
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("验证 MongoDB 连接失败: %w", err)
	}

	archiveFile := filepath.Join(outputPath, fmt.Sprintf("db_backup_%s.jsonl.tar.gz", time.Now().Format("2006-01-02_150405")))
	m.logger.Printf("内置数据库备份文件将被保存到: %s", archiveFile)
	out, err := os.Create(archiveFile)
	if err != nil {
		return fmt.Errorf("无法创建数据库备份文件: %w", err)
	}
	removeArchive := true
	outClosed := false
	defer func() {
		if !outClosed {
			_ = out.Close()
		}
		if removeArchive {
			_ = os.Remove(archiveFile)
		}
	}()

	gzipWriter := gzip.NewWriter(out)
	gzipClosed := false
	defer func() {
		if !gzipClosed {
			_ = gzipWriter.Close()
		}
	}()
	tarWriter := tar.NewWriter(gzipWriter)
	tarClosed := false
	defer func() {
		if !tarClosed {
			_ = tarWriter.Close()
		}
	}()

	db := client.Database(dbName)
	collections, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("列出数据库集合失败: %w", err)
	}
	sort.Strings(collections)

	metadata := nativeBackupMetadata{
		Database:    dbName,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Format:      "mongodb-extended-json-lines-v1",
		Collections: make([]nativeCollectionMetadata, 0, len(collections)),
	}
	for _, collectionName := range collections {
		count, tempPath, err := m.writeCollectionJSONL(ctx, db.Collection(collectionName), outputPath)
		if err != nil {
			return err
		}
		if err := addFileToTar(tarWriter, tempPath, nativeCollectionArchiveName(collectionName)); err != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("写入集合 %s 到备份包失败: %w", collectionName, err)
		}
		_ = os.Remove(tempPath)
		metadata.Collections = append(metadata.Collections, nativeCollectionMetadata{
			Name:      collectionName,
			Documents: count,
			File:      nativeCollectionArchiveName(collectionName),
		})
		m.logger.Printf("已备份集合 %s，文档数: %d", collectionName, count)
	}
	if err := addMetadataToTar(tarWriter, metadata); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		tarClosed = true
		return fmt.Errorf("关闭数据库备份 tar 流失败: %w", err)
	}
	tarClosed = true
	if err := gzipWriter.Close(); err != nil {
		gzipClosed = true
		return fmt.Errorf("关闭数据库备份 gzip 流失败: %w", err)
	}
	gzipClosed = true
	if err := out.Close(); err != nil {
		outClosed = true
		return fmt.Errorf("关闭数据库备份文件失败: %w", err)
	}
	outClosed = true
	removeArchive = false

	m.logger.Println("--- 数据库内置备份成功 ---")
	return nil
}

type nativeBackupMetadata struct {
	Database    string                     `json:"database"`
	CreatedAt   string                     `json:"createdAt"`
	Format      string                     `json:"format"`
	Collections []nativeCollectionMetadata `json:"collections"`
}

type nativeCollectionMetadata struct {
	Name      string `json:"name"`
	Documents int64  `json:"documents"`
	File      string `json:"file"`
}

func (m *defaultMaintenance) writeCollectionJSONL(ctx context.Context, collection *mongo.Collection, outputPath string) (int64, string, error) {
	tempFile, err := os.CreateTemp(outputPath, nativeCollectionTempPattern(collection.Name()))
	if err != nil {
		return 0, "", fmt.Errorf("无法创建集合 %s 的临时备份文件: %w", collection.Name(), err)
	}
	tempPath := tempFile.Name()
	removeTemp := true
	tempClosed := false
	defer func() {
		if !tempClosed {
			_ = tempFile.Close()
		}
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	cursor, err := collection.Find(ctx, bson.D{}, options.Find().SetBatchSize(500))
	if err != nil {
		return 0, "", fmt.Errorf("读取集合 %s 失败: %w", collection.Name(), err)
	}
	defer cursor.Close(ctx)

	var count int64
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return 0, "", fmt.Errorf("解码集合 %s 文档失败: %w", collection.Name(), err)
		}
		line, err := bson.MarshalExtJSON(doc, false, false)
		if err != nil {
			return 0, "", fmt.Errorf("序列化集合 %s 文档失败: %w", collection.Name(), err)
		}
		if _, err := tempFile.Write(line); err != nil {
			return 0, "", fmt.Errorf("写入集合 %s 备份失败: %w", collection.Name(), err)
		}
		if _, err := tempFile.WriteString("\n"); err != nil {
			return 0, "", fmt.Errorf("写入集合 %s 备份失败: %w", collection.Name(), err)
		}
		count++
	}
	if err := cursor.Err(); err != nil {
		return 0, "", fmt.Errorf("遍历集合 %s 失败: %w", collection.Name(), err)
	}
	if err := tempFile.Close(); err != nil {
		tempClosed = true
		return 0, "", fmt.Errorf("关闭集合 %s 临时备份文件失败: %w", collection.Name(), err)
	}
	tempClosed = true
	removeTemp = false
	return count, tempPath, nil
}

func addFileToTar(tarWriter *tar.Writer, path, name string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tarWriter, file)
	return err
}

func addMetadataToTar(tarWriter *tar.Writer, metadata nativeBackupMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化备份元数据失败: %w", err)
	}
	header := &tar.Header{
		Name:    "metadata.json",
		Mode:    0644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("写入备份元数据失败: %w", err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		return fmt.Errorf("写入备份元数据失败: %w", err)
	}
	return nil
}

func nativeCollectionArchiveName(collectionName string) string {
	return "collections/" + safeArchiveName(collectionName) + ".jsonl"
}

func nativeCollectionTempPattern(collectionName string) string {
	return ".backup-" + safeArchiveName(collectionName) + "-*.jsonl"
}

func safeArchiveName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", "\x00", "_")
	safe := strings.TrimSpace(replacer.Replace(name))
	if safe == "" {
		return "collection"
	}
	return safe
}
