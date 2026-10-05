package postgres

import (
	"encoding/json"
	"fmt"
)

// JSONB marshals a Go value into the JSONB representation pgx expects. A nil
// map or slice is written as an empty JSON object or array rather than SQL NULL,
// which keeps the NOT NULL columns in the schema simple.
func JSONB(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	if m, ok := v.(map[string]any); ok && m == nil {
		return []byte("{}"), nil
	}
	if s, ok := v.([]string); ok && s == nil {
		return []byte("[]"), nil
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode jsonb: %w", err)
	}
	if len(encoded) == 0 {
		return []byte("{}"), nil
	}
	return encoded, nil
}

// NullText converts an empty string into SQL NULL, for columns where the
// distinction between "absent" and "blank" is meaningful.
func NullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// TextFromNull is the inverse of NullText.
func TextFromNull(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
