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
		h.writeError(w, r, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) adminRequestStats(w http.ResponseWriter, r *http.Request) {
	query, ok := h.parseStatsRange(w, r)
	if !ok {
		return
	}
	series, err := h.admin.RequestStats(r.Context(), query)
	if err != nil {
		h.writeAdminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *Handler) adminRequestTypeStats(w http.ResponseWriter, r *http.Request) {
	query, ok := h.parseStatsRange(w, r)
	if !ok {
		return
	}
	series, err := h.admin.RequestTypeStats(r.Context(), query)
	if err != nil {
		h.writeAdminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *Handler) adminErrorStats(w http.ResponseWriter, r *http.Request) {
	beforeStr := r.URL.Query().Get("before")
	var before time.Time
	if beforeStr != "" {
		var err error
		before, err = time.Parse(time.RFC3339, beforeStr)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, "invalid_cursor", "before must be an RFC3339 timestamp")
			return
		}
	}
	limit := 50

	resp, err := h.admin.ListErrors(r.Context(), before, limit)
	if err != nil {
		h.writeAdminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) parseStatsRange(w http.ResponseWriter, r *http.Request) (admin.QueryRange, bool) {
	query := r.URL.Query()
	from, ok := h.parseOptionalTime(w, r, query.Get("from"), "from")
	if !ok {
		return admin.QueryRange{}, false
	}
	to, ok := h.parseOptionalTime(w, r, query.Get("to"), "to")
	if !ok {
		return admin.QueryRange{}, false
	}
	return admin.QueryRange{From: from, To: to, Interval: query.Get("interval")}, true
}

func (h *Handler) parseOptionalTime(w http.ResponseWriter, r *http.Request, value, name string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_range", name+" must be an RFC3339 timestamp")
		return time.Time{}, false
	}
	return parsed, true
}

func (h *Handler) writeAdminError(w http.ResponseWriter, r *http.Request, err error) {
	var adminErr admin.Error
	if errors.As(err, &adminErr) {
		h.writeError(w, r, http.StatusBadRequest, adminErr.Code, adminErr.Message)
		return
	}
	h.logger.Error("admin_stats_failed", "error", err)
	h.writeError(w, r, http.StatusInternalServerError, "internal_error", "request could not be completed")
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFromContext(r.Context()).Role != auth.RoleAdmin {
			h.writeError(w, r, http.StatusForbidden, "forbidden", "admin access is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
