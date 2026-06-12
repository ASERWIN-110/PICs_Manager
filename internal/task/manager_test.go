package task

import (
	"PICs_Manager/config"
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
	manager := NewManager(failingRunner{}, &config.Config{})

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
	manager := NewManager(failingRunner{}, &config.Config{})

	_, err := manager.StartNewScanTask("/tmp/media", "bad")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("expected error to include invalid mode, got %q", err.Error())
	}
}

func TestStartNewScanTaskRejectsMissingPath(t *testing.T) {
	manager := NewManager(failingRunner{}, &config.Config{})

	_, err := manager.StartNewScanTask("  ", "full")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestStartNewScanTaskRejectsMissingRunner(t *testing.T) {
	manager := NewManager(nil, &config.Config{})

	_, err := manager.StartNewScanTask("/tmp/media", "full")
	if !errors.Is(err, ErrNoRunner) {
		t.Fatalf("expected ErrNoRunner, got %v", err)
	}
}

func TestUpdateConfigAffectsFutureDefaultMode(t *testing.T) {
	runner := captureRunner{cfgs: make(chan config.ScannerConfig, 1)}
	cfg := &config.Config{Scanner: config.ScannerConfig{Mode: "full"}}
	manager := NewManager(runner, cfg)
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
	manager := NewManager(runner, &config.Config{})

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
	manager := NewManager(failingRunner{}, &config.Config{})
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

func TestShutdownCancelsRunningScanTask(t *testing.T) {
	runner := blockingRunner{ctxs: make(chan context.Context, 1)}
	manager := NewManager(runner, &config.Config{})

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

	status := waitForTaskStatus(t, manager, taskID, StatusFailed)
	if !strings.Contains(status.Error, context.Canceled.Error()) {
		t.Fatalf("expected cancellation error, got %q", status.Error)
	}
}

func TestShutdownRejectsNewScanTasks(t *testing.T) {
	manager := NewManager(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{})
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	_, err := manager.StartNewScanTask("/tmp/media", "full")
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("expected ErrShuttingDown, got %v", err)
	}
}
