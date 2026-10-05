package auth

import (
	"time"

	"github.com/google/uuid"
)

// User represents a system user.
type User struct {
	ID                uuid.UUID
	Email             string
	PasswordHash      string
	FirstName         string
	LastName          string
	Status            Status
	EmailVerified     bool
	LastLoginAt       time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Status represents user account status.
type Status string

const (
	StatusActive  Status = "ACTIVE"
	StatusInactive        = "INACTIVE"
	StatusLocked          = "LOCKED"
	StatusPending         = "PENDING"
)

// Role represents a system role.
type Role struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
}

// UserRole maps a user to a role.
type UserRole struct {
	UserID   uuid.UUID
	RoleID   uuid.UUID
	CreatedAt time.Time
}

// RefreshToken represents a user's refresh token.
type RefreshToken struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	TokenHash  string
	ExpiresAt  time.Time
	RevokedAt  time.Time
	CreatedAt  time.Time
	LastUsedAt time.Time
	UserAgent  string
	IPAddress  string
}

// AuditLog represents an authentication audit record.
type AuditLog struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Action    string
	Resource  string
	ResourceID uuid.UUID
	IPAddress string
	UserAgent string
	Metadata  map[string]string
	CreatedAt time.Time
}

// AuditAction represents possible audit actions.
type AuditAction string

const (
	AuditLoginSuccess    AuditAction = "LOGIN_SUCCESS"
	AuditLoginFailed     AuditAction = "LOGIN_FAILED"
	AuditLogout          AuditAction = "LOGOUT"
	AuditTokenRefresh    AuditAction = "TOKEN_REFRESH"
	AuditAccountLocked   AuditAction = "ACCOUNT_LOCKED"
	AuditPasswordChanged AuditAction = "PASSWORD_CHANGED"
)