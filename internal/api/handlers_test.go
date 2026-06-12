package api

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/runstate"
	"PICs_Manager/pkg/security"
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
	router := RegisterRoutesWithRunStore(nil, nil)
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

func TestMaintenanceRoutesRequireTokenWhenConfigured(t *testing.T) {
	oldConfig := config.C
	config.C = &config.Config{Server: config.ServerConfig{MaintenanceToken: "secret"}}
	defer func() { config.C = oldConfig }()

	router := RegisterRoutesWithRunStore(task.NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{}), nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("read-only health endpoint should remain open, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"path":"/tmp/media","mode":"classifyOnly"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized maintenance request, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"path":"/tmp/media","mode":"classifyOnly"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Maintenance-Token", "secret")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected accepted maintenance request, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDevicePairingAndScopeAuth(t *testing.T) {
	oldConfig := config.C
	config.C = &config.Config{
		Scanner:  config.ScannerConfig{ScanPath: "/tmp/media"},
		Security: config.SecurityConfig{Enabled: true, RequireViewerForRead: true},
	}
	defer func() { config.C = oldConfig }()

	authStore, err := security.NewStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	viewerCode, _, err := authStore.CreatePairingCode(context.Background(), "viewer-phone", security.ScopeViewer, time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode viewer returned error: %v", err)
	}
	maintainerCode, _, err := authStore.CreatePairingCode(context.Background(), "maintainer-laptop", security.ScopeMaintainer, time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode maintainer returned error: %v", err)
	}
	router := RegisterRoutesWithServices(
		task.NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, config.C),
		nil,
		nil,
		authStore,
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/series", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected viewer route to require token, got %d body=%s", rec.Code, rec.Body.String())
	}

	viewerToken := claimToken(t, router, viewerCode, "viewer-phone")
	maintainerToken := claimToken(t, router, maintainerCode, "maintainer-laptop")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"path":"/tmp/media","mode":"classifyOnly"}`))
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected viewer token to be forbidden for maintenance, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"path":"/tmp/media","mode":"classifyOnly"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+maintainerToken)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected maintainer token to start task, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func claimToken(t *testing.T, router http.Handler, code, deviceName string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"code": code, "deviceName": deviceName})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal claim response returned error: %v", err)
	}
	if envelope.Data.Token == "" {
		t.Fatalf("claim response missing token: %s", rec.Body.String())
	}
	return envelope.Data.Token
}

func TestSafeConfigRedactsMaintenanceToken(t *testing.T) {
	safe := safeConfigForResponse(&config.Config{Server: config.ServerConfig{MaintenanceToken: "secret"}})
	if safe.Server.MaintenanceToken != "xxxxx" {
		t.Fatalf("expected redacted maintenance token, got %q", safe.Server.MaintenanceToken)
	}
}

func TestRunHandlersExposePersistentRunState(t *testing.T) {
	store, err := runstate.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if err := store.Create(context.Background(), runstate.Run{
		ID:       "run-1",
		Status:   runstate.StatusCompleted,
		Mode:     "classifyOnly",
		Phase:    "finished",
		ScanPath: "/media/inbox",
		Counts:   map[string]int64{"classifiedFiles": 3},
	}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.AppendEvent(context.Background(), runstate.Event{RunID: "run-1", Action: "checkpoint", Phase: "finished"}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	router := RegisterRoutesWithRunStore(nil, nil, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run-1") {
		t.Fatalf("unexpected list response status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/journal", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "checkpoint") {
		t.Fatalf("unexpected journal response status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleStopTaskCancelsRunningTask(t *testing.T) {
	runner := taskBlockingRunner{ctxs: make(chan context.Context, 1)}
	manager := task.NewManagerWithRunStore(runner, &config.Config{})
	taskID, err := manager.StartNewScanTask("/tmp/media", "classifyOnly")
	if err != nil {
		t.Fatalf("StartNewScanTask returned error: %v", err)
	}
	select {
	case <-runner.ctxs:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scan context")
	}
	router := RegisterRoutesWithRunStore(manager, nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+taskID, nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRegisterRoutesHealthEndpoint(t *testing.T) {
	router := RegisterRoutesWithRunStore(nil, nil)
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
	router := RegisterRoutesWithRunStore(nil, nil)
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
	videoPayload, err := json.Marshal(toMediaResponses([]models.Image{{
		ID:           primitive.NewObjectID(),
		SeriesID:     seriesID,
		MediaType:    "video",
		Thumbnail:    "data:image/jpeg;base64,abcd",
		HasThumbnail: true,
	}}))
	if err != nil {
		t.Fatalf("Marshal video media response returned error: %v", err)
	}
	if strings.Contains(string(videoPayload), "thumbnailUrl") {
		t.Fatalf("non-image media should not include thumbnail URL: %s", videoPayload)
	}
}

func TestListMediaBySeriesSeparatesMediaTypes(t *testing.T) {
	seriesID := primitive.NewObjectID()
	store := &apiTypedMediaStore{}
	router := RegisterRoutesWithRunStore(nil, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/series/"+seriesID.Hex()+"/media/video?page=1&limit=20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if len(store.requestedMediaTypes) != 1 || store.requestedMediaTypes[0] != "video" {
		t.Fatalf("expected video media store request, got %#v", store.requestedMediaTypes)
	}
	if !strings.Contains(rec.Body.String(), `"mediaType":"video"`) {
		t.Fatalf("expected video response, got %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/series/"+seriesID.Hex()+"/images?page=1&limit=20", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if len(store.requestedMediaTypes) != 2 || store.requestedMediaTypes[1] != "image" {
		t.Fatalf("expected legacy images route to request image store, got %#v", store.requestedMediaTypes)
	}
	if !strings.Contains(rec.Body.String(), `"mediaType":"image"`) {
		t.Fatalf("expected image response, got %s", rec.Body.String())
	}
}

func TestMediaDownloadSupportsRangeAndRejectsOutsideLibrary(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	root := t.TempDir()
	library := filepath.Join(root, "library")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	mediaID := primitive.NewObjectID()
	mediaPath := filepath.Join(library, "sample.txt")
	if err := os.WriteFile(mediaPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	config.C = &config.Config{Scanner: config.ScannerConfig{FinalLibraryPath: library}}

	router := RegisterRoutesWithRunStore(nil, apiDownloadStore{media: &models.Image{
		ID:       mediaID,
		FileName: "sample.txt",
		FilePath: mediaPath,
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/"+mediaID.Hex()+"/download", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected status %d, got %d: %s", http.StatusPartialContent, rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "2345" {
		t.Fatalf("expected ranged body %q, got %q", "2345", got)
	}

	outsidePath := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile outside returned error: %v", err)
	}
	router = RegisterRoutesWithRunStore(nil, apiDownloadStore{media: &models.Image{
		ID:       mediaID,
		FileName: "outside.txt",
		FilePath: outsidePath,
	}})
	req = httptest.NewRequest(http.MethodGet, "/api/v1/media/"+mediaID.Hex()+"/download", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for outside file, got %d: %s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestMediaDownloadSymlinkPolicy(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	root := t.TempDir()
	library := filepath.Join(root, "library")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	insideTarget := filepath.Join(library, "inside.txt")
	if err := os.WriteFile(insideTarget, []byte("inside"), 0o644); err != nil {
		t.Fatalf("WriteFile inside returned error: %v", err)
	}
	insideLink := filepath.Join(library, "inside-link.txt")
	if err := os.Symlink(insideTarget, insideLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	mediaID := primitive.NewObjectID()
	config.C = &config.Config{Scanner: config.ScannerConfig{FinalLibraryPath: library}}
	router := RegisterRoutesWithRunStore(nil, apiDownloadStore{media: &models.Image{
		ID:       mediaID,
		FileName: "inside-link.txt",
		FilePath: insideLink,
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/"+mediaID.Hex()+"/download", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected symlink to be rejected by default, got %d: %s", rec.Code, rec.Body.String())
	}

	config.C.Scanner.FollowSymlinks = true
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected confined symlink to be allowed, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "inside" {
		t.Fatalf("expected symlink target body %q, got %q", "inside", got)
	}

	outsideTarget := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outsideTarget, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile outside returned error: %v", err)
	}
	outsideLink := filepath.Join(library, "outside-link.txt")
	if err := os.Symlink(outsideTarget, outsideLink); err != nil {
		t.Fatalf("Symlink outside returned error: %v", err)
	}
	router = RegisterRoutesWithRunStore(nil, apiDownloadStore{media: &models.Image{
		ID:       mediaID,
		FileName: "outside-link.txt",
		FilePath: outsideLink,
	}})
	req = httptest.NewRequest(http.MethodGet, "/api/v1/media/"+mediaID.Hex()+"/download", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected escaping symlink to be rejected, got %d: %s", rec.Code, rec.Body.String())
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
	handlers := NewAPIHandlersWithRunStore(nil, nil)
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
	handlers := NewAPIHandlersWithRunStore(nil, apiFakeStore{})
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
	handlers := NewAPIHandlersWithRunStore(nil, nil)
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

type taskBlockingRunner struct {
	ctxs chan context.Context
}

func (r taskBlockingRunner) RunFullScanContext(ctx context.Context, cfg config.ScannerConfig) error {
	r.ctxs <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestHandleStartScanTaskReturnsBadRequestForInvalidMode(t *testing.T) {
	handlers := NewAPIHandlersWithRunStore(task.NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{}), nil)
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

func TestHandleStartScanTaskUsesConfiguredPathForRemoteSecureRequests(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	cfg := &config.Config{
		Scanner:  config.ScannerConfig{ScanPath: "/configured/inbox"},
		Security: config.SecurityConfig{Enabled: true},
	}
	config.C = cfg
	runner := captureRunner{cfgs: make(chan config.ScannerConfig, 1)}
	handlers := NewAPIHandlersWithRunStore(task.NewManagerWithRunStore(runner, cfg), nil)
	body := bytes.NewBufferString(`{"path":"/tmp/evil","mode":"classifyOnly"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", body)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	handlers.HandleStartScanTask(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	select {
	case got := <-runner.cfgs:
		if got.ScanPath != "/configured/inbox" {
			t.Fatalf("expected configured scan path, got %q", got.ScanPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scanner config")
	}
}

func TestHandleStartScanTaskReturnsUnavailableWithoutTaskManager(t *testing.T) {
	handlers := NewAPIHandlersWithRunStore(nil, nil)
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
	handlers := NewAPIHandlersWithRunStore(task.NewManagerWithRunStore(nil, &config.Config{}), nil)
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
	manager := task.NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, &config.Config{})
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	handlers := NewAPIHandlersWithRunStore(manager, nil)
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
	manager := task.NewManagerWithRunStore(runner, initial)
	handlers := NewAPIHandlersWithRunStore(manager, nil)
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
	handlers := NewAPIHandlersWithRunStore(nil, nil)
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
	handlers := NewAPIHandlersWithRunStore(task.NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, config.C), nil)
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
	handlers := NewAPIHandlersWithRunStore(task.NewManagerWithRunStore(captureRunner{cfgs: make(chan config.ScannerConfig, 1)}, config.C), nil)
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
func (s apiFakeStore) Media(string) database.ImageStore    { return apiFakeImageStore{} }
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

type apiTypedMediaStore struct {
	apiFakeStore
	requestedMediaTypes []string
}

func (s *apiTypedMediaStore) Media(mediaType string) database.ImageStore {
	s.requestedMediaTypes = append(s.requestedMediaTypes, mediaType)
	return apiTypedImageStore{mediaType: mediaType}
}

type apiTypedImageStore struct {
	apiFakeImageStore
	mediaType string
}

func (s apiTypedImageStore) ListBySeriesIDCursor(_ context.Context, seriesID primitive.ObjectID, _ string, _ int) ([]models.Image, int64, string, error) {
	return []models.Image{{
		ID:        primitive.NewObjectID(),
		SeriesID:  seriesID,
		MediaType: s.mediaType,
		FileName:  s.mediaType + "-item",
		FilePath:  "/library/" + s.mediaType + "-item",
	}}, 1, "", nil
}

type apiDownloadStore struct {
	apiFakeStore
	media  *models.Image
	series *models.Series
}

func (s apiDownloadStore) Images() database.ImageStore {
	return apiDownloadImageStore{media: s.media}
}

func (s apiDownloadStore) Series() database.SeriesStore {
	return apiDownloadSeriesStore{series: s.series}
}

type apiDownloadImageStore struct {
	apiFakeImageStore
	media *models.Image
}

func (s apiDownloadImageStore) GetByID(_ context.Context, id primitive.ObjectID) (*models.Image, error) {
	if s.media != nil && s.media.ID == id {
		return s.media, nil
	}
	return nil, nil
}

type apiDownloadSeriesStore struct {
	apiFakeSeriesStore
	series *models.Series
}

func (s apiDownloadSeriesStore) GetByID(_ context.Context, id primitive.ObjectID) (*models.Series, error) {
	if s.series != nil && s.series.ID == id {
		return s.series, nil
	}
	return nil, nil
}
