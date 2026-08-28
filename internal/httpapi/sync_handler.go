package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
)

func (h *Handler) synchronize(w http.ResponseWriter, r *http.Request) {
	// Parse X-Sync-Protocol header for feature negotiation.
	protoVersion := 1
	if v := r.Header.Get("X-Sync-Protocol"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			protoVersion = n
		}
	}

	var body syncservice.Request
	if !h.decodeJSON(w, r, &body) {
		return
	}
	response, err := h.sync.Sync(r.Context(), principalFromContext(r.Context()).UserID, body, protoVersion)
	if err != nil {
		var clientErr syncservice.ClientError
		if errors.As(err, &clientErr) {
			// cursor_expired signals that the client must perform a full resync.
			status := http.StatusBadRequest
			if clientErr.Code == "cursor_expired" {
				status = http.StatusConflict
			}
			h.writeError(w, r, status, clientErr.Code, clientErr.Message)
			return
		}
		h.logger.Error("sync_failed", "error", err)
		h.writeError(w, r, http.StatusInternalServerError, "internal_error", "synchronization could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

