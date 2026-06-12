package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusRunning     Status = "running"
	StatusStopping    Status = "stopping"
	StatusStopped     Status = "stopped"
	StatusPausing     Status = "pausing"
	StatusPaused      Status = "paused"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
)

type Run struct {
	ID           string            `json:"id"`
	Status       Status            `json:"status"`
	Mode         string            `json:"mode"`
	Phase        string            `json:"phase"`
	ScanPath     string            `json:"scanPath"`
	Counts       map[string]int64  `json:"counts,omitempty"`
	ErrorSummary []string          `json:"errorSummary,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	StartedAt    time.Time         `json:"startedAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	EndedAt      *time.Time        `json:"endedAt,omitempty"`
}

type Event struct {
	RunID      string            `json:"runId"`
	Time       time.Time         `json:"time"`
	Phase      string            `json:"phase,omitempty"`
	Action     string            `json:"action"`
	Source     string            `json:"source,omitempty"`
	Target     string            `json:"target,omitempty"`
	Status     string            `json:"status,omitempty"`
	Error      string            `json:"error,omitempty"`
	Counts     map[string]int64  `json:"counts,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Checkpoint bool              `json:"checkpoint,omitempty"`
}

type Store struct {
	dir      string
	lockPath string
	mu       sync.Mutex
	lockFile *os.File
}

func NewStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	dir := filepath.Join(root, "runs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建运行状态目录失败: %w", err)
	}
	return &Store{dir: dir, lockPath: filepath.Join(dir, "active.lock")}, nil
}

func (s *Store) Create(ctx context.Context, run Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(run.ID) == "" {
		return errors.New("run id is required")
	}
	now := time.Now()
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.UpdatedAt = now
	if run.Status == "" {
		run.Status = StatusPending
	}
	if run.Counts == nil {
		run.Counts = map[string]int64{}
	}
	return s.writeRun(ctx, run)
}

func (s *Store) Get(ctx context.Context, id string) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.runPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) List(ctx context.Context, limit int) ([]Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	runs := make([]Run, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (s *Store) Update(ctx context.Context, id string, mutate func(*Run)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	run, err := s.getUnlocked(id)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("run %s not found", id)
	}
	mutate(run)
	run.UpdatedAt = time.Now()
	return s.writeRunUnlocked(*run)
}

func (s *Store) AppendEvent(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(event.RunID) == "" {
		return errors.New("event run id is required")
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.journalPath(event.RunID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) Journal(ctx context.Context, id string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(s.journalPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var events []Event
	decoder := json.NewDecoder(file)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func isUnfinishedStatus(status Status) bool {
	switch status {
	case StatusPending, StatusRunning, StatusStopping, StatusPausing:
		return true
	default:
		return false
	}
}

func (s *Store) MarkUnfinishedInterrupted(ctx context.Context) ([]Run, error) {
	runs, err := s.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	interrupted := make([]Run, 0)
	for _, run := range runs {
		if !isUnfinishedStatus(run.Status) {
			continue
		}
		runCopy := run
		now := time.Now()
		if err := s.Update(ctx, run.ID, func(r *Run) {
			r.Status = StatusInterrupted
			r.EndedAt = &now
			r.ErrorSummary = append(r.ErrorSummary, "进程启动时发现上次运行未正常结束，已标记为 interrupted")
		}); err != nil {
			return nil, err
		}
		runCopy.Status = StatusInterrupted
		runCopy.EndedAt = &now
		interrupted = append(interrupted, runCopy)
		_ = s.AppendEvent(ctx, Event{
			RunID:      run.ID,
			Phase:      run.Phase,
			Action:     "startup_recovery",
			Status:     string(StatusInterrupted),
			Error:      "unfinished run marked interrupted",
			Checkpoint: true,
		})
	}
	_ = os.Remove(s.lockPath)
	return interrupted, nil
}

func (s *Store) AcquireLock(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockFile != nil {
		return fmt.Errorf("另一个维护任务正在运行")
	}
	file, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("另一个维护任务正在运行或上次运行未清理锁文件: %s", s.lockPath)
		}
		return err
	}
	if _, err := file.WriteString(runID + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(s.lockPath)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(s.lockPath)
		return err
	}
	s.lockFile = file
	return nil
}

func (s *Store) ReleaseLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.lockFile != nil {
		err = s.lockFile.Close()
		s.lockFile = nil
	}
	if removeErr := os.Remove(s.lockPath); removeErr != nil && !os.IsNotExist(removeErr) && err == nil {
		err = removeErr
	}
	return err
}

func (s *Store) runPath(id string) string {
	return filepath.Join(s.dir, safeFileName(id)+".json")
}

func (s *Store) journalPath(id string) string {
	return filepath.Join(s.dir, safeFileName(id)+".journal.jsonl")
}

func (s *Store) getUnlocked(id string) (*Run, error) {
	data, err := os.ReadFile(s.runPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) writeRun(ctx context.Context, run Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeRunUnlocked(run)
}

func (s *Store) writeRunUnlocked(run Run) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".run-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write([]byte("\n")); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.runPath(run.ID))
}

func safeFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	return replacer.Replace(value)
}
