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
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RHIdentity represents the decoded X-RH-Identity header payload.
type RHIdentity struct {
	Identity struct {
		Type      string `json:"type"`
		AuthType  string `json:"auth_type"`
		Associate *struct {
			RhatUUID  string   `json:"rhatUUID"`
			Email     string   `json:"email"`
			GivenName string   `json:"givenName"`
			Surname   string   `json:"surname"`
			Role      []string `json:"Role"`
		} `json:"associate,omitempty"`
		User *struct {
			Username string `json:"username"`
		} `json:"user,omitempty"`
		Registry *struct {
			OrgID    string `json:"org_id"`
			Username string `json:"username"`
		} `json:"registry,omitempty"`
		OrgID string `json:"org_id,omitempty"`
	} `json:"identity"`
}

// TBRIdentity represents TBR-specific identity information extracted from RHIdentity
type TBRIdentity struct {
	OrgID    string
	Username string
}

// TBRAssociation represents a TBR identity association record
type TBRAssociation struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	TBROrgID       string     `json:"tbr_org_id"`
	TBRUsername    string     `json:"tbr_username"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastAccessedAt *time.Time `json:"last_accessed_at"`
}

// ParseRHIdentity decodes a base64-encoded X-RH-Identity header value
// and unmarshals the JSON payload. It supports both SAML Associate and TBR User identities.
func ParseRHIdentity(headerValue string) (*RHIdentity, error) {
	decoded, err := base64.StdEncoding.DecodeString(headerValue)
	if err != nil {
		return nil, fmt.Errorf("decoding base64: %w", err)
	}

	var id RHIdentity
	if err := json.Unmarshal(decoded, &id); err != nil {
		return nil, fmt.Errorf("unmarshaling identity: %w", err)
	}

	// Log decoded identity type for debugging
	log.Printf("rh-identity: parsed header type=%s auth_type=%s", id.Identity.Type, id.Identity.AuthType)

	// Validate SAML Associate identity
	if id.Identity.Associate != nil {
		if id.Identity.Associate.RhatUUID == "" {
			return nil, fmt.Errorf("missing rhatUUID in Associate identity")
		}
		if id.Identity.Associate.Email == "" {
			return nil, fmt.Errorf("missing email in Associate identity")
		}
		log.Printf("rh-identity: valid SAML Associate identity uuid=%s email=%s", id.Identity.Associate.RhatUUID, id.Identity.Associate.Email)
		return &id, nil
	}

	// Validate TBR User identity
	if id.Identity.User != nil {
		if id.Identity.OrgID == "" || id.Identity.User.Username == "" {
			return nil, fmt.Errorf("missing org_id or username in TBR identity")
		}
		log.Printf("rh-identity: valid TBR identity org_id=%s username=%s", id.Identity.OrgID, id.Identity.User.Username)
		return &id, nil
	}

	// Validate Registry identity (TBR via Turnpike registry auth).
	// Turnpike sends: {"identity": {"type": "Registry", "auth_type": "registry-auth", "registry": {"org_id": "...", "username": "..."}}}
	if id.Identity.Registry != nil {
		if id.Identity.Registry.OrgID == "" || id.Identity.Registry.Username == "" {
			return nil, fmt.Errorf("missing org_id or username in Registry identity")
		}
		log.Printf("rh-identity: valid Registry identity org_id=%s username=%s", id.Identity.Registry.OrgID, id.Identity.Registry.Username)
		return &id, nil
	}

	return nil, fmt.Errorf("identity missing associate, user, and registry fields")
}

// ExtractTBRIdentity extracts TBR identity information from RHIdentity if present.
// Supports both User-type TBR identities and Registry-type identities from Turnpike.
func ExtractTBRIdentity(id *RHIdentity) *TBRIdentity {
	if id.Identity.User != nil && id.Identity.OrgID != "" && id.Identity.User.Username != "" {
		return &TBRIdentity{
			OrgID:    id.Identity.OrgID,
			Username: id.Identity.User.Username,
		}
	}
	if id.Identity.Registry != nil && id.Identity.Registry.OrgID != "" && id.Identity.Registry.Username != "" {
		return &TBRIdentity{
			OrgID:    id.Identity.Registry.OrgID,
			Username: id.Identity.Registry.Username,
		}
	}
	return nil
}

// IsTBRIdentity returns true if the identity is a TBR identity type
func IsTBRIdentity(id *RHIdentity) bool {
	return ExtractTBRIdentity(id) != nil
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

// --- TBR Identity Association Management ---

// ResolveTBRToSSO looks up a TBR identity (org_id + username) and returns the associated SSO username
func (s *RHIdentityStore) ResolveTBRToSSO(ctx context.Context, orgID, tbrUsername string) (string, error) {
	log.Printf("rh-identity: resolving TBR identity org_id=%s username=%s", orgID, tbrUsername)

	var ssoUsername string
	err := s.db.QueryRow(ctx,
		"SELECT user_id FROM tbr_identity_associations WHERE tbr_org_id = $1 AND tbr_username = $2",
		orgID, tbrUsername).Scan(&ssoUsername)
	if err != nil {
		log.Printf("rh-identity: TBR resolution failed org_id=%s username=%s: %v", orgID, tbrUsername, err)
		return "", fmt.Errorf("TBR identity not found: %w", err)
	}

	log.Printf("rh-identity: TBR resolved org_id=%s username=%s -> sso_user=%s", orgID, tbrUsername, ssoUsername)
	return ssoUsername, nil
}

// GetTBRAssociations returns all TBR associations for a given SSO user
func (s *RHIdentityStore) GetTBRAssociations(ctx context.Context, username string) ([]TBRAssociation, error) {
	log.Printf("rh-identity: fetching TBR associations for user=%s", username)

	rows, err := s.db.Query(ctx,
		"SELECT id, user_id, tbr_org_id, tbr_username, created_at, updated_at, last_accessed_at FROM tbr_identity_associations WHERE user_id = $1 ORDER BY created_at",
		username)
	if err != nil {
		return nil, fmt.Errorf("querying TBR associations: %w", err)
	}
	defer rows.Close()

	var associations []TBRAssociation
	for rows.Next() {
		var assoc TBRAssociation
		if err := rows.Scan(&assoc.ID, &assoc.UserID, &assoc.TBROrgID, &assoc.TBRUsername, &assoc.CreatedAt, &assoc.UpdatedAt, &assoc.LastAccessedAt); err != nil {
			return nil, fmt.Errorf("scanning TBR association: %w", err)
		}
		associations = append(associations, assoc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating TBR associations: %w", err)
	}

	log.Printf("rh-identity: found %d TBR associations for user=%s", len(associations), username)
	return associations, nil
}

// CreateTBRAssociation creates a new TBR identity association for an SSO user
func (s *RHIdentityStore) CreateTBRAssociation(ctx context.Context, username, orgID, tbrUsername string) (*TBRAssociation, error) {
	log.Printf("rh-identity: creating TBR association user=%s org_id=%s tbr_username=%s", username, orgID, tbrUsername)

	// Check if the TBR identity is already associated with someone else
	var existingUser string
	err := s.db.QueryRow(ctx,
		"SELECT user_id FROM tbr_identity_associations WHERE tbr_org_id = $1 AND tbr_username = $2",
		orgID, tbrUsername).Scan(&existingUser)
	if err == nil {
		if existingUser == username {
			return nil, fmt.Errorf("TBR identity already associated with your account")
		}
		return nil, fmt.Errorf("TBR identity already associated with another user")
	}

	// Insert the new association
	var assoc TBRAssociation
	err = s.db.QueryRow(ctx,
		`INSERT INTO tbr_identity_associations (user_id, tbr_org_id, tbr_username, created_at, updated_at)
		 VALUES ($1, $2, $3, NOW(), NOW())
		 RETURNING id, user_id, tbr_org_id, tbr_username, created_at, updated_at, last_accessed_at`,
		username, orgID, tbrUsername).Scan(&assoc.ID, &assoc.UserID, &assoc.TBROrgID, &assoc.TBRUsername, &assoc.CreatedAt, &assoc.UpdatedAt, &assoc.LastAccessedAt)
	if err != nil {
		return nil, fmt.Errorf("creating TBR association: %w", err)
	}

	log.Printf("rh-identity: created TBR association id=%s user=%s org_id=%s tbr_username=%s", assoc.ID, username, orgID, tbrUsername)
	return &assoc, nil
}

// UpdateTBRLastAccessed updates the last_accessed_at timestamp for a TBR identity association
// It includes throttling to avoid excessive writes under heavy usage
func (s *RHIdentityStore) UpdateTBRLastAccessed(ctx context.Context, orgID, tbrUsername string) error {
	// Only update if the existing value is older than 1 minute to avoid excessive writes
	result, err := s.db.Exec(ctx,
		`UPDATE tbr_identity_associations
		 SET last_accessed_at = NOW()
		 WHERE tbr_org_id = $1 AND tbr_username = $2
		 AND (last_accessed_at IS NULL OR last_accessed_at < NOW() - INTERVAL '1 minute')`,
		orgID, tbrUsername)
	if err != nil {
		return fmt.Errorf("updating TBR last accessed: %w", err)
	}

	if result.RowsAffected() > 0 {
		log.Printf("rh-identity: updated last_accessed_at for TBR org_id=%s username=%s", orgID, tbrUsername)
	}

	return nil
}

func (s *RHIdentityStore) DeleteTBRAssociation(ctx context.Context, username, associationID string) error {
	log.Printf("rh-identity: deleting TBR association id=%s user=%s", associationID, username)

	tag, err := s.db.Exec(ctx,
		"DELETE FROM tbr_identity_associations WHERE id = $1 AND user_id = $2",
		associationID, username)
	if err != nil {
		return fmt.Errorf("deleting TBR association: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("TBR association not found or not owned by user")
	}

	log.Printf("rh-identity: deleted TBR association id=%s user=%s", associationID, username)
	return nil
}

// --- Authenticator interface (no-ops for rh-identity) ---

// Authenticate is not supported with the rh-identity backend.
func (s *RHIdentityStore) Authenticate(username, password string) (string, error) {
	return "", fmt.Errorf("login not supported with rh-identity backend")
}

// ValidateCredentials is not supported with the rh-identity backend.
func (s *RHIdentityStore) ValidateCredentials(username, password string) (string, error) {
	return "", fmt.Errorf("basic auth not supported with rh-identity backend")
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

	users := []UserInfo{}
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
