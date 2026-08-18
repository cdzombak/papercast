// Package instapaper is a client for the Instapaper Full API,
// covering xAuth token exchange, bookmark listing, and article text retrieval.
package instapaper

import (
	"encoding/json"
	"fmt"
	"os"
)

// Credentials is an OAuth 1.0a token pair for the Instapaper API.
type Credentials struct {
	Token       string `json:"oauth_token"`
	TokenSecret string `json:"oauth_token_secret"`
}

// LoadCredentials reads a Credentials JSON file from path.
// The returned error wraps the underlying os error so callers can
// detect a missing file and suggest running -instapaper-login.
func LoadCredentials(path string) (*Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read instapaper credentials %s: %w", path, err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse instapaper credentials %s: %w", path, err)
	}
	return &c, nil
}

// SaveCredentials writes c to path as JSON with 0600 permissions.
func SaveCredentials(path string, c *Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instapaper credentials: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write instapaper credentials %s: %w", path, err)
	}
	return nil
}
