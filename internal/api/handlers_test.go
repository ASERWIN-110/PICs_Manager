package api

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type captureRunner struct {
	cfgs chan config.ScannerConfig
}

func TestRegisterRoutesAllowsLocalDevOrigins(t *testing.T) {
	router := RegisterRoutes(nil, nil)
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:5173"} {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/api/v1/series", nil)
			req.Header.Set("Origin", origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("expected Access-Control-Allow-Origin %q, got %q", origin, got)
			}
		})
	}
}

func TestRegisterRoutesHealthEndpoint(t *testing.T) {
	router := RegisterRoutes(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "OK" {
		t.Fatalf("expected health body OK, got %q", rec.Body.String())
	}
}

func TestRegisterRoutesDoesNotExposeLegacyScanTaskPath(t *testing.T) {
	router := RegisterRoutes(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/scan", bytes.NewBufferString(`{"path":"/tmp/media","mode":"full"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d: %s", http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	}
}

func TestListResponsesDoNotInlineBase64Thumbnails(t *testing.T) {
	seriesID := primitive.NewObjectID()
	imageID := primitive.NewObjectID()
	seriesPayload, err := json.Marshal(toSeriesResponses([]models.Series{{
		ID:           seriesID,
		Name:         "Series",
		Path:         "/library/Series",
		Thumbnail:    "data:image/jpeg;base64,abcd",
		HasThumbnail: true,
	}}))
	if err != nil {
		t.Fatalf("Marshal series response returned error: %v", err)
	}
	if strings.Contains(string(seriesPayload), "base64") || strings.Contains(string(seriesPayload), "abcd") {
		t.Fatalf("series list response inlined thumbnail data: %s", seriesPayload)
	}
	if !strings.Contains(string(seriesPayload), "/api/v1/series/"+seriesID.Hex()+"/thumbnail") {
		t.Fatalf("series list response missing thumbnail URL: %s", seriesPayload)
	}
	emptySeriesPayload, err := json.Marshal(toSeriesResponses([]models.Series{{
		ID:   primitive.NewObjectID(),
		Name: "No cover",
		Path: "/library/No cover",
	}}))
	if err != nil {
		t.Fatalf("Marshal empty series response returned error: %v", err)
	}
	if strings.Contains(string(emptySeriesPayload), "thumbnailUrl") {
		t.Fatalf("series without thumbnail flag should not include thumbnail URL: %s", emptySeriesPayload)
	}

	mediaPayload, err := json.Marshal(toMediaResponses([]models.Image{{
		ID:           imageID,
		SeriesID:     seriesID,
		MediaType:    "image",
		Thumbnail:    "data:image/jpeg;base64,abcd",
		HasThumbnail: true,
	}}))
	if err != nil {
		t.Fatalf("Marshal media response returned error: %v", err)
	}
	if strings.Contains(string(mediaPayload), "base64") || strings.Contains(string(mediaPayload), "abcd") {
		t.Fatalf("media list response inlined thumbnail data: %s", mediaPayload)
	}
	if !strings.Contains(string(mediaPayload), "/api/v1/images/"+imageID.Hex()+"/thumbnail") {
		t.Fatalf("media list response missing thumbnail URL: %s", mediaPayload)
	}
	emptyMediaPayload, err := json.Marshal(toMediaResponses([]models.Image{{
		ID:        primitive.NewObjectID(),
		SeriesID:  seriesID,
		MediaType: "image",
	}}))
	if err != nil {
		t.Fatalf("Marshal empty media response returned error: %v", err)
	}
	if strings.Contains(string(emptyMediaPayload), "thumbnailUrl") {
		t.Fatalf("media without thumbnail flag should not include thumbnail URL: %s", emptyMediaPayload)
	}
}

func TestServeThumbnailStreamsDecodedImage(t *testing.T) {
	rec := httptest.NewRecorder()
	serveThumbnail(rec, "data:image/jpeg;base64,/9j/")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("expected image/jpeg content type, got %q", got)
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("unexpected decoded thumbnail bytes: %v", got)
	}
}

func TestParseCursorPaginationRequiresCursorAfterFirstPage(t *testing.T) {
	tests := []struct {
		name   string
		target string
		ok     bool
	}{
		{name: "first page without cursor", target: "/api/v1/series?page=1", ok: true},
		{name: "second page without cursor", target: "/api/v1/series?page=2", ok: false},
		{name: "second page with empty cursor", target: "/api/v1/series?page=2&cursor=", ok: false},
		{name: "second page with cursor", target: "/api/v1/series?page=2&cursor=abc", ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			_, _, _, ok, err := parseCursorPagination(req)
			if err != nil {
				t.Fatalf("parseCursorPagination returned error: %v", err)
			}

			if ok != tt.ok {
				t.Fatalf("expected ok=%v", tt.ok)
			}
		})
	}
}

func TestParseCursorPaginationRejectsInvalidNumbers(t *testing.T) {
	tests := []string{
		"/api/v1/series?page=abc",
		"/api/v1/series?page=0",
		"/api/v1/series?limit=abc",
		"/api/v1/series?limit=0",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			_, _, _, _, err := parseCursorPagination(req)
			if err == nil {
				t.Fatal("expected invalid pagination error")
			}
		})
	}
}

func TestParseCursorPaginationCapsLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/series?limit=500", nil)
	_, limit, _, ok, err := parseCursorPagination(req)
	if err != nil {
		t.Fatalf("parseCursorPagination returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected pagination to be valid")
	}
	if limit != 100 {
		t.Fatalf("expected capped limit 100, got %d", limit)
	}
}

func TestHandleSearchByImageRejectsOversizedUpload(t *testing.T) {
	handlers := NewAPIHandlers(nil, nil)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("image", "large.png")
	if err != nil {
		t.Fatalf("CreateFormFile returned error: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), int(maxImageSearchUploadBytes)+1)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/image", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	handlers.HandleSearchByImage(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_multipart_form" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestHandleSearchByImageRejectsInvalidImageUpload(t *testing.T) {
	handlers := NewAPIHandlers(nil, apiFakeStore{})
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("image", "not-image.txt")
	if err != nil {
		t.Fatalf("CreateFormFile returned error: %v", err)
	}
	if _, err := part.Write([]byte("not an image")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/image", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	handlers.HandleSearchByImage(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_image" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestImageSearchTempPatternUsesSafeSuffix(t *testing.T) {
	tests := map[string]string{
		"sample.JPG": "upload-*.jpg",
		"scan.png":   "upload-*.png",
		"note.txt":   "upload-*.img",
		"":           "upload-*.img",
	}
	for name, want := range tests {
		if got := imageSearchTempPattern(name); got != want {
			t.Fatalf("imageSearchTempPattern(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestOrderSeriesByIDsPreservesSimilarityOrder(t *testing.T) {
	first := primitive.NewObjectID()
	second := primitive.NewObjectID()
	unrelated := primitive.NewObjectID()
	series := []models.Series{
		{ID: unrelated, Name: "z"},
		{ID: second, Name: "second"},
		{ID: first, Name: "first"},
	}

	got := orderSeriesByIDs(series, []primitive.ObjectID{first, second})

	if len(got) != 3 {
		t.Fatalf("expected all series to be returned, got %d: %+v", len(got), got)
	}
	if got[0].ID != first || got[1].ID != second {
		t.Fatalf("expected requested ID order first, got %+v", got)
	}
	if got[2].ID != unrelated {
		t.Fatalf("expected unrelated series appended last, got %+v", got)
	}
}

func TestDatabaseHandlersReturnUnavailableWithoutDB(t *testing.T) {
	handlers := NewAPIHandlers(nil, nil)
	tests := []struct {
		name   string
		method string
		target string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{
			name:   "list series",
			method: http.MethodGet,
			target: "/api/v1/series",
			call:   handlers.HandleListSeries,
		},
		{
			name:   "search text",
			method: http.MethodGet,
			target: "/api/v1/search/text?q=0307",
			call:   handlers.HandleSearchText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			rec := httptest.NewRecorder()

			tt.call(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			}
			var envelope apiResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("Unmarshal returned error: %v", err)
			}
			if envelope.Error == nil || envelope.Error.Code != "database_unavailable" {
				t.Fatalf("unexpected error envelope: %+v", envelope.Error)
			}
		})
	}
}

func (r captureRunner) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	r.cfgs <- cfg
	return nil
}

func TestHandleStartScanTaskReturnsBadRequestForInvalidMode(t *testing.T) {
	handlers := NewAPIHandlers(task.NewManager(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{}), nil)
	body := bytes.NewBufferString(`{"path":"/tmp/media","mode":"bad"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", body)
	rec := httptest.NewRecorder()

	handlers.HandleStartScanTask(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_task" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestHandleStartScanTaskReturnsUnavailableWithoutTaskManager(t *testing.T) {
	handlers := NewAPIHandlers(nil, nil)
	body := bytes.NewBufferString(`{"path":"/tmp/media","mode":"full"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", body)
	rec := httptest.NewRecorder()

	handlers.HandleStartScanTask(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "task_manager_unavailable" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestHandleStartScanTaskReturnsUnavailableWithoutRunner(t *testing.T) {
	handlers := NewAPIHandlers(task.NewManager(nil, &config.Config{}), nil)
	body := bytes.NewBufferString(`{"path":"/tmp/media","mode":"full"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", body)
	rec := httptest.NewRecorder()

	handlers.HandleStartScanTask(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "scan_runner_unavailable" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestHandleStartScanTaskReturnsUnavailableWhenShuttingDown(t *testing.T) {
	manager := task.NewManager(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{})
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	handlers := NewAPIHandlers(manager, nil)
	body := bytes.NewBufferString(`{"path":"/tmp/media","mode":"full"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", body)
	rec := httptest.NewRecorder()

	handlers.HandleStartScanTask(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "task_manager_shutting_down" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestHandleUpdateConfigWritesFileAndUpdatesTaskManagerConfig(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	runner := captureRunner{cfgs: make(chan config.ScannerConfig, 1)}
	initial := &config.Config{Scanner: config.ScannerConfig{Mode: "full"}}
	config.C = initial
	manager := task.NewManager(runner, initial)
	handlers := NewAPIHandlers(manager, nil)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	handlers.configPath = configPath

	newConfig := config.Config{
		Scanner: config.ScannerConfig{
			Mode:         "classifyOnly",
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	}
	payload, err := json.Marshal(newConfig)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handlers.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if config.C != initial {
		t.Fatal("expected config.C pointer to be updated in place")
	}
	if config.C.Scanner.Mode != "classifyOnly" {
		t.Fatalf("expected global config mode classifyOnly, got %q", config.C.Scanner.Mode)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config file to be written: %v", err)
	}

	taskID, err := manager.StartNewScanTask("/tmp/media", "")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	waitForTaskStatus(t, manager, taskID, task.StatusCompleted)

	select {
	case got := <-runner.cfgs:
		if got.Mode != "classifyOnly" {
			t.Fatalf("expected updated task manager config, got mode %q", got.Mode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scan runner")
	}
}

func TestHandleGetConfigRedactsDatabasePassword(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Database: config.DatabaseConfig{
			URI: "mongodb://dev_user:secret@localhost:27017/?authSource=admin",
		},
		Scanner: config.ScannerConfig{
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	}
	handlers := NewAPIHandlers(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	rec := httptest.NewRecorder()

	handlers.HandleGetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("config response leaked database password: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "xxxxx") {
		t.Fatalf("expected redacted placeholder in response: %s", rec.Body.String())
	}
}

func TestHandleUpdateConfigPreservesRuntimeSecretAndWritesSanitizedURI(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	config.C = &config.Config{
		Database: config.DatabaseConfig{
			URI:  "mongodb://dev_user:secret@localhost:27017/?authSource=admin",
			Name: "media_manager",
		},
		Scanner: config.ScannerConfig{
			Mode:         "full",
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	}
	handlers := NewAPIHandlers(task.NewManager(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, config.C), nil)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	handlers.configPath = configPath

	payload, err := json.Marshal(config.Config{
		Database: config.DatabaseConfig{
			URI:  "mongodb://dev_user:xxxxx@localhost:27017/?authSource=admin",
			Name: "media_manager",
		},
		Scanner: config.ScannerConfig{
			Mode:         "classifyOnly",
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handlers.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if config.C.Database.URI != "mongodb://dev_user:secret@localhost:27017/?authSource=admin" {
		t.Fatalf("expected runtime URI to preserve secret, got %q", config.C.Database.URI)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if strings.Contains(string(content), "secret") || strings.Contains(string(content), "xxxxx") {
		t.Fatalf("expected config file URI to be sanitized, got:\n%s", content)
	}
	if !strings.Contains(string(content), "mongodb://localhost:27017/?authSource=admin") {
		t.Fatalf("expected sanitized Mongo URI in config file, got:\n%s", content)
	}
}

func TestWriteFileAtomicReplacesContentAndCleansTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := writeFileAtomic(path, []byte("new"), 0644); err != nil {
		t.Fatalf("writeFileAtomic returned error: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("expected replaced content, got %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("expected mode 0644, got %v", got)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".config-*.tmp"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("expected no temp files, got %v", leftovers)
	}
}

func TestHandleUpdateConfigRejectsInvalidScannerConfig(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	config.C = &config.Config{Scanner: config.ScannerConfig{Mode: "full"}}
	handlers := NewAPIHandlers(task.NewManager(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, config.C), nil)
	handlers.configPath = filepath.Join(t.TempDir(), "config.yaml")

	payload, err := json.Marshal(config.Config{
		Scanner: config.ScannerConfig{
			Mode:         "bad",
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handlers.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	var envelope apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_config" {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func waitForTaskStatus(t *testing.T, manager *task.Manager, taskID string, want task.TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.GetTaskStatus(taskID)
		if err != nil {
			t.Fatalf("GetTaskStatus returned error: %v", err)
		}
		if status.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := manager.GetTaskStatus(taskID)
	if err != nil {
		t.Fatalf("GetTaskStatus returned error: %v", err)
	}
	t.Fatalf("expected status %q, got %q", want, status.Status)
}

type apiFakeStore struct{}

func (s apiFakeStore) Series() database.SeriesStore        { return apiFakeSeriesStore{} }
func (s apiFakeStore) Images() database.ImageStore         { return apiFakeImageStore{} }
func (s apiFakeStore) EnsureIndexes(context.Context) error { return nil }
func (s apiFakeStore) Diagnostics(context.Context) (database.Diagnostics, error) {
	return database.Diagnostics{}, nil
}
func (s apiFakeStore) DropAllCollections(context.Context) error { return nil }
func (s apiFakeStore) Close(context.Context) error              { return nil }

type apiFakeSeriesStore struct{}

func (s apiFakeSeriesStore) GetByID(context.Context, primitive.ObjectID) (*models.Series, error) {
	return nil, nil
}
func (s apiFakeSeriesStore) ListCursor(context.Context, string, int) ([]models.Series, int64, string, error) {
	return nil, 0, "", nil
}
func (s apiFakeSeriesStore) SearchByNameCursor(context.Context, string, string, int) ([]models.Series, int64, string, error) {
	return nil, 0, "", nil
}
func (s apiFakeSeriesStore) BulkWrite(context.Context, []mongo.WriteModel) error { return nil }
func (s apiFakeSeriesStore) FindManyByNames(context.Context, []string) ([]models.Series, []string, error) {
	return nil, nil, nil
}
func (s apiFakeSeriesStore) GetByIDs(context.Context, []primitive.ObjectID) ([]models.Series, error) {
	return nil, nil
}

type apiFakeImageStore struct{}

func (s apiFakeImageStore) GetByID(context.Context, primitive.ObjectID) (*models.Image, error) {
	return nil, nil
}
func (s apiFakeImageStore) ListBySeriesIDCursor(context.Context, primitive.ObjectID, string, int) ([]models.Image, int64, string, error) {
	return nil, 0, "", nil
}
func (s apiFakeImageStore) FindSimilarByPHash(context.Context, string, int) ([]models.Image, error) {
	panic("FindSimilarByPHash should not be called for invalid uploads")
}
func (s apiFakeImageStore) Delete(context.Context, primitive.ObjectID) error { return nil }
func (s apiFakeImageStore) CountBySeriesID(context.Context, primitive.ObjectID) (int64, error) {
	return 0, nil
}
func (s apiFakeImageStore) BulkWrite(context.Context, []mongo.WriteModel) error { return nil }
func (s apiFakeImageStore) GetFirstThumbnailMedia(context.Context, primitive.ObjectID) (*models.Image, error) {
	return nil, nil
}
func (s apiFakeImageStore) GetAllBySeriesID(context.Context, primitive.ObjectID) ([]models.Image, error) {
	return nil, nil
}
