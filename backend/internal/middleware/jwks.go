package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// parseJWKS reads a JSON Web Key Set and returns the usable public keys keyed
// by their `kid`. Keys whose algorithm is not in allowedAlgs, or whose curve
// type is unsupported, are skipped rather than failing the whole fetch: an
// identity provider routinely publishes keys for algorithms this service does
// not accept.
func parseJWKS(r io.Reader) (map[string]any, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode jwks document: %w", err)
	}
	if len(doc.Keys) == 0 {
		return nil, errors.New("jwks document contains no keys")
	}

	supported := make(map[string]bool, len(allowedAlgs))
	for _, alg := range allowedAlgs {
		supported[alg] = true
	}

	out := make(map[string]any, len(doc.Keys))
	var skipped int
	for _, key := range doc.Keys {
		if key.Kid == "" {
			skipped++
			continue
		}
		if key.Alg != "" && !supported[key.Alg] {
			skipped++
			continue
		}

		pub, err := key.publicKey()
		if err != nil {
			// One malformed key must not invalidate the rest of the set.
			skipped++
			continue
		}
		out[key.Kid] = pub
	}

	if len(out) == 0 {
		return nil, errors.New("jwks document contained no usable keys")
	}
	if skipped > 0 {
		log.Warn("skipped unusable jwks keys", "skipped", skipped, "usable", len(out))
	}
	return out, nil
}

// jwk is the subset of RFC 7517 that this service understands.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`

	// RSA.
	N string `json:"n"`
	E string `json:"e"`

	// EC.
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// publicKey converts a JWK into a crypto public key.
func (k jwk) publicKey() (any, error) {
	// A key declared for encryption cannot be used for signature verification.
	if k.Use != "" && k.Use != "sig" {
		return nil, fmt.Errorf("jwk %q has use %q, expected sig", k.Kid, k.Use)
	}

	switch k.Kty {
	case "RSA":
		return k.rsaPublicKey()
	case "EC":
		return k.ecPublicKey()
	default:
		return nil, fmt.Errorf("jwk %q has unsupported key type %q", k.Kid, k.Kty)
	}
}

func (k jwk) rsaPublicKey() (any, error) {
	if k.N == "" || k.E == "" {
		return nil, fmt.Errorf("jwk %q is missing RSA parameters", k.Kid)
	}

	modulus, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("jwk %q has invalid modulus: %w", k.Kid, err)
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("jwk %q has invalid exponent: %w", k.Kid, err)
	}

	// The exponent is a big-endian integer of arbitrary length; fold it into an
	// int rather than assuming a single byte.
	exponent := 0
	for _, b := range exponentBytes {
		exponent = exponent<<8 | int(b)
	}
	if exponent <= 0 {
		return nil, fmt.Errorf("jwk %q has a non-positive exponent", k.Kid)
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: exponent,
	}, nil
}

func (k jwk) ecPublicKey() (any, error) {
	if k.X == "" || k.Y == "" {
		return nil, fmt.Errorf("jwk %q is missing EC parameters", k.Kid)
	}

	x, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("jwk %q has invalid x coordinate: %w", k.Kid, err)
	}
	y, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("jwk %q has invalid y coordinate: %w", k.Kid, err)
	}

	var curve elliptic.Curve
	var size int

	switch k.Crv {
	case "P-256":
		curve, size = elliptic.P256(), 32
	case "P-384":
		curve, size = elliptic.P384(), 48
	case "P-521":
		curve, size = elliptic.P521(), 66
	default:
		return nil, fmt.Errorf("jwk %q uses unsupported curve %q", k.Kid, k.Crv)
	}

	if len(x) != size || len(y) != size {
		return nil, fmt.Errorf("jwk %q has coordinates of the wrong length for curve %s", k.Kid, k.Crv)
	}

	pub := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(x),
		Y:     new(big.Int).SetBytes(y),
	}
	// Reject points that are not actually on the curve, which would otherwise
	// cause an opaque verification failure deep inside the crypto package.
	if !curve.IsOnCurve(pub.X, pub.Y) {
		return nil, fmt.Errorf("jwk %q public point is not on curve %s", k.Kid, k.Crv)
	}
	return pub, nil
}
