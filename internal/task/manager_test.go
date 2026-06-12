package task

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/runstate"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type failingRunner struct{}

func (f failingRunner) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	return errors.New("scan failed")
}

type captureRunner struct {
	cfgs chan config.ScannerConfig
}

func (r captureRunner) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	r.cfgs <- cfg
	return nil
}

type contextCaptureRunner struct {
	cfgs chan config.ScannerConfig
	ctxs chan context.Context
}

func (r contextCaptureRunner) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	r.ctxs <- ctx
	r.cfgs <- cfg
	return nil
}

type blockingRunner struct {
	ctxs chan context.Context
}

func (r blockingRunner) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	r.ctxs <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestScanTaskFailureIsReported(t *testing.T) {
	manager := NewManagerWithRunStore(failingRunner{}, &config.Config{})

	taskID, err := manager.StartNewScanTask("/tmp/media", "full")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}

	status := waitForTaskStatus(t, manager, taskID, StatusFailed)
	if status.Error != "scan failed" {
		t.Fatalf("expected error message %q, got %q", "scan failed", status.Error)
	}
	if status.EndTime == nil {
		t.Fatal("expected EndTime to be set")
	}
}

func waitForTaskStatus(t *testing.T, manager *Manager, taskID string, want TaskStatus) *Task {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.GetTaskStatus(taskID)
		if err != nil {
			t.Fatalf("GetTaskStatus returned error: %v", err)
		}
		if status.Status == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}

	status, err := manager.GetTaskStatus(taskID)
	if err != nil {
		t.Fatalf("GetTaskStatus returned error: %v", err)
	}
	t.Fatalf("expected status %q, got %q", want, status.Status)
	return nil
}

func TestStartNewScanTaskRejectsInvalidMode(t *testing.T) {
	manager := NewManagerWithRunStore(failingRunner{}, &config.Config{})

	_, err := manager.StartNewScanTask("/tmp/media", "bad")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("expected error to include invalid mode, got %q", err.Error())
	}
}

func TestStartNewScanTaskRejectsMissingPath(t *testing.T) {
	manager := NewManagerWithRunStore(failingRunner{}, &config.Config{})

	_, err := manager.StartNewScanTask("  ", "full")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestStartNewScanTaskRejectsMissingRunner(t *testing.T) {
	manager := NewManagerWithRunStore(nil, &config.Config{})

	_, err := manager.StartNewScanTask("/tmp/media", "full")
	if !errors.Is(err, ErrNoRunner) {
		t.Fatalf("expected ErrNoRunner, got %v", err)
	}
}

func TestStartNewScanTaskRejectsStoppingOrPausingTask(t *testing.T) {
	for _, status := range []TaskStatus{StatusStopping, StatusPausing} {
		manager := NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{})
		manager.tasks["existing"] = &Task{ID: "existing", Status: status, StartTime: time.Now()}

		_, err := manager.StartNewScanTask("/tmp/media", "classifyOnly")
		if !errors.Is(err, ErrTaskConflict) {
			t.Fatalf("status %s: expected ErrTaskConflict, got %v", status, err)
		}
	}
}

func TestUpdateConfigAffectsFutureDefaultMode(t *testing.T) {
	runner := captureRunner{cfgs: make(chan config.ScannerConfig, 1)}
	cfg := &config.Config{Scanner: config.ScannerConfig{Mode: "full"}}
	manager := NewManagerWithRunStore(runner, cfg)
	manager.UpdateConfig(config.Config{Scanner: config.ScannerConfig{Mode: " classifyOnly "}})

	taskID, err := manager.StartNewScanTask("/tmp/media", "")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	waitForTaskStatus(t, manager, taskID, StatusCompleted)

	select {
	case got := <-runner.cfgs:
		if got.Mode != "classifyOnly" {
			t.Fatalf("expected updated default mode classifyOnly, got %q", got.Mode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner config")
	}
}

func TestRunScanUsesContextAwareRunnerWhenAvailable(t *testing.T) {
	runner := contextCaptureRunner{
		cfgs: make(chan config.ScannerConfig, 1),
		ctxs: make(chan context.Context, 1),
	}
	manager := NewManagerWithRunStore(runner, &config.Config{})

	taskID, err := manager.StartNewScanTask("/tmp/media", "classifyOnly")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	waitForTaskStatus(t, manager, taskID, StatusCompleted)

	select {
	case ctx := <-runner.ctxs:
		if ctx == nil {
			t.Fatal("expected non-nil context")
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("expected scan context to be canceled after task completion")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for context-aware runner")
	}
	select {
	case got := <-runner.cfgs:
		if got.ScanPath != "/tmp/media" || got.Mode != "classifyOnly" {
			t.Fatalf("unexpected scanner config: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scanner config")
	}
}

func TestFinishedTaskHistoryIsBounded(t *testing.T) {
	manager := NewManagerWithRunStore(failingRunner{}, &config.Config{})
	taskIDs := make([]string, 0, maxFinishedTaskHistory+5)

	for i := 0; i < maxFinishedTaskHistory+5; i++ {
		taskID, err := manager.StartNewScanTask("/tmp/media", "full")
		if err != nil {
			t.Fatalf("StartNewScanTask returned error at %d: %v", i, err)
		}
		taskIDs = append(taskIDs, taskID)
		waitForTaskStatus(t, manager, taskID, StatusFailed)
	}

	manager.mu.RLock()
	taskCount := len(manager.tasks)
	manager.mu.RUnlock()
	if taskCount != maxFinishedTaskHistory {
		t.Fatalf("expected %d retained finished tasks, got %d", maxFinishedTaskHistory, taskCount)
	}

	if _, err := manager.GetTaskStatus(taskIDs[0]); err == nil {
		t.Fatal("expected oldest finished task to be pruned")
	}
	if _, err := manager.GetTaskStatus(taskIDs[len(taskIDs)-1]); err != nil {
		t.Fatalf("expected newest finished task to remain queryable: %v", err)
	}
}

func TestFinishedTaskHistoryIncludesStoppedAndPausedTasks(t *testing.T) {
	manager := NewManagerWithRunStore(nil, &config.Config{})
	now := time.Now()
	for i := 0; i < maxFinishedTaskHistory+6; i++ {
		status := StatusStopped
		if i%2 == 0 {
			status = StatusPaused
		}
		endTime := now.Add(time.Duration(i) * time.Second)
		manager.tasks[string(rune('a'+i))] = &Task{
			ID:        string(rune('a' + i)),
			Status:    status,
			StartTime: endTime,
			EndTime:   &endTime,
		}
	}

	manager.mu.Lock()
	manager.pruneFinishedTasksLocked()
	taskCount := len(manager.tasks)
	manager.mu.Unlock()

	if taskCount != maxFinishedTaskHistory {
		t.Fatalf("expected %d retained terminal tasks, got %d", maxFinishedTaskHistory, taskCount)
	}
}

func TestGetTaskStatusMergesPersistedRunProgress(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	const taskID = "task-1"
	if err := store.Create(context.Background(), runstate.Run{
		ID:           taskID,
		Status:       runstate.StatusRunning,
		Phase:        "archive_done",
		Counts:       map[string]int64{"pathChanges": 7},
		ErrorSummary: []string{"latest warning"},
	}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	manager := NewManagerWithRunStore(nil, &config.Config{}, store)
	manager.tasks[taskID] = &Task{
		ID:        taskID,
		Status:    StatusRunning,
		Phase:     "running",
		Counts:    map[string]int64{"old": 1},
		StartTime: time.Now(),
	}

	status, err := manager.GetTaskStatus(taskID)
	if err != nil {
		t.Fatalf("GetTaskStatus returned error: %v", err)
	}
	if status.Phase != "archive_done" {
		t.Fatalf("expected persisted phase, got %+v", status)
	}
	if status.Counts["pathChanges"] != 7 || status.Counts["old"] != 0 {
		t.Fatalf("expected persisted counts, got %+v", status.Counts)
	}
	if status.Error != "latest warning" {
		t.Fatalf("expected persisted error summary, got %q", status.Error)
	}
}

func TestShutdownCancelsRunningScanTask(t *testing.T) {
	runner := blockingRunner{ctxs: make(chan context.Context, 1)}
	manager := NewManagerWithRunStore(runner, &config.Config{})

	taskID, err := manager.StartNewScanTask("/tmp/media", "full")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}

	var scanCtx context.Context
	select {
	case scanCtx = <-runner.ctxs:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scan context")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	select {
	case <-scanCtx.Done():
	default:
		t.Fatal("expected scan context to be canceled")
	}

	status := waitForTaskStatus(t, manager, taskID, StatusStopped)
	if !strings.Contains(status.Error, "停止") {
		t.Fatalf("expected stopped message, got %q", status.Error)
	}
}

func TestStopTaskMarksRunStopped(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	runner := blockingRunner{ctxs: make(chan context.Context, 1)}
	manager := NewManagerWithRunStore(runner, &config.Config{}, store)

	taskID, err := manager.StartNewScanTask("/tmp/media", "classifyOnly")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	select {
	case <-runner.ctxs:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scan context")
	}
	if _, err := manager.StopTask(taskID); err != nil {
		t.Fatalf("StopTask returned error: %v", err)
	}
	status := waitForTaskStatus(t, manager, taskID, StatusStopped)
	if !strings.Contains(status.Error, "停止") {
		t.Fatalf("expected stopped message, got %q", status.Error)
	}
	run, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if run.Status != runstate.StatusStopped {
		t.Fatalf("expected run stopped, got %+v", run)
	}
}

func TestStopTaskRejectsUnknownTask(t *testing.T) {
	manager := NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{})

	_, err := manager.StopTask("missing")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}
}

func TestPauseTaskMarksRunPaused(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	runner := blockingRunner{ctxs: make(chan context.Context, 1)}
	manager := NewManagerWithRunStore(runner, &config.Config{}, store)

	taskID, err := manager.StartNewScanTask("/tmp/media", "classifyOnly")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	select {
	case <-runner.ctxs:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scan context")
	}
	if _, err := manager.PauseTask(taskID); err != nil {
		t.Fatalf("PauseTask returned error: %v", err)
	}
	status := waitForTaskStatus(t, manager, taskID, StatusPaused)
	if !strings.Contains(status.Error, "暂停") {
		t.Fatalf("expected paused message, got %q", status.Error)
	}
	run, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if run.Status != runstate.StatusPaused {
		t.Fatalf("expected run paused, got %+v", run)
	}
}

func TestShutdownRejectsNewScanTasks(t *testing.T) {
	manager := NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{})
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	_, err := manager.StartNewScanTask("/tmp/media", "full")
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("expected ErrShuttingDown, got %v", err)
	}
}

func TestManagerPersistsRunState(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	manager := NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{}, store)

	taskID, err := manager.StartNewScanTask("/tmp/media", "classifyOnly")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	waitForTaskStatus(t, manager, taskID, StatusCompleted)

	run, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if run == nil || run.Status != runstate.StatusCompleted || run.EndedAt == nil {
		t.Fatalf("unexpected run state: %+v", run)
	}
	events, err := store.Journal(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Journal returned error: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected run journal events")
	}
}

func TestManagerRejectsWhenRunStoreLockIsHeld(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if err := store.AcquireLock("external"); err != nil {
		t.Fatalf("AcquireLock returned error: %v", err)
	}
	manager := NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{}, store)

	_, err = manager.StartNewScanTask("/tmp/media", "classifyOnly")
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("expected ErrTaskConflict, got %v", err)
	}
}

func TestManagerRecoverUnfinishedRuns(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if err := store.Create(context.Background(), runstate.Run{ID: "run-1", Status: runstate.StatusRunning}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	manager := NewManagerWithRunStore(nil, &config.Config{}, store)

	if err := manager.RecoverUnfinishedRuns(context.Background()); err != nil {
		t.Fatalf("RecoverUnfinishedRuns returned error: %v", err)
	}
	run, err := store.Get(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if run.Status != runstate.StatusInterrupted {
		t.Fatalf("expected interrupted run, got %+v", run)
	}
}
