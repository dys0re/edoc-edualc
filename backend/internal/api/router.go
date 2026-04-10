package api

import (
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/remote"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRouter creates the Gin engine with all routes.
func NewRouter(p provider.Provider, cfg *config.Config, workDir string, memoryStore *memory.Store, sessionStore *session.Store, pool *pgxpool.Pool) *gin.Engine {
	r := gin.Default()
	r.Use(CORSMiddleware())

	h := NewHandler(p, cfg, workDir, memoryStore, sessionStore, pool)

	// Remote session manager (shared across all WS connections)
	remoteMgr := remote.NewManager()
	rh := remote.NewHandler(remoteMgr, p, cfg, workDir, memoryStore, sessionStore)

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
		api.POST("/sessions/:id/compact", h.SessionCompactSSE)

		// Remote sessions (WebSocket)
		api.GET("/remote", rh.ListSessions)
		api.GET("/remote/:session_id/status", rh.Status)
		api.GET("/remote/:session_id/ws", rh.Connect)

		// Other
		api.POST("/compact", h.CompactSSE)
		api.POST("/command", h.Command)
		api.GET("/models", h.Models)
	}

	return r
}
