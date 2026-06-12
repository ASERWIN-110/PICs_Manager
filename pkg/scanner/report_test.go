package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeDirectoryHealthCountsSameNameAndWarnings(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	quarantine := filepath.Join(root, "quarantine")
	seriesDir := filepath.Join(library, "A", "Series")
	sameNameDir := filepath.Join(seriesDir, sameNameDirName, "Series_1", "hash")
	if err := os.MkdirAll(sameNameDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seriesDir, "Series_1.jpg"), []byte("a"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seriesDir, "Series_2.jpg"), []byte("b"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sameNameDir, "Series_1.jpg"), []byte("c"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.MkdirAll(quarantine, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(quarantine, "bad.jpg"), []byte("bad"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	report, err := AnalyzeDirectoryHealth(context.Background(), library, quarantine, 1)
	if err != nil {
		t.Fatalf("AnalyzeDirectoryHealth returned error: %v", err)
	}
	if report.SameNameFiles != 1 {
		t.Fatalf("expected one same-name file, got %+v", report)
	}
	if report.QuarantineFiles != 1 {
		t.Fatalf("expected one quarantine file, got %+v", report)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("expected max files warning, got %+v", report)
	}
}

func TestWriteDirectoryHealthReportCreatesJSON(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	if err := os.MkdirAll(library, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	report, err := WriteDirectoryHealthReport(context.Background(), library, filepath.Join(root, "missing"), root, 0, "run-1", 2)
	if err != nil {
		t.Fatalf("WriteDirectoryHealthReport returned error: %v", err)
	}
	if report.RunID != "run-1" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.UnsupportedFiles != 2 {
		t.Fatalf("expected unsupported count in report, got %+v", report)
	}
	reportPath := filepath.Join(root, "reports", "run-1.health.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("report file missing: %v", err)
	}
	var decoded DirectoryHealthReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("report JSON invalid: %v", err)
	}
	if decoded.UnsupportedFiles != 2 {
		t.Fatalf("expected unsupported count in JSON, got %+v", decoded)
	}
}
