package httpapi

import (
	"errors"
	"net/http"

	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
)

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request) {
	var body syncservice.SnapshotRequest
	if !h.decodeJSON(w, r, &body) {
		return
	}
	response, err := h.snapshot_.Snapshot(r.Context(), principalFromContext(r.Context()).UserID, body)
	if err != nil {
		var clientErr syncservice.ClientError
		if errors.As(err, &clientErr) {
			h.writeError(w, r, http.StatusBadRequest, clientErr.Code, clientErr.Message)
			return
		}
		h.logger.Error("snapshot_failed", "error", err)
		h.writeError(w, r, http.StatusInternalServerError, "internal_error", "snapshot could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
