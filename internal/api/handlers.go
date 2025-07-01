// 文件: internal/api/handlers.go
package api

import (
	"PICs_Manager/config" // [修正] 引入您项目根目录下的config包
	"PICs_Manager/internal/models"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/hasher"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"gopkg.in/yaml.v3" // [新增] 引入YAML库来保存配置
)

// APIHandlers 持有所有依赖
type APIHandlers struct {
	taskManager *task.Manager
	db          database.Store
	// [修正] 移除 config 字段，我们将使用全局的 config.C
}

// NewAPIHandlers 创建一个新的API处理器实例
// [修正] 移除 config 参数
func NewAPIHandlers(tm *task.Manager, db database.Store) *APIHandlers {
	return &APIHandlers{
		taskManager: tm,
		db:          db,
	}
}

// --- 辅助函数 ---

// [新增] respondJSON 辅助函数，用于统一返回JSON响应
func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
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

// [新增] respondError 辅助函数，用于统一返回错误信息
func respondError(w http.ResponseWriter, code int, message string) {
	respondJSON(w, code, map[string]string{"error": message})
}

// --- 任务处理器 ---

func (h *APIHandlers) HandleStartScanTask(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "无效的请求体: "+err.Error())
		return
	}
	if payload.Path == "" {
		respondError(w, http.StatusBadRequest, "缺少 'path' 字段")
		return
	}
	taskID, err := h.taskManager.StartNewScanTask(payload.Path)
	if err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"taskId": taskID})
}

func (h *APIHandlers) HandleGetTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	status, err := h.taskManager.GetTaskStatus(taskID)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, status)
}

// --- 系列处理器 ---

func (h *APIHandlers) HandleListSeries(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 20
	}
	series, total, err := h.db.Series().List(r.Context(), page, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "无法获取系列列表: "+err.Error())
		return
	}
	response := map[string]interface{}{
		"data": series,
		"pagination": map[string]interface{}{
			"currentPage": page,
			"totalPages":  int(math.Ceil(float64(total) / float64(limit))),
			"totalItems":  total,
		},
	}
	respondJSON(w, http.StatusOK, response)
}

func (h *APIHandlers) HandleListImagesBySeries(w http.ResponseWriter, r *http.Request) {
	seriesID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "seriesID"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "无效的系列ID")
		return
	}
	images, err := h.db.Images().GetAllBySeriesID(r.Context(), seriesID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "无法获取图片列表: "+err.Error())
		return
	}
	respondJSON(w, http.StatusOK, images)
}

// --- 搜索处理器 ---

func (h *APIHandlers) HandleSearchText(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		respondError(w, http.StatusBadRequest, "缺少搜索查询参数 'q'")
		return
	}
	series, total, err := h.db.Series().SearchByName(r.Context(), query, 1, 100)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "搜索系列失败: "+err.Error())
		return
	}
	response := map[string]interface{}{
		"data": series,
		"pagination": map[string]interface{}{
			"currentPage": 1,
			"totalPages":  1,
			"totalItems":  total,
		},
	}
	respondJSON(w, http.StatusOK, response)
}

func (h *APIHandlers) HandleSearchByImage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		respondError(w, http.StatusBadRequest, "无法解析表单: "+err.Error())
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		respondError(w, http.StatusBadRequest, "获取上传文件失败: "+err.Error())
		return
	}
	defer file.Close()
	tempFile, err := os.CreateTemp("", "upload-*.png")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "创建临时文件失败")
		return
	}
	defer os.Remove(tempFile.Name())
	if _, err := io.Copy(tempFile, file); err != nil {
		respondError(w, http.StatusInternalServerError, "写入临时文件失败")
		return
	}
	tempFile.Close()
	pHash, err := hasher.CalculatePerceptualHash(tempFile.Name())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "计算图片哈希失败: "+err.Error())
		return
	}
	similarImages, err := h.db.Images().FindSimilarByPHash(r.Context(), pHash, 50)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "数据库查找失败: "+err.Error())
		return
	}
	seriesIDs := make(map[primitive.ObjectID]bool)
	for _, img := range similarImages {
		seriesIDs[img.SeriesID] = true
	}
	var uniqueSeriesIDs []primitive.ObjectID
	for id := range seriesIDs {
		uniqueSeriesIDs = append(uniqueSeriesIDs, id)
	}
	var series []models.Series
	if len(uniqueSeriesIDs) > 0 {
		series, err = h.db.Series().GetByIDs(r.Context(), uniqueSeriesIDs)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "获取系列信息失败: "+err.Error())
			return
		}
	}
	response := map[string]interface{}{
		"data": series,
		"pagination": map[string]interface{}{
			"currentPage": 1,
			"totalPages":  1,
			"totalItems":  len(series),
		},
	}
	respondJSON(w, http.StatusOK, response)
}

// --- 配置处理器 ---

// HandleGetConfig 获取当前应用配置
func (h *APIHandlers) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	// [修正] 直接返回全局配置变量 config.C
	respondJSON(w, http.StatusOK, config.C)
}

// HandleUpdateConfig 更新并保存应用配置
func (h *APIHandlers) HandleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var newConfig config.Config
	if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
		respondError(w, http.StatusBadRequest, "无效的配置格式: "+err.Error())
		return
	}

	// [修正] 实现将配置写回 config.yaml 文件的逻辑
	// 1. 将接收到的新配置数据序列化为YAML格式
	yamlData, err := yaml.Marshal(&newConfig)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "序列化配置为YAML失败: "+err.Error())
		return
	}

	// 2. 将YAML数据写入到 config.yaml 文件
	// 假设配置文件在当前工作目录
	if err := os.WriteFile("config.yaml", yamlData, 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "写入config.yaml文件失败: "+err.Error())
		return
	}

	// 3. 更新内存中的全局配置变量
	config.C = &newConfig

	respondJSON(w, http.StatusOK, config.C)
}
