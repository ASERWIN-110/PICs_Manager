// 文件: internal/api/routes.go
package api

import (
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/runstate"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// RegisterRoutesWithRunStore 注册所有API路由。
func RegisterRoutesWithRunStore(tm *task.Manager, db database.Store, runStores ...*runstate.Store) *chi.Mux {
	var runs *runstate.Store
	if len(runStores) > 0 {
		runs = runStores[0]
	}
	r := chi.NewRouter()

	// --- 中间件 (Middleware) ---
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// 配置CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "X-Maintenance-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	handlers := NewAPIHandlersWithRunStore(tm, db, runs)

	// --- API路由 ---
	r.Route("/api/v1", func(r chi.Router) {
		r.With(handlers.RequireMaintenanceAuth).Post("/tasks", handlers.HandleStartScanTask)
		r.Get("/tasks/{taskId}", handlers.HandleGetTaskStatus)
		r.With(handlers.RequireMaintenanceAuth).Post("/tasks/{taskId}/pause", handlers.HandlePauseTask)
		r.With(handlers.RequireMaintenanceAuth).Delete("/tasks/{taskId}", handlers.HandleStopTask)
		r.Get("/runs", handlers.HandleListRuns)
		r.Get("/runs/{runId}", handlers.HandleGetRun)
		r.Get("/runs/{runId}/journal", handlers.HandleGetRunJournal)
		r.Get("/series", handlers.HandleListSeries)
		r.Get("/series/{seriesId}/thumbnail", handlers.HandleSeriesThumbnail)
		r.Get("/series/{seriesId}/images", handlers.HandleListMediaBySeries)
		r.Get("/series/{seriesId}/media/{mediaType}", handlers.HandleListMediaBySeries)
		r.Get("/images/{imageId}/thumbnail", handlers.HandleMediaThumbnail)
		r.Get("/search/text", handlers.HandleSearchText)
		r.Post("/search/image", handlers.HandleSearchByImage)
		r.Get("/config", handlers.HandleGetConfig)
		r.With(handlers.RequireMaintenanceAuth).Put("/config", handlers.HandleUpdateConfig)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	return r
}
