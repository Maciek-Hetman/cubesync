package httpapi

import (
	"net/http"

	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
)

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	event := r.URL.Query().Get("event")
	req := syncservice.StatsRequest{Event: event}
	response, err := h.stats_.ComputeStats(r.Context(), principalFromContext(r.Context()).UserID, req)
	if err != nil {
		h.logger.Error("stats_failed", "error", err)
		h.writeError(w, r, http.StatusInternalServerError, "internal_error", "statistics could not be computed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
