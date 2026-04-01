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

// Package auth provides authentication and session management for Bridge.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// Argon2id parameters per architecture-decisions.md.
	argonMemory     = 64 * 1024 // 64 MB
	argonIterations = 3
	argonParallelism = 4
	argonKeyLength  = 32
	argonSaltLength = 16
)

// Authenticator defines the interface for authentication backends.
type Authenticator interface {
	Authenticate(username, password string) (string, error)
	ValidateToken(token string) (string, bool)
	InvalidateToken(token string)
}

// UserManager defines the interface for user management operations.
type UserManager interface {
	CreateUser(ctx context.Context, username, password string, isAdmin bool) error
	DeleteUser(ctx context.Context, username string) error
	ListUsers(ctx context.Context) ([]UserInfo, error)
	ChangePassword(ctx context.Context, username, newPassword string) error
	SetAdmin(ctx context.Context, username string, isAdmin bool) error
	IsAdmin(ctx context.Context, username string) (bool, error)
	VerifyUserPassword(ctx context.Context, username, password string) (bool, error)
}

// UserInfo contains metadata about a user.
type UserInfo struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	IsAdmin   bool      `json:"is_admin"`
}

// HashPassword produces an argon2id hash suitable for storage in config.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an argon2id hash.
func VerifyPassword(hash, password string) bool {
	// Parse: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if err != nil {
		return false
	}

	salt, err := hex.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expectedKey, err := hex.DecodeString(parts[5])
	if err != nil {
		return false
	}

	computedKey := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expectedKey)))
	return subtle.ConstantTimeCompare(computedKey, expectedKey) == 1
}

func generateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- HTTP Handlers ---

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
	Message  string `json:"message,omitempty"`
}

// LoginHandler returns an HTTP handler for POST /api/v1/auth/login.
func LoginHandler(store Authenticator, mgr UserManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		// Login is not supported with the rh-identity backend.
		if _, ok := store.(*RHIdentityStore); ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login not supported with rh-identity backend"})
			return
		}

		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		token, err := store.Authenticate(req.Username, req.Password)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}

		resp := loginResponse{
			Token:    token,
			Username: req.Username,
		}
		if mgr != nil {
			if admin, err := mgr.IsAdmin(r.Context(), req.Username); err == nil {
				resp.IsAdmin = admin
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// AuthMiddleware returns middleware that validates session tokens on
// protected routes. It skips /api/v1/auth/login and /api/v1/health.
// When the auth backend is rh-identity, it reads the X-RH-Identity header
// instead of requiring Bearer tokens.
func AuthMiddleware(store Authenticator, mgr UserManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Public and internal routes.
			if path == "/api/v1/auth/login" ||
				path == "/api/v1/health" || path == "/api/v1/system-info" ||
				strings.HasPrefix(path, "/api/v1/internal/") ||
				strings.HasPrefix(path, "/api/v1/webhooks/") ||
				(r.Method == http.MethodPost && isSessionIngestionPath(path)) {
				next.ServeHTTP(w, r)
				return
			}

			// Non-API routes (dashboard static files) are not protected.
			if !strings.HasPrefix(path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// RH Identity mode: trust the X-RH-Identity header from Turnpike.
			if rhStore, ok := store.(*RHIdentityStore); ok {
				headerVal := r.Header.Get("X-RH-Identity")
				if headerVal == "" {
					// For /api/v1/auth/me without a header, return auth_backend
					// so the frontend can detect rh-identity mode.
					if path == "/api/v1/auth/me" {
						log.Printf("auth: /api/v1/auth/me without X-RH-Identity — returning auth_backend only")
						next.ServeHTTP(w, r)
						return
					}
					log.Printf("auth: rejected %s %s — missing X-RH-Identity header", r.Method, path)
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing X-RH-Identity header"})
					return
				}
				log.Printf("auth: rh-identity header present for %s %s", r.Method, path)
				identity, err := ParseRHIdentity(headerVal)
				if err != nil {
					log.Printf("auth: invalid X-RH-Identity for %s %s: %v", r.Method, path, err)
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid X-RH-Identity header"})
					return
				}
				username, err := rhStore.UpsertUser(r.Context(), identity)
				if err != nil {
					log.Printf("auth: user provisioning failed for %s %s: %v", r.Method, path, err)
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user provisioning failed"})
					return
				}
				log.Printf("auth: rh-identity user=%s admin=%v for %s %s", username, false, r.Method, path)
				r.Header.Set("X-Alcove-User", username)
				if mgr != nil {
					if admin, err := mgr.IsAdmin(r.Context(), username); err == nil && admin {
						r.Header.Set("X-Alcove-Admin", "true")
					}
				}
				next.ServeHTTP(w, r)
				return
			}

			// Extract token from Authorization header or query parameter.
			// Query parameter fallback is needed for SSE (EventSource can't set headers).
			var tokenStr string
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
					tokenStr = parts[1]
				}
			}
			if tokenStr == "" {
				tokenStr = r.URL.Query().Get("token")
			}
			if tokenStr == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing authorization header"})
				return
			}

			username, ok := store.ValidateToken(tokenStr)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
				return
			}

			// Set username in header for downstream handlers.
			r.Header.Set("X-Alcove-User", username)

			// Set admin flag header if user is an admin.
			if mgr != nil {
				if admin, err := mgr.IsAdmin(context.Background(), username); err == nil && admin {
					r.Header.Set("X-Alcove-Admin", "true")
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// MeHandler returns an HTTP handler for GET /api/v1/auth/me.
// The authBackend parameter is included in the response so the frontend
// can adapt its UI (e.g., hide login form for rh-identity).
func MeHandler(authBackend string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		username := r.Header.Get("X-Alcove-User")
		isAdmin := r.Header.Get("X-Alcove-Admin") == "true"
		writeJSON(w, http.StatusOK, map[string]any{
			"username":     username,
			"is_admin":     isAdmin,
			"auth_backend": authBackend,
		})
	}
}

// isSessionIngestionPath returns true for Skiff/Gate→Bridge internal POST paths.
// These are authenticated via session tokens, not user tokens.
func isSessionIngestionPath(path string) bool {
	return strings.HasSuffix(path, "/transcript") ||
		strings.HasSuffix(path, "/status") ||
		strings.HasSuffix(path, "/proxy-log")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
