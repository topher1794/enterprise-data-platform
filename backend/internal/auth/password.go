package auth

import (
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
)

// ErrPasswordTooShort is returned when a password doesn't meet minimum requirements.
var ErrPasswordTooShort = errors.New("password must be at least 8 characters")

// HashPassword hashes a plaintext password using bcrypt with a cost factor of 14.
// The resulting hash includes the cost factor, salt, and checksum.
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", ErrPasswordTooShort
	}

	// Use bcrypt with cost factor 14 (adjustable for security/performance tradeoff)
	// 14 is the default recommended cost for new installations.
	const cost = 14

	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		slog.Error("failed to hash password", "error", err)
		return "", fmt.Errorf("password hashing failed: %w", err)
	}

	return string(hash), nil
}

// VerifyPassword checks that the provided plaintext password matches the bcrypt hash.
// Returns nil if the password is valid, or an error if it doesn't match.
func VerifyPassword(password string, hash string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		// Log the failed verification without revealing whether the hash exists
		// to avoid user enumeration
		slog.Warn("password verification failed", "error", err)
		return fmt.Errorf("password verification failed: %w", err)
	}
	return nil
}