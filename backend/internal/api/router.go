package api

import (
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/gin-gonic/gin"
)

// NewRouter creates the Gin engine with all routes.
func NewRouter(p provider.Provider, cfg *config.Config, workDir string, memoryStore *memory.Store, sessionStore *session.Store) *gin.Engine {
	r := gin.Default()
	r.Use(CORSMiddleware())

	h := NewHandler(p, cfg, workDir, memoryStore, sessionStore)

	r.GET("/health", h.Health)

	api := r.Group("/api")
	{
		// Chat (stateless)
		api.POST("/chat", h.ChatSSE)

		// Sessions (stateful, requires PG)
		api.POST("/sessions", h.CreateSession)
		api.GET("/sessions", h.ListSessions)
		api.GET("/sessions/:id", h.GetSession)
		api.DELETE("/sessions/:id", h.DeleteSession)
		api.PATCH("/sessions/:id", h.UpdateSession)
		api.POST("/sessions/:id/chat", h.SessionChatSSE)

		// Other
		api.POST("/compact", h.CompactSSE)
		api.GET("/models", h.Models)
	}

	return r
}
