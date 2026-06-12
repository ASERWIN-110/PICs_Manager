// 文件: internal/api/routes.go
package api

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"PICs_Manager/pkg/runstate"
	"PICs_Manager/pkg/security"
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
	return RegisterRoutesWithServices(tm, db, runs, nil)
}

func RegisterRoutesWithServices(tm *task.Manager, db database.Store, runs *runstate.Store, authStore *security.Store) *chi.Mux {
	r := chi.NewRouter()

	// --- 中间件 (Middleware) ---
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// 配置CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsAllowedOrigins(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "X-Maintenance-Token"},
		ExposedHeaders:   []string{"Link", "Content-Disposition"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	handlers := NewAPIHandlersWithServices(tm, db, runs, authStore)

	// --- API路由 ---
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/auth/status", handlers.HandleAuthStatus)
		r.Post("/auth/claim", handlers.HandleClaimPairingCode)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Post("/tasks", handlers.HandleStartScanTask)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Get("/tasks/{taskId}", handlers.HandleGetTaskStatus)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Post("/tasks/{taskId}/pause", handlers.HandlePauseTask)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Delete("/tasks/{taskId}", handlers.HandleStopTask)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Get("/runs", handlers.HandleListRuns)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Get("/runs/{runId}", handlers.HandleGetRun)
		r.With(handlers.RequireScope(security.ScopeMaintainer)).Get("/runs/{runId}/journal", handlers.HandleGetRunJournal)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/series", handlers.HandleListSeries)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/series/{seriesId}/thumbnail", handlers.HandleSeriesThumbnail)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/series/{seriesId}/download", handlers.HandleSeriesDownload)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/series/{seriesId}/images", handlers.HandleListMediaBySeries)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/series/{seriesId}/media/{mediaType}", handlers.HandleListMediaBySeries)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/images/{imageId}/thumbnail", handlers.HandleMediaThumbnail)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/media/{mediaId}/download", handlers.HandleMediaDownload)
		r.With(handlers.RequireScope(security.ScopeViewer)).Get("/search/text", handlers.HandleSearchText)
		r.With(handlers.RequireScope(security.ScopeViewer)).Post("/search/image", handlers.HandleSearchByImage)
		r.With(handlers.RequireScope(security.ScopeAdmin)).Get("/config", handlers.HandleGetConfig)
		r.With(handlers.RequireScope(security.ScopeAdmin)).Put("/config", handlers.HandleUpdateConfig)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	return r
}

func corsAllowedOrigins() []string {
	if config.C != nil && len(config.C.Security.CORSAllowedOrigins) > 0 {
		return config.C.Security.CORSAllowedOrigins
	}
	return []string{"http://localhost:*", "http://127.0.0.1:*"}
}
