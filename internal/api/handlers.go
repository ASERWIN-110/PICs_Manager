// 文件: internal/api/handlers.go
package api

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/hasher"
	"PICs_Manager/pkg/runstate"
	"PICs_Manager/pkg/security"
	"archive/zip"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"gopkg.in/yaml.v3"
)

const maxImageSearchUploadBytes int64 = 10 << 20

// APIHandlers 持有所有依赖
type APIHandlers struct {
	taskManager *task.Manager
	db          database.Store
	runs        *runstate.Store
	authStore   *security.Store
	configPath  string
}

// NewAPIHandlersWithRunStore 创建一个新的API处理器实例。
func NewAPIHandlersWithRunStore(tm *task.Manager, db database.Store, runStores ...*runstate.Store) *APIHandlers {
	var runs *runstate.Store
	if len(runStores) > 0 {
		runs = runStores[0]
	}
	return NewAPIHandlersWithServices(tm, db, runs, nil)
}

func NewAPIHandlersWithServices(tm *task.Manager, db database.Store, runs *runstate.Store, authStore *security.Store) *APIHandlers {
	return &APIHandlers{
		taskManager: tm,
		db:          db,
		runs:        runs,
		authStore:   authStore,
		configPath:  "config.yaml",
	}
}

func (h *APIHandlers) RequireMaintenanceAuth(next http.Handler) http.Handler {
	return h.RequireScope(security.ScopeMaintainer)(next)
}

func (h *APIHandlers) RequireScope(required security.Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.allowUnauthenticated(required, r) {
				next.ServeHTTP(w, r)
				return
			}
			token := requestMaintenanceToken(r)
			if h.legacyTokenAllowed(token, required) {
				next.ServeHTTP(w, r)
				return
			}
			if h.authStore == nil {
				respondError(w, http.StatusUnauthorized, "auth_required", "接口需要有效设备 token")
				return
			}
			if _, err := h.authStore.Authenticate(r.Context(), token, required); err != nil {
				if errors.Is(err, security.ErrForbidden) {
					respondError(w, http.StatusForbidden, "insufficient_scope", "设备权限不足")
					return
				}
				respondError(w, http.StatusUnauthorized, "auth_required", "接口需要有效设备 token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *APIHandlers) allowUnauthenticated(required security.Scope, r *http.Request) bool {
	if config.C == nil || !config.C.Security.Enabled {
		token := ""
		if config.C != nil {
			token = strings.TrimSpace(config.C.Server.MaintenanceToken)
		}
		if token == "" && required == security.ScopeAdmin {
			return isLocalRequest(r)
		}
		return token == ""
	}
	if required == security.ScopeViewer && !config.C.Security.RequireViewerForRead {
		return true
	}
	if required == security.ScopeAdmin && config.C.Security.AllowLocalAdmin && isLocalRequest(r) {
		return true
	}
	return false
}

func (h *APIHandlers) legacyTokenAllowed(token string, required security.Scope) bool {
	if config.C == nil {
		return false
	}
	legacy := strings.TrimSpace(config.C.Server.MaintenanceToken)
	if legacy == "" || token == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(legacy), []byte(token)) != 1 {
		return false
	}
	return security.HasScope(security.ScopeAdmin, required)
}

func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requestMaintenanceToken(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Device-Token")); value != "" {
		return value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Maintenance-Token")); value != "" {
		return value
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):])
	}
	return ""
}

// --- 辅助函数 ---

type apiResponse struct {
	Data  interface{} `json:"data,omitempty"`
	Meta  interface{} `json:"meta,omitempty"`
	Error *apiError   `json:"error,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type paginationMeta struct {
	CurrentPage int    `json:"currentPage"`
	TotalPages  int    `json:"totalPages"`
	TotalItems  int64  `json:"totalItems"`
	Limit       int    `json:"limit"`
	NextCursor  string `json:"nextCursor,omitempty"`
}

type listMeta struct {
	Pagination paginationMeta `json:"pagination"`
}

type seriesResponse struct {
	ID           primitive.ObjectID `json:"id"`
	Name         string             `json:"name"`
	Path         string             `json:"path"`
	ImageCount   int                `json:"imageCount"`
	ThumbnailURL string             `json:"thumbnailUrl,omitempty"`
	CreatedAt    interface{}        `json:"createdAt"`
	UpdatedAt    interface{}        `json:"updatedAt"`
}

type mediaResponse struct {
	ID             primitive.ObjectID `json:"id"`
	SeriesID       primitive.ObjectID `json:"seriesId"`
	FileHash       string             `json:"fileHash"`
	MediaType      string             `json:"mediaType"`
	PerceptualHash string             `json:"perceptualHash"`
	FileName       string             `json:"fileName"`
	FilePath       string             `json:"filePath"`
	ThumbnailURL   string             `json:"thumbnailUrl,omitempty"`
	CreatedAt      interface{}        `json:"createdAt"`
	UpdatedAt      interface{}        `json:"updatedAt"`
}

type authStatusResponse struct {
	Enabled              bool `json:"enabled"`
	RequireViewerForRead bool `json:"requireViewerForRead"`
	AllowLocalAdmin      bool `json:"allowLocalAdmin"`
	PairingAvailable     bool `json:"pairingAvailable"`
}

type claimPairingResponse struct {
	Token  string          `json:"token"`
	Device security.Device `json:"device"`
}

func toSeriesResponses(items []models.Series) []seriesResponse {
	result := make([]seriesResponse, len(items))
	for idx, item := range items {
		response := seriesResponse{
			ID:         item.ID,
			Name:       item.Name,
			Path:       item.Path,
			ImageCount: item.ImageCount,
			CreatedAt:  item.CreatedAt,
			UpdatedAt:  item.UpdatedAt,
		}
		if item.HasThumbnail {
			response.ThumbnailURL = "/api/v1/series/" + item.ID.Hex() + "/thumbnail"
		}
		result[idx] = response
	}
	return result
}

func toMediaResponses(items []models.Image) []mediaResponse {
	result := make([]mediaResponse, len(items))
	for idx, item := range items {
		response := mediaResponse{
			ID:             item.ID,
			SeriesID:       item.SeriesID,
			FileHash:       item.FileHash,
			MediaType:      item.MediaType,
			PerceptualHash: item.PerceptualHash,
			FileName:       item.FileName,
			FilePath:       item.FilePath,
			CreatedAt:      item.CreatedAt,
			UpdatedAt:      item.UpdatedAt,
		}
		if item.HasThumbnail && isImageMediaType(item.MediaType) {
			response.ThumbnailURL = "/api/v1/images/" + item.ID.Hex() + "/thumbnail"
		}
		result[idx] = response
	}
	return result
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	respondEnvelope(w, status, apiResponse{Data: data})
}

func respondList(w http.ResponseWriter, status int, data interface{}, pagination paginationMeta) {
	respondEnvelope(w, status, apiResponse{Data: data, Meta: listMeta{Pagination: pagination}})
}

func respondEnvelope(w http.ResponseWriter, status int, payload apiResponse) {
	response, err := json.Marshal(payload)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(response)
}

func respondError(w http.ResponseWriter, status int, code, message string) {
	respondEnvelope(w, status, apiResponse{Error: &apiError{Code: code, Message: message}})
}

func (h *APIHandlers) requireDB(w http.ResponseWriter) bool {
	if h.db != nil {
		return true
	}
	respondError(w, http.StatusServiceUnavailable, "database_unavailable", "数据库未初始化")
	return false
}

func parsePagination(r *http.Request) (page, limit int, err error) {
	page, err = parsePositiveQueryInt(r, "page", 1)
	if err != nil {
		return 0, 0, err
	}
	limit, err = parsePositiveQueryInt(r, "limit", 20)
	if err != nil {
		return 0, 0, err
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit, nil
}

func parsePositiveQueryInt(r *http.Request, name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func parseCursorPagination(r *http.Request) (page, limit int, cursor string, ok bool, err error) {
	page, limit, err = parsePagination(r)
	if err != nil {
		return 0, 0, "", false, err
	}
	cursor = strings.TrimSpace(r.URL.Query().Get("cursor"))
	if page > 1 && cursor == "" {
		return page, limit, cursor, false, nil
	}
	return page, limit, cursor, true, nil
}

func makePagination(page, limit int, total int64) paginationMeta {
	totalPages := 0
	if total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(limit)))
	}
	return paginationMeta{
		CurrentPage: page,
		TotalPages:  totalPages,
		TotalItems:  total,
		Limit:       limit,
	}
}

func makeCursorPagination(page, limit int, total int64, nextCursor string) paginationMeta {
	pagination := makePagination(page, limit, total)
	pagination.NextCursor = nextCursor
	return pagination
}

// --- 认证处理器 ---

func (h *APIHandlers) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	status := authStatusResponse{}
	if config.C != nil {
		status.Enabled = config.C.Security.Enabled
		status.RequireViewerForRead = config.C.Security.RequireViewerForRead
		status.AllowLocalAdmin = config.C.Security.AllowLocalAdmin
	}
	status.PairingAvailable = h.authStore != nil
	respondJSON(w, http.StatusOK, status)
}

func (h *APIHandlers) HandleClaimPairingCode(w http.ResponseWriter, r *http.Request) {
	if h.authStore == nil {
		respondError(w, http.StatusServiceUnavailable, "pairing_unavailable", "设备配对未启用")
		return
	}
	var payload struct {
		Code       string `json:"code"`
		DeviceName string `json:"deviceName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_request_body", "无效的请求体: "+err.Error())
		return
	}
	token, device, err := h.authStore.ClaimPairingCode(r.Context(), payload.Code, payload.DeviceName)
	if err != nil {
		if errors.Is(err, security.ErrUnauthorized) {
			respondError(w, http.StatusUnauthorized, "invalid_pairing_code", "配对码无效、过期或已使用")
			return
		}
		respondError(w, http.StatusInternalServerError, "pairing_failed", "设备配对失败: "+err.Error())
		return
	}
	respondJSON(w, http.StatusOK, claimPairingResponse{Token: token, Device: device})
}

// --- 任务处理器 ---

func (h *APIHandlers) HandleStartScanTask(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_request_body", "无效的请求体: "+err.Error())
		return
	}
	payload.Path = strings.TrimSpace(payload.Path)
	payload.Mode = strings.TrimSpace(payload.Mode)
	if remoteRestrictedMaintenance(r) {
		if config.C == nil || strings.TrimSpace(config.C.Scanner.ScanPath) == "" {
			respondError(w, http.StatusBadRequest, "missing_configured_scan_path", "安全模式下远程维护只能使用配置中的 scanPath")
			return
		}
		payload.Path = strings.TrimSpace(config.C.Scanner.ScanPath)
	}
	if payload.Path == "" {
		respondError(w, http.StatusBadRequest, "missing_path", "缺少 'path' 字段")
		return
	}
	if h.taskManager == nil {
		respondError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "任务管理器未初始化")
		return
	}
	taskID, err := h.taskManager.StartNewScanTask(payload.Path, payload.Mode)
	if err != nil {
		if errors.Is(err, task.ErrInvalidInput) {
			respondError(w, http.StatusBadRequest, "invalid_task", err.Error())
			return
		}
		if errors.Is(err, task.ErrNoRunner) {
			respondError(w, http.StatusServiceUnavailable, "scan_runner_unavailable", err.Error())
			return
		}
		if errors.Is(err, task.ErrShuttingDown) {
			respondError(w, http.StatusServiceUnavailable, "task_manager_shutting_down", err.Error())
			return
		}
		if errors.Is(err, task.ErrTaskConflict) {
			respondError(w, http.StatusConflict, "task_conflict", err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, "task_start_failed", err.Error())
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]string{"taskId": taskID})
}

func (h *APIHandlers) HandleGetTaskStatus(w http.ResponseWriter, r *http.Request) {
	if h.taskManager == nil {
		respondError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "任务管理器未初始化")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	status, err := h.taskManager.GetTaskStatus(taskID)
	if err != nil {
		respondError(w, http.StatusNotFound, "task_not_found", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, status)
}

func (h *APIHandlers) HandleStopTask(w http.ResponseWriter, r *http.Request) {
	h.handleCancelTask(w, r, h.taskManager.StopTask)
}

func (h *APIHandlers) HandlePauseTask(w http.ResponseWriter, r *http.Request) {
	h.handleCancelTask(w, r, h.taskManager.PauseTask)
}

func (h *APIHandlers) handleCancelTask(w http.ResponseWriter, r *http.Request, cancel func(string) (*task.Task, error)) {
	if h.taskManager == nil {
		respondError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "任务管理器未初始化")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	status, err := cancel(taskID)
	if err != nil {
		if errors.Is(err, task.ErrTaskNotFound) {
			respondError(w, http.StatusNotFound, "task_not_found", err.Error())
			return
		}
		if errors.Is(err, task.ErrInvalidInput) {
			respondError(w, http.StatusBadRequest, "invalid_task", err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, "task_stop_failed", err.Error())
		return
	}
	respondJSON(w, http.StatusAccepted, status)
}

func (h *APIHandlers) HandleListRuns(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		respondError(w, http.StatusServiceUnavailable, "run_store_unavailable", "运行状态存储未初始化")
		return
	}
	limit, err := parsePositiveQueryInt(r, "limit", 50)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	if limit > 200 {
		limit = 200
	}
	runs, err := h.runs.List(r.Context(), limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "run_list_failed", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, runs)
}

func (h *APIHandlers) HandleGetRun(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		respondError(w, http.StatusServiceUnavailable, "run_store_unavailable", "运行状态存储未初始化")
		return
	}
	runID := strings.TrimSpace(chi.URLParam(r, "runId"))
	run, err := h.runs.Get(r.Context(), runID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "run_lookup_failed", err.Error())
		return
	}
	if run == nil {
		respondError(w, http.StatusNotFound, "run_not_found", "运行记录不存在")
		return
	}
	respondJSON(w, http.StatusOK, run)
}

func (h *APIHandlers) HandleGetRunJournal(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		respondError(w, http.StatusServiceUnavailable, "run_store_unavailable", "运行状态存储未初始化")
		return
	}
	runID := strings.TrimSpace(chi.URLParam(r, "runId"))
	events, err := h.runs.Journal(r.Context(), runID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "run_journal_failed", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, events)
}

// --- 系列处理器 ---

func (h *APIHandlers) HandleListSeries(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	page, limit, cursor, ok, err := parseCursorPagination(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	if !ok {
		respondError(w, http.StatusBadRequest, "cursor_required", "page 大于 1 时必须提供 cursor")
		return
	}
	series, total, nextCursor, err := h.db.Series().ListCursor(r.Context(), cursor, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "series_list_failed", "无法获取系列列表: "+err.Error())
		return
	}
	respondList(w, http.StatusOK, toSeriesResponses(series), makeCursorPagination(page, limit, total, nextCursor))
}

func (h *APIHandlers) HandleListMediaBySeries(w http.ResponseWriter, r *http.Request) {
	seriesID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "seriesId"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_series_id", "无效的系列ID")
		return
	}
	if !h.requireDB(w) {
		return
	}
	page, limit, cursor, ok, err := parseCursorPagination(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	if !ok {
		respondError(w, http.StatusBadRequest, "cursor_required", "page 大于 1 时必须提供 cursor")
		return
	}
	mediaType := requestedMediaType(r)
	media, total, nextCursor, err := h.db.Media(mediaType).ListBySeriesIDCursor(r.Context(), seriesID, cursor, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "series_images_failed", "无法获取媒体列表: "+err.Error())
		return
	}
	respondList(w, http.StatusOK, toMediaResponses(media), makeCursorPagination(page, limit, total, nextCursor))
}

func requestedMediaType(r *http.Request) string {
	mediaType := strings.TrimSpace(chi.URLParam(r, "mediaType"))
	if mediaType == "" {
		mediaType = strings.TrimSpace(r.URL.Query().Get("mediaType"))
	}
	if mediaType == "" {
		return "image"
	}
	return mediaType
}

func (h *APIHandlers) HandleSeriesThumbnail(w http.ResponseWriter, r *http.Request) {
	seriesID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "seriesId"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_series_id", "无效的系列ID")
		return
	}
	if !h.requireDB(w) {
		return
	}
	series, err := h.db.Series().GetByID(r.Context(), seriesID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "series_lookup_failed", "获取系列失败: "+err.Error())
		return
	}
	if series == nil || series.Thumbnail == "" {
		respondError(w, http.StatusNotFound, "thumbnail_not_found", "系列缩略图不存在")
		return
	}
	serveThumbnail(w, series.Thumbnail)
}

func (h *APIHandlers) HandleMediaThumbnail(w http.ResponseWriter, r *http.Request) {
	imageID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "imageId"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_image_id", "无效的媒体ID")
		return
	}
	if !h.requireDB(w) {
		return
	}
	image, err := h.db.Images().GetByID(r.Context(), imageID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "image_lookup_failed", "获取媒体失败: "+err.Error())
		return
	}
	if image == nil || !isImageMediaType(image.MediaType) || image.Thumbnail == "" {
		respondError(w, http.StatusNotFound, "thumbnail_not_found", "媒体缩略图不存在")
		return
	}
	serveThumbnail(w, image.Thumbnail)
}

func isImageMediaType(mediaType string) bool {
	mediaType = strings.TrimSpace(mediaType)
	return mediaType == "" || strings.EqualFold(mediaType, "image")
}

func serveThumbnail(w http.ResponseWriter, thumbnail string) {
	contentType, encoded, ok := strings.Cut(thumbnail, ";base64,")
	if !ok {
		respondError(w, http.StatusInternalServerError, "invalid_thumbnail", "缩略图格式无效")
		return
	}
	contentType = strings.TrimPrefix(contentType, "data:")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "invalid_thumbnail", "缩略图解码失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *APIHandlers) HandleMediaDownload(w http.ResponseWriter, r *http.Request) {
	mediaID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "mediaId"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_media_id", "无效的媒体ID")
		return
	}
	if !h.requireDB(w) {
		return
	}
	media, err := h.db.Images().GetByID(r.Context(), mediaID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "media_lookup_failed", "获取媒体失败: "+err.Error())
		return
	}
	if media == nil {
		respondError(w, http.StatusNotFound, "media_not_found", "媒体不存在")
		return
	}
	filePath, err := safeLibraryPath(media.FilePath)
	if err != nil {
		respondError(w, http.StatusForbidden, "unsafe_media_path", err.Error())
		return
	}
	file, err := os.Open(filePath)
	if err != nil {
		respondError(w, http.StatusNotFound, "media_file_not_found", "媒体文件不存在: "+err.Error())
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "media_file_stat_failed", "读取媒体文件状态失败: "+err.Error())
		return
	}
	if info.IsDir() {
		respondError(w, http.StatusBadRequest, "media_path_is_directory", "媒体路径是目录")
		return
	}
	setAttachmentHeader(w, media.FileName)
	http.ServeContent(w, r, media.FileName, info.ModTime(), file)
}

func (h *APIHandlers) HandleSeriesDownload(w http.ResponseWriter, r *http.Request) {
	seriesID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "seriesId"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_series_id", "无效的系列ID")
		return
	}
	if !h.requireDB(w) {
		return
	}
	series, err := h.db.Series().GetByID(r.Context(), seriesID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "series_lookup_failed", "获取系列失败: "+err.Error())
		return
	}
	if series == nil {
		respondError(w, http.StatusNotFound, "series_not_found", "系列不存在")
		return
	}
	dirPath, err := safeLibraryPath(series.Path)
	if err != nil {
		respondError(w, http.StatusForbidden, "unsafe_series_path", err.Error())
		return
	}
	info, err := os.Stat(dirPath)
	if err != nil {
		respondError(w, http.StatusNotFound, "series_path_not_found", "系列目录不存在: "+err.Error())
		return
	}
	if !info.IsDir() {
		respondError(w, http.StatusBadRequest, "series_path_not_directory", "系列路径不是目录")
		return
	}

	archiveName := safeDownloadName(series.Name)
	if archiveName == "" {
		archiveName = series.ID.Hex()
	}
	w.Header().Set("Content-Type", "application/zip")
	setAttachmentHeader(w, archiveName+".zip")
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()
	if err := writeSeriesZip(r.Context(), zipWriter, dirPath, archiveName); err != nil {
		// Headers may already be written; log the error in-band for clients that
		// inspect the broken archive and return to stop writing more entries.
		_, _ = zipWriter.Create("ERROR.txt")
		return
	}
}

func safeLibraryPath(rawPath string) (string, error) {
	if config.C == nil {
		return "", errors.New("配置未初始化")
	}
	if strings.TrimSpace(config.C.Scanner.FinalLibraryPath) == "" {
		return "", errors.New("finalLibraryPath 未配置")
	}
	root, err := filepath.Abs(config.C.Scanner.FinalLibraryPath)
	if err != nil {
		return "", fmt.Errorf("无法解析最终库路径: %w", err)
	}
	candidate, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("无法解析媒体路径: %w", err)
	}
	if err := requirePathInside(root, candidate); err != nil {
		return "", err
	}
	if !config.C.Scanner.FollowSymlinks {
		if err := rejectSymlinkComponents(root, candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("无法解析最终库真实路径: %w", err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("无法解析媒体真实路径: %w", err)
	}
	if err := requirePathInside(resolvedRoot, resolvedCandidate); err != nil {
		return "", err
	}
	return resolvedCandidate, nil
}

func requirePathInside(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return fmt.Errorf("无法校验路径边界: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("路径不在最终库内: %s", candidate)
	}
	return nil
}

func rejectSymlinkComponents(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("路径包含 symlink，已按配置拒绝: %s", current)
		}
	}
	return nil
}

func setAttachmentHeader(w http.ResponseWriter, fileName string) {
	fileName = safeDownloadName(fileName)
	if fileName == "" {
		fileName = "download"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fileName}))
}

func safeDownloadName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Trim(name, ". ")
	replacer := strings.NewReplacer("/", "_", "\\", "_", "\x00", "_")
	return replacer.Replace(name)
}

func writeSeriesZip(ctx context.Context, zipWriter *zip.Writer, dirPath string, archiveName string) error {
	return filepath.WalkDir(dirPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == dirPath {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if config.C == nil || !config.C.Scanner.FollowSymlinks {
				return nil
			}
		}
		sourcePath, err := safeLibraryPath(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(sourcePath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(archiveName, rel))
		header.Method = zip.Deflate
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// --- 搜索处理器 ---

func (h *APIHandlers) HandleSearchText(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		respondError(w, http.StatusBadRequest, "missing_query", "缺少搜索查询参数 'q'")
		return
	}
	if !h.requireDB(w) {
		return
	}
	page, limit, cursor, ok, err := parseCursorPagination(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	if !ok {
		respondError(w, http.StatusBadRequest, "cursor_required", "page 大于 1 时必须提供 cursor")
		return
	}
	series, total, nextCursor, err := h.db.Series().SearchByNameCursor(r.Context(), query, cursor, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "series_search_failed", "搜索系列失败: "+err.Error())
		return
	}
	respondList(w, http.StatusOK, toSeriesResponses(series), makeCursorPagination(page, limit, total, nextCursor))
}

func (h *APIHandlers) HandleSearchByImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageSearchUploadBytes)
	if err := r.ParseMultipartForm(maxImageSearchUploadBytes); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_multipart_form", "无法解析表单: "+err.Error())
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		respondError(w, http.StatusBadRequest, "missing_image", "获取上传文件失败: "+err.Error())
		return
	}
	defer file.Close()
	if !h.requireDB(w) {
		return
	}
	tempFile, err := os.CreateTemp("", imageSearchTempPattern(header.Filename))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "temp_file_failed", "创建临时文件失败")
		return
	}
	tempFileName := tempFile.Name()
	defer os.Remove(tempFileName)
	if _, err := io.Copy(tempFile, file); err != nil {
		_ = tempFile.Close()
		respondError(w, http.StatusInternalServerError, "temp_file_write_failed", "写入临时文件失败")
		return
	}
	if err := tempFile.Close(); err != nil {
		respondError(w, http.StatusInternalServerError, "temp_file_close_failed", "关闭临时文件失败")
		return
	}
	pHash, err := hasher.CalculatePerceptualHash(tempFileName)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_image", "上传文件不是可识别的图片: "+err.Error())
		return
	}
	similarImages, err := h.db.Images().FindSimilarByPHash(r.Context(), pHash, 50)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "image_search_failed", "数据库查找失败: "+err.Error())
		return
	}
	seenSeriesIDs := make(map[primitive.ObjectID]struct{})
	uniqueSeriesIDs := make([]primitive.ObjectID, 0, len(similarImages))
	for _, img := range similarImages {
		if _, ok := seenSeriesIDs[img.SeriesID]; ok {
			continue
		}
		seenSeriesIDs[img.SeriesID] = struct{}{}
		uniqueSeriesIDs = append(uniqueSeriesIDs, img.SeriesID)
	}
	var series []models.Series
	if len(uniqueSeriesIDs) > 0 {
		series, err = h.db.Series().GetByIDs(r.Context(), uniqueSeriesIDs)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "series_lookup_failed", "获取系列信息失败: "+err.Error())
			return
		}
		series = orderSeriesByIDs(series, uniqueSeriesIDs)
	}
	limit := len(series)
	if limit < 1 {
		limit = 1
	}
	respondList(w, http.StatusOK, toSeriesResponses(series), makePagination(1, limit, int64(len(series))))
}

func imageSearchTempPattern(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif":
		return "upload-*" + ext
	default:
		return "upload-*.img"
	}
}

func orderSeriesByIDs(series []models.Series, ids []primitive.ObjectID) []models.Series {
	seriesByID := make(map[primitive.ObjectID]models.Series, len(series))
	for _, item := range series {
		seriesByID[item.ID] = item
	}

	ordered := make([]models.Series, 0, len(series))
	for _, id := range ids {
		if item, ok := seriesByID[id]; ok {
			ordered = append(ordered, item)
			delete(seriesByID, id)
		}
	}

	if len(seriesByID) == 0 {
		return ordered
	}
	remaining := make([]models.Series, 0, len(seriesByID))
	for _, item := range seriesByID {
		remaining = append(remaining, item)
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].Name < remaining[j].Name
	})
	return append(ordered, remaining...)
}

// --- 配置处理器 ---

// HandleGetConfig 获取当前应用配置
func (h *APIHandlers) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, safeConfigForResponse(config.C))
}

// HandleUpdateConfig 更新并保存应用配置
func (h *APIHandlers) HandleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var newConfig config.Config
	if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_config", "无效的配置格式: "+err.Error())
		return
	}
	if err := config.ValidateConfig(newConfig); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_config", "配置校验失败: "+err.Error())
		return
	}

	runtimeConfig := newConfig
	if shouldPreserveLocalOnlyConfig(r) && config.C != nil {
		preserveLocalOnlyConfig(&runtimeConfig, config.C)
	}
	if isRedactedURI(runtimeConfig.Database.URI) && config.C != nil {
		runtimeConfig.Database.URI = config.C.Database.URI
	}
	if runtimeConfig.Server.MaintenanceToken == "xxxxx" && config.C != nil {
		runtimeConfig.Server.MaintenanceToken = config.C.Server.MaintenanceToken
	}
	fileConfig := runtimeConfig
	fileConfig.Database.URI = sanitizeURIForConfigFile(fileConfig.Database.URI)

	yamlData, err := yaml.Marshal(&fileConfig)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "config_marshal_failed", "序列化配置为YAML失败: "+err.Error())
		return
	}

	configPath := h.configPath
	if configPath == "" {
		configPath = "config.yaml"
	}
	if err := writeFileAtomic(configPath, yamlData, 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "config_write_failed", "写入config.yaml文件失败: "+err.Error())
		return
	}

	if config.C == nil {
		config.C = &runtimeConfig
	} else {
		*config.C = runtimeConfig
	}
	if h.taskManager != nil {
		h.taskManager.UpdateConfig(runtimeConfig)
	}

	respondJSON(w, http.StatusOK, safeConfigForResponse(config.C))
}

func shouldPreserveLocalOnlyConfig(r *http.Request) bool {
	if config.C == nil || !config.C.Security.Enabled {
		return false
	}
	return !isLocalRequest(r)
}

func remoteRestrictedMaintenance(r *http.Request) bool {
	return shouldPreserveLocalOnlyConfig(r)
}

func preserveLocalOnlyConfig(next *config.Config, current *config.Config) {
	next.Database = current.Database
	next.Logger = current.Logger
	next.Server.Port = current.Server.Port
	next.Server.Timeout = current.Server.Timeout
	next.Scanner.ScanPath = current.Scanner.ScanPath
	next.Scanner.StagingPath = current.Scanner.StagingPath
	next.Scanner.FinalLibraryPath = current.Scanner.FinalLibraryPath
	next.Scanner.BackupPath = current.Scanner.BackupPath
	next.Scanner.QuarantinePath = current.Scanner.QuarantinePath
	next.Scanner.CorruptionLogPath = current.Scanner.CorruptionLogPath
	next.Scanner.DuplicatesDir = current.Scanner.DuplicatesDir
}

func safeConfigForResponse(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	safe := *cfg
	safe.Database.URI = redactURISecret(safe.Database.URI)
	if safe.Server.MaintenanceToken != "" {
		safe.Server.MaintenanceToken = "xxxxx"
	}
	return &safe
}

func redactURISecret(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	username := parsed.User.Username()
	if username == "" {
		parsed.User = url.UserPassword("", "xxxxx")
		return parsed.String()
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		parsed.User = url.UserPassword(username, "xxxxx")
	}
	return parsed.String()
}

func sanitizeURIForConfigFile(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}

func isRedactedURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return strings.Contains(raw, "xxxxx")
	}
	password, hasPassword := parsed.User.Password()
	return hasPassword && password == "xxxxx"
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
