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
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// ChangeOwnPasswordHandler returns an HTTP handler for PUT /api/v1/auth/password.
// Any authenticated user can change their own password.
func ChangeOwnPasswordHandler(mgr UserManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		username := r.Header.Get("X-Alcove-User")
		if username == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
			return
		}

		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		if req.CurrentPassword == "" || req.NewPassword == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "current_password and new_password are required"})
			return
		}

		if len(req.NewPassword) < 8 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new password must be at least 8 characters"})
			return
		}

		// Verify current password
		valid, err := mgr.VerifyUserPassword(r.Context(), username, req.CurrentPassword)
		if err != nil || !valid {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "current password is incorrect"})
			return
		}

		// Change password
		if err := mgr.ChangePassword(r.Context(), username, req.NewPassword); err != nil {
			log.Printf("error: changing password for %s: %v", username, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to change password"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
	}
}

// UserAPI provides HTTP handlers for user CRUD operations.
type UserAPI struct {
	mgr UserManager
}

// NewUserAPI creates a UserAPI backed by the given UserManager.
func NewUserAPI(mgr UserManager) *UserAPI {
	return &UserAPI{mgr: mgr}
}

// isRequestAdmin checks if the current request was made by an admin user.
func isRequestAdmin(r *http.Request) bool {
	return r.Header.Get("X-Alcove-Admin") == "true"
}

// HandleUsers handles GET and POST on /api/v1/users.
func (a *UserAPI) HandleUsers(w http.ResponseWriter, r *http.Request) {
	if !isRequestAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := a.mgr.ListUsers(r.Context())
		if err != nil {
			log.Printf("error: listing users: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list users"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"users": users,
			"count": len(users),
		})
	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			IsAdmin  bool   `json:"is_admin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.Username == "" || req.Password == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password are required"})
			return
		}
		if err := a.mgr.CreateUser(r.Context(), req.Username, req.Password, req.IsAdmin); err != nil {
			log.Printf("error: creating user: %v", err)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "failed to create user: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"username": req.Username, "created": "true"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// HandleUserByID handles /api/v1/users/{username}, /api/v1/users/{username}/password,
// and /api/v1/users/{username}/admin.
func (a *UserAPI) HandleUserByID(w http.ResponseWriter, r *http.Request) {
	if !isRequestAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
	parts := strings.SplitN(path, "/", 2)
	username := parts[0]

	if username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username required"})
		return
	}

	// Handle /api/v1/users/{username}/password
	if len(parts) == 2 && parts[1] == "password" {
		if r.Method != http.MethodPut {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.Password == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password is required"})
			return
		}
		if err := a.mgr.ChangePassword(r.Context(), username, req.Password); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
		return
	}

	// Handle /api/v1/users/{username}/admin
	if len(parts) == 2 && parts[1] == "admin" {
		if r.Method != http.MethodPut {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		// Prevent self-demotion.
		currentUser := r.Header.Get("X-Alcove-User")
		if currentUser == username {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot change your own admin status"})
			return
		}

		var req struct {
			IsAdmin bool `json:"is_admin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := a.mgr.SetAdmin(r.Context(), username, req.IsAdmin); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
		return
	}

	// Handle /api/v1/users/{username}
	switch r.Method {
	case http.MethodDelete:
		// Prevent self-deletion.
		currentUser := r.Header.Get("X-Alcove-User")
		if currentUser == username {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot delete your own account"})
			return
		}
		if err := a.mgr.DeleteUser(r.Context(), username); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
