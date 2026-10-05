package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/edp/edp-control-plane/internal/middleware"
	"github.com/edp/edp-control-plane/internal/platform"
	"github.com/google/uuid"
)

// Handler handles authentication HTTP requests.
type Handler struct {
	service      Service
	authenticator *middleware.Authenticator
}

// Service wraps the AuthService for HTTP handlers.
type Service interface {
	Login(ctx context.Context, email, password string) (*LoginResponse, error)
	RefreshToken(ctx context.Context, tokenString string) (*RefreshResponse, error)
	Logout(ctx context.Context, tokenString string) error
	LogoutAll(ctx context.Context, userID uuid.UUID) error
	Me(ctx context.Context, userID uuid.UUID) (*MeResponse, error)
	ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error
}

// NewHandler creates a new authentication handler.
func NewHandler(service Service, authenticator *middleware.Authenticator) *Handler {
	return &Handler{
		service:      service,
		authenticator: authenticator,
	}
}

// LoginHandler handles POST /api/v1/auth/login
func (h *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := parseJSONBody(r, &req); err != nil {
		platform.Fail(w, r, err)
		return
	}

	resp, err := h.service.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	// Set cookies - handled by the router/middleware, but we return the info
	platform.JSON(w, http.StatusOK, resp)
}

// RefreshHandler handles POST /api/v1/auth/refresh
func (h *Handler) RefreshHandler(w http.ResponseWriter, r *http.Request) {
	// Read refresh token from cookie
	refreshToken, err := r.Cookie("edp_refresh_token")
	if err != nil {
		platform.Fail(w, r, ErrMissingRefreshToken)
		return
	}

	resp, err := h.service.RefreshToken(r.Context(), refreshToken.Value)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	// Set new cookies - handled by middleware
	platform.JSON(w, http.StatusOK, resp)
}

// LogoutHandler handles POST /api/v1/auth/logout
func (h *Handler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	refreshToken, err := r.Cookie("edp_refresh_token")
	if err != nil {
		// Already logged out, succeed anyway
		platform.JSON(w, http.StatusNoContent, nil)
		return
	}

	if err := h.service.Logout(r.Context(), refreshToken.Value); err != nil {
		platform.Fail(w, r, err)
		return
	}

	// Clear refresh token cookie
	clearRefreshCookie(w)

	platform.JSON(w, http.StatusNoContent, nil)
}

// LogoutAllHandler handles POST /api/v1/auth/logout-all
func (h *Handler) LogoutAllHandler(w http.ResponseWriter, r *http.Request) {
	// Get user ID from context (set by auth middleware)
	actor := platform.ActorFrom(r.Context())
	if actor == nil {
		platform.Fail(w, r, platform.NewUnauthorized(" authentication required "))
		return
	}

	userID := actor.ID
	if err := h.service.LogoutAll(ctx, userID); err != nil {
		platform.Fail(w, r, err)
		return
	}

	// Clear all refresh cookies
	clearAllRefreshCookies(w)

	platform.JSON(w, http.StatusNoContent, nil)
}

// MeHandler handles GET /api/v1/auth/me
func (h *Handler) MeHandler(w http.ResponseWriter, r *http.Request) {
	actor := platform.ActorFrom(r.Context())
	if actor == nil {
		platform.Fail(w, r, platform.NewUnauthorized(" authentication required "))
		return
	}

	resp, err := h.service.Me(r.Context(), actor.ID)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, resp)
}

// ChangePasswordHandler handles POST /api/v1/auth/change-password
func (h *Handler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	actor := platform.ActorFrom(r.Context())
	if actor == nil {
		platform.Fail(w, r, platform.NewUnauthorized(" authentication required "))
		return
	}

	var req ChangePasswordRequest
	if err := parseJSONBody(r, &req); err != nil {
		platform.Fail(w, r, err)
		return
	}

	err := h.service.ChangePassword(r.Context(), actor.ID, req.CurrentPassword, req.NewPassword)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

// SetRoutes registers the auth routes on the given router.
func (h *Handler) SetRoutes(r *http.Mux) {
	// Public routes (no authentication required)
	r.POST("/api/v1/auth/login", h.LoginHandler)
	r.POST("/api/v1/auth/refresh", h.RefreshHandler)
	r.POST("/api/v1/auth/logout", h.LogoutHandler)
	r.POST("/api/v1/auth/logout-all", h.LogoutAllHandler)
	r.GET("/api/v1/auth/me", h.MeHandler)
	r.POST("/api/v1/auth/change-password", h.ChangePasswordHandler)
}

// clearRefreshCookie clears the refresh token cookie.
func clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "edp_refresh_token",
		Value:    "",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   -1,
	})
}

// clearAllRefreshCookies clears all refresh token cookies.
func clearAllRefreshCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "edp_refresh_token",
		Value:    "",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   -1,
	})
	// Also clear access token cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "edp_access_token",
		Value:    "",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   -1,
	})
}

// parseJSONBody parses the JSON body of an HTTP request into the target structure.
func parseJSONBody(r *http.Request, dst any) error {
	// Use the existing decoder from the middleware package or implement a simple one
	// For now, use a basic approach
	return httpDecode(r.Body, dst)
}