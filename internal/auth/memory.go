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
	"fmt"
	"sync"
	"time"
)

const (
	// Session and rate-limiting constants.
	sessionExpiry     = 8 * time.Hour
	tokenBytes        = 32
	maxFailedAttempts = 5
	failedWindow      = 15 * time.Minute
	lockoutDuration   = 30 * time.Minute
)

// MemoryStore manages users, sessions, and rate limiting in memory.
type MemoryStore struct {
	mu       sync.RWMutex
	users    map[string]string         // username -> argon2id hash
	sessions map[string]*sessionEntry  // token -> session
	failures map[string]*failureRecord // username -> failure tracking
}

type sessionEntry struct {
	Username  string
	ExpiresAt time.Time
}

type failureRecord struct {
	Attempts []time.Time
	LockedAt *time.Time
}

// NewMemoryStore creates a store populated from the given user list.
// Each entry is "username:argon2id_hash".
func NewMemoryStore(users map[string]string) *MemoryStore {
	s := &MemoryStore{
		users:    make(map[string]string),
		sessions: make(map[string]*sessionEntry),
		failures: make(map[string]*failureRecord),
	}
	for k, v := range users {
		s.users[k] = v
	}
	return s
}

// ValidateCredentials checks username/password with rate limiting without
// creating a session. Returns the username on success.
func (s *MemoryStore) ValidateCredentials(username, password string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check lockout.
	if rec, ok := s.failures[username]; ok && rec.LockedAt != nil {
		if time.Since(*rec.LockedAt) < lockoutDuration {
			return "", fmt.Errorf("account locked, try again later")
		}
		// Lockout expired — reset.
		delete(s.failures, username)
	}

	hash, ok := s.users[username]
	if !ok || !VerifyPassword(hash, password) {
		s.recordFailure(username)
		return "", fmt.Errorf("invalid credentials")
	}

	// Clear failures on success.
	delete(s.failures, username)

	return username, nil
}

// Authenticate checks username/password with rate limiting. Returns a session
// token on success.
func (s *MemoryStore) Authenticate(username, password string) (string, error) {
	// Validate credentials first
	validatedUser, err := s.ValidateCredentials(username, password)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate session token.
	token, err := generateToken()
	if err != nil {
		return "", err
	}

	s.sessions[token] = &sessionEntry{
		Username:  validatedUser,
		ExpiresAt: time.Now().Add(sessionExpiry),
	}

	return token, nil
}

// ValidateToken checks if a token is valid and returns the username.
func (s *MemoryStore) ValidateToken(token string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[token]
	if !ok {
		return "", false
	}
	if time.Now().After(sess.ExpiresAt) {
		return "", false
	}
	return sess.Username, true
}

// InvalidateToken removes a session token.
func (s *MemoryStore) InvalidateToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

func (s *MemoryStore) recordFailure(username string) {
	rec, ok := s.failures[username]
	if !ok {
		rec = &failureRecord{}
		s.failures[username] = rec
	}

	now := time.Now()
	cutoff := now.Add(-failedWindow)

	// Prune old attempts.
	valid := rec.Attempts[:0]
	for _, t := range rec.Attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	rec.Attempts = valid

	if len(rec.Attempts) >= maxFailedAttempts {
		rec.LockedAt = &now
	}
}

// Ensure MemoryStore implements Authenticator at compile time.
var _ Authenticator = (*MemoryStore)(nil)
