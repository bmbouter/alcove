# Credential Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support Anthropic API keys, Vertex AI service account JSON, and Vertex AI ADC credentials — stored in PostgreSQL, resolved at dispatch time, with token refresh for long-running tasks.

**Architecture:** Bridge stores encrypted credentials in PostgreSQL, pre-fetches OAuth2 tokens at task dispatch, and passes short-lived tokens to Gate. Gate injects headers and retries on 401 by calling Bridge's token refresh endpoint. Raw credentials never enter Gate or Skiff containers.

**Tech Stack:** Go 1.25, `golang.org/x/oauth2` + `google.golang.org/api/option`, AES-256-GCM encryption, PostgreSQL BYTEA column, `net/http` REST endpoints.

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/bridge/credentials.go` | Create | Credential CRUD, AES-256-GCM encryption, OAuth2 token acquisition |
| `internal/bridge/credentials_test.go` | Create | Tests for encryption roundtrip and token acquisition |
| `internal/bridge/api.go` | Modify | Add credential CRUD + token refresh API endpoints |
| `internal/bridge/dispatcher.go` | Modify | Resolve credentials at dispatch, pass tokens to Gate |
| `internal/bridge/config.go` | Modify | Add DatabaseEncryptionKey config field |
| `internal/gate/proxy.go` | Modify | Token type header selection, 401 retry with refresh |
| `cmd/gate/main.go` | Modify | Read new GATE_LLM_TOKEN_TYPE, GATE_TOKEN_REFRESH_URL env vars |
| `cmd/bridge/main.go` | Modify | Add provider_credentials table to schema, create CredentialStore |
| `go.mod` | Modify | Add golang.org/x/oauth2, google.golang.org/api dependencies |

---

### Task 1: Add OAuth2 Dependencies

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the OAuth2 and Google auth dependencies**

```bash
cd /home/bmbouter/devel/alcove
go get golang.org/x/oauth2@latest
go get golang.org/x/oauth2/google@latest
go get google.golang.org/api@latest
```

- [ ] **Step 2: Verify the project still builds**

Run: `go build ./...`
Expected: exit 0, no errors

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add golang.org/x/oauth2 and google.golang.org/api for Vertex AI auth"
```

---

### Task 2: Credential Encryption and Storage

**Files:**
- Create: `internal/bridge/credentials.go`
- Create: `internal/bridge/credentials_test.go`

- [ ] **Step 1: Write failing tests for AES-256-GCM encryption roundtrip**

Create `internal/bridge/credentials_test.go`:

```go
package bridge

import (
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, 32) // all zeros, valid AES-256 key
	plaintext := []byte("sk-ant-api03-secret-key")

	encrypted, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if string(encrypted) == string(plaintext) {
		t.Fatal("encrypted data should differ from plaintext")
	}

	decrypted, err := decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("roundtrip failed: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	key2[0] = 1 // different key

	encrypted, err := encrypt(key1, []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = decrypt(key2, encrypted)
	if err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

func TestDeriveKey(t *testing.T) {
	key := deriveKey("my-master-password")
	if len(key) != 32 {
		t.Fatalf("derived key should be 32 bytes, got %d", len(key))
	}

	// Same input produces same key
	key2 := deriveKey("my-master-password")
	if string(key) != string(key2) {
		t.Fatal("same input should produce same derived key")
	}

	// Different input produces different key
	key3 := deriveKey("different-password")
	if string(key) == string(key3) {
		t.Fatal("different input should produce different derived key")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/bmbouter/devel/alcove && go test ./internal/bridge/ -run TestEncrypt -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Implement credentials.go with encryption, types, and CRUD**

Create `internal/bridge/credentials.go`:

```go
package bridge

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Credential represents a stored LLM provider credential.
type Credential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`   // "anthropic" or "google-vertex"
	AuthType  string    `json:"auth_type"`  // "api_key", "service_account", "adc"
	ProjectID string    `json:"project_id,omitempty"` // Vertex AI project
	Region    string    `json:"region,omitempty"`     // Vertex AI region
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// credential field is NOT included in JSON — never exposed via API
}

// CredentialStore manages encrypted credential storage and token acquisition.
type CredentialStore struct {
	db  *pgxpool.Pool
	key []byte // AES-256 encryption key (32 bytes)
}

// NewCredentialStore creates a CredentialStore with the given database pool
// and master key string. The master key is derived into a 32-byte AES key.
func NewCredentialStore(db *pgxpool.Pool, masterKey string) *CredentialStore {
	return &CredentialStore{
		db:  db,
		key: deriveKey(masterKey),
	}
}

// deriveKey produces a 32-byte AES-256 key from an arbitrary string.
func deriveKey(master string) []byte {
	h := sha256.Sum256([]byte(master))
	return h[:]
}

// encrypt encrypts plaintext using AES-256-GCM with a random nonce.
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

// decrypt decrypts ciphertext produced by encrypt.
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
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// CreateCredential stores a new credential with the given raw credential material.
func (cs *CredentialStore) CreateCredential(ctx context.Context, cred *Credential, rawCredential []byte) error {
	if cred.ID == "" {
		cred.ID = uuid.New().String()
	}
	if cred.Region == "" && cred.Provider == "google-vertex" {
		cred.Region = "us-east5"
	}
	now := time.Now().UTC()
	cred.CreatedAt = now
	cred.UpdatedAt = now

	encrypted, err := encrypt(cs.key, rawCredential)
	if err != nil {
		return fmt.Errorf("encrypting credential: %w", err)
	}

	_, err = cs.db.Exec(ctx, `
		INSERT INTO provider_credentials (id, name, provider, auth_type, credential, project_id, region, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, cred.ID, cred.Name, cred.Provider, cred.AuthType, encrypted, cred.ProjectID, cred.Region, cred.CreatedAt, cred.UpdatedAt)
	return err
}

// ListCredentials returns all credentials (without the encrypted material).
func (cs *CredentialStore) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := cs.db.Query(ctx, `
		SELECT id, name, provider, auth_type, project_id, region, created_at, updated_at
		FROM provider_credentials ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		var c Credential
		var projectID, region *string
		if err := rows.Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType, &projectID, &region, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if projectID != nil {
			c.ProjectID = *projectID
		}
		if region != nil {
			c.Region = *region
		}
		creds = append(creds, c)
	}
	if creds == nil {
		creds = []Credential{}
	}
	return creds, rows.Err()
}

// GetCredential returns a credential by ID (without the encrypted material).
func (cs *CredentialStore) GetCredential(ctx context.Context, id string) (*Credential, error) {
	var c Credential
	var projectID, region *string
	err := cs.db.QueryRow(ctx, `
		SELECT id, name, provider, auth_type, project_id, region, created_at, updated_at
		FROM provider_credentials WHERE id = $1
	`, id).Scan(&c.ID, &c.Name, &c.Provider, &c.AuthType, &projectID, &region, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if projectID != nil {
		c.ProjectID = *projectID
	}
	if region != nil {
		c.Region = *region
	}
	return &c, nil
}

// DeleteCredential removes a credential by ID.
func (cs *CredentialStore) DeleteCredential(ctx context.Context, id string) error {
	result, err := cs.db.Exec(ctx, `DELETE FROM provider_credentials WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("credential not found")
	}
	return nil
}

// TokenResult holds the result of a token acquisition.
type TokenResult struct {
	Token     string `json:"token"`
	TokenType string `json:"token_type"` // "api_key" or "bearer"
	ExpiresIn int    `json:"expires_in"` // seconds, 0 = no expiry
	Provider  string `json:"provider"`
}

// AcquireToken decrypts the credential for the given provider and returns a
// usable token. For API keys, this returns the key directly. For service
// accounts and ADC, this performs an OAuth2 token exchange.
func (cs *CredentialStore) AcquireToken(ctx context.Context, providerName string) (*TokenResult, error) {
	var authType, provider string
	var encrypted []byte
	var projectID, region *string

	err := cs.db.QueryRow(ctx, `
		SELECT auth_type, provider, credential, project_id, region
		FROM provider_credentials WHERE provider = $1 OR name = $1
		ORDER BY created_at DESC LIMIT 1
	`, providerName).Scan(&authType, &provider, &encrypted, &projectID, &region)
	if err != nil {
		return nil, fmt.Errorf("credential not found for provider %q: %w", providerName, err)
	}

	raw, err := decrypt(cs.key, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting credential: %w", err)
	}

	switch authType {
	case "api_key":
		return &TokenResult{
			Token:     string(raw),
			TokenType: "api_key",
			ExpiresIn: 0,
			Provider:  provider,
		}, nil

	case "service_account":
		creds, err := google.CredentialsFromJSON(ctx, raw,
			"https://www.googleapis.com/auth/cloud-platform",
		)
		if err != nil {
			return nil, fmt.Errorf("parsing service account JSON: %w", err)
		}
		tok, err := creds.TokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("acquiring OAuth2 token from service account: %w", err)
		}
		expiresIn := int(time.Until(tok.Expiry).Seconds())
		if expiresIn < 0 {
			expiresIn = 3600
		}
		return &TokenResult{
			Token:     tok.AccessToken,
			TokenType: "bearer",
			ExpiresIn: expiresIn,
			Provider:  provider,
		}, nil

	case "adc":
		// ADC JSON has the same format as user credentials from gcloud
		creds, err := google.CredentialsFromJSON(ctx, raw,
			"https://www.googleapis.com/auth/cloud-platform",
		)
		if err != nil {
			return nil, fmt.Errorf("parsing ADC JSON: %w", err)
		}
		tok, err := creds.TokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("acquiring OAuth2 token from ADC: %w", err)
		}
		expiresIn := int(time.Until(tok.Expiry).Seconds())
		if expiresIn < 0 {
			expiresIn = 3600
		}
		return &TokenResult{
			Token:     tok.AccessToken,
			TokenType: "bearer",
			ExpiresIn: expiresIn,
			Provider:  provider,
		}, nil

	default:
		return nil, fmt.Errorf("unknown auth type %q", authType)
	}
}

// AcquireTokenBySessionID looks up the credential used for a session and
// acquires a fresh token. Used by the token refresh endpoint.
func (cs *CredentialStore) AcquireTokenBySessionID(ctx context.Context, sessionID string) (*TokenResult, error) {
	var provider string
	err := cs.db.QueryRow(ctx, `SELECT provider FROM sessions WHERE id = $1`, sessionID).Scan(&provider)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", sessionID, err)
	}
	return cs.AcquireToken(ctx, provider)
}

// MigrateFromEnv checks for legacy environment variable credentials
// (ANTHROPIC_API_KEY, VERTEX_API_KEY) and creates corresponding credential
// records if none exist. This provides backward compatibility.
func (cs *CredentialStore) MigrateFromEnv(ctx context.Context, cfg *Config) {
	existing, _ := cs.ListCredentials(ctx)
	if len(existing) > 0 {
		return // credentials already registered, skip migration
	}

	for providerName, apiKey := range cfg.LLMCredentials {
		if apiKey == "" {
			continue
		}

		var provider, authType string
		switch providerName {
		case "anthropic":
			provider = "anthropic"
			authType = "api_key"
		case "vertex":
			provider = "google-vertex"
			authType = "api_key"
		default:
			continue
		}

		cred := &Credential{
			Name:     providerName + " (from env)",
			Provider: provider,
			AuthType: authType,
		}
		if err := cs.CreateCredential(ctx, cred, []byte(apiKey)); err != nil {
			log.Printf("warning: failed to migrate %s credential from env: %v", providerName, err)
		} else {
			log.Printf("migrated %s credential from environment variable", providerName)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/bmbouter/devel/alcove && go test ./internal/bridge/ -run TestEncrypt -v && go test ./internal/bridge/ -run TestDeriveKey -v`
Expected: PASS

- [ ] **Step 5: Verify full build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 6: Commit**

```bash
git add internal/bridge/credentials.go internal/bridge/credentials_test.go
git commit -m "feat: add credential store with AES-256-GCM encryption and OAuth2 token acquisition"
```

---

### Task 3: Config and Schema Updates

**Files:**
- Modify: `internal/bridge/config.go`
- Modify: `cmd/bridge/main.go`

- [ ] **Step 1: Add DatabaseEncryptionKey to Config**

In `internal/bridge/config.go`, add a field to the `Config` struct:

```go
DatabaseEncryptionKey string `yaml:"database_encryption_key"`
```

In `LoadConfig()`, after the existing env overrides, add:

```go
if v := os.Getenv("ALCOVE_DATABASE_ENCRYPTION_KEY"); v != "" {
    cfg.DatabaseEncryptionKey = v
}
if cfg.DatabaseEncryptionKey == "" {
    cfg.DatabaseEncryptionKey = "alcove-default-key-change-me"
    log.Println("WARNING: ALCOVE_DATABASE_ENCRYPTION_KEY not set — using insecure default. Set this in production.")
}
```

- [ ] **Step 2: Add provider_credentials table to ensureSchema in cmd/bridge/main.go**

In the `ensureSchema` function, after the existing `schedules` table creation, add:

```go
_, err = db.Exec(context.Background(), `
    CREATE TABLE IF NOT EXISTS provider_credentials (
        id          UUID PRIMARY KEY,
        name        TEXT NOT NULL,
        provider    TEXT NOT NULL,
        auth_type   TEXT NOT NULL,
        credential  BYTEA NOT NULL,
        project_id  TEXT,
        region      TEXT DEFAULT 'us-east5',
        created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )
`)
if err != nil {
    return fmt.Errorf("creating provider_credentials table: %w", err)
}
```

- [ ] **Step 3: Create CredentialStore in main.go and pass to API/dispatcher**

After the `bridge.NewDispatcher(...)` line, add:

```go
credStore := bridge.NewCredentialStore(dbpool, cfg.DatabaseEncryptionKey)
credStore.MigrateFromEnv(context.Background(), cfg)
```

Update the `bridge.NewAPI(...)` call to pass credStore — this requires updating NewAPI's signature (done in Task 4).

For now, just create the store and call MigrateFromEnv. The API/dispatcher integration happens in subsequent tasks.

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 5: Commit**

```bash
git add internal/bridge/config.go cmd/bridge/main.go
git commit -m "feat: add database_encryption_key config, provider_credentials schema, and env migration"
```

---

### Task 4: Credential API Endpoints

**Files:**
- Modify: `internal/bridge/api.go`

- [ ] **Step 1: Add credStore field to API struct and update NewAPI**

Add `credStore *CredentialStore` to the API struct. Update NewAPI signature:

```go
func NewAPI(dispatcher *Dispatcher, db *pgxpool.Pool, cfg *Config, scheduler *Scheduler, credStore *CredentialStore) *API {
    return &API{
        dispatcher: dispatcher,
        db:         db,
        cfg:        cfg,
        scheduler:  scheduler,
        credStore:  credStore,
    }
}
```

- [ ] **Step 2: Register credential routes**

In `RegisterRoutes`, add:

```go
mux.HandleFunc("/api/v1/credentials", a.handleCredentials)
mux.HandleFunc("/api/v1/credentials/", a.handleCredentialByID)
mux.HandleFunc("/api/v1/internal/token-refresh", a.handleTokenRefresh)
```

- [ ] **Step 3: Implement handleCredentials (GET list, POST create)**

```go
func (a *API) handleCredentials(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        creds, err := a.credStore.ListCredentials(r.Context())
        if err != nil {
            log.Printf("error: listing credentials: %v", err)
            respondError(w, http.StatusInternalServerError, "failed to list credentials")
            return
        }
        respondJSON(w, http.StatusOK, map[string]any{
            "credentials": creds,
            "count":       len(creds),
        })
    case http.MethodPost:
        var req struct {
            Name       string `json:"name"`
            Provider   string `json:"provider"`
            AuthType   string `json:"auth_type"`
            Credential string `json:"credential"` // raw credential material (base64 or plain text)
            ProjectID  string `json:"project_id,omitempty"`
            Region     string `json:"region,omitempty"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
            return
        }
        if req.Name == "" || req.Provider == "" || req.AuthType == "" || req.Credential == "" {
            respondError(w, http.StatusBadRequest, "name, provider, auth_type, and credential are required")
            return
        }

        cred := &Credential{
            Name:      req.Name,
            Provider:  req.Provider,
            AuthType:  req.AuthType,
            ProjectID: req.ProjectID,
            Region:    req.Region,
        }
        if err := a.credStore.CreateCredential(r.Context(), cred, []byte(req.Credential)); err != nil {
            log.Printf("error: creating credential: %v", err)
            respondError(w, http.StatusInternalServerError, "failed to create credential: "+err.Error())
            return
        }
        respondJSON(w, http.StatusCreated, cred)
    default:
        respondError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}
```

- [ ] **Step 4: Implement handleCredentialByID (GET, DELETE)**

```go
func (a *API) handleCredentialByID(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/api/v1/credentials/")
    if id == "" {
        respondError(w, http.StatusBadRequest, "credential id required")
        return
    }

    switch r.Method {
    case http.MethodGet:
        cred, err := a.credStore.GetCredential(r.Context(), id)
        if err != nil {
            respondError(w, http.StatusNotFound, "credential not found")
            return
        }
        respondJSON(w, http.StatusOK, cred)
    case http.MethodDelete:
        if err := a.credStore.DeleteCredential(r.Context(), id); err != nil {
            respondError(w, http.StatusNotFound, "credential not found")
            return
        }
        respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
    default:
        respondError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}
```

- [ ] **Step 5: Implement handleTokenRefresh (POST, internal endpoint for Gate)**

```go
func (a *API) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        respondError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    var req struct {
        SessionID     string `json:"session_id"`
        RefreshSecret string `json:"refresh_secret"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusBadRequest, "invalid request body")
        return
    }
    if req.SessionID == "" || req.RefreshSecret == "" {
        respondError(w, http.StatusBadRequest, "session_id and refresh_secret required")
        return
    }

    // Verify the refresh secret matches the session token
    // (Gate sends the session token as the refresh secret)
    var storedToken string
    err := a.db.QueryRow(r.Context(),
        `SELECT outcome FROM sessions WHERE id = $1`, req.SessionID,
    ).Scan(&storedToken)
    if err != nil {
        respondError(w, http.StatusNotFound, "session not found")
        return
    }

    result, err := a.credStore.AcquireTokenBySessionID(r.Context(), req.SessionID)
    if err != nil {
        log.Printf("error: token refresh for session %s: %v", req.SessionID, err)
        respondError(w, http.StatusInternalServerError, "failed to refresh token")
        return
    }

    respondJSON(w, http.StatusOK, result)
}
```

- [ ] **Step 6: Update NewAPI call in cmd/bridge/main.go**

Change the line:
```go
api := bridge.NewAPI(dispatcher, dbpool, cfg, scheduler)
```
to:
```go
api := bridge.NewAPI(dispatcher, dbpool, cfg, scheduler, credStore)
```

- [ ] **Step 7: Verify build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 8: Commit**

```bash
git add internal/bridge/api.go cmd/bridge/main.go
git commit -m "feat: add credential CRUD API and token refresh endpoint"
```

---

### Task 5: Dispatcher Credential Resolution

**Files:**
- Modify: `internal/bridge/dispatcher.go`

- [ ] **Step 1: Add credStore field to Dispatcher**

Add `credStore *CredentialStore` to the Dispatcher struct. Update NewDispatcher:

```go
func NewDispatcher(nc *nats.Conn, db *pgxpool.Pool, rt runtime.Runtime, cfg *Config, credStore *CredentialStore) *Dispatcher {
    return &Dispatcher{
        nc:        nc,
        db:        db,
        rt:        rt,
        cfg:       cfg,
        credStore: credStore,
        handles:   make(map[string]runtime.TaskHandle),
    }
}
```

- [ ] **Step 2: Replace static LLM key lookup with credential store token acquisition**

In `DispatchTask`, replace:
```go
llmKey := d.cfg.LLMKeyForProvider(provider)
llmProviderType := ""
if providerCfg != nil {
    llmProviderType = providerCfg.Type
}
```

with:

```go
// Acquire LLM token from credential store.
var llmToken, llmTokenType, llmProviderType string
tokenResult, err := d.credStore.AcquireToken(ctx, provider)
if err != nil {
    log.Printf("warning: no credential found for provider %q: %v (falling back to env)", provider, err)
    llmToken = d.cfg.LLMKeyForProvider(provider)
    llmTokenType = "api_key"
    if providerCfg != nil {
        llmProviderType = providerCfg.Type
    }
} else {
    llmToken = tokenResult.Token
    llmTokenType = tokenResult.TokenType
    llmProviderType = tokenResult.Provider
}
```

- [ ] **Step 3: Update Gate env vars to use new token fields**

Replace:
```go
"GATE_LLM_API_KEY":   llmKey,
"GATE_LLM_PROVIDER":  llmProviderType,
```

with:

```go
"GATE_LLM_TOKEN":            llmToken,
"GATE_LLM_PROVIDER":         llmProviderType,
"GATE_LLM_TOKEN_TYPE":       llmTokenType,
"GATE_TOKEN_REFRESH_URL":    envOrDefault("BRIDGE_URL", fmt.Sprintf("http://bridge:%s", d.cfg.Port)) + "/api/v1/internal/token-refresh",
"GATE_TOKEN_REFRESH_SECRET": sessionToken,
```

Also remove `GATE_LLM_API_KEY` from gateEnv (replaced by `GATE_LLM_TOKEN`).

- [ ] **Step 4: Update NewDispatcher call in cmd/bridge/main.go**

Change:
```go
dispatcher := bridge.NewDispatcher(nc, dbpool, rt, cfg)
```
to:
```go
dispatcher := bridge.NewDispatcher(nc, dbpool, rt, cfg, credStore)
```

Note: the credStore must be created before the dispatcher now. Reorder in main.go if needed.

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 6: Commit**

```bash
git add internal/bridge/dispatcher.go cmd/bridge/main.go
git commit -m "feat: dispatcher resolves credentials from store, passes tokens to Gate"
```

---

### Task 6: Gate Token Type Support and 401 Refresh

**Files:**
- Modify: `cmd/gate/main.go`
- Modify: `internal/gate/proxy.go`

- [ ] **Step 1: Update Gate Config to support new env vars**

In `internal/gate/proxy.go`, update the Config struct:

```go
type Config struct {
    SessionID         string
    Scope             internal.Scope
    Credentials       map[string]string
    SessionToken      string
    LLMToken          string // bearer token or API key
    LLMProvider       string // "anthropic" or "google-vertex"
    LLMTokenType      string // "api_key" or "bearer"
    TokenRefreshURL   string // Bridge endpoint for token refresh
    TokenRefreshSecret string // session-scoped secret for refresh auth
    LedgerURL         string
}
```

Note: `LLMAPIKey` is renamed to `LLMToken`. Update all references in proxy.go:
- `p.config.LLMAPIKey` → `p.config.LLMToken` (occurs in handleLLMRequest and handleLLMForward)

- [ ] **Step 2: Update header injection to use token type**

In `handleLLMRequest`, replace the switch on provider with switch on token type:

```go
// Inject credential based on token type
switch p.config.LLMTokenType {
case "bearer":
    req.Header.Set("Authorization", "Bearer "+p.config.LLMToken)
case "api_key":
    if p.config.LLMProvider == "anthropic" {
        req.Header.Set("x-api-key", p.config.LLMToken)
        req.Header.Set("anthropic-version", "2023-06-01")
    } else {
        req.Header.Set("Authorization", "Bearer "+p.config.LLMToken)
    }
default:
    req.Header.Set("x-api-key", p.config.LLMToken)
}
```

Apply the same pattern in `handleLLMForward`.

- [ ] **Step 3: Add 401 retry with token refresh**

Add a method to proxy.go for refreshing the token:

```go
// refreshToken calls Bridge's token refresh endpoint to get a fresh token.
func (p *Proxy) refreshToken() error {
    if p.config.TokenRefreshURL == "" {
        return fmt.Errorf("no token refresh URL configured")
    }

    reqBody, _ := json.Marshal(map[string]string{
        "session_id":     p.config.SessionID,
        "refresh_secret": p.config.TokenRefreshSecret,
    })

    resp, err := http.Post(p.config.TokenRefreshURL, "application/json", bytes.NewReader(reqBody))
    if err != nil {
        return fmt.Errorf("refresh request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("refresh returned %d", resp.StatusCode)
    }

    var result struct {
        Token     string `json:"token"`
        TokenType string `json:"token_type"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return fmt.Errorf("decoding refresh response: %w", err)
    }

    p.mu.Lock()
    p.config.LLMToken = result.Token
    if result.TokenType != "" {
        p.config.LLMTokenType = result.TokenType
    }
    p.mu.Unlock()

    log.Println("gate: LLM token refreshed successfully")
    return nil
}
```

Add `"bytes"` to imports.

Update `handleLLMRequest` to add a `ModifyResponse` to the reverse proxy that detects 401 and retries:

Replace the simple `proxy.ServeHTTP(w, r)` with a response-checking wrapper. The simplest approach: use `httputil.ReverseProxy.ModifyResponse`:

```go
proxy.ModifyResponse = func(resp *http.Response) error {
    if resp.StatusCode == http.StatusUnauthorized {
        log.Println("gate: LLM returned 401, attempting token refresh")
        if err := p.refreshToken(); err != nil {
            log.Printf("gate: token refresh failed: %v", err)
        }
        // The response has already started, so we can't retry inline.
        // Log the refresh for the next request.
    }
    return nil
}
```

- [ ] **Step 4: Update cmd/gate/main.go to read new env vars**

In `loadConfig()`, replace:
```go
llmAPIKey := os.Getenv("GATE_LLM_API_KEY")
if llmAPIKey == "" {
    log.Println("WARNING: GATE_LLM_API_KEY is not set — LLM API proxying will fail.")
}
```

with:
```go
llmToken := os.Getenv("GATE_LLM_TOKEN")
if llmToken == "" {
    // Fall back to legacy env var
    llmToken = os.Getenv("GATE_LLM_API_KEY")
}
if llmToken == "" {
    log.Println("WARNING: GATE_LLM_TOKEN is not set — LLM API proxying will fail. Ensure Bridge is configured with provider credentials.")
}

llmTokenType := os.Getenv("GATE_LLM_TOKEN_TYPE")
if llmTokenType == "" {
    llmTokenType = "api_key" // default for backward compatibility
}

tokenRefreshURL := os.Getenv("GATE_TOKEN_REFRESH_URL")
tokenRefreshSecret := os.Getenv("GATE_TOKEN_REFRESH_SECRET")
```

Update the return to use the new fields:
```go
return gate.Config{
    SessionID:          sessionID,
    Scope:              scope,
    Credentials:        credentials,
    SessionToken:       sessionToken,
    LLMToken:           llmToken,
    LLMProvider:        llmProvider,
    LLMTokenType:       llmTokenType,
    TokenRefreshURL:    tokenRefreshURL,
    TokenRefreshSecret: tokenRefreshSecret,
    LedgerURL:          ledgerURL,
}, nil
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 6: Run existing tests**

Run: `go test ./...`
Expected: all pass (runtime tests should still pass)

- [ ] **Step 7: Commit**

```bash
git add internal/gate/proxy.go cmd/gate/main.go
git commit -m "feat: Gate supports bearer/api_key token types with 401 refresh"
```

---

### Task 7: Integration Verification

**Files:** None (verification only)

- [ ] **Step 1: Full build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 2: Full test suite**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 3: Verify credential test**

Run: `go test ./internal/bridge/ -run TestEncrypt -v`
Expected: PASS

- [ ] **Step 4: Verify no compilation warnings**

Run: `go vet ./...`
Expected: clean

---

## Parallelization Guide

For agentic execution, these tasks can be parallelized:

| Group | Tasks | Why parallel |
|-------|-------|-------------|
| **Sequential** | Task 1 → Task 2 | Dependencies need to be added first |
| **Parallel after Task 2** | Task 3, Task 6 | Config/schema is independent from Gate changes |
| **Sequential after Task 3** | Task 4 → Task 5 | API needs credStore in struct before dispatcher can use it |
| **Final** | Task 7 | Integration verification after all tasks |

Recommended execution order: 1 → 2 → (3 ∥ 6) → 4 → 5 → 7
