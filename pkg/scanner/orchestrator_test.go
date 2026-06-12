package scanner

import (
	"PICs_Manager/config"
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuarantineCorruptedImagesKeepsHealthyAndMovesDamaged(t *testing.T) {
	root := t.TempDir()
	healthyPath := filepath.Join(root, "Series_1.png")
	corruptedPath := filepath.Join(root, "Series_2.png")
	quarantineDir := filepath.Join(root, "quarantine")

	writeTestPNG(t, healthyPath)
	if err := os.WriteFile(corruptedPath, []byte("not an image"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	healthy, corruptedCount, err := quarantineCorruptedImages(context.Background(), []string{healthyPath, corruptedPath}, quarantineDir, 2)
	if err != nil {
		t.Fatalf("quarantineCorruptedImages returned error: %v", err)
	}
	if corruptedCount != 1 {
		t.Fatalf("expected 1 corrupted image, got %d", corruptedCount)
	}
	if len(healthy) != 1 || healthy[0] != healthyPath {
		t.Fatalf("unexpected healthy files: %v", healthy)
	}
	if _, err := os.Stat(filepath.Join(quarantineDir, "corrupted", "Series_2.png")); err != nil {
		t.Fatalf("corrupted image was not quarantined: %v", err)
	}
}

func TestQuarantineCorruptedImagesReturnsCanceledContext(t *testing.T) {
	root := t.TempDir()
	corruptedPath := filepath.Join(root, "Series_1.png")
	quarantineDir := filepath.Join(root, "quarantine")
	if err := os.WriteFile(corruptedPath, []byte("not an image"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	healthy, corruptedCount, err := quarantineCorruptedImages(ctx, []string{corruptedPath}, quarantineDir, 1)

	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(healthy) != 0 || corruptedCount != 0 {
		t.Fatalf("expected no processed files after cancellation, healthy=%v corrupted=%d", healthy, corruptedCount)
	}
	if _, err := os.Stat(corruptedPath); err != nil {
		t.Fatalf("expected original file to remain after cancellation: %v", err)
	}
}

func TestNewOrchestratorRejectsMissingConfig(t *testing.T) {
	if _, err := NewOrchestrator(nil, nil); err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestFullModeRequiresDatabaseStore(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Logger: config.LoggerConfig{Path: filepath.Join(root, "logs")},
		Scanner: config.ScannerConfig{
			Mode:             "full",
			ScanPath:         filepath.Join(root, "scan"),
			StagingPath:      filepath.Join(root, "staging"),
			FinalLibraryPath: filepath.Join(root, "library"),
			BackupPath:       filepath.Join(root, "backup"),
			QuarantinePath:   filepath.Join(root, "quarantine"),
			FilePatterns:     []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	}

	orchestrator, err := NewOrchestrator(&cfg, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator returned error: %v", err)
	}
	err = orchestrator.RunFullScanContext(context.Background(), cfg.Scanner)
	if err == nil {
		t.Fatal("expected full mode to reject missing database store")
	}
	if !strings.Contains(err.Error(), "full 模式需要数据库存储") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOrchestratorHandlesCorruptedNonMediaNameConflictAndSeriesAggregation(t *testing.T) {
	root := t.TempDir()
	scanDir := filepath.Join(root, "scan")
	cfg := config.Config{
		Logger: config.LoggerConfig{Path: filepath.Join(root, "logs")},
		Scanner: config.ScannerConfig{
			Mode:             "classifyOnly",
			ScanPath:         scanDir,
			StagingPath:      filepath.Join(root, "staging"),
			FinalLibraryPath: filepath.Join(root, "library"),
			BackupPath:       filepath.Join(root, "backup"),
			QuarantinePath:   filepath.Join(root, "quarantine"),
			WorkerCount:      8,
			BatchSize:        10,
			FilePatterns:     []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
			SeriesGroupRules: []config.SeriesGroupRule{
				{Name: "文本+数字", Pattern: `^(?P<group>.+?)\s*(\d+)$`},
			},
			MediaTypes: []config.MediaTypeConfig{
				{
					Type:         "image",
					Extensions:   []string{".png"},
					FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
				},
			},
		},
	}

	for _, dir := range []string{
		filepath.Join(scanDir, "a"),
		filepath.Join(scanDir, "b"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("MkdirAll returned error: %v", err)
		}
	}

	writeTestPNGWithColor(t, filepath.Join(scanDir, "a", "Dup_1.png"), color.RGBA{R: 255, A: 255})
	writeTestPNGWithColor(t, filepath.Join(scanDir, "b", "Dup_1.png"), color.RGBA{G: 255, A: 255})
	writeTestPNG(t, filepath.Join(scanDir, "Solo_1.png"))
	writeTestPNG(t, filepath.Join(scanDir, "Solo_2.png"))
	writeTestPNG(t, filepath.Join(scanDir, "Comic 1_1.png"))
	writeTestPNG(t, filepath.Join(scanDir, "Comic 2_1.png"))
	if err := os.WriteFile(filepath.Join(scanDir, "Broken_1.png"), []byte("not an image"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	nonMediaPath := filepath.Join(scanDir, "note.bin")
	if err := os.WriteFile(nonMediaPath, []byte("not configured media"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	orchestrator, err := NewOrchestrator(&cfg, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator returned error: %v", err)
	}
	if err := orchestrator.RunFullScanContext(context.Background(), cfg.Scanner); err != nil {
		t.Fatalf("RunFullScanContext returned error: %v", err)
	}

	assertExists(t, filepath.Join(root, "quarantine", "corrupted", "Broken_1.png"))
	assertExists(t, nonMediaPath)

	assertExists(t, filepath.Join(root, "library", "D", "Dup", "Dup_1.png"))
	if !hasSameNameFile(t, filepath.Join(root, "library", "D", "Dup"), "Dup_1.png") {
		t.Fatal("expected different-hash duplicate under .same-name")
	}

	assertExists(t, filepath.Join(root, "library", "S", "Solo", "Solo_1.png"))
	assertExists(t, filepath.Join(root, "library", "S", "Solo", "Solo_2.png"))

	assertExists(t, filepath.Join(root, "library", "C", "Comic_agg", "Comic 1", "Comic 1_1.png"))
	assertExists(t, filepath.Join(root, "library", "C", "Comic_agg", "Comic 2", "Comic 2_1.png"))
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path to exist: %s: %v", path, err)
	}
}
