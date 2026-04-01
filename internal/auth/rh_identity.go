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
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RHIdentity represents the decoded X-RH-Identity header payload.
type RHIdentity struct {
	Identity struct {
		Type     string `json:"type"`
		AuthType string `json:"auth_type"`
		Associate *struct {
			RhatUUID  string   `json:"rhatUUID"`
			Email     string   `json:"email"`
			GivenName string   `json:"givenName"`
			Surname   string   `json:"surname"`
			Role      []string `json:"Role"`
		} `json:"associate,omitempty"`
	} `json:"identity"`
}

// ParseRHIdentity decodes a base64-encoded X-RH-Identity header value
// and unmarshals the JSON payload.
func ParseRHIdentity(headerValue string) (*RHIdentity, error) {
	decoded, err := base64.StdEncoding.DecodeString(headerValue)
	if err != nil {
		return nil, fmt.Errorf("decoding base64: %w", err)
	}

	var id RHIdentity
	if err := json.Unmarshal(decoded, &id); err != nil {
		return nil, fmt.Errorf("unmarshaling identity: %w", err)
	}

	if id.Identity.Associate == nil {
		return nil, fmt.Errorf("missing associate in identity")
	}
	if id.Identity.Associate.RhatUUID == "" {
		return nil, fmt.Errorf("missing rhatUUID in identity")
	}
	if id.Identity.Associate.Email == "" {
		return nil, fmt.Errorf("missing email in identity")
	}

	return &id, nil
}

// RHIdentityStore implements Authenticator and UserManager for the rh-identity
// auth backend. It trusts the X-RH-Identity header from Red Hat's Turnpike
// gateway and provisions users automatically on first access.
type RHIdentityStore struct {
	db *pgxpool.Pool
}

// NewRHIdentityStore creates a new RH Identity auth store backed by PostgreSQL.
func NewRHIdentityStore(db *pgxpool.Pool) *RHIdentityStore {
	return &RHIdentityStore{db: db}
}

// UpsertUser creates or updates a user based on the RH Identity header.
// It uses external_id (rhatUUID) as the primary lookup key, with username
// (email) as fallback for users created by BootstrapAdmins without an
// external_id. Returns the username.
func (s *RHIdentityStore) UpsertUser(ctx context.Context, id *RHIdentity) (string, error) {
	assoc := id.Identity.Associate
	displayName := assoc.GivenName + " " + assoc.Surname

	// Try to find existing user by external_id first.
	var username string
	err := s.db.QueryRow(ctx,
		"SELECT username FROM auth_users WHERE external_id = $1", assoc.RhatUUID).Scan(&username)
	if err == nil {
		// User exists with matching external_id — update display_name.
		_, err = s.db.Exec(ctx,
			"UPDATE auth_users SET display_name = $1, updated_at = NOW() WHERE external_id = $2",
			displayName, assoc.RhatUUID)
		if err != nil {
			return "", fmt.Errorf("updating user display name: %w", err)
		}
		return username, nil
	}

	// Fallback: check by username (email). This handles users created by
	// BootstrapAdmins which have no external_id set yet.
	err = s.db.QueryRow(ctx,
		"SELECT username FROM auth_users WHERE username = $1", assoc.Email).Scan(&username)
	if err == nil {
		// User exists by username — backfill external_id and update display_name.
		_, err = s.db.Exec(ctx,
			"UPDATE auth_users SET external_id = $1, display_name = $2, updated_at = NOW() WHERE username = $3",
			assoc.RhatUUID, displayName, assoc.Email)
		if err != nil {
			return "", fmt.Errorf("backfilling external_id: %w", err)
		}
		return username, nil
	}

	// User doesn't exist at all — insert.
	_, err = s.db.Exec(ctx,
		`INSERT INTO auth_users (username, password, external_id, display_name, auth_source, is_admin, created_at, updated_at)
		 VALUES ($1, NULL, $2, $3, 'rh-identity', false, NOW(), NOW())`,
		assoc.Email, assoc.RhatUUID, displayName)
	if err != nil {
		return "", fmt.Errorf("creating user: %w", err)
	}

	return assoc.Email, nil
}

// --- Authenticator interface (no-ops for rh-identity) ---

// Authenticate is not supported with the rh-identity backend.
func (s *RHIdentityStore) Authenticate(username, password string) (string, error) {
	return "", fmt.Errorf("login not supported with rh-identity backend")
}

// ValidateToken is not used with the rh-identity backend.
func (s *RHIdentityStore) ValidateToken(token string) (string, bool) {
	return "", false
}

// InvalidateToken is a no-op for the rh-identity backend.
func (s *RHIdentityStore) InvalidateToken(token string) {}

// --- UserManager interface ---

// CreateUser is not supported with the rh-identity backend.
func (s *RHIdentityStore) CreateUser(ctx context.Context, username, password string, isAdmin bool) error {
	return fmt.Errorf("user creation not supported with rh-identity backend; users are auto-provisioned")
}

// DeleteUser removes a user by username.
func (s *RHIdentityStore) DeleteUser(ctx context.Context, username string) error {
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
func (s *RHIdentityStore) ListUsers(ctx context.Context) ([]UserInfo, error) {
	rows, err := s.db.Query(ctx,
		"SELECT username, created_at, is_admin FROM auth_users ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	users := []UserInfo{}
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.Username, &u.CreatedAt, &u.IsAdmin); err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating users: %w", err)
	}
	return users, nil
}

// ChangePassword is not supported with the rh-identity backend.
func (s *RHIdentityStore) ChangePassword(ctx context.Context, username, newPassword string) error {
	return fmt.Errorf("password change not supported with rh-identity backend")
}

// SetAdmin updates the admin flag for a user.
func (s *RHIdentityStore) SetAdmin(ctx context.Context, username string, isAdmin bool) error {
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
func (s *RHIdentityStore) IsAdmin(ctx context.Context, username string) (bool, error) {
	var isAdmin bool
	err := s.db.QueryRow(ctx,
		"SELECT is_admin FROM auth_users WHERE username = $1", username).Scan(&isAdmin)
	if err != nil {
		return false, fmt.Errorf("checking admin status: %w", err)
	}
	return isAdmin, nil
}

// VerifyUserPassword is not supported with the rh-identity backend.
func (s *RHIdentityStore) VerifyUserPassword(ctx context.Context, username, password string) (bool, error) {
	return false, fmt.Errorf("password verification not supported with rh-identity backend")
}

// BootstrapAdmins ensures the given email addresses exist as admin users.
// For each email: if the user exists, set is_admin=true; if not, create a
// new user with is_admin=true and auth_source="rh-identity".
func (s *RHIdentityStore) BootstrapAdmins(ctx context.Context, admins []string) error {
	for _, email := range admins {
		_, err := s.db.Exec(ctx,
			`INSERT INTO auth_users (username, password, is_admin, auth_source, created_at, updated_at)
			 VALUES ($1, NULL, true, 'rh-identity', NOW(), NOW())
			 ON CONFLICT (username) DO UPDATE SET is_admin = true, updated_at = NOW()`,
			email)
		if err != nil {
			return fmt.Errorf("bootstrapping admin %s: %w", email, err)
		}
	}
	return nil
}
