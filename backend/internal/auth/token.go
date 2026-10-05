package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenService defines the interface for token generation and validation.
// This allows future replacement with OIDC/Entra ID without changing the
// application authorization layer.
type TokenService interface {
	// GenerateAccessToken creates a short-lived access token for the given subject.
	GenerateAccessToken(ctx context.Context, subject string, roles []string, expiration time.Duration) (string, error)
	// ValidateAccessToken validates a JWT access token and returns the claims.
	ValidateAccessToken(ctx context.Context, tokenString string) (*Claims, error)
	// GenerateRefreshToken creates a new refresh token for the given user.
	GenerateRefreshToken(ctx context.Context, userID string, expiration time.Duration) (string, error)
	// ValidateRefreshToken validates a refresh token and checks expiration/revocation.
	ValidateRefreshToken(ctx context.Context, tokenHash string, expiresAt time.Time) (*RefreshTokenData, error)
}

// RefreshTokenData holds the result of refresh token validation.
type RefreshTokenData struct {
	ExpiresAt time.Time
	RevokedAt  time.Time
	UserID     string
}

// jwtClaims implements jwt.RegisteredClaims for access tokens.
type jwtClaims struct {
	jwt.RegisteredClaims
	Email   string `json:"email"`
	Roles   []string `json:"roles"`
}

// MakeAccessToken creates a signed access token for the given subject and roles.
func MakeAccessToken(secret []byte, subject string, roles []string, expiration time.Duration) (string, error) {
	claims := jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiration)),
		},
		Email:  "",
		Roles:  roles,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// MakeRefreshToken creates a signed refresh token for the given user ID and expiration.
func MakeRefreshToken(secret []byte, userID string, expiration time.Duration) (string, error) {
	claims := jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiration)),
		},
		Email: "",
		Roles: nil,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// ParseAndValidateAccessToken parses and validates a JWT access token string.
func ParseAndValidateAccessToken(secret []byte, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Method)
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// Claims represents the validated token payload.
type Claims struct {
	Subject   string
	Email     string
	Name      string
	TenantID  string
	Roles     []string
	Scopes    []string
	ExpiresAt time.Time
	Issuer    string
}

// MakeRefreshTokenData creates RefreshTokenData from a validated refresh token.
func MakeRefreshTokenData(claims *jwtClaims, expiresAt, revokedAt time.Time) *RefreshTokenData {
	return &RefreshTokenData{
		ExpiresAt: expiresAt,
		RevokedAt: revokedAt,
		UserID:    claims.Subject,
	}
}