package runstate

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStoreMarksUnfinishedRunsInterrupted(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	ctx := context.Background()
	if err := store.Create(ctx, Run{ID: "run-1", Status: StatusRunning, Mode: "full", ScanPath: "/media"}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	interrupted, err := store.MarkUnfinishedInterrupted(ctx)
	if err != nil {
		t.Fatalf("MarkUnfinishedInterrupted returned error: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != "run-1" {
		t.Fatalf("unexpected interrupted runs: %+v", interrupted)
	}
	run, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if run.Status != StatusInterrupted || run.EndedAt == nil {
		t.Fatalf("run was not marked interrupted: %+v", run)
	}
}

func TestStoreMarksStoppingAndPausingRunsInterrupted(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	ctx := context.Background()
	for _, run := range []Run{
		{ID: "stopping", Status: StatusStopping},
		{ID: "pausing", Status: StatusPausing},
		{ID: "paused", Status: StatusPaused},
	} {
		if err := store.Create(ctx, run); err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
	}

	interrupted, err := store.MarkUnfinishedInterrupted(ctx)
	if err != nil {
		t.Fatalf("MarkUnfinishedInterrupted returned error: %v", err)
	}
	if len(interrupted) != 2 {
		t.Fatalf("expected two interrupted runs, got %+v", interrupted)
	}
	for _, id := range []string{"stopping", "pausing"} {
		run, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if run.Status != StatusInterrupted {
			t.Fatalf("expected %s interrupted, got %+v", id, run)
		}
	}
	run, err := store.Get(ctx, "paused")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if run.Status != StatusPaused {
		t.Fatalf("paused terminal run should be preserved, got %+v", run)
	}
}

func TestStoreLockRejectsConcurrentMaintenanceTask(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if err := store.AcquireLock("run-1"); err != nil {
		t.Fatalf("AcquireLock returned error: %v", err)
	}
	if err := store.AcquireLock("run-2"); err == nil {
		t.Fatal("expected second AcquireLock to fail")
	}
	if err := store.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock returned error: %v", err)
	}
	if err := store.AcquireLock("run-2"); err != nil {
		t.Fatalf("AcquireLock after release returned error: %v", err)
	}
}

func TestStoreAppendsJournalEvents(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	ctx := context.Background()
	if err := store.Create(ctx, Run{ID: "run-1", Status: StatusPending}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.AppendEvent(ctx, Event{RunID: "run-1", Action: "file_before", Source: "a.jpg"}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	events, err := store.Journal(ctx, "run-1")
	if err != nil {
		t.Fatalf("Journal returned error: %v", err)
	}
	if len(events) != 1 || events[0].Action != "file_before" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestStoreReadsLargeJournalEvents(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	ctx := context.Background()
	if err := store.Create(ctx, Run{ID: "run-1", Status: StatusPending}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	largePath := strings.Repeat("nested-directory/", 5000) + "file.jpg"
	if err := store.AppendEvent(ctx, Event{RunID: "run-1", Action: "file_after_classify", Source: largePath}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}

	events, err := store.Journal(ctx, "run-1")
	if err != nil {
		t.Fatalf("Journal returned error: %v", err)
	}
	if len(events) != 1 || events[0].Source != largePath {
		t.Fatalf("unexpected large journal event: len=%d event=%+v", len(events), events)
	}
}

func TestStorePrunesOldTerminalRuns(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	ctx := context.Background()
	base := time.Now().Add(-5 * time.Hour)
	for i, id := range []string{"old-1", "old-2", "keep-1", "running"} {
		status := StatusCompleted
		if id == "running" {
			status = StatusRunning
		}
		if err := store.Create(ctx, Run{
			ID:        id,
			Status:    status,
			StartedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Create %s returned error: %v", id, err)
		}
		if err := store.AppendEvent(ctx, Event{RunID: id, Action: "checkpoint"}); err != nil {
			t.Fatalf("AppendEvent %s returned error: %v", id, err)
		}
	}

	removed, err := store.Prune(ctx, 1, 0)
	if err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed runs, got %d", removed)
	}
	for _, id := range []string{"old-1", "old-2"} {
		run, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s returned error: %v", id, err)
		}
		if run != nil {
			t.Fatalf("expected %s pruned, got %+v", id, run)
		}
		events, err := store.Journal(ctx, id)
		if err != nil {
			t.Fatalf("Journal %s returned error: %v", id, err)
		}
		if len(events) != 0 {
			t.Fatalf("expected %s journal pruned, got %+v", id, events)
		}
	}
	for _, id := range []string{"keep-1", "running"} {
		run, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s returned error: %v", id, err)
		}
		if run == nil {
			t.Fatalf("expected %s retained", id)
		}
	}
}
