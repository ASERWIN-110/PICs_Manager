package task

import (
	"PICs_Manager/config"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ScanRunner interface {
	RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error
}

// TaskStatus 定义了任务可能的状态。
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"

	maxFinishedTaskHistory = 100
)

var (
	ErrTaskConflict = errors.New("task_conflict")
	ErrInvalidInput = errors.New("invalid_task_input")
	ErrNoRunner     = errors.New("scan_runner_unavailable")
	ErrShuttingDown = errors.New("task_manager_shutting_down")
)

// Task 结构体代表一个具体的后台任务。
type Task struct {
	ID        string     `json:"id"`
	Status    TaskStatus `json:"status"`
	Progress  float64    `json:"progress"`
	Mode      string     `json:"mode"`
	Error     string     `json:"error,omitempty"`
	StartTime time.Time  `json:"startTime"`
	EndTime   *time.Time `json:"endTime,omitempty"`

	scanPath string
}

// Manager 结构体是任务管理器。
type Manager struct {
	tasks       map[string]*Task
	taskCancels map[string]context.CancelFunc
	stopping    bool
	mu          sync.RWMutex
	wg          sync.WaitGroup

	scanner ScanRunner
	config  *config.Config
}

func NewManager(s ScanRunner, cfg *config.Config) *Manager {
	return &Manager{
		tasks:       make(map[string]*Task),
		taskCancels: make(map[string]context.CancelFunc),
		scanner:     s,
		config:      cfg,
	}
}

// StartNewScanTask 创建一个新的扫描任务，并立即在后台启动它。
func (m *Manager) StartNewScanTask(path, mode string) (string, error) {
	path = strings.TrimSpace(path)
	mode = strings.TrimSpace(mode)
	if path == "" {
		return "", fmt.Errorf("%w: 扫描路径不能为空", ErrInvalidInput)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopping {
		return "", fmt.Errorf("%w: 任务管理器正在关闭", ErrShuttingDown)
	}
	if m.scanner == nil {
		return "", fmt.Errorf("%w: 扫描器未初始化", ErrNoRunner)
	}
	for _, task := range m.tasks {
		if task.Status == StatusPending || task.Status == StatusRunning {
			return "", fmt.Errorf("%w: 另一个扫描任务正在进行中 (ID: %s)，请等待其完成后再试", ErrTaskConflict, task.ID)
		}
	}

	taskID := uuid.New().String()
	if mode == "" && m.config != nil {
		mode = strings.TrimSpace(m.config.Scanner.Mode)
	}
	if mode == "" {
		mode = "full"
	}
	if mode != "full" && mode != "classifyOnly" {
		return "", fmt.Errorf("%w: 无效扫描模式 %q，可选值: full, classifyOnly", ErrInvalidInput, mode)
	}
	newTask := &Task{
		ID:        taskID,
		Status:    StatusPending,
		Progress:  0,
		Mode:      mode,
		StartTime: time.Now(),
		scanPath:  path,
	}
	m.tasks[taskID] = newTask
	ctx, cancel := context.WithCancel(context.Background())
	m.taskCancels[taskID] = cancel

	m.wg.Add(1)
	go m.runScan(newTask, ctx, cancel)

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

	taskCopy := *task
	return &taskCopy, nil
}

func (m *Manager) UpdateConfig(cfg config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config == nil {
		m.config = &cfg
		return
	}
	*m.config = cfg
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopping = true
	cancels := make([]context.CancelFunc, 0, len(m.taskCancels))
	for _, cancel := range m.taskCancels {
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runScan 是执行具体扫描工作的内部函数。
func (m *Manager) runScan(task *Task, ctx context.Context, cancel context.CancelFunc) {
	defer m.wg.Done()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.taskCancels, task.ID)
		m.mu.Unlock()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			m.finishTask(task, StatusFailed, 100, fmt.Sprintf("扫描任务发生 panic: %v", recovered))
		}
	}()

	m.mu.Lock()
	task.Status = StatusRunning
	m.mu.Unlock()

	slog.Info("扫描任务启动", "taskID", task.ID, "scanPath", task.scanPath, "mode", task.Mode)

	m.mu.Lock()
	task.Progress = 50.0
	m.mu.Unlock()

	m.mu.RLock()
	var taskScannerConfig config.ScannerConfig
	if m.config != nil {
		taskScannerConfig = m.config.Scanner
	}
	m.mu.RUnlock()
	taskScannerConfig.ScanPath = task.scanPath
	taskScannerConfig.Mode = task.Mode

	if err := m.scanner.RunFullScanContext(ctx, taskScannerConfig); err != nil {
		m.finishTask(task, StatusFailed, 100, err.Error())
		slog.Error("扫描任务失败", "taskID", task.ID, "error", err)
		return
	}

	m.finishTask(task, StatusCompleted, 100, "")
	slog.Info("扫描任务完成", "taskID", task.ID)
}

func (m *Manager) finishTask(task *Task, status TaskStatus, progress float64, errMessage string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task.Status = status
	task.Progress = progress
	task.Error = errMessage
	endTime := time.Now()
	task.EndTime = &endTime
	m.pruneFinishedTasksLocked()
}

func (m *Manager) pruneFinishedTasksLocked() {
	if len(m.tasks) <= maxFinishedTaskHistory {
		return
	}

	finished := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		if task.Status == StatusCompleted || task.Status == StatusFailed {
			finished = append(finished, task)
		}
	}
	if len(finished) <= maxFinishedTaskHistory {
		return
	}

	sort.Slice(finished, func(i, j int) bool {
		left, right := finished[i], finished[j]
		switch {
		case left.EndTime == nil && right.EndTime == nil:
			return left.StartTime.Before(right.StartTime)
		case left.EndTime == nil:
			return false
		case right.EndTime == nil:
			return true
		default:
			return left.EndTime.Before(*right.EndTime)
		}
	})

	for _, task := range finished[:len(finished)-maxFinishedTaskHistory] {
		delete(m.tasks, task.ID)
	}
}
