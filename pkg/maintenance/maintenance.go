package maintenance

import (
	"PICs_Manager/pkg/hasher"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Maintenance 定义了维护工具的接口
type Maintenance interface {
	GenerateFileManifest(ctx context.Context, libraryPath, outputPath string) error
	BackupDatabase(ctx context.Context, dbURI, dbName, outputPath string) error
}

type defaultMaintenance struct {
	logger     *log.Logger
	logFile    *os.File
	numWorkers int
}

// NewMaintenance 创建一个新的维护模块实例
func NewMaintenance(logDir string, workerCount int) (Maintenance, error) {
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

// GenerateFileManifest 并发地为媒体库生成文件清单
func (m *defaultMaintenance) GenerateFileManifest(ctx context.Context, libraryPath, outputPath string) error {
	m.logger.Println("--- 开始生成文件清单 (File Manifest) ---")

	// 1. 创建输出文件
	manifestFileName := fmt.Sprintf("manifest_%s.txt", time.Now().Format("2006-01-02"))
	manifestPath := filepath.Join(outputPath, manifestFileName)
	file, err := os.Create(manifestPath)
	if err != nil {
		return fmt.Errorf("无法创建清单文件: %w", err)
	}
	defer file.Close()
	m.logger.Printf("清单文件将被保存到: %s", manifestPath)

	// 2. 设置并发工作池
	var wg sync.WaitGroup
	tasks := make(chan string, m.numWorkers)
	results := make(chan string, m.numWorkers)

	// 启动哈希计算工人
	for i := 0; i < m.numWorkers; i++ {
		wg.Add(1)
		go m.manifestWorker(&wg, tasks, results)
	}

	// 启动一个单独的协程来将结果写入文件，避免并发写文件
	var writeWg sync.WaitGroup
	writeWg.Add(1)
	go func() {
		defer writeWg.Done()
		for line := range results {
			if _, err := file.WriteString(line); err != nil {
				m.logger.Printf("错误: 写入清单文件失败: %v", err)
			}
		}
	}()

	// 3. 分发任务
	m.logger.Println("开始扫描文件并分发任务...")
	err = filepath.WalkDir(libraryPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			tasks <- path
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("扫描媒体库失败: %w", err)
	}

	close(tasks)
	wg.Wait()
	close(results)
	writeWg.Wait()

	m.logger.Println("--- 文件清单生成完毕 ---")
	return nil
}

// manifestWorker 是计算哈希并格式化输出的工人
func (m *defaultMaintenance) manifestWorker(wg *sync.WaitGroup, tasks <-chan string, results chan<- string) {
	defer wg.Done()
	for path := range tasks {
		hash, err := hasher.CalculateSHA256(path)
		if err != nil {
			m.logger.Printf("警告: 计算文件 %s 的哈希失败: %v", path, err)
			continue
		}
		// 为了可移植性，将路径分隔符统一为 '/'
		relPath, _ := filepath.Rel(filepath.Dir(path), path) // 这里可以优化为相对于库根目录
		line := fmt.Sprintf("%s *%s\n", hash, filepath.ToSlash(relPath))
		results <- line
	}
}

// BackupDatabase 调用 mongodump 工具来备份数据库
func (m *defaultMaintenance) BackupDatabase(ctx context.Context, dbURI, dbName, outputPath string) error {
	m.logger.Println("--- 开始执行数据库备份 ---")

	// 检查 mongodump 命令是否存在
	if _, err := exec.LookPath("mongodump"); err != nil {
		m.logger.Println("致命错误: 在系统 PATH 中找不到 'mongodump' 命令。")
		m.logger.Println("请确保您已正确安装 MongoDB Database Tools，并将其添加到了系统环境变量中。")
		return fmt.Errorf("'mongodump' command not found in PATH")
	}

	// 1. 创建输出文件路径
	backupFileName := fmt.Sprintf("db_backup_%s.gz", time.Now().Format("2006-01-02_150405"))
	archiveFile := filepath.Join(outputPath, backupFileName)
	m.logger.Printf("数据库备份文件将被保存到: %s", archiveFile)

	// 2. 构建并执行命令
	cmd := exec.CommandContext(ctx, "mongodump",
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
