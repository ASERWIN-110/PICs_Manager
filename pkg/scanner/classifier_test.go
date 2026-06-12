package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/pkg/hasher"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifierMovesNameConflictToSameNameDirectoryWhenHashDiffers(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	destDir := filepath.Join(root, "staging")
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(destDir, "Series"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	existingTarget := filepath.Join(destDir, "Series", "Series_1.jpg")
	if err := os.WriteFile(existingTarget, []byte("existing"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	sourceFile := filepath.Join(sourceDir, "Series_1.jpg")
	if err := os.WriteFile(sourceFile, []byte("new"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	sourceHash, err := hasher.CalculateSHA256(sourceFile)
	if err != nil {
		t.Fatalf("CalculateSHA256 returned error: %v", err)
	}

	scannerCfg := config.ScannerConfig{FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`}}
	classifier, err := NewClassifier(logDir, destDir, scannerCfg)
	if err != nil {
		t.Fatalf("NewClassifier returned error: %v", err)
	}
	defer classifier.Close()

	seriesNames, fileNames, err := classifier.ClassifyAndMove([]string{sourceFile})
	if err != nil {
		t.Fatalf("ClassifyAndMove returned error: %v", err)
	}
	if got, want := len(seriesNames), 0; got != want {
		t.Fatalf("expected %d series, got %d", want, got)
	}
	if got, want := len(fileNames), 0; got != want {
		t.Fatalf("expected %d file, got %d", want, got)
	}
	if _, err := os.Stat(existingTarget); err != nil {
		t.Fatalf("existing target missing: %v", err)
	}
	expectedSameNamePath := filepath.Join(destDir, "Series", sameNameDirName, "Series_1", sourceHash, "Series_1.jpg")
	if _, err := os.Stat(expectedSameNamePath); err != nil {
		t.Fatal("expected different-hash same-name file under .same-name")
	}
}

func TestClassifierDeletesNameConflictWhenHashMatches(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	destDir := filepath.Join(root, "staging")
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(destDir, "Series"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	existingTarget := filepath.Join(destDir, "Series", "Series_1.jpg")
	sourceFile := filepath.Join(sourceDir, "Series_1.jpg")
	if err := os.WriteFile(existingTarget, []byte("same"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(sourceFile, []byte("same"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	scannerCfg := config.ScannerConfig{FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`}}
	classifier, err := NewClassifier(logDir, destDir, scannerCfg)
	if err != nil {
		t.Fatalf("NewClassifier returned error: %v", err)
	}
	defer classifier.Close()

	_, fileNames, err := classifier.ClassifyAndMove([]string{sourceFile})
	if err != nil {
		t.Fatalf("ClassifyAndMove returned error: %v", err)
	}
	if len(fileNames) != 0 {
		t.Fatalf("expected duplicate not to be processed for DB sync, got %d", len(fileNames))
	}
	if _, err := os.Stat(existingTarget); err != nil {
		t.Fatalf("existing target missing: %v", err)
	}
	if _, err := os.Stat(sourceFile); !os.IsNotExist(err) {
		t.Fatalf("expected duplicate source to be deleted, err=%v", err)
	}
}

func TestClassifierReturnsErrorForUnmatchedInput(t *testing.T) {
	root := t.TempDir()
	sourceFile := filepath.Join(root, "unmatched.jpg")
	if err := os.WriteFile(sourceFile, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	scannerCfg := config.ScannerConfig{FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`}}
	classifier, err := NewClassifier(logDir, filepath.Join(root, "staging"), scannerCfg)
	if err != nil {
		t.Fatalf("NewClassifier returned error: %v", err)
	}
	defer classifier.Close()

	_, fileNames, err := classifier.ClassifyAndMove([]string{sourceFile})
	if err == nil {
		t.Fatal("expected classification error")
	}
	if len(fileNames) != 0 {
		t.Fatalf("expected no processed files, got %d", len(fileNames))
	}
}

func TestClassifierRejectsEmptySanitizedSeriesName(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	destDir := filepath.Join(root, "staging")
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	sourceFile := filepath.Join(sourceDir, "<>_1.jpg")
	if err := os.WriteFile(sourceFile, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	scannerCfg := config.ScannerConfig{FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`}}
	classifier, err := NewClassifier(logDir, destDir, scannerCfg)
	if err != nil {
		t.Fatalf("NewClassifier returned error: %v", err)
	}
	defer classifier.Close()

	seriesNames, fileNames, err := classifier.ClassifyAndMove([]string{sourceFile})
	if err == nil {
		t.Fatal("expected empty sanitized series name error")
	}
	if got, want := len(seriesNames), 0; got != want {
		t.Fatalf("expected %d series, got %d", want, got)
	}
	if got, want := len(fileNames), 0; got != want {
		t.Fatalf("expected %d processed files, got %d", want, got)
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Fatalf("expected source file to remain in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "<>_1.jpg")); !os.IsNotExist(err) {
		t.Fatalf("expected no file in staging root, err=%v", err)
	}
}

func TestClassifierUsesIndependentMediaTypePatterns(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	sourceDir := filepath.Join(root, "source")
	destDir := filepath.Join(root, "staging")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	sourceFile := filepath.Join(sourceDir, "Movie-001.mp4")
	if err := os.WriteFile(sourceFile, []byte("video"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	scannerCfg := config.ScannerConfig{
		FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		MediaTypes: []config.MediaTypeConfig{
			{
				Type:         "video",
				Extensions:   []string{".mp4"},
				FilePatterns: []string{`^(.*?)-(\d+)(\.[a-zA-Z0-9_]+)?$`},
			},
		},
	}
	classifier, err := NewClassifier(logDir, destDir, scannerCfg)
	if err != nil {
		t.Fatalf("NewClassifier returned error: %v", err)
	}
	defer classifier.Close()

	seriesNames, fileNames, err := classifier.ClassifyAndMove([]string{sourceFile})
	if err != nil {
		t.Fatalf("ClassifyAndMove returned error: %v", err)
	}
	if got, want := seriesNames[0], "Movie"; got != want {
		t.Fatalf("expected series %q, got %q", want, got)
	}
	if got, want := fileNames[0], "Movie-001.mp4"; got != want {
		t.Fatalf("expected file %q, got %q", want, got)
	}
}

func TestClassifierDoesNotNormalizeSuffixLikeFileNames(t *testing.T) {
	root := t.TempDir()
	sourceFile := filepath.Join(root, "Series_1-1.jpg")
	if err := os.WriteFile(sourceFile, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	scannerCfg := config.ScannerConfig{FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`}}
	classifier, err := NewClassifier(logDir, filepath.Join(root, "staging"), scannerCfg)
	if err != nil {
		t.Fatalf("NewClassifier returned error: %v", err)
	}
	defer classifier.Close()

	_, _, err = classifier.ClassifyAndMove([]string{sourceFile})
	if err == nil {
		t.Fatal("expected suffix-like file name not to be normalized into a match")
	}
}

func hasSameNameFile(t *testing.T, seriesDir, fileName string) bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(seriesDir, sameNameDirName, sameNameBucket(fileName), "*", fileName))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	return len(matches) > 0
}
