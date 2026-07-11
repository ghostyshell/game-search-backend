// Package middleware builds the HTTP router, CORS, and logger/recovery.
package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"

	"game-search-backend/internal/config"
	"game-search-backend/internal/handlers"
)

// Server bundles the handler groups + the mux.
type Server struct {
	Games   *handlers.GamesHandler
	Debrid  *handlers.DebridHandler
	Health  *handlers.HealthHandler
	cfg     *config.Config
	mux     *http.ServeMux
}

// NewServer wires handlers and builds the mux.
func NewServer(cfg *config.Config, gh *handlers.GamesHandler, dh *handlers.DebridHandler, hh *handlers.HealthHandler) *Server {
	s := &Server{Games: gh, Debrid: dh, Health: hh, cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/health", s.Health.Health)
	s.mux.HandleFunc("/stats", s.Health.Stats)
	s.mux.HandleFunc("/debrid/hosts", s.Debrid.Hosts)
	// /games and /games/{slug} share a prefix; dispatch by path.
	s.mux.HandleFunc("/games", s.Games.BrowseOrSearch)
	s.mux.HandleFunc("/games/", s.gamesBySlug)
}

// gamesBySlug dispatches /games/{slug}. /games alone (no trailing slug) goes to
// BrowseOrSearch (registered above).
func (s *Server) gamesBySlug(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/games/")
	if slug == "" {
		s.Games.BrowseOrSearch(w, r)
		return
	}
	if r.URL.Query().Get("q") != "" && slug == "search" {
		s.Games.BrowseOrSearch(w, r)
		return
	}
	s.Games.GameDetail(w, r, slug)
}

// ServeHTTP routes through CORS + logging + recovery.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cors(s.cfg.CORS)(logging(recoverer(s.mux))).ServeHTTP(w, r)
}

// cors wraps with permissive-but-configured CORS.
func cors(c config.CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowed := false
			wildcard := false
			for _, o := range c.Origins {
				if o == "*" {
					wildcard = true
					allowed = true
					break
				}
				if o == origin {
					allowed = true
					break
				}
			}
			if allowed {
				// A wildcard is echoed as the literal "*" and never paired with
				// Allow-Credentials: browsers reject credentials under "*" anyway,
				// and reflecting the request origin under a wildcard config would
				// turn the policy into an open reflector. An explicit origin is
				// echoed back and may carry credentials.
				if wildcard {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					if c.Credentials {
						w.Header().Set("Access-Control-Allow-Credentials", "true")
					}
				}
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", strings.Join(c.Methods, ", "))
				w.Header().Set("Access-Control-Allow-Headers", strings.Join(c.AllowedHeaders, ", "))
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}