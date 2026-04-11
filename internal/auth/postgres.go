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

package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	pgSessionExpiry   = 8 * time.Hour
	pgMaxFailed       = 5
	pgFailedWindow    = 15 * time.Minute
	pgLockoutDuration = 30 * time.Minute
)

// PgStore implements Authenticator and UserManager backed by PostgreSQL.
// Rate limiting is kept in-memory for simplicity; sessions and users are
// persisted in the database.
type PgStore struct {
	db       *pgxpool.Pool
	mu       sync.RWMutex
	failures map[string]*failureRecord // rate limiting stays in-memory
}

// NewPgStore creates a new PostgreSQL-backed auth store. It starts a background
// goroutine that cleans up expired sessions every hour.
func NewPgStore(db *pgxpool.Pool) *PgStore {
	s := &PgStore{
		db:       db,
		failures: make(map[string]*failureRecord),
	}

	// Background cleanup of expired sessions.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			s.db.Exec(context.Background(),
				"DELETE FROM auth_sessions WHERE expires_at < NOW()")
		}
	}()

	return s
}

// ValidateCredentials checks username/password with rate limiting without
// creating a session. Returns the username on success.
func (s *PgStore) ValidateCredentials(username, password string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check lockout.
	if rec, ok := s.failures[username]; ok && rec.LockedAt != nil {
		if time.Since(*rec.LockedAt) < pgLockoutDuration {
			return "", fmt.Errorf("account locked, try again later")
		}
		// Lockout expired — reset.
		delete(s.failures, username)
	}

	// Look up the user.
	var hash string
	err := s.db.QueryRow(context.Background(),
		"SELECT password FROM auth_users WHERE username = $1", username).Scan(&hash)
	if err != nil || !VerifyPassword(hash, password) {
		s.pgRecordFailure(username)
		return "", fmt.Errorf("invalid credentials")
	}

	// Clear failures on success.
	delete(s.failures, username)

	return username, nil
}

// Authenticate checks username/password with rate limiting and returns a
// session token on success. The token is persisted in PostgreSQL.
func (s *PgStore) Authenticate(username, password string) (string, error) {
	// Validate credentials first
	validatedUser, err := s.ValidateCredentials(username, password)
	if err != nil {
		return "", err
	}

	// Generate session token.
	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}

	// Persist session.
	expiresAt := time.Now().Add(pgSessionExpiry)
	_, err = s.db.Exec(context.Background(),
		"INSERT INTO auth_sessions (token, username, expires_at, created_at) VALUES ($1, $2, $3, $4)",
		token, validatedUser, expiresAt, time.Now())
	if err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}

	return token, nil
}

// ValidateToken checks if a token is valid and returns the associated username.
func (s *PgStore) ValidateToken(token string) (string, bool) {
	var username string
	var expiresAt time.Time
	err := s.db.QueryRow(context.Background(),
		"SELECT username, expires_at FROM auth_sessions WHERE token = $1", token).Scan(&username, &expiresAt)
	if err != nil {
		return "", false
	}

	if time.Now().After(expiresAt) {
		// Clean up the expired token.
		s.db.Exec(context.Background(),
			"DELETE FROM auth_sessions WHERE token = $1", token)
		return "", false
	}

	return username, true
}

// InvalidateToken removes a session token.
func (s *PgStore) InvalidateToken(token string) {
	s.db.Exec(context.Background(),
		"DELETE FROM auth_sessions WHERE token = $1", token)
}

// pgRecordFailure tracks a failed login attempt for rate limiting.
// Must be called with s.mu held.
func (s *PgStore) pgRecordFailure(username string) {
	rec, ok := s.failures[username]
	if !ok {
		rec = &failureRecord{}
		s.failures[username] = rec
	}

	now := time.Now()
	cutoff := now.Add(-pgFailedWindow)

	// Prune old attempts.
	valid := rec.Attempts[:0]
	for _, t := range rec.Attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	rec.Attempts = valid

	if len(rec.Attempts) >= pgMaxFailed {
		rec.LockedAt = &now
	}
}

// --- UserManager methods ---

// CreateUser creates a new user with the given username and password.
func (s *PgStore) CreateUser(ctx context.Context, username, password string, isAdmin bool) error {
	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	_, err = s.db.Exec(ctx,
		"INSERT INTO auth_users (username, password, is_admin, created_at, updated_at) VALUES ($1, $2, $3, NOW(), NOW())",
		username, hash, isAdmin)
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

// DeleteUser removes a user by username.
func (s *PgStore) DeleteUser(ctx context.Context, username string) error {
	tag, err := s.db.Exec(ctx,
		"DELETE FROM auth_users WHERE username = $1", username)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

// ListUsers returns all users ordered by creation time.
func (s *PgStore) ListUsers(ctx context.Context) ([]UserInfo, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
			u.username,
			u.created_at,
			u.is_admin,
			COALESCE(s.session_count, 0) as session_count
		FROM auth_users u
		LEFT JOIN (
			SELECT submitter, COUNT(*) as session_count
			FROM sessions
			GROUP BY submitter
		) s ON u.username = s.submitter
		ORDER BY u.created_at`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	users := []UserInfo{} // empty slice, not nil
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.Username, &u.CreatedAt, &u.IsAdmin, &u.SessionCount); err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating users: %w", err)
	}
	return users, nil
}

// ChangePassword updates a user's password.
func (s *PgStore) ChangePassword(ctx context.Context, username, newPassword string) error {
	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	tag, err := s.db.Exec(ctx,
		"UPDATE auth_users SET password = $1, updated_at = NOW() WHERE username = $2",
		hash, username)
	if err != nil {
		return fmt.Errorf("changing password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

// SetAdmin updates the admin flag for a user.
func (s *PgStore) SetAdmin(ctx context.Context, username string, isAdmin bool) error {
	tag, err := s.db.Exec(ctx,
		"UPDATE auth_users SET is_admin = $1, updated_at = NOW() WHERE username = $2",
		isAdmin, username)
	if err != nil {
		return fmt.Errorf("setting admin flag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", username)
	}
	return nil
}

// IsAdmin returns whether the given user has admin privileges.
func (s *PgStore) IsAdmin(ctx context.Context, username string) (bool, error) {
	var isAdmin bool
	err := s.db.QueryRow(ctx,
		"SELECT is_admin FROM auth_users WHERE username = $1", username).Scan(&isAdmin)
	if err != nil {
		return false, fmt.Errorf("checking admin status: %w", err)
	}
	return isAdmin, nil
}

// VerifyUserPassword checks if the given password matches the stored hash for the user.
func (s *PgStore) VerifyUserPassword(ctx context.Context, username, password string) (bool, error) {
	var hash string
	err := s.db.QueryRow(ctx, "SELECT password FROM auth_users WHERE username = $1", username).Scan(&hash)
	if err != nil {
		return false, fmt.Errorf("user not found: %w", err)
	}
	return VerifyPassword(hash, password), nil
}

// SeedUsers inserts users from a config map (username -> hash) without
// overwriting existing entries. This is idempotent — YAML users seed on first
// run but never overwrite API-changed passwords.
func (s *PgStore) SeedUsers(ctx context.Context, users map[string]string) error {
	for username, hash := range users {
		_, err := s.db.Exec(ctx,
			"INSERT INTO auth_users (username, password, created_at, updated_at) VALUES ($1, $2, NOW(), NOW()) ON CONFLICT (username) DO NOTHING",
			username, hash)
		if err != nil {
			return fmt.Errorf("seeding user %s: %w", username, err)
		}
	}
	return nil
}
