package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/admin"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
)

func (h *Handler) adminOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := h.admin.Overview(r.Context())
	if err != nil {
		h.logger.Error("admin_overview_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) adminRequestStats(w http.ResponseWriter, r *http.Request) {
	query, ok := parseStatsRange(w, r)
	if !ok {
		return
	}
	series, err := h.admin.RequestStats(r.Context(), query)
	if err != nil {
		h.writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *Handler) adminErrorStats(w http.ResponseWriter, r *http.Request) {
	query, ok := parseStatsRange(w, r)
	if !ok {
		return
	}
	series, err := h.admin.ErrorStats(r.Context(), query)
	if err != nil {
		h.writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func parseStatsRange(w http.ResponseWriter, r *http.Request) (admin.QueryRange, bool) {
	query := r.URL.Query()
	from, ok := parseOptionalTime(w, query.Get("from"), "from")
	if !ok {
		return admin.QueryRange{}, false
	}
	to, ok := parseOptionalTime(w, query.Get("to"), "to")
	if !ok {
		return admin.QueryRange{}, false
	}
	return admin.QueryRange{From: from, To: to, Interval: query.Get("interval")}, true
}

func parseOptionalTime(w http.ResponseWriter, value, name string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_range", name+" must be an RFC3339 timestamp")
		return time.Time{}, false
	}
	return parsed, true
}

func (h *Handler) writeAdminError(w http.ResponseWriter, err error) {
	var adminErr admin.Error
	if errors.As(err, &adminErr) {
		writeError(w, http.StatusBadRequest, adminErr.Code, adminErr.Message)
		return
	}
	h.logger.Error("admin_stats_failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFromContext(r.Context()).Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "admin access is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
