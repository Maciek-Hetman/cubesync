package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type principalKey struct{}

type principal struct {
	UserID        uuid.UUID
	EmailVerified bool
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.auth.Register(r.Context(), body.Email, body.Password); err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_required"})
}

func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.auth.ResendVerification(r.Context(), body.Email); err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h *Handler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	rawToken := r.URL.Query().Get("token")
	session, err := h.auth.VerifyEmail(r.Context(), rawToken)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	session, err := h.auth.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	session, err := h.auth.Refresh(r.Context(), body.RefreshToken)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.auth.Logout(r.Context(), body.RefreshToken); err != nil {
		h.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.auth.ForgotPassword(r.Context(), body.Email); err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	session, err := h.auth.ResetPassword(r.Context(), body.Token, body.NewPassword)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) federatedLogin(w http.ResponseWriter, r *http.Request) {
	var body auth.FederatedInput
	if !decodeJSON(w, r, &body) {
		return
	}
	session, err := h.auth.FederatedLogin(r.Context(), chi.URLParam(r, "provider"), body)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) linkFederated(w http.ResponseWriter, r *http.Request) {
	var body auth.FederatedInput
	if !decodeJSON(w, r, &body) {
		return
	}
	p := principalFromContext(r.Context())
	if err := h.auth.LinkFederated(r.Context(), p.UserID, chi.URLParam(r, "provider"), body); err != nil {
		h.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, err := h.auth.User(r.Context(), principalFromContext(r.Context()).UserID)
	if err != nil {
		h.logger.Error("get_profile_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		scheme, token, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
			return
		}
		userID, claims, err := h.auth.TokenManager().ParseAccessToken(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal{
			UserID: userID, EmailVerified: claims.EmailVerified,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) requireVerified(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalFromContext(r.Context()).EmailVerified {
			writeError(w, http.StatusForbidden, "email_not_verified", "verify your email before synchronizing")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func principalFromContext(ctx context.Context) principal {
	value, _ := ctx.Value(principalKey{}).(principal)
	return value
}

func (h *Handler) writeAuthError(w http.ResponseWriter, err error) {
	var authErr auth.Error
	if !errors.As(err, &authErr) {
		h.logger.Error("auth_request_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	status := http.StatusBadRequest
	switch authErr.Code {
	case "invalid_credentials", "invalid_refresh_token", "invalid_social_token", "invalid_social_code":
		status = http.StatusUnauthorized
	case "email_not_verified":
		status = http.StatusForbidden
	case "email_in_use", "account_link_required", "identity_in_use", "provider_already_linked", "refresh_token_reused":
		status = http.StatusConflict
	case "email_delivery_failed", "provider_not_configured":
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, authErr.Code, authErr.Message)
}
