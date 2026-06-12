package maintenance

import (
	"PICs_Manager/pkg/hasher"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateFileManifestUsesLibraryRelativePathsAndSortedOutput(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	output := filepath.Join(root, "out")
	logDir := filepath.Join(root, "logs")

	writeFile(t, filepath.Join(library, "b", "two.txt"), "two")
	writeFile(t, filepath.Join(library, "a", "one.txt"), "one")

	m, err := NewMaintenance(logDir, 2)
	if err != nil {
		t.Fatalf("NewMaintenance returned error: %v", err)
	}
	defer closeMaintenanceForTest(t, m)

	if err := m.GenerateFileManifest(context.Background(), library, output); err != nil {
		t.Fatalf("GenerateFileManifest returned error: %v", err)
	}

	manifestPath := findSingleManifest(t, output)
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 manifest lines, got %d: %q", len(lines), content)
	}

	oneHash := hashFile(t, filepath.Join(library, "a", "one.txt"))
	twoHash := hashFile(t, filepath.Join(library, "b", "two.txt"))
	if lines[0] != oneHash+" *a/one.txt" {
		t.Fatalf("unexpected first line: %q", lines[0])
	}
	if lines[1] != twoHash+" *b/two.txt" {
		t.Fatalf("unexpected second line: %q", lines[1])
	}
}

func TestGenerateFileManifestReturnsContextCancellation(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	writeFile(t, filepath.Join(library, "a.txt"), "data")

	m, err := NewMaintenance(filepath.Join(root, "logs"), 1)
	if err != nil {
		t.Fatalf("NewMaintenance returned error: %v", err)
	}
	defer closeMaintenanceForTest(t, m)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(root, "out")
	err = m.GenerateFileManifest(ctx, library, output)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(output, "manifest_*.txt"))
	if globErr != nil {
		t.Fatalf("Glob returned error: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("expected cancellation not to leave manifest files, got %v", matches)
	}
}

func TestGenerateFileManifestDoesNotDeadlockWhenManyFilesFailHashing(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	output := filepath.Join(root, "out")
	if err := os.MkdirAll(library, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	for i := 0; i < 4; i++ {
		linkPath := filepath.Join(library, fmt.Sprintf("missing-%d.bin", i))
		if err := os.Symlink(filepath.Join(library, "does-not-exist"), linkPath); err != nil {
			if errors.Is(err, os.ErrPermission) {
				t.Skipf("symlink not permitted: %v", err)
			}
			t.Fatalf("Symlink returned error: %v", err)
		}
	}
	m, err := NewMaintenance(filepath.Join(root, "logs"), 1)
	if err != nil {
		t.Fatalf("NewMaintenance returned error: %v", err)
	}
	defer closeMaintenanceForTest(t, m)

	done := make(chan error, 1)
	go func() {
		done <- m.GenerateFileManifest(context.Background(), library, output)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected hashing errors")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GenerateFileManifest deadlocked while reporting hash errors")
	}

	matches, globErr := filepath.Glob(filepath.Join(output, "manifest_*.txt"))
	if globErr != nil {
		t.Fatalf("Glob returned error: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("expected failed manifest generation not to write manifest files, got %v", matches)
	}
}

func TestBackupDatabaseCreatesOutputDirectory(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	fakeMongodump := filepath.Join(fakeBin, "mongodump")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(fakeMongodump, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", fakeBin, os.PathListSeparator, os.Getenv("PATH")))

	output := filepath.Join(root, "backup", "nested")
	m, err := NewMaintenance(filepath.Join(root, "logs"), 1)
	if err != nil {
		t.Fatalf("NewMaintenance returned error: %v", err)
	}
	defer closeMaintenanceForTest(t, m)

	if err := m.BackupDatabase(context.Background(), "mongodb://example.invalid:27017", "pics", output); err != nil {
		t.Fatalf("BackupDatabase returned error: %v", err)
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatalf("expected output directory to exist, info=%v err=%v", info, err)
	}
}

func TestBackupDatabasePassesCompleteMongoURIToMongodump(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	fakeMongodump := filepath.Join(fakeBin, "mongodump")
	argsPath := filepath.Join(root, "mongodump.args")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MONGODUMP_ARGS_FILE\"\nexit 0\n"
	if err := os.WriteFile(fakeMongodump, []byte(script), 0755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", fakeBin, os.PathListSeparator, os.Getenv("PATH")))
	t.Setenv("MONGODUMP_ARGS_FILE", argsPath)

	m, err := NewMaintenance(filepath.Join(root, "logs"), 1)
	if err != nil {
		t.Fatalf("NewMaintenance returned error: %v", err)
	}
	defer closeMaintenanceForTest(t, m)

	uri := "mongodb://dev_user:secret@127.0.0.1:27017/?authSource=admin"
	if err := m.BackupDatabase(context.Background(), uri, "pics_verify", filepath.Join(root, "backup")); err != nil {
		t.Fatalf("BackupDatabase returned error: %v", err)
	}

	content, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(content)), "\n")
	assertArgPair(t, args, "--uri", uri)
	assertArgPair(t, args, "--db", "pics_verify")
	if !containsArg(args, "--gzip") {
		t.Fatalf("expected --gzip in mongodump args: %v", args)
	}
	if !containsArgPrefix(args, "--archive="+filepath.Join(root, "backup", "db_backup_")) {
		t.Fatalf("expected --archive under backup dir, got args: %v", args)
	}
}

func TestBackupDatabaseFallsBackToNativeExporterWhenMongodumpMissing(t *testing.T) {
	root := t.TempDir()
	originalLookPath := execLookPath
	execLookPath = func(file string) (string, error) {
		if file != "mongodump" {
			t.Fatalf("unexpected executable lookup: %s", file)
		}
		return "", os.ErrNotExist
	}
	defer func() {
		execLookPath = originalLookPath
	}()

	m, err := NewMaintenance(filepath.Join(root, "logs"), 1)
	if err != nil {
		t.Fatalf("NewMaintenance returned error: %v", err)
	}
	defer closeMaintenanceForTest(t, m)

	err = m.BackupDatabase(context.Background(), "mongodb://localhost:27017", "", filepath.Join(root, "backup"))
	if err == nil {
		t.Fatal("expected fallback validation error")
	}
	if !strings.Contains(err.Error(), "数据库名不能为空") {
		t.Fatalf("expected native fallback validation error, got %v", err)
	}
	if strings.Contains(err.Error(), "mongodump") {
		t.Fatalf("expected fallback instead of mongodump missing error, got %v", err)
	}
}

func TestNativeCollectionTempPatternUsesSafeUniqueName(t *testing.T) {
	got := nativeCollectionTempPattern("bad/name\\collection")
	want := ".backup-bad_name_collection-*.jsonl"
	if got != want {
		t.Fatalf("nativeCollectionTempPattern returned %q, want %q", got, want)
	}
}

func TestWriteManifestFileReplacesContentAndCleansTempFile(t *testing.T) {
	output := t.TempDir()
	manifestName := fmt.Sprintf("manifest_%s.txt", time.Now().Format("2006-01-02"))
	manifestPath := filepath.Join(output, manifestName)
	if err := os.WriteFile(manifestPath, []byte("old"), 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	gotPath, err := writeManifestFile(output, []manifestEntry{{RelPath: "a.txt", Hash: "abc"}})
	if err != nil {
		t.Fatalf("writeManifestFile returned error: %v", err)
	}

	if filepath.Base(gotPath) != manifestName {
		t.Fatalf("unexpected manifest path: %s", gotPath)
	}
	content, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "abc *a.txt\n" {
		t.Fatalf("unexpected manifest content: %q", content)
	}
	leftovers, err := filepath.Glob(filepath.Join(output, ".manifest-*.tmp"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("expected no temp files, got %v", leftovers)
	}
}

func closeMaintenanceForTest(t *testing.T, m Maintenance) {
	t.Helper()
	if err := m.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}

func findSingleManifest(t *testing.T, output string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(output, "manifest_*.txt"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one manifest, got %d: %v", len(matches), matches)
	}
	return matches[0]
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	hash, err := hasher.CalculateSHA256(path)
	if err != nil {
		t.Fatalf("CalculateSHA256 returned error: %v", err)
	}
	return hash
}

func assertArgPair(t *testing.T, args []string, key, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return
		}
	}
	t.Fatalf("expected arg pair %s %s, got %v", key, value, args)
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsArgPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}
