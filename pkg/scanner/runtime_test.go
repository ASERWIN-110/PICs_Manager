package scanner

import (
	"PICs_Manager/config"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureMaintenanceWindowHandlesOvernightWindow(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 12, 1, 30, 0, 0, time.Local) }
	if err := ensureMaintenanceWindow("23:00-02:00", now); err != nil {
		t.Fatalf("expected time to be in overnight window: %v", err)
	}
}

func TestEnsureMaintenanceWindowRejectsOutsideWindow(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.Local) }
	if err := ensureMaintenanceWindow("23:00-02:00", now); err == nil {
		t.Fatal("expected outside window error")
	}
}

func TestDefaultWorkerCountIsConservative(t *testing.T) {
	got := defaultWorkerCount(0)
	if got < 1 || got > 4 {
		t.Fatalf("expected default worker count in [1,4], got %d", got)
	}
	if got := defaultWorkerCount(8); got != 8 {
		t.Fatalf("explicit worker count should be preserved, got %d", got)
	}
}

func TestCountUnsupportedFilesUsesConfiguredMediaExtensions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Series_1.png"), []byte("media"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.bin"), []byte("unsupported"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("ignored"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	count, err := CountUnsupportedFiles(context.Background(), root, config.ScannerConfig{
		FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		MediaTypes: []config.MediaTypeConfig{
			{
				Type:         "image",
				Extensions:   []string{".png"},
				FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
			},
		},
	})
	if err != nil {
		t.Fatalf("CountUnsupportedFiles returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one unsupported file, got %d", count)
	}
}
