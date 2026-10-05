package auth

import (
	"time"

	"github.com/google/uuid"
)

// LoginRequest is the request body for user login.
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// LoginResponse is the successful login response.
type LoginResponse struct {
	User    UserResponse `json:"user"`
	ExpiresIn int64        `json:"expires_in"`
}

// UserResponse is the user information returned in auth responses.
type UserResponse struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Roles         []string  `json:"roles"`
	Status        string    `json:"status"`
	EmailVerified bool      `json:"email_verified"`
}

// RefreshRequest is the request body for token refresh.
type RefreshRequest struct {
	// No body needed - token read from cookie
}

// RefreshResponse is the token refresh response.
type RefreshResponse struct {
	AccessToken  string `json:"-"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"-"` // handled via cookie
}

// LogoutRequest is the request body for logout.
type LogoutRequest struct {
	// No body needed - token read from cookie
}

// LogoutResponse is the logout response.
type LogoutResponse struct{}

// LogoutAllRequest is the request body for logout-all.
type LogoutAllRequest struct{}

// LogoutAllResponse is the logout-all response.
type LogoutAllResponse struct{}

// MeResponse is the me endpoint response.
type MeResponse struct {
	User UserResponse `json:"user"`
}

// ChangePasswordRequest is the request body for changing password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" validate:"required"`
	NewPassword     string `json:"new_password" validate:"required,min=8"`
}

// LoginAuditLog is the audit log for login events.
type LoginAuditLog struct {
	AuditLog
}

// RefreshAuditLog is the audit log for token refresh events.
type RefreshAuditLog struct {
	AuditLog
}

// LogoutAuditLog is the audit log for logout events.
type LogoutAuditLog struct {
	AuditLog
}

// CreateUserRequest is the request body for user creation (admin only).
type CreateUserRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	RoleIDs   []uuid.UUID `json:"role_ids,omitempty"`
}

// UpdateUserRequest is the request body for user update (admin only).
type UpdateUserRequest struct {
	Email     string `json:"email" validate:"required,email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	RoleIDs   []uuid.UUID `json:"role_ids,omitempty"`
}