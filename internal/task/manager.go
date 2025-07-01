package task

import (
	"PICs_Manager/config"      // [新增] 引入config包以使用配置类型
	"PICs_Manager/pkg/scanner" // 引入scanner包
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskStatus 定义了任务可能的状态。
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
)

// Task 结构体代表一个具体的后台任务。
type Task struct {
	ID        string     `json:"id"`
	Status    TaskStatus `json:"status"`
	Progress  float64    `json:"progress"`
	Error     string     `json:"error,omitempty"`
	StartTime time.Time  `json:"startTime"`
	EndTime   *time.Time `json:"endTime,omitempty"`

	scanPath string
}

// Manager 结构体是任务管理器。
type Manager struct {
	tasks map[string]*Task
	mu    sync.RWMutex

	scanner *scanner.Orchestrator
	config  *config.Config // [新增] 注入对全局配置的引用
}

// NewManager 创建并返回一个新的任务管理器实例。
// [修正] 函数现在接收扫描器和配置实例作为参数。
func NewManager(s *scanner.Orchestrator, cfg *config.Config) *Manager {
	return &Manager{
		tasks:   make(map[string]*Task),
		scanner: s,
		config:  cfg, // 存储配置实例
	}
}

// StartNewScanTask 创建一个新的扫描任务，并立即在后台启动它。
func (m *Manager) StartNewScanTask(path string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, task := range m.tasks {
		if task.Status == StatusRunning {
			return "", fmt.Errorf("另一个扫描任务正在进行中 (ID: %s)，请等待其完成后再试", task.ID)
		}
	}

	taskID := uuid.New().String()
	newTask := &Task{
		ID:        taskID,
		Status:    StatusPending,
		Progress:  0,
		StartTime: time.Now(),
		scanPath:  path,
	}
	m.tasks[taskID] = newTask

	go m.runScan(newTask)

	return taskID, nil
}

// GetTaskStatus 根据任务ID检索特定任务的当前状态。
func (m *Manager) GetTaskStatus(taskID string) (*Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, fmt.Errorf("找不到任务ID: %s", taskID)
	}

	return task, nil
}

// runScan 是执行具体扫描工作的内部函数。
func (m *Manager) runScan(task *Task) {
	m.mu.Lock()
	task.Status = StatusRunning
	m.mu.Unlock()

	fmt.Printf("任务启动: %s, 扫描路径: %s\n", task.ID, task.scanPath)

	m.mu.Lock()
	task.Progress = 50.0
	m.mu.Unlock()

	// [修正] 创建一个此任务专用的扫描配置，并用任务的路径覆盖默认扫描路径。
	taskScannerConfig := m.config.Scanner
	taskScannerConfig.ScanPath = task.scanPath

	// [修正] 调用真实的扫描器逻辑。
	// 根据 cli/main.go 的用法，RunFullScan 接收一个配置且不返回错误。
	// 注意：由于 RunFullScan 不返回错误，我们无法在此处捕获具体的执行失败。
	// 任务状态将直接变为 "completed"。一个更健壮的实现需要 RunFullScan 返回一个 error。
	m.scanner.RunFullScan(taskScannerConfig)

	m.mu.Lock()
	defer m.mu.Unlock()

	// 由于无法从 RunFullScan 捕获错误，我们直接将任务标记为完成。
	task.Status = StatusCompleted
	task.Progress = 100
	fmt.Printf("任务 %s 已执行，标记为完成\n", task.ID)

	endTime := time.Now()
	task.EndTime = &endTime
}
