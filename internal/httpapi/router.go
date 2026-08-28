package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/admin"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	config    config.Config
	db        *pgxpool.Pool
	logger    *slog.Logger
	auth      *auth.Service
	sync      *syncservice.Service
	admin     *admin.Service
	snapshot_ *syncservice.SnapshotService
	stats_    *syncservice.StatsService
}

func NewRouter(cfg config.Config, db *pgxpool.Pool, logger *slog.Logger) http.Handler {
	authService := auth.NewService(cfg, db, auth.NewMailer(cfg, logger), auth.NewOIDCVerifier(cfg))
	h := &Handler{
		config:    cfg,
		db:        db,
		logger:    logger,
		auth:      authService,
		sync:      syncservice.NewService(db, cfg.MaxSyncMutations, cfg.MaxSyncChanges, cfg.MaxSyncResponseBytes),
		admin:     admin.NewService(db),
		snapshot_: syncservice.NewSnapshotService(db, cfg.MaxSyncResponseBytes),
		stats_:    syncservice.NewStatsService(db),
	}
	authLimit := newIPRateLimiter(10, 5)
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(h.accessLog)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(h.securityHeaders)
	r.Use(h.cors)
	if cfg.EnableCompression {
		r.Use(middleware.Compress(5, "application/json"))
	}

	r.Get("/health/live", h.live)
	r.Get("/health/ready", h.ready)
	r.Get("/v1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"name": "CubeTimer Sync API", "version": "v1"})
	})
	r.Route("/v1/auth", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authLimit.middleware)
			r.Post("/register", h.register)
			r.Post("/email/resend", h.resendVerification)
			r.Post("/email/verify", h.verifyEmail)
			r.Post("/login", h.login)
			r.Post("/refresh", h.refresh)
			r.Post("/logout", h.logout)
			r.Post("/password/forgot", h.forgotPassword)
			r.Post("/password/reset", h.resetPassword)
			r.Post("/federated/{provider}", h.federatedLogin)
		})
		r.Group(func(r chi.Router) {
			r.Use(h.authenticate)
			r.Post("/link/{provider}", h.linkFederated)
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/v1/me", h.me)
		r.With(h.requireVerified).Post("/v1/sync", h.synchronize)
		r.With(h.requireVerified).Post("/v1/snapshot", h.snapshot)
		r.With(h.requireVerified).Get("/v1/stats", h.stats)
		r.With(h.requireVerified).Get("/v1/sessions", h.listSessions)
		r.With(h.requireVerified).Get("/v1/sessions/{id}/solves", h.listSessionSolves)
		r.Group(func(r chi.Router) {
			r.Use(h.requireAdmin)
			r.Get("/v1/admin/stats/overview", h.adminOverview)
			r.Get("/v1/admin/stats/requests", h.adminRequestStats)
			r.Get("/v1/admin/stats/errors", h.adminErrorStats)
		})
	})

	return r
}

func (h *Handler) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.config.ReadinessTimeout)
	defer cancel()
	if err := h.db.Ping(ctx); err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "not_ready", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) cors(next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(h.config.AllowedOrigins))
	for _, origin := range h.config.AllowedOrigins {
		allowed[origin] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := allowed[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Sync-Protocol")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		duration := time.Since(started)
		status := ww.Status()
		h.logger.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", ww.BytesWritten(),
			"duration_ms", duration.Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
		route := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			route = rc.RoutePattern()
		}
		h.admin.RecordRequestAsync(r.Method, route, status, duration)
	})
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if h != nil && h.admin != nil && r != nil {
		userID := principalFromContext(r.Context()).UserID
		route := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			route = rc.RoutePattern()
		}
		h.admin.RecordErrorAsync(userID, r.Method, route, status, code, message)
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	return true
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
