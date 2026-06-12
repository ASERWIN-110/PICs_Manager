package scanner

import (
	"encoding/base64"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

const tinyWebPBase64 = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"

func TestPreprocessorDeletesNumberedCopyWhenHashMatches(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	basePath := filepath.Join(sourceDir, "Series_1.png")
	copyPath := filepath.Join(sourceDir, "Series_1 (1).png")
	writeTestPNG(t, basePath)
	writeTestPNG(t, copyPath)

	preprocessor, err := NewPreprocessor(logDir, 1)
	if err != nil {
		t.Fatalf("NewPreprocessor returned error: %v", err)
	}
	defer preprocessor.Close()

	files, err := preprocessor.ProcessDirectory(sourceDir)
	if err != nil {
		t.Fatalf("ProcessDirectory returned error: %v", err)
	}
	if got, want := len(files), 1; got != want {
		t.Fatalf("expected %d files, got %d", want, got)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected base file to remain: %v", err)
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("expected same-hash numbered copy to be deleted, err=%v", err)
	}
}

func TestPreprocessorMovesNumberedCopyToSameNameDirectoryWhenHashDiffers(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	basePath := filepath.Join(sourceDir, "Series_1.png")
	copyPath := filepath.Join(sourceDir, "Series_1 (1).png")
	writeTestPNGWithColor(t, basePath, color.RGBA{R: 255, A: 255})
	writeTestPNGWithColor(t, copyPath, color.RGBA{G: 255, A: 255})

	preprocessor, err := NewPreprocessor(logDir, 1)
	if err != nil {
		t.Fatalf("NewPreprocessor returned error: %v", err)
	}
	defer preprocessor.Close()

	files, err := preprocessor.ProcessDirectory(sourceDir)
	if err != nil {
		t.Fatalf("ProcessDirectory returned error: %v", err)
	}
	if got, want := len(files), 2; got != want {
		t.Fatalf("expected %d files, got %d", want, got)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected base file to remain: %v", err)
	}
	if !hasSameNameFile(t, sourceDir, "Series_1.png") {
		t.Fatal("expected different-hash numbered copy to be moved into same-name directory")
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("expected original numbered copy path to be moved, err=%v", err)
	}
}

func TestPreprocessorRepairsDamagedBaseWithNumberedCopyAndKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	basePath := filepath.Join(sourceDir, "Series_1.png")
	copyPath := filepath.Join(sourceDir, "Series_1 (1).png")
	corruptedOriginalPath := filepath.Join(sourceDir, "Series_1_corrupted_original.png")
	if err := os.WriteFile(basePath, []byte("not an image"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	writeTestPNG(t, copyPath)

	preprocessor, err := NewPreprocessor(logDir, 1)
	if err != nil {
		t.Fatalf("NewPreprocessor returned error: %v", err)
	}
	defer preprocessor.Close()

	files, err := preprocessor.ProcessDirectory(sourceDir)
	if err != nil {
		t.Fatalf("ProcessDirectory returned error: %v", err)
	}
	if got, want := len(files), 2; got != want {
		t.Fatalf("expected %d files after repair, got %d", want, got)
	}
	if isImageFileDamaged(basePath) {
		t.Fatal("expected healthy numbered copy to replace damaged base")
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("expected numbered copy to be moved into base path, err=%v", err)
	}
	if !isImageFileDamaged(corruptedOriginalPath) {
		t.Fatal("expected damaged original to remain under a preserved name")
	}
}

func TestImageDamageCheckSupportsWebP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.webp")
	data, err := base64.StdEncoding.DecodeString(tinyWebPBase64)
	if err != nil {
		t.Fatalf("DecodeString returned error: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if !isImageExtension(path) {
		t.Fatal("expected .webp to be treated as an image extension")
	}
	if isImageFileDamaged(path) {
		t.Fatal("expected valid webp image to pass damage check")
	}
}
