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
	"testing"
	"golang.org/x/crypto/bcrypt"
)

func TestVerifyPasswordWithArgon2ID(t *testing.T) {
	password := "testpassword"
	
	// Create an argon2id hash
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}
	
	// Verify it works
	if !VerifyPassword(hash, password) {
		t.Errorf("VerifyPassword failed for argon2id hash")
	}
	
	// Verify wrong password fails
	if VerifyPassword(hash, "wrongpassword") {
		t.Errorf("VerifyPassword should fail for wrong password")
	}
}

func TestVerifyPasswordWithLegacyBcrypt(t *testing.T) {
	password := "testpassword"
	
	// Create a bcrypt hash (simulating a legacy hash)
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to create bcrypt hash: %v", err)
	}
	
	// The current VerifyPassword function should return false for bcrypt hashes
	// since it only supports argon2id
	if VerifyPassword(string(bcryptHash), password) {
		t.Errorf("VerifyPassword should return false for bcrypt hash (it only supports argon2id)")
	}
}

func TestPasswordHashFormat(t *testing.T) {
	password := "testpassword"
	
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}
	
	// Verify the hash has the correct format
	if len(hash) < 20 {
		t.Errorf("Hash too short: %s", hash)
	}
	
	if !starts(hash, "$argon2id$") {
		t.Errorf("Hash doesn't start with $argon2id$: %s", hash)
	}
	
	// Verify the hash has the expected number of parts
	parts := splitString(hash, "$")
	if len(parts) != 6 {
		t.Errorf("Expected 6 parts when splitting hash by $, got %d: %v", len(parts), parts)
	}
}

// Helper functions to avoid import dependencies in this test
func starts(s, prefix string) bool {
	return len(s) >= len(prefix) && s[0:len(prefix)] == prefix
}

func splitString(s, sep string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if i < len(s)-len(sep)+1 && s[i:i+len(sep)] == sep {
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
