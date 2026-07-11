package handlers

import (
	"net/http"

	"game-search-backend/internal/models"
	"game-search-backend/pkg/storage"
)

// DebridHandler exposes the cached host -> providers map so the frontend can
// show which providers support a given link's host without calling the
// providers itself.
type DebridHandler struct {
	DB storage.Database
}

// Hosts handles GET /debrid/hosts.
func (h *DebridHandler) Hosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.DB.GetDebridHosts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if hosts == nil {
		hosts = map[string][]models.DebridProvider{}
	}
	// Re-shape to host -> []string for JSON friendliness.
	out := make(map[string][]string, len(hosts))
	for host, providers := range hosts {
		ps := make([]string, 0, len(providers))
		for _, p := range providers {
			ps = append(ps, string(p))
		}
		out[host] = ps
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}