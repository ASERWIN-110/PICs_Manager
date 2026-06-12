package e2e_test

import (
	"PICs_Manager/config"
	dbmongo "PICs_Manager/pkg/database/mongo"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type runRecord struct {
	ID           string           `json:"id"`
	Status       string           `json:"status"`
	Mode         string           `json:"mode"`
	Phase        string           `json:"phase"`
	Counts       map[string]int64 `json:"counts"`
	ErrorSummary []string         `json:"errorSummary"`
}

type healthReport struct {
	RunID            string   `json:"runId"`
	Files            int      `json:"files"`
	SameNameFiles    int      `json:"sameNameFiles"`
	QuarantineFiles  int      `json:"quarantineFiles"`
	UnsupportedFiles int      `json:"unsupportedFiles"`
	Warnings         []string `json:"warnings"`
}

type taskStartEnvelope struct {
	Data struct {
		TaskID string `json:"taskId"`
	} `json:"data"`
}

type taskStatusEnvelope struct {
	Data struct {
		ID     string           `json:"id"`
		Status string           `json:"status"`
		Phase  string           `json:"phase"`
		Counts map[string]int64 `json:"counts"`
		Error  string           `json:"error"`
	} `json:"data"`
}

func TestLongMaintenanceCLIEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long maintenance e2e in short mode")
	}
	repo := repoRoot(t)
	root := t.TempDir()
	cli := buildBinary(t, repo, root, "pics-cli", "./cmd/cli")

	dataset := filepath.Join(root, "scan")
	writeDataset(t, dataset)
	writeConfig(t, root, e2eConfig{
		Port:       ":0",
		Token:      "secret",
		Database:   uniqueDBName(t),
		Mode:       "classifyOnly",
		Scan:       dataset,
		Staging:    filepath.Join(root, "staging1"),
		Library:    filepath.Join(root, "library1"),
		Backup:     filepath.Join(root, "backup1"),
		Quarantine: filepath.Join(root, "quarantine1"),
		Logs:       filepath.Join(root, "logs1"),
	})

	runCommand(t, root, cli, "-action", "scan", "-mode", "classifyOnly")
	firstManifest := manifest(t, filepath.Join(root, "library1"))
	assertClassifiedTree(t, firstManifest)
	assertPathExists(t, filepath.Join(root, "quarantine1", "corrupted", "Broken_1.png"))
	assertPathExists(t, filepath.Join(dataset, "note.bin"))

	run := latestRun(t, filepath.Join(root, "logs1", "runs"))
	if run.Status != "completed" || run.Mode != "classifyOnly" {
		t.Fatalf("unexpected run state after scan: %+v", run)
	}
	journal := readFile(t, filepath.Join(root, "logs1", "runs", run.ID+".journal.jsonl"))
	for _, want := range []string{"preprocess_done", "media_filter_done", "classify_done", "archive_done", "health_report_done", "cli_scan_finished"} {
		if !strings.Contains(journal, want) {
			t.Fatalf("journal missing %q:\n%s", want, journal)
		}
	}
	report := latestHealthReport(t, filepath.Join(root, "backup1", "reports"))
	if report.UnsupportedFiles != 1 || report.QuarantineFiles != 1 || report.SameNameFiles != 2 {
		t.Fatalf("unexpected health report: %+v", report)
	}

	runCommand(t, root, cli, "-action", "list-runs")
	runCommand(t, root, cli, "-action", "show-run", "-run-id", run.ID)
	runCommand(t, root, cli, "-action", "run-journal", "-run-id", run.ID)
	verifyOutput := runCommand(t, root, cli, "-action", "verify-run", "-run-id", run.ID)
	if !strings.Contains(verifyOutput, "recoveryStatus=complete") {
		t.Fatalf("verify-run did not report complete recovery:\n%s", verifyOutput)
	}
	healthOutput := runCommand(t, root, cli, "-action", "health-report")
	if !strings.Contains(healthOutput, "unsupported=1") {
		t.Fatalf("health-report did not include unsupported count:\n%s", healthOutput)
	}

	writeConfig(t, root, e2eConfig{
		Port:       ":0",
		Token:      "secret",
		Database:   uniqueDBName(t),
		Mode:       "classifyOnly",
		Scan:       filepath.Join(root, "library1"),
		Staging:    filepath.Join(root, "staging2"),
		Library:    filepath.Join(root, "library2"),
		Backup:     filepath.Join(root, "backup2"),
		Quarantine: filepath.Join(root, "quarantine2"),
		Logs:       filepath.Join(root, "logs2"),
	})
	runCommand(t, root, cli, "-action", "scan", "-mode", "classifyOnly")
	secondManifest := manifest(t, filepath.Join(root, "library2"))
	if diff := compareManifests(firstManifest, secondManifest); diff != "" {
		t.Fatalf("classified tree is not unique after reclassification:\n%s", diff)
	}
}

func TestLongMaintenanceMongoAndAPIE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long maintenance mongo/api e2e in short mode")
	}
	repo := repoRoot(t)
	root := t.TempDir()
	cli := buildBinary(t, repo, root, "pics-cli", "./cmd/cli")
	server := buildBinary(t, repo, root, "manager-server", "./cmd/manager-server")
	dbName := uniqueDBName(t)
	port := freePort(t)

	scan := filepath.Join(root, "scan")
	writeDataset(t, scan)
	writeConfig(t, root, e2eConfig{
		Port:       port,
		Token:      "secret",
		Database:   dbName,
		Mode:       "full",
		Scan:       scan,
		Staging:    filepath.Join(root, "staging"),
		Library:    filepath.Join(root, "library"),
		Backup:     filepath.Join(root, "backup"),
		Quarantine: filepath.Join(root, "quarantine"),
		Logs:       filepath.Join(root, "logs"),
	})

	if !mongoAvailable(t, root, cli) {
		t.Skip("MongoDB unavailable for full/api e2e")
	}
	cleanupMongoCollections(t, root)
	runCommand(t, root, cli, "-action", "scan", "-mode", "full")
	assertMongoCollectionCounts(t, root, map[string]int64{
		"images": 7,
		"videos": 2,
		"audios": 2,
		"texts":  2,
	})
	stats := runCommand(t, root, cli, "-action", "stats")
	if !strings.Contains(stats, "media=") && !strings.Contains(stats, "媒体") {
		t.Fatalf("stats output did not look valid:\n%s", stats)
	}
	runCommand(t, root, cli, "-action", "rebuild-database")
	assertMongoCollectionCounts(t, root, map[string]int64{
		"images": 7,
		"videos": 2,
		"audios": 2,
		"texts":  2,
	})
	runCommand(t, root, cli, "-action", "list-series", "-limit", "3")

	cmd := exec.Command(server)
	cmd.Dir = root
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start manager-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	baseURL := "http://127.0.0.1" + port
	waitHTTP(t, baseURL+"/health", http.StatusOK, 10*time.Second, func(req *http.Request) {})

	expectHTTP(t, http.MethodGet, baseURL+"/api/v1/config", "", http.StatusOK, nil)
	expectHTTP(t, http.MethodPost, baseURL+"/api/v1/tasks", `{"path":"`+jsonEscape(scan)+`","mode":"classifyOnly"}`, http.StatusUnauthorized, nil)
	taskBody := expectHTTP(t, http.MethodPost, baseURL+"/api/v1/tasks", `{"path":"`+jsonEscape(scan)+`","mode":"classifyOnly"}`, http.StatusAccepted, func(req *http.Request) {
		req.Header.Set("X-Maintenance-Token", "secret")
	})
	var taskStart taskStartEnvelope
	if err := json.Unmarshal([]byte(taskBody), &taskStart); err != nil {
		t.Fatalf("task start response invalid JSON: %v\n%s", err, taskBody)
	}
	if taskStart.Data.TaskID == "" {
		t.Fatalf("task start response missing taskId: %s\nserver logs:\n%s", taskBody, logs.String())
	}
	waitTaskTerminal(t, baseURL, taskStart.Data.TaskID, 10*time.Second)
	expectHTTP(t, http.MethodGet, baseURL+"/api/v1/runs", "", http.StatusOK, nil)
}

type e2eConfig struct {
	Port       string
	Token      string
	Database   string
	Mode       string
	Scan       string
	Staging    string
	Library    string
	Backup     string
	Quarantine string
	Logs       string
}

func writeConfig(t *testing.T, dir string, cfg e2eConfig) {
	t.Helper()
	content := fmt.Sprintf(`server:
  port: %q
  timeout: 10s
  maintenanceToken: %q
database:
  uri: "mongodb://localhost:27017"
  name: %q
logger:
  level: "debug"
  format: "text"
  path: %q
scanner:
  mode: %q
  scanPath: %q
  stagingPath: %q
  finalLibraryPath: %q
  backupPath: %q
  quarantinePath: %q
  corruptionLogPath: %q
  duplicatesDir: "_duplicates"
  workerCount: 2
  batchSize: 20
  ioThrottleMs: 0
  maintenanceWindow: ""
  maxFilesPerDir: 2
  filePatterns:
    - '^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$'
  mediaTypes:
    - type: "image"
      extensions: [".png"]
      filePatterns:
        - '^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$'
    - type: "video"
      extensions: [".mp4"]
      filePatterns:
        - '^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$'
    - type: "audio"
      extensions: [".mp3"]
      filePatterns:
        - '^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$'
    - type: "text"
      extensions: [".txt"]
      filePatterns:
        - '^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$'
  seriesGroupPatterns:
    - name: "文本+数字"
      pattern: '^(?P<group>.+?)\s*(\d+)$'
`, cfg.Port, cfg.Token, cfg.Database, cfg.Logs, cfg.Mode, cfg.Scan, cfg.Staging, cfg.Library, cfg.Backup, cfg.Quarantine, filepath.Join(dir, "corrupted.log"))
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func writeDataset(t *testing.T, root string) {
	t.Helper()
	mustMkdir(t, filepath.Join(root, "a"))
	mustMkdir(t, filepath.Join(root, "b"))
	mustMkdir(t, filepath.Join(root, "c"))
	writePNG(t, filepath.Join(root, "a", "Dup_1.png"), color.RGBA{R: 255, A: 255})
	writePNG(t, filepath.Join(root, "b", "Dup_1.png"), color.RGBA{G: 255, A: 255})
	writePNG(t, filepath.Join(root, "c", "Dup_1.png"), color.RGBA{B: 255, A: 255})
	writePNG(t, filepath.Join(root, "Solo_1.png"), color.RGBA{R: 100, G: 100, B: 100, A: 255})
	writePNG(t, filepath.Join(root, "Comic 1_1.png"), color.RGBA{R: 180, A: 255})
	writePNG(t, filepath.Join(root, "Comic 2_1.png"), color.RGBA{G: 180, A: 255})
	writeFile(t, filepath.Join(root, "Video_1.mp4"), []byte("video-bytes"))
	writeFile(t, filepath.Join(root, "Audio_1.mp3"), []byte("audio-bytes"))
	writeFile(t, filepath.Join(root, "Notes_1.txt"), []byte("notes"))
	writePNG(t, filepath.Join(root, "Shared_1.png"), color.RGBA{R: 90, G: 120, B: 150, A: 255})
	writeFile(t, filepath.Join(root, "Shared_1.mp4"), []byte("shared-video"))
	writeFile(t, filepath.Join(root, "Shared_1.mp3"), []byte("shared-audio"))
	writeFile(t, filepath.Join(root, "Shared_1.txt"), []byte("shared-text"))
	writeFile(t, filepath.Join(root, "Broken_1.png"), []byte("not a png"))
	writeFile(t, filepath.Join(root, "note.bin"), []byte("unsupported"))
}

func assertClassifiedTree(t *testing.T, m map[string]string) {
	t.Helper()
	for _, rel := range []string{
		filepath.Join("images", "D", "Dup", "Dup_1.png"),
		filepath.Join("images", "S", "Solo", "Solo_1.png"),
		filepath.Join("images", "C", "Comic_agg", "Comic 1", "Comic 1_1.png"),
		filepath.Join("images", "C", "Comic_agg", "Comic 2", "Comic 2_1.png"),
		filepath.Join("videos", "V", "Video", "Video_1.mp4"),
		filepath.Join("audios", "A", "Audio", "Audio_1.mp3"),
		filepath.Join("texts", "N", "Notes", "Notes_1.txt"),
		filepath.Join("images", "S", "Shared", "Shared_1.png"),
		filepath.Join("videos", "S", "Shared", "Shared_1.mp4"),
		filepath.Join("audios", "S", "Shared", "Shared_1.mp3"),
		filepath.Join("texts", "S", "Shared", "Shared_1.txt"),
	} {
		if _, ok := m[rel]; !ok {
			t.Fatalf("classified manifest missing %s: %v", rel, sortedKeys(m))
		}
	}
	if got := sameNameCount(m); got != 2 {
		t.Fatalf("expected 2 same-name variants, got %d: %v", got, sortedKeys(m))
	}
}

func buildBinary(t *testing.T, repo, root, name, pkg string) string {
	t.Helper()
	out := filepath.Join(root, name)
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, pkg)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s failed: %v\n%s", pkg, err, output)
	}
	return out
}

func runCommand(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out\n%s", bin, args, output)
	}
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", bin, args, err, output)
	}
	return string(output)
}

func mongoAvailable(t *testing.T, dir, cli string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "-action", "stats")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("MongoDB e2e probe failed: %v\n%s", err, output)
		return false
	}
	return true
}

func cleanupMongoCollections(t *testing.T, configDir string) {
	t.Helper()
	if err := config.LoadConfig(configDir); err != nil {
		t.Logf("skip Mongo cleanup registration: %v", err)
		return
	}
	cfg := *config.C
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store, err := dbmongo.NewStore(ctx, &cfg)
		if err != nil {
			t.Logf("Mongo cleanup connect failed: %v", err)
			return
		}
		defer store.Close(context.Background())
		if err := store.DropAllCollections(ctx); err != nil {
			t.Logf("Mongo cleanup failed: %v", err)
		}
	})
}

func assertMongoCollectionCounts(t *testing.T, configDir string, want map[string]int64) {
	t.Helper()
	if err := config.LoadConfig(configDir); err != nil {
		t.Fatalf("load config for Mongo collection assertion: %v", err)
	}
	cfg := *config.C
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Database.URI))
	if err != nil {
		t.Fatalf("connect Mongo for collection assertion: %v", err)
	}
	defer client.Disconnect(context.Background())
	db := client.Database(cfg.Database.Name)
	for collection, expected := range want {
		got, err := db.Collection(collection).CountDocuments(ctx, map[string]any{})
		if err != nil {
			t.Fatalf("count %s: %v", collection, err)
		}
		if got != expected {
			t.Fatalf("collection %s expected %d documents, got %d", collection, expected, got)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func latestRun(t *testing.T, dir string) runRecord {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob runs: %v", err)
	}
	var newest string
	var newestMod time.Time
	for _, path := range entries {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat run: %v", err)
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest = path
			newestMod = info.ModTime()
		}
	}
	if newest == "" {
		t.Fatalf("no run files in %s", dir)
	}
	var run runRecord
	if err := json.Unmarshal([]byte(readFile(t, newest)), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	return run
}

func latestHealthReport(t *testing.T, dir string) healthReport {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.health.json"))
	if err != nil {
		t.Fatalf("glob reports: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no health reports in %s", dir)
	}
	sort.Strings(files)
	var report healthReport
	if err := json.Unmarshal([]byte(readFile(t, files[len(files)-1])), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return report
}

func manifest(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		result[rel] = hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		t.Fatalf("walk manifest: %v", err)
	}
	return result
}

func compareManifests(left, right map[string]string) string {
	if len(left) != len(right) {
		return fmt.Sprintf("size differs left=%d right=%d\nleft=%v\nright=%v", len(left), len(right), sortedKeys(left), sortedKeys(right))
	}
	for key, value := range left {
		if right[key] != value {
			return fmt.Sprintf("entry differs at %s left=%s right=%s", key, value, right[key])
		}
	}
	return ""
}

func sameNameCount(m map[string]string) int {
	count := 0
	for rel := range m {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == ".same-name" {
				count++
				break
			}
		}
	}
	return count
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writePNG(t *testing.T, path string, c color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	writeFile(t, path, buf.Bytes())
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path exists %s: %v", path, err)
	}
}

func uniqueDBName(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())))
	return "pics_e2e_" + hex.EncodeToString(sum[:8])
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer ln.Close()
	return fmt.Sprintf(":%d", ln.Addr().(*net.TCPAddr).Port)
}

func waitHTTP(t *testing.T, url string, status int, timeout time.Duration, mutate func(*http.Request)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if mutate != nil {
			mutate(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == status {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s status %d: %v", url, status, lastErr)
}

func waitTaskTerminal(t *testing.T, baseURL, taskID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last taskStatusEnvelope
	for time.Now().Before(deadline) {
		body := expectHTTP(t, http.MethodGet, baseURL+"/api/v1/tasks/"+taskID, "", http.StatusOK, nil)
		if err := json.Unmarshal([]byte(body), &last); err != nil {
			t.Fatalf("task status response invalid JSON: %v\n%s", err, body)
		}
		switch last.Data.Status {
		case "completed":
			if last.Data.Phase == "" {
				t.Fatalf("completed task missing phase: %+v", last.Data)
			}
			return
		case "failed":
			t.Fatalf("task failed: %+v", last.Data)
		case "stopped", "paused":
			t.Fatalf("task ended unexpectedly: %+v", last.Data)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for task %s to finish; last=%+v", taskID, last.Data)
}

func expectHTTP(t *testing.T, method, url, body string, status int, mutate func(*http.Request)) string {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Fatalf("%s %s expected status %d got %d:\n%s", method, url, status, resp.StatusCode, data)
	}
	return string(data)
}

func jsonEscape(s string) string {
	data, _ := json.Marshal(s)
	return strings.Trim(string(data), `"`)
}
