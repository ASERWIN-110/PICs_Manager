// 文件: internal/api/routes.go
package api

import (
	"PICs_Manager/internal/task"
	"PICs_Manager/pkg/database"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// RegisterRoutes 注册所有API路由
func RegisterRoutes(tm *task.Manager, db database.Store) *chi.Mux {
	r := chi.NewRouter()

	// --- 中间件 (Middleware) ---
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// 配置CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	
	handlers := NewAPIHandlers(tm, db)

	// --- API路由 ---
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/tasks/scan", handlers.HandleStartScanTask)
		r.Get("/tasks/{taskId}", handlers.HandleGetTaskStatus)
		r.Get("/series", handlers.HandleListSeries)
		r.Get("/series/{seriesID}/images", handlers.HandleListImagesBySeries)
		r.Get("/search/text", handlers.HandleSearchText)
		r.Post("/search/image", handlers.HandleSearchByImage)
		r.Get("/config", handlers.HandleGetConfig)
		r.Put("/config", handlers.HandleUpdateConfig)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	return r
}
