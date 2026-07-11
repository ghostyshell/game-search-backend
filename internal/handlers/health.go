package handlers

import (
	"net/http"
	"time"

	"game-search-backend/pkg/storage"
)

// HealthHandler returns liveness + DB health.
type HealthHandler struct {
	DB storage.Database
}

// Health handles GET /health.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	dbErr := h.DB.HealthCheck(r.Context())
	resp := map[string]any{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"responseTimeMs": time.Since(start).Milliseconds(),
	}
	if dbErr != nil {
		resp["status"] = "degraded"
		resp["database"] = dbErr.Error()
	} else {
		resp["database"] = "ok"
	}
	writeJSON(w, http.StatusOK, resp)
}

// Stats handles GET /stats (collection counts).
func (h *HealthHandler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.DB.Stats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}