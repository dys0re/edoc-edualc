package api

import (
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/gin-gonic/gin"
)

// NewRouter creates the Gin engine with all routes.
func NewRouter(p provider.Provider, cfg *config.Config, workDir string, memoryStore *memory.Store) *gin.Engine {
	r := gin.Default()
	r.Use(CORSMiddleware())

	h := NewHandler(p, cfg, workDir, memoryStore)

	r.GET("/health", h.Health)

	api := r.Group("/api")
	{
		api.POST("/chat", h.ChatSSE)
		api.POST("/compact", h.CompactSSE)
		api.GET("/models", h.Models)
	}

	return r
}
