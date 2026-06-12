package task

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/runstate"
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
	StatusStopping  TaskStatus = "stopping"
	StatusStopped   TaskStatus = "stopped"
	StatusPausing   TaskStatus = "pausing"
	StatusPaused    TaskStatus = "paused"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"

	maxFinishedTaskHistory = 100
)

var (
	ErrTaskConflict = errors.New("task_conflict")
	ErrInvalidInput = errors.New("invalid_task_input")
	ErrNoRunner     = errors.New("scan_runner_unavailable")
	ErrShuttingDown = errors.New("task_manager_shutting_down")
	ErrTaskNotFound = errors.New("task_not_found")
)

// Task 结构体代表一个具体的后台任务。
type Task struct {
	ID        string           `json:"id"`
	Status    TaskStatus       `json:"status"`
	Progress  float64          `json:"progress"`
	Mode      string           `json:"mode"`
	Phase     string           `json:"phase,omitempty"`
	Counts    map[string]int64 `json:"counts,omitempty"`
	Error     string           `json:"error,omitempty"`
	StartTime time.Time        `json:"startTime"`
	EndTime   *time.Time       `json:"endTime,omitempty"`

	scanPath string
}

// Manager 结构体是任务管理器。
type Manager struct {
	tasks         map[string]*Task
	taskCancels   map[string]context.CancelFunc
	cancelTargets map[string]TaskStatus
	stopping      bool
	mu            sync.RWMutex
	wg            sync.WaitGroup

	scanner  ScanRunner
	config   *config.Config
	runStore *runstate.Store
}

func NewManagerWithRunStore(s ScanRunner, cfg *config.Config, stores ...*runstate.Store) *Manager {
	var store *runstate.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	return &Manager{
		tasks:         make(map[string]*Task),
		taskCancels:   make(map[string]context.CancelFunc),
		cancelTargets: make(map[string]TaskStatus),
		scanner:       s,
		config:        cfg,
		runStore:      store,
	}
}

func (m *Manager) RecoverUnfinishedRuns(ctx context.Context) error {
	if m.runStore == nil {
		return nil
	}
	interrupted, err := m.runStore.MarkUnfinishedInterrupted(ctx)
	if err != nil {
		return err
	}
	for _, run := range interrupted {
		slog.Warn("发现未完成维护任务，已标记为 interrupted", "runID", run.ID, "phase", run.Phase, "scanPath", run.ScanPath)
	}
	return nil
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
		if isActiveStatus(task.Status) {
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
		Phase:     "pending",
		Counts:    map[string]int64{},
		StartTime: time.Now(),
		scanPath:  path,
	}
	if m.runStore != nil {
		if err := m.runStore.AcquireLock(taskID); err != nil {
			return "", fmt.Errorf("%w: %v", ErrTaskConflict, err)
		}
		if err := m.runStore.Create(context.Background(), runstate.Run{
			ID:       taskID,
			Status:   runstate.StatusPending,
			Mode:     mode,
			Phase:    "pending",
			ScanPath: path,
			Counts:   map[string]int64{},
		}); err != nil {
			_ = m.runStore.ReleaseLock()
			return "", err
		}
		_ = m.runStore.AppendEvent(context.Background(), runstate.Event{
			RunID:      taskID,
			Phase:      "pending",
			Action:     "task_created",
			Status:     string(runstate.StatusPending),
			Checkpoint: true,
		})
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
	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.RUnlock()
		return nil, fmt.Errorf("找不到任务ID: %s", taskID)
	}

	taskCopy := *task
	if task.Counts != nil {
		taskCopy.Counts = cloneCounts(task.Counts)
	}
	m.mu.RUnlock()
	if m.runStore != nil {
		if run, err := m.runStore.Get(context.Background(), taskID); err == nil && run != nil {
			if strings.TrimSpace(run.Phase) != "" {
				taskCopy.Phase = run.Phase
			}
			if len(run.Counts) > 0 {
				taskCopy.Counts = cloneCounts(run.Counts)
			}
			if len(run.ErrorSummary) > 0 && strings.TrimSpace(taskCopy.Error) == "" {
				taskCopy.Error = run.ErrorSummary[len(run.ErrorSummary)-1]
			}
		}
	}
	return &taskCopy, nil
}

func (m *Manager) StopTask(taskID string) (*Task, error) {
	return m.cancelTask(taskID, StatusStopping, StatusStopped, runstate.StatusStopping, "task_stop_requested")
}

func (m *Manager) PauseTask(taskID string) (*Task, error) {
	return m.cancelTask(taskID, StatusPausing, StatusPaused, runstate.StatusPausing, "task_pause_requested")
}

func (m *Manager) cancelTask(taskID string, pendingStatus, finalStatus TaskStatus, runPendingStatus runstate.Status, action string) (*Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("%w: task id is required", ErrInvalidInput)
	}

	m.mu.Lock()
	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: 找不到任务ID: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != StatusPending && task.Status != StatusRunning {
		taskCopy := *task
		m.mu.Unlock()
		return &taskCopy, nil
	}
	task.Status = pendingStatus
	task.Phase = string(pendingStatus)
	m.cancelTargets[taskID] = finalStatus
	cancel := m.taskCancels[taskID]
	taskCopy := *task
	m.mu.Unlock()

	if m.runStore != nil {
		_ = m.runStore.Update(context.Background(), taskID, func(run *runstate.Run) {
			run.Status = runPendingStatus
			run.Phase = string(pendingStatus)
		})
		_ = m.runStore.AppendEvent(context.Background(), runstate.Event{
			RunID:      taskID,
			Phase:      string(pendingStatus),
			Action:     action,
			Status:     string(runPendingStatus),
			Checkpoint: true,
		})
	}
	if cancel != nil {
		cancel()
	}
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
		if m.runStore != nil {
			if err := m.runStore.ReleaseLock(); err != nil {
				slog.Error("释放维护任务锁失败", "taskID", task.ID, "error", err)
			}
		}
		m.mu.Lock()
		delete(m.taskCancels, task.ID)
		delete(m.cancelTargets, task.ID)
		m.mu.Unlock()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			m.finishTask(task, StatusFailed, 100, fmt.Sprintf("扫描任务发生 panic: %v", recovered))
		}
	}()

	m.mu.Lock()
	task.Status = StatusRunning
	task.Phase = "running"
	m.mu.Unlock()
	if m.runStore != nil {
		_ = m.runStore.Update(ctx, task.ID, func(run *runstate.Run) {
			run.Status = runstate.StatusRunning
			run.Phase = "running"
		})
		_ = m.runStore.AppendEvent(ctx, runstate.Event{
			RunID:      task.ID,
			Phase:      "running",
			Action:     "task_started",
			Status:     string(runstate.StatusRunning),
			Checkpoint: true,
		})
	}

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
	if m.runStore != nil {
		ctx = runstate.WithRecorder(ctx, runstate.Recorder{Store: m.runStore, RunID: task.ID})
	}

	if err := m.scanner.RunFullScanContext(ctx, taskScannerConfig); err != nil {
		if errors.Is(err, context.Canceled) {
			finalStatus := m.cancelTarget(task.ID)
			message := "任务已停止，后续可根据最终库和 journal 校验恢复"
			if finalStatus == StatusPaused {
				message = "任务已暂停，后续可根据最终库和 journal 校验恢复"
			}
			m.finishTask(task, finalStatus, 100, message)
			slog.Warn("扫描任务已取消", "taskID", task.ID, "status", finalStatus)
			return
		}
		m.finishTask(task, StatusFailed, 100, err.Error())
		slog.Error("扫描任务失败", "taskID", task.ID, "error", err)
		return
	}

	m.finishTask(task, StatusCompleted, 100, "")
	slog.Info("扫描任务完成", "taskID", task.ID)
}

func (m *Manager) cancelTarget(taskID string) TaskStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if status, ok := m.cancelTargets[taskID]; ok {
		return status
	}
	return StatusStopped
}

func (m *Manager) finishTask(task *Task, status TaskStatus, progress float64, errMessage string) {
	m.mu.Lock()

	task.Status = status
	task.Progress = progress
	task.Error = errMessage
	endTime := time.Now()
	task.EndTime = &endTime
	m.pruneFinishedTasksLocked()
	m.mu.Unlock()

	if m.runStore == nil {
		return
	}
	runStatus := runstate.StatusCompleted
	if status == StatusStopped {
		runStatus = runstate.StatusStopped
	} else if status == StatusPaused {
		runStatus = runstate.StatusPaused
	} else if status != StatusCompleted {
		runStatus = runstate.StatusFailed
	}
	_ = m.runStore.Update(context.Background(), task.ID, func(run *runstate.Run) {
		run.Status = runStatus
		run.EndedAt = &endTime
		if errMessage != "" {
			run.ErrorSummary = append(run.ErrorSummary, errMessage)
		}
	})
	_ = m.runStore.AppendEvent(context.Background(), runstate.Event{
		RunID:      task.ID,
		Phase:      "finished",
		Action:     "task_finished",
		Status:     string(runStatus),
		Error:      errMessage,
		Checkpoint: true,
	})
}

func (m *Manager) pruneFinishedTasksLocked() {
	if len(m.tasks) <= maxFinishedTaskHistory {
		return
	}

	finished := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		if isTerminalStatus(task.Status) {
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

func isTerminalStatus(status TaskStatus) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusStopped, StatusPaused:
		return true
	default:
		return false
	}
}

func isActiveStatus(status TaskStatus) bool {
	switch status {
	case StatusPending, StatusRunning, StatusStopping, StatusPausing:
		return true
	default:
		return false
	}
}

func cloneCounts(counts map[string]int64) map[string]int64 {
	if counts == nil {
		return nil
	}
	cloned := make(map[string]int64, len(counts))
	for key, value := range counts {
		cloned[key] = value
	}
	return cloned
}
