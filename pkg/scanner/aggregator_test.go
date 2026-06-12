package scanner

import (
	"PICs_Manager/pkg/runstate"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMergeDirectoryContentsMovesNameConflictToSameNameDirectoryWhenHashDiffers(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	destDir := filepath.Join(root, "library", "Series")
	quarantineDir := filepath.Join(root, "quarantine")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	destFile := filepath.Join(destDir, "Series_1.jpg")
	srcFile := filepath.Join(srcDir, "Series_1.jpg")
	if err := os.WriteFile(destFile, []byte("existing"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(srcFile, []byte("new"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := (&configBasedAggregator{}).mergeDirectoryContents(srcDir, destDir, quarantineDir); err != nil {
		t.Fatalf("mergeDirectoryContents returned error: %v", err)
	}

	if got, err := os.ReadFile(destFile); err != nil || string(got) != "existing" {
		t.Fatalf("existing destination changed: content=%q err=%v", got, err)
	}
	sameNameFile := findSameNameFile(t, destDir, "Series_1.jpg")
	if got, err := os.ReadFile(sameNameFile); err != nil || string(got) != "new" {
		t.Fatalf("same-name file missing or changed: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("expected source directory to be removed after merge, err=%v", err)
	}
}

func TestAggregatorRecordsMergeJournalEvents(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	destDir := filepath.Join(root, "library", "Series")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "Series_1.jpg"), []byte("existing"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Series_1.jpg"), []byte("new"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	store, err := runstate.NewStore(root)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if err := store.Create(context.Background(), runstate.Run{ID: "run-1"}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	aggregator := &configBasedAggregator{recorder: runstate.Recorder{Store: store, RunID: "run-1"}}

	if err := aggregator.mergeDirectoryContents(srcDir, destDir, filepath.Join(root, "quarantine")); err != nil {
		t.Fatalf("mergeDirectoryContents returned error: %v", err)
	}
	events, err := store.Journal(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("Journal returned error: %v", err)
	}
	if !hasJournalAction(events, "file_before_merge") || !hasJournalAction(events, "file_after_merge") {
		t.Fatalf("expected merge journal events, got %+v", events)
	}
}

func TestMergeDirectoryContentsDeletesNameConflictWhenHashMatches(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	destDir := filepath.Join(root, "library", "Series")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	destFile := filepath.Join(destDir, "Series_1.jpg")
	srcFile := filepath.Join(srcDir, "Series_1.jpg")
	if err := os.WriteFile(destFile, []byte("same"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(srcFile, []byte("same"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := (&configBasedAggregator{}).mergeDirectoryContents(srcDir, destDir, filepath.Join(root, "quarantine")); err != nil {
		t.Fatalf("mergeDirectoryContents returned error: %v", err)
	}
	if got, err := os.ReadFile(destFile); err != nil || string(got) != "same" {
		t.Fatalf("existing destination changed: content=%q err=%v", got, err)
	}
	if hasSameNameFile(t, destDir, "Series_1.jpg") {
		t.Fatal("expected no same-name file for same hash duplicate")
	}
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("expected source directory to be removed after merge, err=%v", err)
	}
}

func TestAggregatePhase3ReturnsReadDirError(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	aggregator, err := NewAggregatorWithMediaRoots(logDir, nil, 1, nil)
	if err != nil {
		t.Fatalf("NewAggregator returned error: %v", err)
	}
	defer aggregator.Close()

	impl := aggregator.(*configBasedAggregator)
	_, _, err = impl.phase3_aggregateWithinArchiveFolders(filepath.Join(root, "missing"), filepath.Join(root, "quarantine"))
	if err == nil {
		t.Fatal("expected ReadDir error")
	}
	if !strings.Contains(err.Error(), "无法读取最终库归档目录") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAggregationWorkerReportsReadDirError(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	aggregator, err := NewAggregatorWithMediaRoots(logDir, nil, 1, nil)
	if err != nil {
		t.Fatalf("NewAggregator returned error: %v", err)
	}
	defer aggregator.Close()

	impl := aggregator.(*configBasedAggregator)
	tasks := make(chan string, 1)
	errs := make(chan error, 1)
	movedSet := make(map[string]string)
	unMovedSet := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go impl.aggregationWorker(&wg, tasks, filepath.Join(root, "quarantine"), movedSet, unMovedSet, &mu, errs)
	tasks <- filepath.Join(root, "missing-archive")
	close(tasks)
	wg.Wait()
	close(errs)

	err, ok := <-errs
	if !ok {
		t.Fatal("expected worker error")
	}
	if !strings.Contains(err.Error(), "无法读取归档目录") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestArchiveStagingFoldersRejectsUnexpectedFiles(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	stagingDir := filepath.Join(root, "staging")
	finalDir := filepath.Join(root, "library")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "loose.txt"), []byte("leftover"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	aggregator, err := NewAggregatorWithMediaRoots(logDir, nil, 1, nil)
	if err != nil {
		t.Fatalf("NewAggregator returned error: %v", err)
	}
	defer aggregator.Close()

	impl := aggregator.(*configBasedAggregator)
	_, _, err = impl.phase2_archiveStagingFolders(stagingDir, finalDir, filepath.Join(root, "quarantine"))
	if err == nil {
		t.Fatal("expected unexpected staging file error")
	}
	if !strings.Contains(err.Error(), "中转站包含非目录条目") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestArchiveStagingFoldersIgnoresSystemFiles(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	stagingDir := filepath.Join(root, "staging")
	finalDir := filepath.Join(root, "library")
	seriesDir := filepath.Join(stagingDir, "Series")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, ".DS_Store"), []byte("system"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(finalDir, "S"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	aggregator, err := NewAggregatorWithMediaRoots(logDir, nil, 1, nil)
	if err != nil {
		t.Fatalf("NewAggregator returned error: %v", err)
	}
	defer aggregator.Close()

	impl := aggregator.(*configBasedAggregator)
	moved, _, err := impl.phase2_archiveStagingFolders(stagingDir, finalDir, filepath.Join(root, "quarantine"))
	if err != nil {
		t.Fatalf("phase2_archiveStagingFolders returned error: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("expected one moved directory, got %d", len(moved))
	}
	if _, err := os.Stat(filepath.Join(finalDir, "S", "Series")); err != nil {
		t.Fatalf("expected series directory archived: %v", err)
	}
}

func findSameNameFile(t *testing.T, seriesDir, fileName string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(seriesDir, sameNameDirName, sameNameBucket(fileName), "*", fileName))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one same-name file, got %d: %v", len(matches), matches)
	}
	return matches[0]
}
