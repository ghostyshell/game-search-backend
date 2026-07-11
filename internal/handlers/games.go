// Package handlers exposes the HTTP API for game-search-backend.
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"game-search-backend/internal/models"
	"game-search-backend/pkg/storage"
)

// GamesHandler holds the DB handle for game endpoints.
type GamesHandler struct {
	DB storage.Database
}

// BrowseOrSearch handles GET /games and GET /games/search.
// Query params: q (optional title query), platform (optional), limit, offset.
func (h *GamesHandler) BrowseOrSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	platform := models.Platform(strings.TrimSpace(r.URL.Query().Get("platform")))
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)

	var (
		games []models.Game
		err   error
	)
	if q != "" {
		games, err = h.DB.SearchGames(r.Context(), q, platform, limit, offset)
	} else {
		games, err = h.DB.BrowseGames(r.Context(), platform, limit, offset)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if games == nil {
		games = []models.Game{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"games":  games,
		"limit":   limit,
		"offset":  offset,
		"query":   q,
		"platform": string(platform),
	})
}

// GameDetail handles GET /games/{slug}.
func (h *GamesHandler) GameDetail(w http.ResponseWriter, r *http.Request, slug string) {
	game, links, err := h.DB.GetGameBySlug(r.Context(), slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "game not found"})
		return
	}
	if links == nil {
		links = []models.GameLink{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"game":  game,
		"links": links,
	})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}