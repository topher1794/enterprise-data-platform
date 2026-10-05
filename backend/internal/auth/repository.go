package auth

import (
	"context"

	"github.com/google/uuid"
)

// UserRepository defines the interface for user data access.
type UserRepository interface {
	// FindByEmail finds a user by normalized email address.
	FindByEmail(ctx context.Context, email string) (*User, error)
	// FindByID finds a user by UUID.
	FindByID(ctx context.Context, id uuid.UUID) (*User, error)
	// Create creates a new user.
	Create(ctx context.Context, user *User) error
	// UpdateLastLogin updates the user's last login timestamp.
	UpdateLastLogin(ctx context.Context, id uuid.UUID) error
	// UpdateStatus updates the user's account status.
	UpdateStatus(ctx context.Context, id uuid.UUID, status Status) error
}

// RoleRepository defines the interface for role data access.
type RoleRepository interface {
	// FindByName finds a role by name.
	FindByName(ctx context.Context, name string) (*Role, error)
	// ListByUser finds all roles for a user.
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*Role, error)
}

// UserRoleRepository defines the interface for user-role mappings.
type UserRoleRepository interface {
	// Assign assigns a role to a user.
	Assign(ctx context.Context, userID, roleID uuid.UUID) error
	// Revoke revokes a role from a user.
	Revoke(ctx context.Context, userID, roleID uuid.UUID) error
	// ListByUser lists all roles for a user.
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*UserRole, error)
}

// RefreshTokenRepository defines the interface for refresh token data access.
type RefreshTokenRepository interface {
	// Create creates a new refresh token.
	Create(ctx context.Context, token *RefreshToken) error
	// FindByHash finds a refresh token by its hash.
	FindByHash(ctx context.Context, tokenHash string) (*RefreshToken, error)
	// Revoke revokes a refresh token.
	Revoke(ctx context.Context, tokenID uuid.UUID) error
	// RevokeAllForUser revokes all refresh tokens for a user.
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
	// ExistsByUserAndToken checks if a token exists for a user.
	ExistsByUserAndToken(ctx context.Context, userID uuid.UUID, tokenHash string) bool
}

// AuditLogRepository defines the interface for audit log data access.
type AuditLogRepository interface {
	// Create creates a new audit log entry.
	Create(ctx context.Context, log *AuditLog) error
	// ListByUser lists audit logs for a user.
	ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*AuditLog, error)
}

// Repository bundles all authentication data access interfaces by embedding
// the sub-interfaces. This keeps the dependency one-way: audit and other
// domains don't need to import the auth package.
type Repository interface {
	UserRepository
	RoleRepository
	UserRoleRepository
	RefreshTokenRepository
	AuditLogRepository
}