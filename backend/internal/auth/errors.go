package auth

import "errors"

// AuthError is the base authentication error type.
type AuthError struct {
	Code    string
	Message string
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsAuthError returns true if the error is an authentication error.
func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// Specific authentication error types.

// ErrInvalidCredentials is returned when login credentials are invalid.
// Does not reveal whether the email exists.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrAccountLocked is returned when the account is locked.
var ErrAccountLocked = errors.New("account is locked")

// ErrAccountPending is returned when the account is pending verification.
var ErrAccountPending = errors.New("account is pending")

// ErrInvalidRefreshToken is returned when the refresh token is invalid.
var ErrInvalidRefreshToken = errors.New("invalid refresh token")

// ErrRefreshTokenExpired is returned when the refresh token has expired.
var ErrRefreshTokenExpired = errors.New("refresh token has expired")

// ErrMissingRefreshToken is returned when no refresh token cookie is present.
var ErrMissingRefreshToken = errors.New("refresh token cookie is missing")

// AuthErrorf creates an authentication error with the given code and message.
func AuthErrorf(code, message string) error {
	return &AuthError{
		Code:    code,
		Message: message,
	}
}