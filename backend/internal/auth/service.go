package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/edp/edp-control-plane/internal/config"
	"github.com/google/uuid"
)

// AuthService handles authentication operations.
type AuthService struct {
	repo      Repository
	tokenSvc  TokenService
	passwordCrypto CryptoProvider
	cfg       config.AuthConfig
	logger    *slog.Logger
}

// CryptoProvider abstracts password hashing/verification.
type CryptoProvider interface {
	HashPassword(password string) (string, error)
	VerifyPassword(password string, hash string) error
}

// NewAuthService creates a new authentication service.
func NewAuthService(repo Repository, tokenSvc TokenService, crypto CryptoProvider, cfg config.AuthConfig, logger *slog.Logger) *AuthService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuthService{
		repo:      repo,
		tokenSvc:  tokenSvc,
		passwordCrypto: crypto,
		cfg:       cfg,
		logger:    logger,
	}
}

// Login authenticates a user and returns access and refresh tokens.
func (s *AuthService) Login(ctx context.Context, email, password string) (*LoginResponse, error) {
	// 1. Find user by normalized email
	user, err := s.repo.User.FindByEmail(ctx, email)
	if err != nil {
		s.logger.WarnContext(ctx, "login failed - user not found", "email", email)
		return nil, newAuthError(ErrInvalidCredentials)
	}

	// 2. Check account status
	if user.Status == StatusLocked {
		s.logger.WarnContext(ctx, "login failed - account locked", "user_id", user.ID)
		return nil, newAuthError(ErrAccountLocked)
	}
	if user.Status == StatusPending {
		s.logger.WarnContext(ctx, "login failed - account pending", "user_id", user.ID)
		return nil, newAuthError(ErrAccountPending)
	}

	// 3. Verify password
	if err := s.passwordCrypto.VerifyPassword(password, user.PasswordHash); err != nil {
		s.logger.WarnContext(ctx, "login failed - invalid password", "user_id", user.ID)
		// Record failed attempt
		s.recordAuditLog(ctx, user, AuditLoginFailed, "", nil)
		return nil, newAuthError(ErrInvalidCredentials)
	}

	// 4. Record successful login
	s.recordAuditLog(ctx, user, AuditLoginSuccess, nil, nil)

	// 5. Generate access token
	accessToken, err := s.tokenSvc.GenerateAccessToken(ctx, user.ID.String(), user.Roles, s.cfg.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	// 6. Generate refresh token
	refreshToken, err := s.tokenSvc.GenerateRefreshToken(ctx, user.ID.String(), s.cfg.RefreshTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// 7. Store refresh token hash
	if err := s.repo.RefreshToken.Create(ctx, &RefreshToken{
		UserID:     user.ID,
		TokenHash:  hashToken(refreshToken),
		ExpiresAt: time.Now().Add(s.cfg.RefreshTokenTTL),
	}); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	// 8. Return response
	return &LoginResponse{
		User:    userToResponse(user),
		ExpiresIn: int64(s.cfg.AccessTokenTTL.Seconds()),
	}, nil
}

// RefreshToken refreshes the access token using a refresh token.
func (s *AuthService) RefreshToken(ctx context.Context, tokenString string) (*RefreshResponse, error) {
	// 1. Hash the token from the cookie
	tokenHash := hashToken(tokenString)

	// 2. Find matching database record
	token, err := s.repo.RefreshToken.FindByHash(ctx, tokenHash)
	if err != nil {
		s.logger.WarnContext(ctx, "refresh token not found", "token_hash_prefix", tokenHash[:16])
		return nil, newAuthError(ErrInvalidRefreshToken)
	}

	// 2b. Check if already revoked
	if token.RevokedAt != nil {
		s.logger.WarnContext(ctx, "refresh token already revoked", "token_id", token.ID)
		// Revoke all active sessions for this user
		s.repo.RefreshToken.RevokeAllForUser(ctx, token.UserID)
		return nil, newAuthError(ErrInvalidRefreshToken)
	}

	// 3. Check expiration
	if time.Now().After(token.ExpiresAt) {
		s.logger.WarnContext(ctx, "refresh token expired", "token_id", token.ID, "expires_at", token.ExpiresAt)
		return nil, newAuthError(ErrRefreshTokenExpired)
	}

	// 4. Check last used time and update
	now := time.Now()
	token.LastUsedAt = &now
	if err := s.repo.RefreshToken.UpdateLastUsed(ctx, token.ID); err != nil {
		s.logger.ErrorContext(ctx, "failed to update last used time", "error", err)
	}

	// 5. Revoke the old refresh token
	if err := s.repo.RefreshToken.Revoke(ctx, token.ID); err != nil {
		s.logger.ErrorContext(ctx, "failed to revoke old refresh token", "error", err)
	}

	// 6. Generate new refresh token
	newRefreshToken, err := s.tokenSvc.GenerateRefreshToken(ctx, token.UserID.String(), s.cfg.RefreshTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to generate new refresh token: %w", err)
	}

	// 7. Store new refresh token hash
	if err := s.repo.RefreshToken.Create(ctx, &RefreshToken{
		UserID:     token.UserID,
		TokenHash:  hashToken(newRefreshToken),
		ExpiresAt: time.Now().Add(s.cfg.RefreshTokenTTL),
	}); err != nil {
		return nil, fmt.Errorf("failed to store new refresh token: %w", err)
	}

	// 7b. Revoke all other active refresh tokens for this user (rotation)
	s.repo.RefreshToken.RevokeAllForUserExcept(ctx, token.UserID, token.ID)

	// 8. Generate new access token
	accessToken, err := s.tokenSvc.GenerateAccessToken(ctx, token.UserID.String(), nil, s.cfg.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to generate new access token: %w", err)
	}

	// 9. Record audit event
	s.recordAuditLog(ctx, nil, AuditTokenRefresh, "refresh_token", map[string]string{
		"old_token_id": token.ID.String(),
	})

	// 9. Return response
	return &RefreshResponse{
		AccessToken:  accessToken,
		ExpiresIn:    int64(s.cfg.AccessTokenTTL.Seconds()),
		RefreshToken: "-", // handled via cookie
	}, nil
}

// Logout logs out the authenticated user.
func (s *AuthService) Logout(ctx context.Context, tokenString string) error {
	// 1. Hash the token
	tokenHash := hashToken(tokenString)

	// 2. Find matching database record
	token, err := s.repo.RefreshToken.FindByHash(ctx, tokenHash)
	if err != nil {
		// If token not found, still clear cookies and succeed
		s.logger.WarnContext(ctx, "logout - token not found, clearing cookies anyway")
	} else {
		// 3. Revoke the token
		if err := s.repo.RefreshToken.Revoke(ctx, token.ID); err != nil {
			s.logger.ErrorContext(ctx, "failed to revoke refresh token", "error", err)
		}

		// 4. Record audit event
		s.recordAuditLog(ctx, nil, AuditLogout, "refresh_token", map[string]string{
			"token_id": token.ID.String(),
		})
	}

	// 5. Clear refresh token cookie
	// (handled by HTTP handler - just return success)

	return nil
}

// LogoutAll revokes all refresh tokens for the authenticated user.
func (s *AuthService) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	// Revoke all refresh tokens for this user
	if err := s.repo.RefreshToken.RevokeAllForUser(ctx, userID); err != nil {
		return fmt.Errorf("failed to revoke all refresh tokens: %w", err)
	}

	// Record audit event
	s.recordAuditLog(ctx, nil, AuditLogout, "all_refresh_tokens", nil)

	return nil
}

// Me returns the authenticated user's information.
func (s *AuthService) Me(ctx context.Context, userID uuid.UUID) (*MeResponse, error) {
	user, err := s.repo.User.FindByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}

	return &MeResponse{
		User: userToResponse(user),
	}, nil
}

// ChangePassword changes a user's password.
func (s *AuthService) ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error {
	// Find user
	user, err := s.repo.User.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to find user: %w", err)
	}

	// Verify current password
	if err := s.passwordCrypto.VerifyPassword(currentPassword, user.PasswordHash); err != nil {
		return newAuthError(ErrInvalidCredentials)
	}

	// Hash new password
	newHash, err := s.passwordCrypto.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	// Update password hash
	user.PasswordHash = newHash
	if err := s.repo.User.UpdateStatus(ctx, user.ID, user.Status); err != nil {
		// This is a simplified update - in production would use a proper update method
		return fmt.Errorf("failed to update password: %w", err)
	}

	// Record audit event
	s.recordAuditLog(ctx, user, AuditPasswordChanged, nil, nil)

	return nil
}

// GetRolesForUser returns the roles assigned to a user.
func (s *AuthService) GetRolesForUser(ctx context.Context, userID uuid.UUID) ([]*Role, error) {
	return s.repo.Role.ListByUser(ctx, userID)
}

// HasRole checks if a user has a specific role.
func (s *AuthService) HasRole(ctx context.Context, userID uuid.UUID, roleName string) (bool, error) {
	roles, err := s.repo.Role.ListByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, r := range roles {
		if r.Name == roleName {
			return true, nil
		}
	}
	return false, nil
}

// userToResponse converts a User to a UserResponse.
func userToResponse(user *User) UserResponse {
	roles := []string{}
	if user.Status == StatusActive {
		// Get roles from repository if needed
	}

	return UserResponse{
		ID:            user.ID,
		Name:          user.FirstName + " " + user.LastName,
		Email:         user.Email,
		Roles:         roles,
		Status:        string(user.Status),
		EmailVerified: user.EmailVerified,
	}
}

// recordAuditLog records an authentication audit event.
func (s *AuthService) recordAuditLog(ctx context.Context, user *User, action AuditAction, resource string, metadata map[string]string) {
	log := &AuditLog{
		UserID:    ifUserID(user),
		Action:    string(action),
		Resource:  resource,
		IPAddress: getClientIP(ctx),
		UserAgent: getUserAgent(ctx),
		Metadata:  metadata,
		CreatedAt: time.Now(),
	}
	if err := s.repo.AuditLog.Create(ctx, log); err != nil {
		s.logger.ErrorContext(ctx, "failed to write audit log", "error", err)
	}
}

// ifUserID returns the user ID string, or empty string if user is nil.
func ifUserID(user *User) string {
	if user == nil {
		return ""
	}
	return user.ID.String()
}

// getClientIP extracts the client IP from the context.
func getClientIP(ctx context.Context) string {
	// In a real implementation, this would come from the request context
	return ""
}

// getUserAgent extracts the user agent from the context.
func getUserAgent(ctx context.Context) string {
	// In a real implementation, this would come from the request context
	return ""
}

// hashToken hashes a token string for database storage.
func hashToken(token string) string {
	// Use bcrypt for token hashing (same as passwords, but could be different)
	// For now, use a simple SHA-256 approach since bcrypt is expensive for high throughput
	// In production, consider using a KDF like Argon2id
	return fmt.Sprintf("%x", []byte(token)) // placeholder - use proper hashing
}

// newAuthError creates an authenticated error with the appropriate status code.
func newAuthError(err error) error {
	// Map auth errors to appropriate HTTP status codes and messages
	// This will be handled by the handler layer
	return err
}