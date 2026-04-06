// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bridge

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2/google"
)

// Credential holds metadata about a stored provider credential.
// The raw credential material is never included — it stays encrypted in the database.
type Credential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`   // "anthropic" or "google-vertex"
	AuthType  string    `json:"auth_type"`  // "api_key", "service_account", or "adc"
	ProjectID string    `json:"project_id"` // GCP project ID (Vertex only)
	Region    string    `json:"region"`     // GCP region (Vertex only)
	APIHost   string    `json:"api_host,omitempty"` // custom API host (e.g., self-hosted GitLab)
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Owner     string    `json:"owner,omitempty"`
}

// CredentialStore manages encrypted credential storage in PostgreSQL.
type CredentialStore struct {
	db  *pgxpool.Pool
	key []byte // 32-byte AES-256 key
}

// TokenResult is the result of acquiring a token for a provider.
type TokenResult struct {
	Token     string `json:"token"`
	TokenType string `json:"token_type"` // "api_key" or "bearer"
	ExpiresIn int    `json:"expires_in"` // seconds, 0 if non-expiring
	Provider  string `json:"provider"`
}

// deriveKey derives a 32-byte AES-256 key from a master password using SHA-256.
func deriveKey(master string) []byte {
	h := sha256.Sum256([]byte(master))
	return h[:]
}

// encrypt encrypts plaintext using AES-256-GCM. The random nonce is prepended
// to the returned ciphertext.
func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt: splits the nonce prefix and decrypts with AES-256-GCM.
func decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ct, nil)
}

// NewCredentialStore creates a CredentialStore with the given database pool
// and master encryption key.
func NewCredentialStore(db *pgxpool.Pool, masterKey string) *CredentialStore {
	return &CredentialStore{
		db:  db,
		key: deriveKey(masterKey),
	}
}

// CreateCredential stores a new credential with encrypted material.
func (cs *CredentialStore) CreateCredential(ctx context.Context, cred *Credential, rawCredential []byte, owner string) error {
	cred.ID = uuid.New().String()
	now := time.Now().UTC()
	cred.CreatedAt = now
	cred.UpdatedAt = now
	cred.Owner = owner

	encrypted, err := encrypt(cs.key, rawCredential)
	if err != nil {
		return fmt.Errorf("encrypting credential: %w", err)
	}

	_, err = cs.db.Exec(ctx,
		`INSERT INTO provider_credentials (id, name, provider, auth_type, credential, project_id, region, api_host, created_at, updated_at, owner)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		cred.ID, cred.Name, cred.Provider, cred.AuthType, encrypted,
		cred.ProjectID, cred.Region, cred.APIHost, cred.CreatedAt, cred.UpdatedAt, owner)
	if err != nil {
		return fmt.Errorf("inserting credential: %w", err)
	}

	return nil
}

// ListCredentials returns credentials without their encrypted material.
// If owner is non-empty, only credentials belonging to that owner are returned.
func (cs *CredentialStore) ListCredentials(ctx context.Context, owner string) ([]Credential, error) {
	query := `SELECT id, name, provider, auth_type, project_id, region, api_host, created_at, updated_at, owner
		FROM provider_credentials`
	args := []any{}
	if owner != "" {
		query += ` WHERE owner = $1`
		args = append(args, owner)
	}
	// Always exclude system credentials from user lists.
	if owner != "_system" {
		if len(args) > 0 {
			query += ` AND owner != '_system'`
		} else {
			query += ` WHERE owner != '_system'`
		}
	}
	query += ` ORDER BY created_at DESC`

	rows, err := cs.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying credentials: %w", err)
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType,
			&c.ProjectID, &c.Region, &c.APIHost, &c.CreatedAt, &c.UpdatedAt, &c.Owner); err != nil {
			return nil, fmt.Errorf("scanning credential: %w", err)
		}
		creds = append(creds, c)
	}

	if creds == nil {
		creds = []Credential{}
	}

	return creds, rows.Err()
}

// GetCredential returns a single credential by ID without its encrypted material.
// If owner is non-empty, only returns the credential if it belongs to that owner.
func (cs *CredentialStore) GetCredential(ctx context.Context, id, owner string) (*Credential, error) {
	query := `SELECT id, name, provider, auth_type, project_id, region, api_host, created_at, updated_at, owner
		FROM provider_credentials WHERE id = $1`
	args := []any{id}
	if owner != "" {
		query += ` AND owner = $2`
		args = append(args, owner)
	}

	var c Credential
	err := cs.db.QueryRow(ctx, query, args...).Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType,
		&c.ProjectID, &c.Region, &c.APIHost, &c.CreatedAt, &c.UpdatedAt, &c.Owner)
	if err != nil {
		return nil, fmt.Errorf("credential not found: %w", err)
	}
	return &c, nil
}

// DeleteCredential removes a credential by ID. Returns an error if not found.
// If owner is non-empty, only deletes the credential if it belongs to that owner.
func (cs *CredentialStore) DeleteCredential(ctx context.Context, id, owner string) error {
	query := `DELETE FROM provider_credentials WHERE id = $1`
	args := []any{id}
	if owner != "" {
		query += ` AND owner = $2`
		args = append(args, owner)
	}

	result, err := cs.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("deleting credential: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("credential %s not found", id)
	}
	return nil
}

// AcquireToken looks up the credential for the given provider name, decrypts it,
// and returns a usable token. For API keys this is the raw key; for service
// accounts and ADC it performs an OAuth2 token exchange.
func (cs *CredentialStore) AcquireToken(ctx context.Context, providerName string) (*TokenResult, error) {
	var authType, provider string
	var encrypted []byte
	err := cs.db.QueryRow(ctx,
		`SELECT auth_type, provider, credential FROM provider_credentials WHERE (provider = $1 OR name = $1) AND owner != '_system' ORDER BY created_at DESC LIMIT 1`, providerName,
	).Scan(&authType, &provider, &encrypted)
	if err != nil {
		return nil, fmt.Errorf("credential for provider %q not found: %w", providerName, err)
	}

	raw, err := decrypt(cs.key, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting credential for %q: %w", providerName, err)
	}

	switch authType {
	case "api_key":
		return &TokenResult{
			Token:     string(raw),
			TokenType: "api_key",
			ExpiresIn: 0,
			Provider:  provider,
		}, nil

	case "service_account", "adc":
		scopes := []string{"https://www.googleapis.com/auth/cloud-platform"}
		creds, err := google.CredentialsFromJSON(ctx, raw, scopes...)
		if err != nil {
			return nil, fmt.Errorf("parsing credentials for %q: %w", providerName, err)
		}
		tok, err := creds.TokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("acquiring OAuth2 token for %q: %w", providerName, err)
		}
		expiresIn := 0
		if !tok.Expiry.IsZero() {
			expiresIn = int(time.Until(tok.Expiry).Seconds())
			if expiresIn < 0 {
				expiresIn = 0
			}
		}
		return &TokenResult{
			Token:     tok.AccessToken,
			TokenType: "bearer",
			ExpiresIn: expiresIn,
			Provider:  provider,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported auth type %q for provider %q", authType, providerName)
	}
}

// AcquireSystemToken looks up a credential owned by '_system' for the given provider
// name and returns a usable token. This is used by BridgeLLM to acquire tokens for
// system-level LLM credentials that are not associated with any user.
func (cs *CredentialStore) AcquireSystemToken(ctx context.Context, providerName string) (*TokenResult, error) {
	var authType, provider string
	var encrypted []byte
	err := cs.db.QueryRow(ctx,
		`SELECT auth_type, provider, credential FROM provider_credentials
		 WHERE (provider = $1 OR name = $1) AND owner = '_system'
		 ORDER BY created_at DESC LIMIT 1`,
		providerName).Scan(&authType, &provider, &encrypted)
	if err != nil {
		return nil, fmt.Errorf("system credential for provider %q not found: %w", providerName, err)
	}

	raw, err := decrypt(cs.key, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting system credential for %q: %w", providerName, err)
	}

	switch authType {
	case "api_key":
		return &TokenResult{
			Token:     string(raw),
			TokenType: "api_key",
			ExpiresIn: 0,
			Provider:  provider,
		}, nil

	case "service_account", "adc":
		scopes := []string{"https://www.googleapis.com/auth/cloud-platform"}
		creds, err := google.CredentialsFromJSON(ctx, raw, scopes...)
		if err != nil {
			return nil, fmt.Errorf("parsing system credentials for %q: %w", providerName, err)
		}
		tok, err := creds.TokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("acquiring OAuth2 token for system %q: %w", providerName, err)
		}
		expiresIn := 0
		if !tok.Expiry.IsZero() {
			expiresIn = int(time.Until(tok.Expiry).Seconds())
			if expiresIn < 0 {
				expiresIn = 0
			}
		}
		return &TokenResult{
			Token:     tok.AccessToken,
			TokenType: "bearer",
			ExpiresIn: expiresIn,
			Provider:  provider,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported auth type %q for system provider %q", authType, providerName)
	}
}

// DeleteSystemCredentialByName removes a system credential by name.
// This is used to clean up existing system credentials before saving new ones.
func (cs *CredentialStore) DeleteSystemCredentialByName(ctx context.Context, name string) error {
	_, err := cs.db.Exec(ctx,
		`DELETE FROM provider_credentials WHERE name = $1 AND owner = '_system'`, name)
	return err
}

// AcquireSCMToken looks up a stored credential for a GitHub or GitLab service.
// Unlike LLM tokens, SCM tokens are typically PATs that don't need OAuth2 exchange.
func (cs *CredentialStore) AcquireSCMToken(ctx context.Context, service string) (string, error) {
	var encrypted []byte
	err := cs.db.QueryRow(ctx,
		`SELECT credential FROM provider_credentials WHERE provider = $1 OR name = $1 ORDER BY created_at DESC LIMIT 1`,
		service).Scan(&encrypted)
	if err != nil {
		return "", fmt.Errorf("no credential found for service %q: %w", service, err)
	}
	raw, err := decrypt(cs.key, encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypting credential for %q: %w", service, err)
	}
	return string(raw), nil
}

// AcquireSCMTokenWithHost looks up an SCM credential and returns both the
// token and the API host. Falls back to default hosts when api_host is empty.
func (cs *CredentialStore) AcquireSCMTokenWithHost(ctx context.Context, service string) (token string, apiHost string, err error) {
	var encrypted []byte
	var host string
	err = cs.db.QueryRow(ctx,
		`SELECT credential, COALESCE(api_host, '') FROM provider_credentials WHERE provider = $1 OR name = $1 ORDER BY created_at DESC LIMIT 1`,
		service).Scan(&encrypted, &host)
	if err != nil {
		return "", "", fmt.Errorf("no credential found for service %q: %w", service, err)
	}
	raw, err := decrypt(cs.key, encrypted)
	if err != nil {
		return "", "", fmt.Errorf("decrypting credential for %q: %w", service, err)
	}
	return string(raw), host, nil
}

// AcquireSCMTokenForOwner looks up an SCM credential for a specific owner and returns
// the token and API host. Falls back to AcquireSCMTokenWithHost if no owner-specific credential exists.
func (cs *CredentialStore) AcquireSCMTokenForOwner(ctx context.Context, service, owner string) (token string, apiHost string, err error) {
	var encrypted []byte
	var host string
	err = cs.db.QueryRow(ctx,
		`SELECT credential, COALESCE(api_host, '') FROM provider_credentials
		WHERE (provider = $1 OR name = $1) AND owner = $2
		ORDER BY created_at DESC LIMIT 1`,
		service, owner).Scan(&encrypted, &host)
	if err != nil {
		// Fall back to any available credential for this service.
		return cs.AcquireSCMTokenWithHost(ctx, service)
	}
	raw, err := decrypt(cs.key, encrypted)
	if err != nil {
		return "", "", fmt.Errorf("decrypting credential for %q: %w", service, err)
	}
	return string(raw), host, nil
}

// AcquireTokenBySessionID looks up the provider for a session and acquires a token.
func (cs *CredentialStore) AcquireTokenBySessionID(ctx context.Context, sessionID string) (*TokenResult, error) {
	var provider string
	err := cs.db.QueryRow(ctx,
		`SELECT provider FROM sessions WHERE id = $1`, sessionID,
	).Scan(&provider)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", sessionID, err)
	}
	return cs.AcquireToken(ctx, provider)
}

// FirstAvailableProvider returns the first LLM credential (excluding system and
// SCM credentials). Used for default provider resolution when no provider is
// specified in a task request.
func (cs *CredentialStore) FirstAvailableProvider(ctx context.Context) (*Credential, error) {
	var c Credential
	err := cs.db.QueryRow(ctx,
		`SELECT id, name, provider, auth_type, project_id, region, api_host, created_at, updated_at, owner
		FROM provider_credentials
		WHERE owner != '_system'
		AND provider NOT IN ('github', 'gitlab', 'jira')
		ORDER BY created_at ASC LIMIT 1`,
	).Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType,
		&c.ProjectID, &c.Region, &c.APIHost, &c.CreatedAt, &c.UpdatedAt, &c.Owner)
	if err != nil {
		return nil, fmt.Errorf("no LLM credentials available: %w", err)
	}
	return &c, nil
}

// LookupProviderCredential looks up credential metadata (without decrypting)
// by provider or credential name. Returns nil if not found.
func (cs *CredentialStore) LookupProviderCredential(ctx context.Context, providerName string) (*Credential, error) {
	var c Credential
	err := cs.db.QueryRow(ctx,
		`SELECT id, name, provider, auth_type, project_id, region, api_host, created_at, updated_at, owner
		FROM provider_credentials
		WHERE (provider = $1 OR name = $1) AND owner != '_system'
		ORDER BY created_at DESC LIMIT 1`,
		providerName,
	).Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType,
		&c.ProjectID, &c.Region, &c.APIHost, &c.CreatedAt, &c.UpdatedAt, &c.Owner)
	if err != nil {
		return nil, fmt.Errorf("credential for provider %q not found: %w", providerName, err)
	}
	return &c, nil
}

// ListDistinctProviders returns distinct LLM providers from the credential store,
// mapped to internal.Provider structs. Excludes system and SCM credentials.
func (cs *CredentialStore) ListDistinctProviders(ctx context.Context) ([]Credential, error) {
	rows, err := cs.db.Query(ctx,
		`SELECT DISTINCT ON (provider) id, name, provider, auth_type, project_id, region, api_host, created_at, updated_at, owner
		FROM provider_credentials
		WHERE owner != '_system'
		AND provider NOT IN ('github', 'gitlab', 'jira')
		ORDER BY provider, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying distinct providers: %w", err)
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType,
			&c.ProjectID, &c.Region, &c.APIHost, &c.CreatedAt, &c.UpdatedAt, &c.Owner); err != nil {
			return nil, fmt.Errorf("scanning credential: %w", err)
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

// MigrateFromEnv creates credential records from environment-based config
// if no credentials exist in the database yet. This provides backward
// compatibility with the ANTHROPIC_API_KEY / VERTEX_API_KEY environment
// variable approach.
func (cs *CredentialStore) MigrateFromEnv(ctx context.Context, cfg *Config) {
	// Check if any credentials already exist.
	var count int
	err := cs.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM provider_credentials`).Scan(&count)
	if err != nil {
		log.Printf("warning: could not check existing credentials: %v", err)
		return
	}
	if count > 0 {
		return
	}

	for name, key := range cfg.LLMCredentials {
		if key == "" {
			continue
		}
		cred := &Credential{
			Name:     name,
			Provider: name,
			AuthType: "api_key",
		}
		if err := cs.CreateCredential(ctx, cred, []byte(key), ""); err != nil {
			log.Printf("warning: failed to migrate credential %q from env: %v", name, err)
		} else {
			log.Printf("migrated credential %q from environment", name)
		}
	}
}
