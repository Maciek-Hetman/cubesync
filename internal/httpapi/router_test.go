package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
)

func TestCORSIncludesPUTMethod(t *testing.T) {
	t.Parallel()

	h := &Handler{
		config: config.Config{
			AllowedOrigins: []string{"https://app.cubetimer.io"},
		},
	}

	corsMiddleware := h.cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/v1/me/password", nil)
	req.Header.Set("Origin", "https://app.cubetimer.io")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	w := httptest.NewRecorder()

	corsMiddleware.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 No Content for OPTIONS, got %d", w.Code)
	}

	allowedMethods := w.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowedMethods, "PUT") {
		t.Fatalf("expected Access-Control-Allow-Methods to contain PUT, got %q", allowedMethods)
	}
	if !strings.Contains(allowedMethods, "GET") || !strings.Contains(allowedMethods, "POST") || !strings.Contains(allowedMethods, "DELETE") {
		t.Fatalf("expected full standard methods in header, got %q", allowedMethods)
	}
}
