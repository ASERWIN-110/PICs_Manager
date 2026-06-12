package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/hasher"
	"context"
	"encoding/json"
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

	assertExists(t, filepath.Join(root, "library", "images", "D", "Dup", "Dup_1.png"))
	if !hasSameNameFile(t, filepath.Join(root, "library", "images", "D", "Dup"), "Dup_1.png") {
		t.Fatal("expected different-hash duplicate under .same-name")
	}

	assertExists(t, filepath.Join(root, "library", "images", "S", "Solo", "Solo_1.png"))
	assertExists(t, filepath.Join(root, "library", "images", "S", "Solo", "Solo_2.png"))

	assertExists(t, filepath.Join(root, "library", "images", "C", "Comic_agg", "Comic 1", "Comic 1_1.png"))
	assertExists(t, filepath.Join(root, "library", "images", "C", "Comic_agg", "Comic 2", "Comic 2_1.png"))

	reportFiles, err := filepath.Glob(filepath.Join(root, "backup", "reports", "*.health.json"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(reportFiles) != 1 {
		t.Fatalf("expected one health report, got %v", reportFiles)
	}
	data, err := os.ReadFile(reportFiles[0])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var report DirectoryHealthReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("health report JSON invalid: %v", err)
	}
	if report.UnsupportedFiles != 1 {
		t.Fatalf("expected one unsupported file in health report, got %+v", report)
	}
}

func TestClassifiedLibraryCanBeReclassifiedIntoSameTree(t *testing.T) {
	root := t.TempDir()
	scanDir := filepath.Join(root, "scan")
	if err := os.MkdirAll(filepath.Join(scanDir, "a"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(scanDir, "b"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(scanDir, "c"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	writeTestPNGWithColor(t, filepath.Join(scanDir, "a", "Dup_1.png"), color.RGBA{R: 255, A: 255})
	writeTestPNGWithColor(t, filepath.Join(scanDir, "b", "Dup_1.png"), color.RGBA{G: 255, A: 255})
	writeTestPNGWithColor(t, filepath.Join(scanDir, "c", "Dup_1.png"), color.RGBA{B: 255, A: 255})
	writeTestPNG(t, filepath.Join(scanDir, "Solo_1.png"))
	writeTestPNG(t, filepath.Join(scanDir, "Comic 1_1.png"))
	writeTestPNG(t, filepath.Join(scanDir, "Comic 2_1.png"))

	cfg := config.Config{
		Logger:  config.LoggerConfig{Path: filepath.Join(root, "logs1")},
		Scanner: scannerTestConfig(scanDir, filepath.Join(root, "staging1"), filepath.Join(root, "library1"), filepath.Join(root, "backup1"), filepath.Join(root, "quarantine1")),
	}
	orchestrator, err := NewOrchestrator(&cfg, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator returned error: %v", err)
	}
	if err := orchestrator.RunFullScanContext(context.Background(), cfg.Scanner); err != nil {
		t.Fatalf("first RunFullScanContext returned error: %v", err)
	}
	firstManifest := libraryHashManifest(t, filepath.Join(root, "library1"))
	assertManifestContains(t, firstManifest, filepath.Join("images", "D", "Dup", "Dup_1.png"))
	assertManifestContains(t, firstManifest, filepath.Join("images", "C", "Comic_agg", "Comic 1", "Comic 1_1.png"))
	assertManifestContains(t, firstManifest, filepath.Join("images", "C", "Comic_agg", "Comic 2", "Comic 2_1.png"))
	if sameNameCount(firstManifest) != 2 {
		t.Fatalf("expected two same-name variants in first manifest, got %d: %v", sameNameCount(firstManifest), firstManifest)
	}

	cfg2 := config.Config{
		Logger:  config.LoggerConfig{Path: filepath.Join(root, "logs2")},
		Scanner: scannerTestConfig(filepath.Join(root, "library1"), filepath.Join(root, "staging2"), filepath.Join(root, "library2"), filepath.Join(root, "backup2"), filepath.Join(root, "quarantine2")),
	}
	orchestrator2, err := NewOrchestrator(&cfg2, nil)
	if err != nil {
		t.Fatalf("second NewOrchestrator returned error: %v", err)
	}
	if err := orchestrator2.RunFullScanContext(context.Background(), cfg2.Scanner); err != nil {
		t.Fatalf("second RunFullScanContext returned error: %v", err)
	}

	secondManifest := libraryHashManifest(t, filepath.Join(root, "library2"))
	if len(firstManifest) != len(secondManifest) {
		t.Fatalf("manifest size differs: first=%v second=%v", firstManifest, secondManifest)
	}
	for rel, hash := range firstManifest {
		if secondManifest[rel] != hash {
			t.Fatalf("reclassified tree changed at %s: first=%s second=%s allSecond=%v", rel, hash, secondManifest[rel], secondManifest)
		}
	}
}

func scannerTestConfig(scan, staging, library, backup, quarantine string) config.ScannerConfig {
	return config.ScannerConfig{
		Mode:             "classifyOnly",
		ScanPath:         scan,
		StagingPath:      staging,
		FinalLibraryPath: library,
		BackupPath:       backup,
		QuarantinePath:   quarantine,
		WorkerCount:      2,
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
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path to exist: %s: %v", path, err)
	}
}

func libraryHashManifest(t *testing.T, root string) map[string]string {
	t.Helper()
	manifest := make(map[string]string)
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := hasher.CalculateSHA256(path)
		if err != nil {
			return err
		}
		manifest[rel] = hash
		return nil
	}); err != nil {
		t.Fatalf("WalkDir returned error: %v", err)
	}
	return manifest
}

func assertManifestContains(t *testing.T, manifest map[string]string, rel string) {
	t.Helper()
	if _, ok := manifest[rel]; !ok {
		t.Fatalf("expected manifest to contain %s, got %v", rel, manifest)
	}
}

func sameNameCount(manifest map[string]string) int {
	count := 0
	for rel := range manifest {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == sameNameDirName {
				count++
				break
			}
		}
	}
	return count
}
